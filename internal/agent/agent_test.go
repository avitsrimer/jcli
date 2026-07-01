package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer binds a real unix socket in a temp dir with the given store and starts serving.
// it returns the running server and its socket path; the caller must Close it. cfg may tune ttl
// and idle before the serve loop starts so the watchdog never races a test write.
func newTestServer(t *testing.T, store keychainStore, cfg ...func(*Server)) (*Server, string) {
	t.Helper()
	sock := shortSock(t)
	srv, err := newServer(store, sock)
	require.NoError(t, err)
	for _, fn := range cfg {
		fn(srv)
	}
	go srv.Serve()
	return srv, sock
}

// shortSock returns a socket path short enough for the sockaddr_un 104-byte limit. t.TempDir
// paths under /var/folders are too long to bind, so we use a short dir under os.TempDir and
// register cleanup ourselves.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

// roundTrip dials the socket, sends one request, and decodes the response. It models the CLI
// side: one request per connection.
func roundTrip(t *testing.T, sock string, req request) response {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, json.NewEncoder(conn).Encode(req))
	var resp response
	require.NoError(t, json.NewDecoder(conn).Decode(&resp))
	return resp
}

func TestServer_GetToken_CacheHitAvoidsSecondKeychainCall(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return "tok-1", nil },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	require.Empty(t, got.Error)
	assert.Equal(t, "tok-1", got.Token)

	// second get within the TTL must be served from memory, not re-read from the keychain.
	got = roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	assert.Equal(t, "tok-1", got.Token)
	assert.Len(t, store.GetCalls(), 1)
}

func TestServer_GetToken_TTLExpiryForcesRefetch(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return "tok-fresh", nil },
	}
	srv, sock := newTestServer(t, store, func(s *Server) { s.ttl = 50 * time.Millisecond })
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	assert.Equal(t, "tok-fresh", got.Token)

	// sleep well past the TTL (4x) so the second read is a cache miss even under CI scheduling jitter.
	time.Sleep(200 * time.Millisecond)

	got = roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	assert.Equal(t, "tok-fresh", got.Token)
	assert.Len(t, store.GetCalls(), 2)
}

func TestServer_GetToken_MissingMapsToAuthError(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return "", ErrNoToken },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	assert.Empty(t, got.Token)
	assert.True(t, got.Auth)
	assert.Contains(t, got.Error, "no token stored")
}

func TestServer_SetToken_ProxiesToKeychain(t *testing.T) {
	store := &keychainStoreMock{
		SetFunc: func(_, _ string) error { return nil },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "set-token", Profile: "work", Token: "secret"})
	require.Empty(t, got.Error)
	calls := store.SetCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "work", calls[0].Profile)
	assert.Equal(t, "secret", calls[0].Token)
}

func TestServer_SetToken_PropagatesError(t *testing.T) {
	store := &keychainStoreMock{
		SetFunc: func(_, _ string) error { return errors.New("write failed") },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "set-token", Profile: "work", Token: "secret"})
	assert.Contains(t, got.Error, "write failed")
}

func TestServer_SetToken_EvictsCachedTokenSoRotationIsImmediate(t *testing.T) {
	// model token rotation via re-login: the keychain returns the old token first and the new
	// token after Set. A long TTL ensures the second get would hit the stale cache if set-token
	// failed to evict — proving the eviction rather than a TTL lapse.
	current := "tok-old"
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return current, nil },
		SetFunc: func(_, token string) error { current = token; return nil },
	}
	srv, sock := newTestServer(t, store, func(s *Server) { s.ttl = time.Hour })
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	require.Empty(t, got.Error)
	assert.Equal(t, "tok-old", got.Token)

	got = roundTrip(t, sock, request{Op: "set-token", Profile: "work", Token: "tok-new"})
	require.Empty(t, got.Error)
	require.Len(t, store.SetCalls(), 1, "set-token must reach the keychain")

	// without waiting for the TTL, the next get must reflect the rotated token, not the cached one.
	got = roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	require.Empty(t, got.Error)
	assert.Equal(t, "tok-new", got.Token, "rotated token must be served immediately after set-token")
	assert.Len(t, store.GetCalls(), 2, "the cache entry must have been evicted, forcing a re-read")
}

func TestServer_DeleteToken_ProxiesAndEvicts(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc:    func(_ string) (string, error) { return "tok", nil },
		DeleteFunc: func(_ string) error { return nil },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	// prime the cache, then delete.
	roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	got := roundTrip(t, sock, request{Op: "delete-token", Profile: "work"})
	require.Empty(t, got.Error)
	require.Len(t, store.DeleteCalls(), 1)

	srv.mu.Lock()
	_, present := srv.cache["work"]
	srv.mu.Unlock()
	assert.False(t, present, "deleted profile must be evicted from cache")
}

