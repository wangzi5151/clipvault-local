package main

// Screen-lock handling (IPC.md §捕获规则-8).
//
// The platform file implements startLockMonitor(onLock), which calls
// onLock(true) when the workstation locks and onLock(false) on unlock.
// Any failure degrades silently: the feature is simply unavailable.

// runLockMonitor starts the platform lock monitor in this goroutine.
func (a *App) runLockMonitor() {
	startLockMonitor(a.onLockEvent)
}

// onLockEvent applies settings.lock_action when the workstation locks.
func (a *App) onLockEvent(locked bool) {
	if !locked {
		return // stay paused until the user resumes manually
	}
	st, err := a.getStore().GetSettings()
	if err != nil {
		logf("lock: cannot read settings: %v", err)
		return
	}
	switch st.LockAction {
	case "pause":
		if st.Recording {
			if _, rerr := a.mSetRecording(false); rerr != nil {
				logf("lock: pause failed: %s", rerr.Message)
				return
			}
			logf("lock: recording paused")
		}
	case "clear":
		n, err := a.getStore().ClearRecent(60)
		if err != nil {
			logf("lock: clear failed: %v", err)
			return
		}
		a.event("entries_cleared", map[string]any{"deleted": n})
		logf("lock: cleared %d entries from the last hour", n)
	}
}
