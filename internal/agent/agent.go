package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// default lifetime knobs for the in-memory cache and the idle self-exit. they are exposed as
// struct fields on Server so tests can shrink them to milliseconds.
const (
	defaultTTL  = 15 * time.Minute
	defaultIdle = 15 * time.Minute
)

// requestReadTimeout bounds only the decode of the inbound request. keychainOpTimeout is armed as a
// write-only deadline after dispatch returns, so it bounds only the response write — it does NOT
// preempt the blocking cgo keychain read (a net.Conn deadline cannot interrupt SecItemCopyMatching),
// so a genuinely hung read leaves the per-connection goroutine blocked in cgo; the client-side
// deadline is the effective bound there.
const (
	requestReadTimeout = 5 * time.Second
	keychainOpTimeout  = 2 * time.Minute
)

// request is the JSON wire format the CLI sends over the unix socket. token is only populated
// for set-token; profile is empty on a global flush.
type request struct {
	Op      string `json:"op"`
	Profile string `json:"profile,omitempty"`
	Token   string `json:"token,omitempty"`
}

// response is the JSON the agent writes back. Token carries the secret for a successful
// get-token; Error is a human-readable message and Auth marks an authentication failure so the
// CLI can map it to exit code 2.
type response struct {
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
	Auth  bool   `json:"auth,omitempty"`
}

// entry is a single cached token. the secret lives in a []byte (not a string) so it can be
// wiped on eviction; expires advances on every use to implement the refresh-on-use TTL.
type entry struct {
	token   []byte
	expires time.Time
}

// Server is the credential agent: a single-instance unix-socket server that fronts a
// keychainStore with an in-memory, TTL-bounded token cache. It serves get/set/delete/flush and
// self-exits after an absolute idle window. Zero value is not usable — construct with newServer.
type Server struct {
	store keychainStore
	ln    net.Listener
	lock  *os.File // held flock guarding single-instance startup

	ttl            time.Duration // refresh-on-use lifetime of a cached token
	idle           time.Duration // absolute idle window before self-exit
	reqReadTimeout time.Duration // bounds only the inbound request decode
	writeTimeout   time.Duration // bounds only the response write (never the keychain read)

	mu      sync.Mutex
	cache   map[string]*entry
	lastUse time.Time

	// peerUID verifies the connecting process owner; swapped out in tests. it returns the
	// peer's uid or an error if the platform cannot determine it.
	peerUID func(conn *net.UnixConn) (int, error)

	done     chan struct{} // closed to stop the accept loop
	doneOnce sync.Once     // guards closing done from Serve and Close
	once     sync.Once     // guards Close cleanup
}

// stop closes done exactly once, regardless of which goroutine (Serve, watchIdle, Close) trips it.
func (s *Server) stop() {
	s.doneOnce.Do(func() { close(s.done) })
}

// Run is the entry point for the hidden `jcli __agent` mode: it constructs the server, claims
// single-instance ownership, serves until the idle timeout lapses, and returns. A lost
// single-instance race is not an error — the winner is already serving.
func Run() error {
	store, err := newKeychainStore()
	if err != nil {
		return fmt.Errorf("init keychain store: %w", err)
	}
	srv, err := newServer(store, SocketPath())
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			return nil
		}
		return err
	}
	defer srv.Close()
	srv.Serve()
	return nil
}

// errAlreadyRunning signals that another agent already holds the single-instance lock; the
// caller should exit cleanly and let the winner serve.
var errAlreadyRunning = errors.New("agent already running")

// SocketPath returns the per-user agent socket path inside a user-private runtime dir. It honors
// $TMPDIR and falls back to os.UserCacheDir; the parent dir is created 0700.
func SocketPath() string {
	dir := runtimeDir()
	return filepath.Join(dir, "agent.sock")
}

// runtimeDir resolves a user-private directory for the socket and lockfile, creating it 0700.
func runtimeDir() string {
	base := os.TempDir()
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		base = cache
	}
	dir := filepath.Join(base, fmt.Sprintf("jcli-%d", os.Getuid()))
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// newServer claims the single-instance lock, removes any stale socket, and binds an exclusive
// 0600 unix listener at sockPath. A second agent that loses the flock race gets errAlreadyRunning.
func newServer(store keychainStore, sockPath string) (*Server, error) {
	lockPath := sockPath + ".lock"
	lock, err := acquireLock(lockPath)
	if err != nil {
		return nil, err // errAlreadyRunning passes through verbatim for the caller to detect
	}

	// we hold the lock — any leftover socket is stale, so remove it before binding.
	if rmErr := os.Remove(sockPath); rmErr != nil && !os.IsNotExist(rmErr) {
		_ = releaseLock(lock)
		return nil, fmt.Errorf("remove stale socket %s: %w", sockPath, rmErr)
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(context.Background(), "unix", sockPath)
	if err != nil {
		_ = releaseLock(lock)
		return nil, fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		_ = releaseLock(lock)
		return nil, fmt.Errorf("chmod socket %s: %w", sockPath, err)
	}

	srv := &Server{
		store:          store,
		ln:             ln,
		lock:           lock,
		ttl:            defaultTTL,
		idle:           defaultIdle,
		reqReadTimeout: requestReadTimeout,
		writeTimeout:   keychainOpTimeout,
		cache:          make(map[string]*entry),
		lastUse:        time.Now(),
		peerUID:        peerUID,
		done:           make(chan struct{}),
	}
	return srv, nil
}

// Serve runs the accept loop and the idle watchdog until the idle window lapses or Close is
// called. It blocks; on return all in-memory tokens have been zeroed.
func (s *Server) Serve() {
	go s.watchIdle()
	// additive hardening: on darwin, zero all token buffers the moment the screen locks. The
	// watcher runs its own CFRunLoop on a dedicated goroutine and never blocks Accept. Off darwin
	// this is a no-op. The lock-flush path cannot be driven headlessly, so it is verified manually
	// (see the plan's Post-Completion section).
	watchScreenLock(s.flushAll)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			// listener closed (idle exit or Close); stop serving.
			s.stop()
			s.flushAll()
			return
		}
		s.handle(conn.(*net.UnixConn))
	}
}

