//go:build windows

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows rich-text support: capture the "HTML Format" clipboard flavor
// into Entry.HTML, and write it back together with plain text on copy.
// Everything here is best-effort and recover()-guarded: rich text must
// NEVER break plain-text capture or copy.

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procRegisterClipboardFormatW   = modUser32.NewProc("RegisterClipboardFormatW")

	procGlobalAlloc  = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procGlobalSize   = modKernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var htmlFormatID = registerHTMLFormat()

func registerHTMLFormat() uint32 {
	name, _ := windows.UTF16PtrFromString("HTML Format")
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
	return uint32(r)
}

// readHTMLFormat returns the HTML fragment from the clipboard, or "" on any
// failure. It never panics out to the caller.
func readHTMLFormat() (result string) {
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	if htmlFormatID == 0 {
		return ""
	}
	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(htmlFormatID))
	if r == 0 {
		return ""
	}
	r, _, _ = procOpenClipboard.Call(0)
	if r == 0 {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(uintptr(htmlFormatID))
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	sz, _, _ := procGlobalSize.Call(h)
	if sz == 0 || sz > 50<<20 {
		procGlobalUnlock.Call(h)
		return ""
	}
	// vet-clean 内存读取：不用 uintptr→unsafe.Pointer 转换，走 ReadProcessMemory
	data := make([]byte, int(sz))
	var n uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), p, &data[0], uintptr(sz), &n); err != nil || n != uintptr(sz) {
		procGlobalUnlock.Call(h)
		return ""
	}
	procGlobalUnlock.Call(h)
	return extractHTMLFragment(data)
}

// extractHTMLFragment parses the CF_HTML header and returns the fragment
// between StartHTML and EndHTML byte offsets.
func extractHTMLFragment(data []byte) string {
	headerEnd := 0
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '\r' && data[i+1] == '\n' {
			// header lines end before the first '<'
			rest := data[i+2:]
			if len(rest) > 0 && rest[0] == '<' {
				headerEnd = i + 2
				break
			}
		}
	}
	var start, end = -1, -1
	for _, line := range strings.Split(string(data[:min(headerEnd, len(data))]), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "StartHTML:"); ok {
			start, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		if v, ok := strings.CutPrefix(line, "EndHTML:"); ok {
			end, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if start < 0 || end <= start || end > len(data) {
		return ""
	}
	frag := data[start:end]
	// trim trailing NULs
	for len(frag) > 0 && frag[len(frag)-1] == 0 {
		frag = frag[:len(frag)-1]
	}
	return string(frag)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildCFHTML wraps fragment in a CF_HTML envelope with correct byte offsets.
func buildCFHTML(fragment string) []byte {
	pre := "<html><body>\r\n<!--StartFragment-->"
	post := "<!--EndFragment-->\r\n</body>\r\n</html>"
	// placeholder header; offsets computed after layout
	headerFmt := "Version:0.9\r\nStartHTML:%08d\r\nEndHTML:%08d\r\nStartFragment:%08d\r\nEndFragment:%08d\r\n"
	// iterate once: offsets depend on header length which depends on offsets.
	// 8-digit padding keeps the header length fixed, so one pass suffices.
	fragBytes := []byte(fragment)
	headerLen := len(fmt.Sprintf(headerFmt, 0, 0, 0, 0))
	startHTML := headerLen
	startFrag := headerLen + len(pre)
	endFrag := startFrag + len(fragBytes)
	endHTML := endFrag + len(post)
	header := fmt.Sprintf(headerFmt, startHTML, endHTML, startFrag, endFrag)
	var out []byte
	out = append(out, []byte(header)...)
	out = append(out, []byte(pre)...)
	out = append(out, fragBytes...)
	out = append(out, []byte(post)...)
	return out
}

func setClipboardBytes(format uint32, data []byte) error {
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)+1))
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return errors.New("GlobalLock failed")
	}
	// vet-clean 内存写入：不用 uintptr→unsafe.Pointer 转换，走 WriteProcessMemory
	if len(data) > 0 {
		var n uintptr
		if err := windows.WriteProcessMemory(windows.CurrentProcess(), p, &data[0], uintptr(len(data)), &n); err != nil || n != uintptr(len(data)) {
			procGlobalUnlock.Call(h)
			return errors.New("WriteProcessMemory failed")
		}
	}
	procGlobalUnlock.Call(h)
	r, _, _ := procSetClipboardData.Call(uintptr(format), h)
	if r == 0 {
		return errors.New("SetClipboardData failed")
	}
	return nil // system owns h now
}

// writeTextToClipboard writes plain text, plus the HTML Format flavor when
// html is non-empty, in a single OpenClipboard session.
func writeTextToClipboard(text, html string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("clipboard write failed")
		}
	}()
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return errors.New("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return errors.New("EmptyClipboard failed")
	}
	utf16, err := windows.UTF16FromString(text) // NUL-terminated
	if err != nil {
		return err
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&utf16[0])), len(utf16)*2)
	if err := setClipboardBytes(cfUnicodeText, raw); err != nil {
		return err
	}
	if html != "" && htmlFormatID != 0 {
		// HTML write-back is best effort; text already succeeded.
		_ = setClipboardBytes(htmlFormatID, buildCFHTML(html))
	}
	return nil
}
