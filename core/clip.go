package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	clipboard "golang.design/x/clipboard"
)

// Clipboard capture limits (IPC.md §捕获规则).
const (
	maxTextBytes  = 1 << 20  // 1MB text
	maxImageBytes = 25 << 20 // 25MB decoded image
	thumbMaxSide  = 320      // thumbnail longest side
	sensitiveTTL  = 60 * time.Second
)

// pendingCapture holds a sensitive capture waiting for the user's
// resolve_sensitive decision (or the 60s timeout).
type pendingCapture struct {
	token     string
	kind      string // "text" | "image"
	text      string
	html      string
	preview   string
	image     []byte // original PNG
	thumb     []byte // thumbnail PNG
	imageW    int
	imageH    int
	imageSize int64
	sha       string
	sensitive bool
	timer     *time.Timer
}

// clipboardOK is set only when clipboard.Init() succeeds. All clipboard
// calls are guarded by it: the library may panic on exotic platforms, and
// a clipboard failure must NEVER kill the sidecar.
var clipboardOK = false

// clipboardInitOnce initializes the clipboard library, recovering from any
// panic the library may raise (defense in depth).
func clipboardInitOnce() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errText(fmt.Sprintf("clipboard init panic: %v", r))
		}
	}()
	if err = clipboard.Init(); err != nil {
		return err
	}
	clipboardOK = true
	return nil
}

// runWatcher starts the clipboard listener. It never returns; on platforms
// without a usable clipboard (e.g. headless Linux) it logs to stderr and
// capture stays silently disabled.
func (a *App) runWatcher() {
	if err := clipboardInitOnce(); err != nil {
		logf("clipboard unavailable, capture disabled: %v", err)
		return
	}
	ctx := context.Background()
	// v0.8.0: a single Watch channel multiplexes formats via Data{Format, Bytes}.
	ch := clipboard.Watch(ctx, clipboard.FmtText, clipboard.FmtImage)
	logf("clipboard watcher started")
	for d := range ch {
		switch d.Format {
		case clipboard.FmtText:
			a.onClipboardText(d.Bytes)
		case clipboard.FmtImage:
			a.onClipboardImage(d.Bytes)
		}
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *App) setLastHash(h string) {
	a.clipMu.Lock()
	a.lastHash = h
	a.lastStoredHash = h
	a.clipMu.Unlock()
}

// captureAllowed checks the recording switch, the kind toggle and lock state.
func (a *App) captureAllowed(kind string) bool {
	st := a.getStore()
	if st.IsLocked() {
		return false
	}
	settings, err := st.GetSettings()
	if err != nil {
		logf("capture: cannot read settings: %v", err)
		return false
	}
	if !settings.Recording {
		return false
	}
	if kind == KindText && !settings.RecordText {
		return false
	}
	if kind == KindImage && !settings.RecordImages {
		return false
	}
	return true
}

func (a *App) skipSensitiveMode() string {
	st, err := a.getStore().GetSettings()
	if err != nil {
		return "ask"
	}
	return st.SkipSensitive
}

// looksLikeFileDrop heuristically detects file-drag clipboard payloads
// (every non-empty line is a file:// URI) so they are ignored per IPC rule 1.
func looksLikeFileDrop(text string) bool {
	lines := strings.Split(text, "\n")
	seen := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if ln == "" {
			continue
		}
		seen++
		if !strings.HasPrefix(strings.ToLower(ln), "file://") {
			return false
		}
	}
	return seen > 0
}

// onClipboardText handles a new text clipboard payload.
func (a *App) onClipboardText(b []byte) {
	if len(b) == 0 || len(b) > maxTextBytes {
		return
	}
	if !a.captureAllowed(KindText) {
		return
	}
	text := string(b)
	if looksLikeFileDrop(text) {
		return // rule 1: never touch file drops
	}
	sha := sha256Hex(b)
	// Rule 3: consecutive identical content (sha256) is not stored twice.
	a.clipMu.Lock()
	if sha == a.lastStoredHash {
		a.clipMu.Unlock()
		return
	}
	a.lastHash = sha
	a.clipMu.Unlock()

	sensitive := false
	if mode := a.skipSensitiveMode(); mode != "off" && IsSensitive(text) {
		sensitive = true
		if mode == "auto" {
			logf("capture: sensitive text auto-skipped")
			return
		}
		// mode == "ask": stash and prompt the UI
		a.stashSensitive(&pendingCapture{
			kind:      KindText,
			text:      text,
			html:      readHTMLFormat(), // best effort, must not break text capture
			preview:   SensitivePreview(text),
			sha:       sha,
			sensitive: true,
		})
		return
	}

	html := readHTMLFormat() // best effort; "" on any failure
	preview := makePreview(text)
	e := &Entry{
		Kind:      KindText,
		Text:      text,
		HTML:      html,
		Preview:   preview,
		SHA256:    sha,
		TextLen:   len([]rune(text)),
		Sensitive: sensitive,
	}
	a.storeCapture(e)
}

