package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "clipvault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addText(t *testing.T, s *Store, text string) int64 {
	t.Helper()
	id, err := s.AddEntry(&Entry{
		Kind: KindText, Text: text, Preview: makePreview(text),
		SHA256: "sha-" + text, TextLen: len([]rune(text)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStoreAddGet(t *testing.T) {
	s := openTestStore(t)
	id := addText(t, s, "hello 剪贴板")
	e, err := s.GetEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "hello 剪贴板" || e.Kind != KindText {
		t.Fatalf("got %+v", e)
	}
	if e.Preview != "hello 剪贴板" {
		t.Fatalf("preview: %q", e.Preview)
	}
}

func TestStoreDefaults(t *testing.T) {
	s := openTestStore(t)
	st, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.MaxEntries != 200 || st.Hotkey != "Shift+Super+V" || st.SkipSensitive != "ask" {
		t.Fatalf("bad defaults: %+v", st)
	}
	if !st.Recording || !st.AutoBackup || st.BackupIntervalDays != 7 {
		t.Fatalf("bad defaults: %+v", st)
	}
}

func TestStoreEvictionSkipsFavorites(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpdateSettings(map[string]any{"max_entries": float64(3)}); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, addText(t, s, fmt.Sprintf("entry-%d", i)))
	}
	// Favorite the oldest surviving entry BEFORE it would be evicted:
	// entries 0,1 evicted by count (3 max), keep 2,3,4. Favorite entry 2.
	if err := s.SetFavorite(ids[2], true); err != nil {
		t.Fatal(err)
	}
	// Add 2 more; non-favorites 3,4 should be evicted, favorite 2 stays.
	addText(t, s, "entry-5")
	addText(t, s, "entry-6")

	entries, total, err := s.ListEntries(ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected 3 entries, got %d", total)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Preview] = e.Favorite
	}
	if !seen["entry-2"] {
		t.Fatal("favorited entry-2 should survive eviction")
	}
	if seen["entry-3"] || seen["entry-4"] {
		t.Fatalf("oldest non-favorites should be evicted: %v", seen)
	}
}

func TestStoreUpdateSettingsValidation(t *testing.T) {
	s := openTestStore(t)
	bad := []map[string]any{
		{"max_entries": float64(0)},
		{"max_entries": float64(2001)},
		{"max_entries": "lots"},
		{"skip_sensitive": "sometimes"},
		{"theme": "neon"},
		{"lock_action": "explode"},
		{"image_space_mb": float64(-1)},
		{"backup_interval_days": float64(0)},
		{"recording": "yes"},
		{"no_such_key": true},
	}
	for _, p := range bad {
		if _, err := s.UpdateSettings(p); err == nil {
			t.Fatalf("expected error for patch %v", p)
		}
	}
	st, err := s.UpdateSettings(map[string]any{
		"max_entries": float64(500), "theme": "dark", "skip_sensitive": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.MaxEntries != 500 || st.Theme != "dark" || st.SkipSensitive != "off" {
		t.Fatalf("patch not applied: %+v", st)
	}
}

func TestStoreTags(t *testing.T) {
	s := openTestStore(t)
	id1 := addText(t, s, "addr-1")
	id2 := addText(t, s, "addr-2")
	if _, err := s.AddTag(id1, "地址"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTag(id2, "地址"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTag(id2, "工作"); err != nil {
		t.Fatal(err)
	}
	tags, err := s.ListTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "地址" {
		t.Fatalf("tags should be frequency-ordered, got %v", tags)
	}
	entries, total, err := s.ListEntries(ListOptions{Tag: "工作", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || entries[0].ID != id2 {
		t.Fatalf("tag filter failed: %d", total)
	}
	rest, err := s.RemoveTag(id2, "工作")
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "地址" {
		t.Fatalf("remove tag failed: %v", rest)
	}
	if _, err := s.AddTag(id1, "   "); err == nil {
		t.Fatal("empty tag should be rejected")
	}
}

func TestStoreSearch(t *testing.T) {
	s := openTestStore(t)
	addText(t, s, "手机号 13800138000")
	addText(t, s, "北京市朝阳区建国路")
	entries, total, err := s.ListEntries(ListOptions{Query: "1380013", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || !strings.Contains(entries[0].Preview, "13800138000") {
		t.Fatalf("search failed: %d", total)
	}
	_, total, err = s.ListEntries(ListOptions{Query: "不存在的关键词xyz", Limit: 10})
	if err != nil || total != 0 {
		t.Fatalf("expected 0, got %d (%v)", total, err)
	}
}

func TestStoreClear(t *testing.T) {
	s := openTestStore(t)
	addText(t, s, "a")
	addText(t, s, "b")
	n, err := s.ClearRecent(60)
	if err != nil || n != 2 {
		t.Fatalf("clear_recent: %d %v", n, err)
	}
	addText(t, s, "c")
	n, err = s.ClearAll()
	if err != nil || n != 1 {
		t.Fatalf("clear_all: %d %v", n, err)
	}
	if err := s.DeleteEntry(999999); err == nil {
		t.Fatal("delete of missing id should fail")
	}
}

func TestStoreImageQuota(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpdateSettings(map[string]any{"image_space_mb": float64(1)}); err != nil {
		t.Fatal(err)
	}
	// 400KB images; quota is 1MB -> favorited + newest survive.
	mkImg := func(i int) *Entry {
		return &Entry{
			Kind: KindImage, Preview: imagePreview(100, 100),
			Image: make([]byte, 400*1024), Thumb: make([]byte, 10),
			ImageW: 100, ImageH: 100, ImageSize: 400 * 1024,
			SHA256: fmt.Sprintf("img-%d", i),
		}
	}
	idFav, err := s.AddEntry(mkImg(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(idFav, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(mkImg(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(mkImg(2)); err != nil {
		t.Fatal(err)
	}
	_, _, images, imageBytes, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	// favorited img-0 (400K) + newest img-2 (400K) = 800K <= 1MB quota:
	// img-1 must be evicted, favorite exempt.
	if images != 2 {
		t.Fatalf("expected 2 images after quota eviction, got %d (%d bytes)", images, imageBytes)
	}
	if _, err := s.GetEntry(idFav); err != nil {
		t.Fatal("favorited image must survive quota eviction")
	}
}

func TestStoreStats(t *testing.T) {
	s := openTestStore(t)
	addText(t, s, "t1")
	addText(t, s, "t2")
	if _, err := s.AddEntry(&Entry{Kind: KindImage, Preview: "图片 · 10×10",
		Image: []byte{1, 2, 3}, ImageSize: 3, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	total, texts, images, imageBytes, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || texts != 2 || images != 1 || imageBytes != 3 {
		t.Fatalf("stats: %d %d %d %d", total, texts, images, imageBytes)
	}
}
