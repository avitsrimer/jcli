package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// the real cgo keychain path in keychain_darwin.go cannot be driven non-interactively (the
// ACL-trusted read depends on the ad-hoc code identity of the binary that created the item) and is
// verified manually (see the plan's Post-Completion section). The keychainStore contract is exercised
// meaningfully through the server in agent_test.go, not through mock-only round-trips.

// TestNewKeychainStore ensures the platform constructor returns a usable store on the build host.
func TestNewKeychainStore(t *testing.T) {
	store, err := newKeychainStore()
	require.NoError(t, err)
	require.NotNil(t, store)
}
