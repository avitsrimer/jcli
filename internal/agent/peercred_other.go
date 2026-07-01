//go:build !darwin

package agent

import (
	"fmt"
	"net"
	"runtime"
)

// peerUID is unsupported off darwin: the agent is a macOS tool, and this stub only exists to keep
// cross-builds (go vet / go build) green. It always errors so a non-darwin agent rejects peers.
func peerUID(_ *net.UnixConn) (int, error) {
	return 0, fmt.Errorf("peer-uid verification unsupported on %s", runtime.GOOS)
}
