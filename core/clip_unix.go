//go:build !windows

package main

import (
	clipboard "golang.design/x/clipboard"
)

// Rich text on non-Windows platforms: best effort per IPC.md.
// golang.design/x/clipboard only exposes text and PNG image flavors, so
// HTML capture is a no-op here; plain-text capture/copy is unaffected.

// readHTMLFormat always returns "" on non-Windows builds.
func readHTMLFormat() string { return "" }

// writeTextToClipboard writes plain text via the cross-platform library.
func writeTextToClipboard(text, _ string) error {
	if !clipboardOK {
		return errText("clipboard unavailable")
	}
	if ch := clipboard.Write(clipboard.FmtText, []byte(text)); ch != nil {
		<-ch
	}
	return nil
}
