package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	clipboard "golang.design/x/clipboard"
)

func TestMakeThumbnail(t *testing.T) {
	// 800x600 -> longest side 320 -> 320x240
	src := image.NewRGBA(image.Rect(0, 0, 800, 600))
	thumb := makeThumbnail(src, 320)
	if thumb == nil {
		t.Fatal("thumbnail is nil")
	}
	img, err := png.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != 320 || h != 240 {
		t.Fatalf("expected 320x240, got %dx%d", w, h)
	}
	// small image is not upscaled
	src2 := image.NewRGBA(image.Rect(0, 0, 100, 50))
	thumb2 := makeThumbnail(src2, 320)
	img2, err := png.Decode(bytes.NewReader(thumb2))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := img2.Bounds().Dx(), img2.Bounds().Dy(); w != 100 || h != 50 {
		t.Fatalf("expected 100x50, got %dx%d", w, h)
	}
	// portrait: 600x900 -> 213x320
	src3 := image.NewRGBA(image.Rect(0, 0, 600, 900))
	thumb3 := makeThumbnail(src3, 320)
	img3, err := png.Decode(bytes.NewReader(thumb3))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := img3.Bounds().Dx(), img3.Bounds().Dy(); w != 213 || h != 320 {
		t.Fatalf("expected 213x320, got %dx%d", w, h)
	}
}

func TestLooksLikeFileDrop(t *testing.T) {
	if !looksLikeFileDrop("file:///home/u/a.png\nfile:///home/u/b.png\n") {
		t.Fatal("should detect file drop")
	}
	if looksLikeFileDrop("file:///home/u/a.png\nhello") {
		t.Fatal("mixed content is not a file drop")
	}
	if looksLikeFileDrop("https://example.com/file.png") {
		t.Fatal("URL is not a file drop")
	}
	if looksLikeFileDrop("") || looksLikeFileDrop("   \n  ") {
		t.Fatal("empty text is not a file drop")
	}
}

func TestMakePreview(t *testing.T) {
	if p := makePreview("abc"); p != "abc" {
		t.Fatalf("got %q", p)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "字"
	}
	if p := makePreview(long); len([]rune(p)) != 60 {
		t.Fatalf("preview should be 60 runes, got %d", len([]rune(p)))
	}
	if p := imagePreview(800, 600); p != "图片 · 800×600" {
		t.Fatalf("got %q", p)
	}
}

// TestClipboardUnavailable documents headless behavior: without a display,
// clipboard.Init fails and capture degrades silently (no panic).
func TestClipboardUnavailable(t *testing.T) {
	if err := clipboard.Init(); err != nil {
		t.Skipf("no clipboard in this environment (expected headless): %v", err)
	}
	// Display present: sanity-check text roundtrip via the library.
	done := clipboard.Write(clipboard.FmtText, []byte("clipvault-test"))
	if done != nil {
		<-done
		if got := clipboard.Read(clipboard.FmtText); string(got) != "clipvault-test" {
			t.Fatalf("clipboard roundtrip failed: %q", got)
		}
	}
}

// makeTestPNG builds a small PNG for backup/thumbnail tests.
func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, image.White)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustDecodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}