func TestServer_Flush(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return "tok", nil },
	}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	t.Run("flush all clears every profile", func(t *testing.T) {
		roundTrip(t, sock, request{Op: "get-token", Profile: "a"})
		roundTrip(t, sock, request{Op: "get-token", Profile: "b"})
		got := roundTrip(t, sock, request{Op: "flush"})
		require.Empty(t, got.Error)

		srv.mu.Lock()
		n := len(srv.cache)
		srv.mu.Unlock()
		assert.Zero(t, n)
	})

	t.Run("flush one keeps others", func(t *testing.T) {
		roundTrip(t, sock, request{Op: "get-token", Profile: "a"})
		roundTrip(t, sock, request{Op: "get-token", Profile: "b"})
		got := roundTrip(t, sock, request{Op: "flush", Profile: "a"})
		require.Empty(t, got.Error)

		srv.mu.Lock()
		_, hasA := srv.cache["a"]
		_, hasB := srv.cache["b"]
		srv.mu.Unlock()
		assert.False(t, hasA)
		assert.True(t, hasB)
	})
}

func TestServer_Close_FlushesCacheAndIsIdempotent(t *testing.T) {
	store := &keychainStoreMock{
		GetFunc: func(_ string) (string, error) { return "tok", nil },
	}
	srv, sock := newTestServer(t, store)

	roundTrip(t, sock, request{Op: "get-token", Profile: "a"})
	srv.mu.Lock()
	require.NotZero(t, len(srv.cache))
	srv.mu.Unlock()

	require.NoError(t, srv.Close())

	srv.mu.Lock()
	n := len(srv.cache)
	srv.mu.Unlock()
	assert.Zero(t, n)

	// second Close is a no-op and must not panic or error.
	require.NoError(t, srv.Close())
}

func TestServer_UnknownOp(t *testing.T) {
	srv, sock := newTestServer(t, &keychainStoreMock{})
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "bogus"})
	assert.Contains(t, got.Error, "unknown op")
}

func TestServer_IdleExit(t *testing.T) {
	srv, _ := newTestServer(t, &keychainStoreMock{}, func(s *Server) {
		s.idle = 30 * time.Millisecond
		s.lastUse = time.Now()
	})

	// the watchdog must close the listener and end Serve once idle elapses. wait for done with a
	// generous ceiling so the assertion is timing-robust under CI load.
	select {
	case <-srv.done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not self-exit on idle")
	}
	_ = srv.Close()
}

func TestServer_SingleInstance(t *testing.T) {
	sock := shortSock(t)
	store := &keychainStoreMock{}

	first, err := newServer(store, sock)
	require.NoError(t, err)
	defer func() { _ = first.Close() }()

	// a second agent on the same socket loses the flock race and reports errAlreadyRunning.
	_, err = newServer(store, sock)
	assert.ErrorIs(t, err, errAlreadyRunning)
}

func TestServer_StaleSocketRemoved(t *testing.T) {
	sock := shortSock(t)
	// leave a stale regular file where the socket should be; startup must remove and rebind.
	require.NoError(t, os.WriteFile(sock, []byte("stale"), 0o600))

	srv, err := newServer(&keychainStoreMock{}, sock)
	require.NoError(t, err)
	defer func() { _ = srv.Close() }()

	info, err := os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.NotZero(t, info.Mode()&os.ModeSocket, "path must now be a socket")
}

func TestServer_PeerUIDAccept(t *testing.T) {
	// the connecting test process shares the server's UID, so the real peerUID extraction must
	// return that UID and the request must be accepted. (rejection is single-UID-untestable and
	// is verified manually per the plan's Post-Completion section.)
	store := &keychainStoreMock{GetFunc: func(_ string) (string, error) { return "ok", nil }}
	srv, sock := newTestServer(t, store)
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	require.Empty(t, got.Error, "same-UID peer must be accepted")
	assert.Equal(t, "ok", got.Token)
}

func TestServer_PeerUIDRejection(t *testing.T) {
	// inject a peerUID that reports a foreign uid to exercise the reject branch without needing a
	// second OS user.
	store := &keychainStoreMock{GetFunc: func(_ string) (string, error) { return "ok", nil }}
	sock := shortSock(t)
	srv, err := newServer(store, sock)
	require.NoError(t, err)
	srv.peerUID = func(*net.UnixConn) (int, error) { return os.Getuid() + 1, nil }
	go srv.Serve()
	defer func() { _ = srv.Close() }()

	got := roundTrip(t, sock, request{Op: "get-token", Profile: "work"})
	assert.Contains(t, got.Error, "not permitted")
	assert.Empty(t, got.Token)
	assert.Empty(t, store.GetCalls(), "a rejected peer must never reach the keychain")
}

func TestZero(t *testing.T) {
	b := []byte("secret")
	zero(b)
	for _, c := range b {
		assert.Zero(t, c)
	}
}

func TestSocketPath(t *testing.T) {
	p := SocketPath()
	assert.Equal(t, "agent.sock", filepath.Base(p))
	_, err := os.Stat(filepath.Dir(p))
	assert.NoError(t, err, "runtime dir should be created")
}
