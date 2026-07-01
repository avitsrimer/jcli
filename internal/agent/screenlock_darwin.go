//go:build darwin

package agent

/*
#cgo CFLAGS: -x objective-c -fno-objc-arc
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>

// goScreenLocked is the Go side of the bridge; it is defined in screenlock_export_darwin.go via
// cgo //export. Declared here so the C observer trampoline can call it.
extern void goScreenLocked(void);

// screenLockObserver is the CFNotificationCenter callback. macOS invokes it on the run loop we
// start below when the distributed "com.apple.screenIsLocked" notification fires; it forwards to
// the registered Go callback. The arguments are ignored — we only care that a lock happened.
static void screenLockObserver(CFNotificationCenterRef center, void *observer, CFStringRef name,
	const void *object, CFDictionaryRef userInfo) {
	goScreenLocked();
}

// runScreenLockLoop registers screenLockObserver for the screen-lock notification on the
// distributed notification center, then runs the current thread's CFRunLoop forever. It must be
// called on a dedicated, OS-thread-locked goroutine because CFRunLoopRun never returns and the
// run loop is thread-bound.
static void runScreenLockLoop(void) {
	CFNotificationCenterRef center = CFNotificationCenterGetDistributedCenter();
	if (center == NULL) {
		return;
	}
	CFNotificationCenterAddObserver(center, NULL, screenLockObserver,
		CFSTR("com.apple.screenIsLocked"), NULL,
		CFNotificationSuspensionBehaviorDeliverImmediately);
	CFRunLoopRun();
}
*/
import "C"

import "sync"

// screenLockCallback holds the agent-supplied flush function, guarded because the observer fires
// from the dedicated run-loop thread while watchScreenLock is called from the agent's boot path.
var (
	screenLockMu       sync.Mutex
	screenLockCallback func()
)

// watchScreenLock wires onLock to the macOS screen-lock notification and starts a dedicated
// CFRunLoop on its own OS-thread-locked goroutine. The callback fires on every screen lock so the
// agent can zero all in-memory token buffers; it must not block the socket-accept loop, which is
// why the run loop lives on a separate goroutine. A nil onLock is ignored.
func watchScreenLock(onLock func()) {
	if onLock == nil {
		return
	}
	screenLockMu.Lock()
	screenLockCallback = onLock
	screenLockMu.Unlock()

	go func() {
		// CFRunLoopRun is thread-bound and never returns, so pin this goroutine to its OS thread
		// for the agent's lifetime; we never UnlockOSThread.
		lockRunLoopThread()
		C.runScreenLockLoop()
	}()
}
