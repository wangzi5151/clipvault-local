//go:build !windows && !linux

package main

// Lock detection is only implemented for Windows and Linux.
// On other platforms the feature degrades silently.
func startLockMonitor(onLock func(bool)) {
	logf("lock monitor: not supported on this platform")
}
