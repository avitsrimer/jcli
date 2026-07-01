//go:build darwin

package agent

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the uid of the process on the other end of conn. macOS has no SO_PEERCRED;
// instead the peer credentials come from getsockopt(SOL_LOCAL, LOCAL_PEERCRED) as an xucred,
// reached through the raw fd exposed by conn.SyscallConn().
func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("raw conn: %w", err)
	}

	var (
		xucred *unix.Xucred
		opErr  error
	)
	if ctrlErr := raw.Control(func(fd uintptr) {
		xucred, opErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); ctrlErr != nil {
		return 0, fmt.Errorf("control raw conn: %w", ctrlErr)
	}
	if opErr != nil {
		return 0, fmt.Errorf("getsockopt LOCAL_PEERCRED: %w", opErr)
	}
	return int(xucred.Uid), nil
}