// watchIdle closes the listener once the process has been idle for the absolute idle window,
// which unblocks Accept and ends Serve. It checks at a fraction of the idle interval.
func (s *Server) watchIdle() {
	interval := s.idle / 4
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			idleFor := time.Since(s.lastUse)
			s.mu.Unlock()
			if idleFor >= s.idle {
				_ = s.ln.Close() // unblocks Accept; Serve will flush and return
				return
			}
		}
	}
}

// handle reads one request from conn, verifies the peer UID, dispatches it, and writes the
// response. One connection serves exactly one request (the CLI is short-lived).
func (s *Server) handle(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()

	uid, err := s.peerUID(conn)
	if err != nil {
		s.writeResponse(conn, response{Error: fmt.Sprintf("peer-uid check failed: %v", err)})
		return
	}
	if uid != os.Getuid() {
		s.writeResponse(conn, response{Error: fmt.Sprintf("peer uid %d not permitted", uid)})
		return
	}

	// bound only the request decode; the blocking keychain read in dispatch must not run under
	// this short deadline or the socket tears down mid Keychain prompt.
	_ = conn.SetReadDeadline(time.Now().Add(s.reqReadTimeout))

	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		s.writeResponse(conn, response{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	s.touch()
	resp := s.dispatch(req)

	// arm a write-only deadline for the response send. it bounds only the write; the blocking cgo
	// keychain read in dispatch has already completed and was never under any deadline.
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	s.writeResponse(conn, resp)
}

// dispatch routes a request to the matching operation and returns the response to send.
func (s *Server) dispatch(req request) response {
	switch req.Op {
	case "get-token":
		return s.getToken(req.Profile)
	case "set-token":
		if err := s.store.Set(req.Profile, req.Token); err != nil {
			return response{Error: fmt.Sprintf("set token: %v", err)}
		}
		// evict any cached entry so a rotated token (re-login) is served immediately
		// instead of the stale value lingering until the TTL lapses.
		s.evict(req.Profile)
		return response{}
	case "delete-token":
		if err := s.store.Delete(req.Profile); err != nil {
			return response{Error: fmt.Sprintf("delete token: %v", err)}
		}
		s.evict(req.Profile)
		return response{}
	case "flush":
		if req.Profile == "" {
			s.flushAll()
		} else {
			s.evict(req.Profile)
		}
		return response{}
	default:
		return response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

// getToken serves a profile's token from the cache, refreshing the TTL on a hit. On a miss it
// reads the keychainStore (the ACL-trusted keychain read on darwin) and caches the result.
func (s *Server) getToken(profile string) response {
	s.mu.Lock()
	if e, ok := s.cache[profile]; ok && time.Now().Before(e.expires) {
		e.expires = time.Now().Add(s.ttl) // refresh-on-use
		tok := string(e.token)
		s.mu.Unlock()
		return response{Token: tok}
	}
	s.mu.Unlock()

	tok, err := s.store.Get(profile)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return response{Error: fmt.Sprintf("no token stored for profile %q", profile), Auth: true}
		}
		return response{Error: fmt.Sprintf("read token: %v", err), Auth: true}
	}

	s.mu.Lock()
	s.cache[profile] = &entry{token: []byte(tok), expires: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return response{Token: tok}
}

// touch records activity to defer the idle self-exit.
func (s *Server) touch() {
	s.mu.Lock()
	s.lastUse = time.Now()
	s.mu.Unlock()
}

// evict drops a single profile's cached token, zeroing its buffer.
func (s *Server) evict(profile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.cache[profile]; ok {
		zero(e.token)
		delete(s.cache, profile)
	}
}

// flushAll zeroes and drops every cached token.
func (s *Server) flushAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.cache {
		zero(e.token)
		delete(s.cache, k)
	}
}

// writeResponse encodes resp to conn, ignoring write errors on a dying connection.
func (s *Server) writeResponse(conn *net.UnixConn, resp response) {
	_ = json.NewEncoder(conn).Encode(resp)
}

// Close stops the accept loop, releases the listener and lock, and zeroes all buffers. Safe to
// call multiple times.
func (s *Server) Close() error {
	s.once.Do(func() {
		s.stop()
		_ = s.ln.Close()
		s.flushAll()
		if s.lock != nil {
			_ = releaseLock(s.lock)
		}
	})
	return nil
}

// zero overwrites a secret buffer in place.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
