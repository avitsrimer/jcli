//go:build !darwin

package agent

import (
	"fmt"
	"runtime"
)

// errUnsupported reports that the macOS Keychain backing is unavailable off darwin. It keeps
// go vet and cross-builds green without dragging cgo onto other platforms.
type stubKeychain struct{}

// newKeychainStore returns a stub that fails every operation on non-darwin platforms.
func newKeychainStore() (keychainStore, error) {
	return stubKeychain{}, nil
}

// Set is unsupported off darwin.
func (stubKeychain) Set(profile, _ string) error {
	return fmt.Errorf("keychain set for profile %q: unsupported platform %s", profile, runtime.GOOS)
}

// Get is unsupported off darwin.
func (stubKeychain) Get(profile string) (string, error) {
	return "", fmt.Errorf("keychain get for profile %q: unsupported platform %s", profile, runtime.GOOS)
}

// Delete is unsupported off darwin.
func (stubKeychain) Delete(profile string) error {
	return fmt.Errorf("keychain delete for profile %q: unsupported platform %s", profile, runtime.GOOS)
}
