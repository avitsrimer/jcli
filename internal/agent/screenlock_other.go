//go:build !darwin

package agent

// watchScreenLock is a no-op off darwin. Screen-lock flush is a macOS-only hardening built on
// CFNotificationCenter; this stub keeps cross-builds (go vet / go build) green.
func watchScreenLock(_ func()) {}