// onClipboardImage handles a new image clipboard payload (PNG bytes).
func (a *App) onClipboardImage(pngBytes []byte) {
	if len(pngBytes) == 0 || len(pngBytes) > maxImageBytes {
		return
	}
	if !a.captureAllowed(KindImage) {
		return
	}
	sha := sha256Hex(pngBytes)
	// Rule 3: consecutive identical content (sha256) is not stored twice.
	a.clipMu.Lock()
	if sha == a.lastStoredHash {
		a.clipMu.Unlock()
		return
	}
	a.lastHash = sha
	a.clipMu.Unlock()
	// IPC rule 4: images are only skipped when skip_sensitive == "auto"
	// (no OCR, ever).
	if a.skipSensitiveMode() == "auto" {
		logf("capture: image auto-skipped (skip_sensitive=auto)")
		return
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		logf("capture: image decode failed: %v", err)
		return
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	if int64(w)*int64(h)*4 > maxImageBytes {
		logf("capture: image decoded size exceeds 25MB, ignored")
		return
	}
	thumb := makeThumbnail(img, thumbMaxSide)
	if thumb == nil {
		logf("capture: thumbnail encode failed")
		return
	}
	e := &Entry{
		Kind:      KindImage,
		Preview:   imagePreview(w, h),
		Image:     pngBytes,
		Thumb:     thumb,
		ImageW:    w,
		ImageH:    h,
		ImageSize: int64(len(pngBytes)),
		SHA256:    sha,
	}
	a.storeCapture(e)
}

// storeCapture inserts the entry, emits entry_added, and records the hash.
func (a *App) storeCapture(e *Entry) {
	id, err := a.getStore().AddEntry(e)
	if err != nil {
		logf("capture: insert failed: %v", err)
		return
	}
	a.clipMu.Lock()
	a.lastStoredHash = e.SHA256
	a.clipMu.Unlock()
	a.event("entry_added", map[string]any{"id": id, "kind": e.Kind})
}

// stashSensitive stores a pending capture and emits sensitive_prompt.
// After sensitiveTTL without resolve_sensitive, it is dropped (treated as skip).
func (a *App) stashSensitive(p *pendingCapture) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		logf("capture: token gen failed: %v", err)
		return
	}
	p.token = hex.EncodeToString(buf[:])
	p.timer = time.AfterFunc(sensitiveTTL, func() {
		a.pendMu.Lock()
		if _, ok := a.pending[p.token]; ok {
			delete(a.pending, p.token)
			a.pendMu.Unlock()
			logf("capture: sensitive prompt timed out, skipped (token %s)", p.token[:8])
			return
		}
		a.pendMu.Unlock()
	})
	a.pendMu.Lock()
	a.pending[p.token] = p
	a.pendMu.Unlock()
	a.event("sensitive_prompt", map[string]any{
		"token":   p.token,
		"preview": p.preview,
		"kind":    p.kind,
	})
}

// resolveSensitive implements the resolve_sensitive method.
func (a *App) resolveSensitive(token string, record bool) error {
	a.pendMu.Lock()
	p, ok := a.pending[token]
	if ok {
		delete(a.pending, token)
	}
	a.pendMu.Unlock()
	if !ok {
		return errSensitiveExpired
	}
	p.timer.Stop()
	if !record {
		logf("capture: sensitive entry discarded by user")
		return nil
	}
	e := &Entry{
		Kind:      p.kind,
		Text:      p.text,
		HTML:      p.html,
		Preview:   p.preview,
		Image:     p.image,
		Thumb:     p.thumb,
		ImageW:    p.imageW,
		ImageH:    p.imageH,
		ImageSize: p.imageSize,
		SHA256:    p.sha,
		TextLen:   len([]rune(p.text)),
		Sensitive: true,
	}
	if e.Kind == KindText && e.Preview == "" {
		e.Preview = makePreview(e.Text)
	}
	a.storeCapture(e)
	return nil
}

var errSensitiveExpired = errText("sensitive prompt expired or unknown token")

type errText string

func (e errText) Error() string { return string(e) }

// copyEntryToClipboard implements the copy method: re-write an entry to the
// system clipboard. Text entries with captured HTML write both formats back
// on Windows; images are written as PNG.
func (a *App) copyEntryToClipboard(id int64) error {
	e, err := a.getStore().GetEntry(id)
	if err != nil {
		return err
	}
	switch e.Kind {
	case KindImage:
		if len(e.Image) == 0 {
			return errText("image data missing")
		}
		if !clipboardOK {
			return errText("clipboard unavailable")
		}
		if ch := clipboard.Write(clipboard.FmtImage, e.Image); ch != nil {
			<-ch
		}
	case KindText:
		if err := writeTextToClipboard(e.Text, e.HTML); err != nil {
			return err
		}
	default:
		return errText("unknown entry kind")
	}
	// Don't re-capture what we just wrote.
	a.setLastHash(e.SHA256)
	return nil
}

// makePreview returns the first 60 runes of text (IPC EntrySummary.preview).
func makePreview(text string) string {
	const maxRunes = 60
	r := []rune(strings.TrimSpace(text))
	r = []rune(strings.ReplaceAll(string(r), "\r", ""))
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return string(r)
}

// imagePreview renders the "图片 · 800×600" preview string.
func imagePreview(w, h int) string {
	return "图片 · " + itoa(w) + "×" + itoa(h)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// makeThumbnail scales img so the longest side <= maxSide (nearest neighbor),
// returning PNG-encoded bytes. Pure Go, no external deps.
func makeThumbnail(img image.Image, maxSide int) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	nw, nh := w, h
	if m := max(w, h); m > maxSide {
		nw = w * maxSide / m
		nh = h * maxSide / m
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil
	}
	return buf.Bytes()
}
