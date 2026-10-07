package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func testApp(t *testing.T, dir string) *App {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(filepath.Join(dir, "clipvault.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		emitter: newEmitter(json.NewEncoder(io.Discard)),
		store:   s,
		pending: make(map[string]*pendingCapture),
	}
	// importBackup swaps app.store for a fresh handle; always close the
	// current one so no db file handle leaks (Windows TempDir cleanup).
	t.Cleanup(func() { app.store.Close() })
	return app
}

func TestBackupRoundtrip(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, filepath.Join(dir, "a"))

	id1, err := a.store.AddEntry(&Entry{Kind: KindText, Text: "hello 备份", Preview: "hello 备份",
		SHA256: "s1", TextLen: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddTag(id1, "工作"); err != nil {
		t.Fatal(err)
	}
	pngBytes := makeTestPNG(t, 64, 48)
	id2, err := a.store.AddEntry(&Entry{Kind: KindImage, Preview: imagePreview(64, 48),
		Image: pngBytes, Thumb: makeThumbnail(mustDecodePNG(t, pngBytes), 320),
		ImageW: 64, ImageH: 48, ImageSize: int64(len(pngBytes)), SHA256: "s2"})
	if err != nil {
		t.Fatal(err)
	}

	bakPath := filepath.Join(dir, "test.cvb"+"ak")
	n, err := a.exportBackup(bakPath)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatal("backup should be non-empty")
	}
	// zip must contain clipvault.db + meta.json with our magic
	zr, err := zip.OpenReader(bakPath)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	zr.Close()
	if !names["clipvault.db"] || !names["meta.json"] {
		t.Fatalf("bad zip contents: %v", names)
	}

	// Import into a fresh database (pre-import auto-backup exercises too).
	b := testApp(t, filepath.Join(dir, "b"))
	imported, skipped, err := b.importBackup(bakPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d", imported, skipped)
	}
	e1, err := b.store.GetEntry(id1)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Text != "hello 备份" || len(e1.Tags) != 1 || e1.Tags[0] != "工作" {
		t.Fatalf("restored text entry mismatch: %+v", e1)
	}
	e2, err := b.store.GetEntry(id2)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ImageW != 64 || e2.ImageH != 48 || len(e2.Image) != len(pngBytes) {
		t.Fatal("restored image entry mismatch")
	}
}

func TestImportRejectsForeignFormat(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir)
	// plain zip without our meta
	p := filepath.Join(dir, "fake.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("meta.json")
	w.Write([]byte(`{"app":"someone-else","format":1}`))
	zw.Close()
	f.Close()
	if _, _, err := a.importBackup(p); err == nil {
		t.Fatal("foreign backup must be rejected")
	}
	// not a zip at all
	np := filepath.Join(dir, "nope.cvbak")
	os.WriteFile(np, []byte("garbage"), 0o644)
	if _, _, err := a.importBackup(np); err == nil {
		t.Fatal("non-zip must be rejected")
	}
}

func TestExportJSON(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir)
	pngBytes := makeTestPNG(t, 32, 32)
	if _, err := a.store.AddEntry(&Entry{Kind: KindText, Text: "导出测试",
		Preview: "导出测试", SHA256: "e1", TextLen: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddEntry(&Entry{Kind: KindImage, Preview: imagePreview(32, 32),
		Image: pngBytes, Thumb: pngBytes, ImageW: 32, ImageH: 32,
		ImageSize: int64(len(pngBytes)), SHA256: "e2"}); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "exp")
	files, err := a.exportJSON(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 { // entries.json + 1 image
		t.Fatalf("expected 2 files, got %d", files)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "entries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	entries := doc["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestAutoBackupNow(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir)
	path, err := a.autoBackupNow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if last := a.store.GetLastAutoBackupMs(); last == 0 {
		t.Fatal("last backup timestamp not recorded")
	}
}
