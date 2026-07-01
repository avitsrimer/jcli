package agent

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// acquireLock opens (creating 0600) the lockfile at path and takes a non-blocking exclusive
// flock. If another process already holds it, it returns errAlreadyRunning so the caller can
// exit cleanly. The returned *os.File must be passed to releaseLock to drop the lock.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lockfile %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errAlreadyRunning
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return f, nil
}

// releaseLock drops the flock and closes the lockfile handle.
func releaseLock(f *os.File) error {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return f.Close()
}
