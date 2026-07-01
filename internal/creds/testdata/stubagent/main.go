// Command stubagent is a test-only stand-in for `jcli __agent`. It binds the unix socket named
// by JCLI_STUB_SOCK and serves a fixed token, modelling the real agent's detached-spawn
// behaviour. It takes an exclusive flock first so that, in a concurrent-spawn race, only one
// instance survives and the losers exit cleanly (mirroring the agent's single-instance bind).
package main

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

type request struct {
	Op      string `json:"op"`
	Profile string `json:"profile,omitempty"`
	Token   string `json:"token,omitempty"`
}

type response struct {
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
	Auth  bool   `json:"auth,omitempty"`
}

func main() {
	sock := os.Getenv("JCLI_STUB_SOCK")
	if sock == "" {
		os.Exit(2)
	}

	// single-instance: a duplicate spawn that loses the flock exits cleanly — the winner serves.
	lock, err := os.OpenFile(sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(0)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		os.Exit(0) // lost the race; the winner already owns the socket
	}

	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(0)
	}

	// record our pid next to the socket so the test can kill us in cleanup (we are detached).
	_ = os.WriteFile(sock+".pid", []byte(strconv.Itoa(os.Getpid())), 0o600)

	// self-exit after a short idle window so the test process never leaks.
	go func() {
		time.Sleep(10 * time.Second)
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(conn)
	}
}

func serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	resp := response{}
	if req.Op == "get-token" {
		resp.Token = "spawned-token"
	}
	_ = json.NewEncoder(conn).Encode(resp)
}
