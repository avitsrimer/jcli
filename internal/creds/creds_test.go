package creds

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortSock returns a socket path short enough for the sockaddr_un 104-byte limit. t.TempDir
// under /var/folders is too long to bind, so we use a short dir under os.TempDir and clean up.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

// stubAgent binds a real unix socket and serves one canned response per connection using the
// supplied handler. It runs until the returned stop func is called. Construction is synchronous:
// the listener is bound before stubAgent returns, so callers can connect immediately.
func stubAgent(t *testing.T, sock string, handler func(req request) response) (stop func()) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var req request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(handler(req))
			}()
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = ln.Close()
			wg.Wait()
		})
	}
}

// newClient builds a Client pointed at sock with fast spawn timing for deterministic tests.
func newClient(sock, self string) *Client {
	return &Client{
		sockPath:     sock,
		self:         self,
		spawnTimeout: 3 * time.Second,
		pollInterval: 5 * time.Millisecond,
	}
}

func TestClient_Token_HappyPath(t *testing.T) {
	sock := shortSock(t)
	stop := stubAgent(t, sock, func(req request) response {
		assert.Equal(t, "get-token", req.Op)
		assert.Equal(t, "work", req.Profile)
		return response{Token: "tok-1"}
	})
	defer stop()

	c := newClient(sock, "/nonexistent")
	tok, err := c.Token("work")
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
}

func TestClient_SetToken_HappyPath(t *testing.T) {
	sock := shortSock(t)
	var gotProfile, gotToken string
	stop := stubAgent(t, sock, func(req request) response {
		gotProfile, gotToken = req.Profile, req.Token
		return response{}
	})
	defer stop()

	c := newClient(sock, "/nonexistent")
	require.NoError(t, c.SetToken("work", "secret"))
	assert.Equal(t, "work", gotProfile)
	assert.Equal(t, "secret", gotToken)
}

func TestClient_DeleteToken_And_Flush(t *testing.T) {
	sock := shortSock(t)
	var ops []string
	stop := stubAgent(t, sock, func(req request) response {
		ops = append(ops, req.Op)
		return response{}
	})
	defer stop()

	c := newClient(sock, "/nonexistent")
	require.NoError(t, c.DeleteToken("work"))
	require.NoError(t, c.Flush())
	assert.Equal(t, []string{"delete-token", "flush"}, ops)
}

func TestClient_Token_AuthError(t *testing.T) {
	sock := shortSock(t)
	stop := stubAgent(t, sock, func(request) response {
		return response{Error: "no token stored for profile \"work\"", Auth: true}
	})
	defer stop()

	c := newClient(sock, "/nonexistent")
	_, err := c.Token("work")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuth)
}

func TestClient_Token_NonAuthError(t *testing.T) {
	sock := shortSock(t)
	stop := stubAgent(t, sock, func(request) response {
		return response{Error: "unknown op"}
	})
	defer stop()

	c := newClient(sock, "/nonexistent")
	_, err := c.Token("work")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAuth)
	assert.Contains(t, err.Error(), "unknown op")
}

func TestClient_SpawnWhenAbsent(t *testing.T) {
	sock := shortSock(t)
	bin := buildStubAgentBin(t, sock)

	// no agent listening yet; the client must spawn `<bin> __agent`, which binds the socket
	// synchronously, then connect and get the token.
	c := newClient(sock, bin)
	tok, err := c.Token("work")
	require.NoError(t, err)
	assert.Equal(t, "spawned-token", tok)
}

func TestClient_SpawnTimeout(t *testing.T) {
	sock := shortSock(t)
	// /usr/bin/true exits immediately without ever binding the socket, so waitConnect must time
	// out rather than hang. tiny timeout keeps the test fast and wall-clock-bounded.
	c := &Client{sockPath: sock, self: "/usr/bin/true", spawnTimeout: 150 * time.Millisecond, pollInterval: 5 * time.Millisecond}
	_, err := c.Token("work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestClient_ConcurrentSpawnRace(t *testing.T) {
	sock := shortSock(t)
	bin := buildStubAgentBin(t, sock)

	// many callers race to find no agent and spawn one. The stub binary takes an exclusive
	// flock so duplicate spawns lose the race and exit; every caller must still succeed by
	// connecting to the winner. This exercises the idempotent-spawn requirement.
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	toks := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newClient(sock, bin)
			toks[i], errs[i] = c.Token("work")
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "caller %d", i)
		assert.Equal(t, "spawned-token", toks[i])
	}
}

func TestClient_SocketGoneMidRequest(t *testing.T) {
	sock := shortSock(t)
	// a listener that accepts then immediately closes the connection without replying, modeling
	// the socket vanishing mid-request. exchange must surface a read error, not hang.
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		_ = conn.Close()
	}()

	c := newClient(sock, "/nonexistent")
	_, err = c.Token("work")
	require.Error(t, err)
}

func TestClient_MalformedResponse(t *testing.T) {
	sock := shortSock(t)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// drain the client's request first so its write completes before we close —
		// closing early races the client's send, surfacing the error at the write
		// ("send request") instead of at the malformed-response decode ("read response").
		_, _ = conn.Read(make([]byte, 256))
		_, _ = conn.Write([]byte("this is not json\n"))
	}()

	c := newClient(sock, "/nonexistent")
	_, err = c.Token("work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read response")
}

func TestIsRefused(t *testing.T) {
	// a dial to a nonexistent socket path yields ENOENT, which isRefused must treat as "spawn".
	_, err := net.DialTimeout("unix", filepath.Join(t.TempDir(), "missing.sock"), 100*time.Millisecond)
	require.Error(t, err)
	assert.True(t, isRefused(err))
}

// buildStubAgentBin compiles the testdata stub-agent program into a temp binary and returns its
// path. The stub binds the given socket synchronously (taking an exclusive flock first so only
// one of a racing herd survives) and serves a fixed token. Compiling a real binary makes the
// spawn path deterministic and not wall-clock-dependent.
func buildStubAgentBin(t *testing.T, sock string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stubagent")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/stubagent")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub agent: %v\n%s", err, combined)
	}
	// the stub reads its socket path from JCLI_STUB_SOCK; Client spawns `<bin> __agent` inheriting
	// the parent env, so set it for the test's duration.
	t.Setenv("JCLI_STUB_SOCK", sock)
	// the spawned stub is detached (we hold no handle), so kill it via the pid it records next to
	// the socket — guarantees no leaked process survives the test.
	t.Cleanup(func() { killStub(sock) })
	return out
}

// killStub terminates the detached stub agent identified by the pid file it wrote beside sock.
func killStub(sock string) {
	data, err := os.ReadFile(sock + ".pid")
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}
