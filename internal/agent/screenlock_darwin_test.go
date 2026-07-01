//go:build darwin

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestScreenLockCallbackWiring proves the pure-Go half of the screen-lock bridge: watchScreenLock
// registers the supplied callback and the C trampoline's Go entry point (goScreenLocked) invokes it.
// The CFRunLoop side is started by watchScreenLock but is not driven here (it cannot be exercised
// headlessly); this covers the nil-guard, the mutex-guarded registration, and the dispatch.
func TestScreenLockCallbackWiring(t *testing.T) {
	t.Run("nil callback is ignored", func(t *testing.T) {
		screenLockMu.Lock()
		screenLockCallback = nil
		screenLockMu.Unlock()

		watchScreenLock(nil)

		screenLockMu.Lock()
		cb := screenLockCallback
		screenLockMu.Unlock()
		assert.Nil(t, cb, "nil callback must not be registered")
	})

	t.Run("registered callback fires on lock", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		watchScreenLock(func() { fired <- struct{}{} })

		// simulate the lock notification by invoking the Go side of the C bridge directly.
		goScreenLocked()

		select {
		case <-fired:
		default:
			t.Fatal("goScreenLocked did not invoke the registered callback")
		}
	})
}
