// Package agent holds the credential-agent half of jcli: the in-memory token cache, the
// unix-socket server, and the macOS Keychain backing. The keychain access is isolated behind
// the keychainStore interface so the agent's logic stays pure-Go and unit-testable, while the
// cgo keychain path lives only in keychain_darwin.go.
package agent

import "errors"

// moq is pinned to v0.5.3: v0.7+ requires Go 1.26 while this module targets Go 1.24.
//go:generate go run github.com/matryer/moq@v0.5.3 -out keychain_mock.go . keychainStore

// ErrNoToken is returned by a keychainStore Get when no item exists for the profile.
var ErrNoToken = errors.New("no token stored for profile")

// keychainStore reads and writes per-profile Jenkins tokens in the platform secret store.
// The interface is deliberately platform-neutral — no cgo types appear in the signatures — so the
// non-darwin stub and the generated moq mock stay clean. On darwin, the item's trusted-application
// ACL is bound to the ad-hoc code identity of the binary that created it: that same binary reads it
// back without a prompt, while a binary with a different code identity (e.g. after a rebuild, since
// the ad-hoc cdhash changes) triggers the standard keychain "Allow / Always Allow" prompt.
type keychainStore interface {
	// Set stores token for profile, creating or replacing the item without prompting.
	Set(profile, token string) error
	// Get returns the token for profile; on darwin the item's ACL trust of the ad-hoc identity gates the read.
	Get(profile string) (string, error)
	// Delete removes the item for profile; a missing item is not an error.
	Delete(profile string) error
}
