//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows lock detection via WTS session notifications (pure Go, x/sys/windows).
//
// A message-only window registers for session change notifications with
// WTSRegisterSessionNotification; WM_WTSSESSION_CHANGE with wParam
// WTS_SESSION_LOCK/UNLOCK drives the callback. Any failure degrades
// silently (the feature just stays unavailable).

const (
	wmWtsSessionChange   = 0x02B1
	wtsSessionLock       = 0x7
	wtsSessionUnlock     = 0x8
	notifyForThisSession = 0x1
	hwndMessage          = ^uintptr(2) // (HWND)-3
)

var (
	modWtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

	procWTSRegisterSessionNotification   = modWtsapi32.NewProc("WTSRegisterSessionNotification")
	procWTSUnRegisterSessionNotification = modWtsapi32.NewProc("WTSUnRegisterSessionNotification")

	procRegisterClassExW = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// winMsg mirrors the Win32 MSG layout on 64-bit (48 bytes).
type winMsg struct {
	hwnd     uintptr
	message  uint32
	_        uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX      int32
	ptY      int32
	lPrivate uint32
}

var (
	lockCallback  func(bool)
	wndProcKeeper uintptr // keep the NewCallback trampoline alive
)

func wtsLockWndProc(hwnd, uMsg, wParam, lParam uintptr) uintptr {
	if uint32(uMsg) == wmWtsSessionChange {
		switch uint32(wParam) {
		case wtsSessionLock:
			if lockCallback != nil {
				lockCallback(true)
			}
		case wtsSessionUnlock:
			if lockCallback != nil {
				lockCallback(false)
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uMsg, wParam, lParam)
	return r
}

func startLockMonitor(onLock func(bool)) {
	defer func() {
		// Silent degradation: any failure just disables the feature.
		if recover() != nil {
			logf("lock monitor: unavailable on this system")
		}
	}()
	lockCallback = onLock
	wndProcKeeper = windows.NewCallback(wtsLockWndProc)

	className, _ := windows.UTF16PtrFromString("ClipVaultLockMonitor")
	wcx := &wndClassExW{
		lpfnWndProc:   wndProcKeeper,
		hInstance:     windows.Handle(0),
		lpszClassName: className,
	}
	wcx.cbSize = uint32(unsafe.Sizeof(*wcx))
	r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wcx)))
	if r == 0 {
		logf("lock monitor: RegisterClassExW failed")
		return
	}
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0, 0, 0, 0,
		hwndMessage, // message-only window
		0, 0, 0,
	)
	if hwnd == 0 {
		logf("lock monitor: CreateWindowExW failed")
		return
	}
	defer procDestroyWindow.Call(hwnd)

	r, _, _ = procWTSRegisterSessionNotification.Call(hwnd, notifyForThisSession)
	if r == 0 {
		logf("lock monitor: WTSRegisterSessionNotification failed")
		return
	}
	defer procWTSUnRegisterSessionNotification.Call(hwnd)

	logf("lock monitor: WTS session notifications active")
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return // WM_QUIT or error
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
