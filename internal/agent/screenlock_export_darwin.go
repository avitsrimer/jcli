//go:build darwin

package agent

// This file is kept separate from screenlock_darwin.go because cgo forbids a file that uses
// //export from also defining C functions in its preamble. Here the preamble is empty.

import "C"

import "runtime"

//export goScreenLocked
func goScreenLocked() {
	screenLockMu.Lock()
	cb := screenLockCallback
	screenLockMu.Unlock()
	if cb != nil {
		cb()
	}
}

// lockRunLoopThread pins the calling goroutine to its OS thread, required before running a
// thread-bound CFRunLoop. It lives here so screenlock_darwin.go's preamble stays focused on C.
func lockRunLoopThread() {
	runtime.LockOSThread()
}
