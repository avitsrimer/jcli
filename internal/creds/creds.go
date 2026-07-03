// Package creds is the client half of the credential agent. It connects to the agent's unix
// socket, spawning a detached agent process on demand if none is listening, and exposes
// token get/set/delete/flush operations over the JSON wire protocol.
package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/avitsrimer/jcli/internal/agent"
)

// ErrAuth indicates the agent reported an authentication failure (e.g. no token stored or the
// keychain read was rejected). The CLI maps this to exit code 2.
var ErrAuth = errors.New("authentication failed")

const (
	// defaultSpawnTimeout bounds how long we wait for a freshly spawned agent's socket to appear.
	defaultSpawnTimeout = 3 * time.Second
	// spawnPollInterval is the gap between connect attempts while waiting for the socket.
	spawnPollInterval = 25 * time.Millisecond
	// dialTimeout bounds a single connection attempt.
	dialTimeout = time.Second
	// requestDeadline bounds a single request/response exchange once connected.
	requestDeadline = 10 * time.Second
)

// request mirrors the agent's wire request exactly. The agent's struct is unexported, so we keep
// a byte-for-byte copy of the JSON shape here; the two must stay in sync (see internal/agent).
type request struct {
	Op      string `json:"op"`
	Profile string `json:"profile,omitempty"`
	Token   string `json:"token,omitempty"`
}

// response mirrors the agent's wire response exactly. Auth flags an authentication failure.
type response struct {
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
	Auth  bool   `json:"auth,omitempty"`
}

// Client talks to the credential agent over its unix socket. The zero value is not usable;
// construct it with New.
type Client struct {
	sockPath     string
	self         string // path to this binary, re-exec'd as `<self> __agent` to spawn the agent
	spawnTimeout time.Duration
	pollInterval time.Duration
}

// New returns a Client wired to the running binary's agent socket. self defaults to
// os.Executable so a connection-refused triggers a detached `<self> __agent` spawn.
func New() (*Client, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	return &Client{
		sockPath:     agent.SocketPath(),
		self:         self,
		spawnTimeout: defaultSpawnTimeout,
		pollInterval: spawnPollInterval,
	}, nil
}

// Token returns the token for profile, triggering the agent's keychain read on a cold read.
// Authentication failures surface as ErrAuth.
func (c *Client) Token(profile string) (string, error) {
	resp, err := c.do(request{Op: "get-token", Profile: profile})
	if err != nil {
		return "", err
	}
	return resp.Token, nil
}

// SetToken stores token for profile via the agent (which proxies to the keychain). The token is
// not logged; the local request copy is zeroed after the exchange.
func (c *Client) SetToken(profile, token string) error {
	_, err := c.do(request{Op: "set-token", Profile: profile, Token: token})
	return err
}

// DeleteToken removes profile's token from the keychain and evicts any cached copy.
func (c *Client) DeleteToken(profile string) error {
	_, err := c.do(request{Op: "delete-token", Profile: profile})
	return err
}

// Flush clears every in-memory token buffer in the agent without touching the keychain.
func (c *Client) Flush() error {
	_, err := c.do(request{Op: "flush"})
	return err
}

// do sends one request over a fresh connection and returns the decoded response. If no agent is
// listening it spawns one (detached) and retries. Agent-reported auth failures become ErrAuth.
func (c *Client) do(req request) (response, error) {
	conn, err := c.connect()
	if err != nil {
		return response{}, err
	}
	defer func() { _ = conn.Close() }()

	resp, err := exchange(conn, req)
	// best-effort: drop the plaintext token from the request copy now that it has been sent.
	req.Token = ""
	if err != nil {
		return response{}, err
	}
	if resp.Error != "" {
		if resp.Auth {
			return response{}, fmt.Errorf("%w: %s", ErrAuth, resp.Error)
		}
		return response{}, errors.New(resp.Error)
	}
	return resp, nil
}

// exchange writes req and decodes the single response on conn under a deadline.
func exchange(conn net.Conn, req request) (response, error) {
	_ = conn.SetDeadline(time.Now().Add(requestDeadline))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, fmt.Errorf("send request: %w", err)
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}

// connect dials the agent socket; on connection-refused (no agent) it spawns one detached and
// waits, bounded, for the socket to accept. A benign refused-then-present transition is never
// fatal — whichever agent wins the single-instance race is the one we connect to.
func (c *Client) connect() (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(context.Background(), "unix", c.sockPath)
	if err == nil {
		return conn, nil
	}
	if !isRefused(err) {
		return nil, fmt.Errorf("connect to agent: %w", err)
	}

	if err := c.spawnAgent(); err != nil {
		return nil, err
	}
	return c.waitConnect()
}

// spawnAgent launches `<self> __agent` fully detached: a new session (Setsid) with no
// controlling terminal and discarded stdio, so it outlives this CLI process and is never reaped
// by it. The spawn is idempotent — a duplicate agent loses the single-instance race and exits.
func (c *Client) spawnAgent() error {
	cmd := exec.CommandContext(context.Background(), c.self, "__agent") //nolint:gosec // self is os.Executable, not user input
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn agent: %w", err)
	}
	// release our handle so the kernel reaps the detached child, not us.
	_ = cmd.Process.Release()
	return nil
}

// waitConnect polls the socket until it accepts a connection or the spawn timeout elapses. A
// refused connection means the agent is still binding; any other dial error is fatal.
func (c *Client) waitConnect() (net.Conn, error) {
	deadline := time.Now().Add(c.spawnTimeout)
	for {
		d := net.Dialer{Timeout: dialTimeout}
		conn, err := d.DialContext(context.Background(), "unix", c.sockPath)
		if err == nil {
			return conn, nil
		}
		if !isRefused(err) {
			return nil, fmt.Errorf("connect to spawned agent: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for agent socket %s", c.spawnTimeout, c.sockPath)
		}
		time.Sleep(c.pollInterval)
	}
}

// isRefused reports whether err is the "no listener" condition: ECONNREFUSED on a present socket
// or ENOENT when the socket file does not exist yet. Both mean "no agent serving — spawn/wait".
func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, os.ErrNotExist)
}
