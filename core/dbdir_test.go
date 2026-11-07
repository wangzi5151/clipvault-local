package main

import (
	"path/filepath"
	"testing"
)

// TestDBDirSwitchMigratesAndReset is the regression test for the stuck
// db_dir bug: switching used to leave the old setting behind, so choosing
// "restore default" could not actually move back.
func TestDBDirSwitchMigratesAndReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultDir := defaultDBDir()
	customDir := filepath.Join(t.TempDir(), "custom-db")

	app := testApp(t, defaultDir)
	id1 := addText(t, app.store, "before-switch")

	res, rerr := app.mSetSettings(map[string]any{"db_dir": customDir})
	if rerr != nil {
		t.Fatalf("switch to custom db_dir: %v", rerr)
	}
	st := res.(Settings)
	if got, want := app.store.Path(), filepath.Join(customDir, "clipvault.db"); got != want {
		t.Fatalf("active db = %q, want %q", got, want)
	}
	if st.DBDir != customDir {
		t.Fatalf("returned db_dir = %q, want %q", st.DBDir, customDir)
	}
	if _, err := app.store.GetEntry(id1); err != nil {
		t.Fatalf("entry was not migrated to the custom dir: %v", err)
	}

	// The default database must remember the custom location for the next launch.
	defStore, err := OpenStore(filepath.Join(defaultDir, "clipvault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defSettings, _ := defStore.GetSettings()
	defStore.Close()
	if defSettings.DBDir != customDir {
		t.Fatalf("default pointer = %q, want %q", defSettings.DBDir, customDir)
	}

	id2 := addText(t, app.store, "after-switch")

	// Reset must migrate the new data back and clear the pointer.
	res, rerr = app.mSetSettings(map[string]any{"db_dir": ""})
	if rerr != nil {
		t.Fatalf("reset db_dir: %v", rerr)
	}
	st = res.(Settings)
	if got, want := app.store.Path(), filepath.Join(defaultDir, "clipvault.db"); got != want {
		t.Fatalf("active db after reset = %q, want %q", got, want)
	}
	if st.DBDir != "" {
		t.Fatalf("reset db_dir = %q, want empty", st.DBDir)
	}
	for _, id := range []int64{id1, id2} {
		if _, err := app.store.GetEntry(id); err != nil {
			t.Fatalf("entry %d missing after reset: %v", id, err)
		}
	}

	// Simulate a restart: the default DB must no longer point at the custom dir.
	restarted, err := OpenStore(filepath.Join(defaultDir, "clipvault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	rst, _ := restarted.GetSettings()
	if rst.DBDir != "" {
		t.Fatalf("restart db_dir = %q, want empty", rst.DBDir)
	}
	_, total, err := restarted.ListEntries(ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("restart sees %d entries, want 2", total)
	}
}

// TestDBDirSwitchSameDirectoryPersists makes sure setting db_dir to the
// current directory does not unnecessarily migrate the database.
func TestDBDirSwitchSameDirectoryPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(t.TempDir(), "existing")
	app := testApp(t, dir)
	id := addText(t, app.store, "stay-put")

	res, rerr := app.mSetSettings(map[string]any{"db_dir": dir})
	if rerr != nil {
		t.Fatalf("set db_dir to current dir: %v", rerr)
	}
	st := res.(Settings)
	if st.DBDir != dir {
		t.Fatalf("db_dir = %q, want %q", st.DBDir, dir)
	}
	if _, err := app.store.GetEntry(id); err != nil {
		t.Fatalf("entry disappeared: %v", err)
	}
	if got := app.store.Path(); got != filepath.Join(dir, "clipvault.db") {
		t.Fatalf("active db = %q", got)
	}
}

// TestImportEncryptedBackup covers the recovery path that was previously
// untested: export an encrypted database, import it elsewhere, then unlock
// it with the original password.
func TestImportEncryptedBackup(t *testing.T) {
	base := t.TempDir()
	src := testApp(t, filepath.Join(base, "src"))
	id := addText(t, src.store, "encrypted 秘密")
	if err := src.store.SetPassword("correct-horse"); err != nil {
		t.Fatal(err)
	}
	bak := filepath.Join(base, "encrypted.cvb")
	if _, err := src.exportBackup(bak); err != nil {
		t.Fatal(err)
	}

	dst := testApp(t, filepath.Join(base, "dst"))
	imported, skipped, err := dst.importBackup(bak)
	if err != nil {
		t.Fatalf("import encrypted backup: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d, want 1/0", imported, skipped)
	}
	if !dst.store.IsLocked() {
		t.Fatal("imported encrypted db should start locked")
	}
	if err := dst.store.SetPassword("wrong"); err == nil {
		t.Fatal("wrong password should not unlock the imported db")
	}
	if err := dst.store.SetPassword("correct-horse"); err != nil {
		t.Fatalf("unlock imported encrypted db: %v", err)
	}
	e, err := dst.store.GetEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "encrypted 秘密" {
		t.Fatalf("decrypted text = %q", e.Text)
	}
}
