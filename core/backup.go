package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup, export and import (IPC.md methods: export_json, export_backup,
// import_backup, auto_backup_now).
//
// .cvbak format: a zip containing
//   - clipvault.db : consistent snapshot of the SQLite file
//   - meta.json    : {"app":"clipvault-local","format":1,...}
// Only backups produced by this program are accepted on import.

const backupMagicApp = "clipvault-local"
const backupFormat = 1

type backupMeta struct {
	App       string `json:"app"`
	Format    int    `json:"format"`
	Version   string `json:"version"`
	CreatedAt int64  `json:"created_at"`
	Entries   int    `json:"entries"`
}

// exportBackup writes a .cvbak file (zip: db snapshot + meta.json).
// Returns the file size in bytes.
func (a *App) exportBackup(path string) (int64, error) {
	st := a.getStore()
	total, _, _, _, err := st.Stats()
	if err != nil {
		return 0, err
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return 0, err
	}
	// Consistent snapshot via VACUUM INTO into a temp file next to target.
	tmp, err := os.CreateTemp(filepath.Dir(path), "cvbak-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := st.vacuumInto(tmpPath); err != nil {
		return 0, fmt.Errorf("snapshot failed: %w", err)
	}
	meta := backupMeta{
		App: backupMagicApp, Format: backupFormat, Version: coreVersion,
		CreatedAt: time.Now().UnixMilli(), Entries: total,
	}
	metaJSON, _ := json.Marshal(meta)

	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	zw := zip.NewWriter(f)
	if err := writeZipEntry(zw, "clipvault.db", tmpPath); err != nil {
		zw.Close()
		f.Close()
		return 0, err
	}
	w, err := zw.Create("meta.json")
	if err != nil {
		zw.Close()
		f.Close()
		return 0, err
	}
	if _, err := w.Write(metaJSON); err != nil {
		zw.Close()
		f.Close()
		return 0, err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	logf("backup written: %s (%d bytes, %d entries)", path, fi.Size(), total)
	return fi.Size(), nil
}

func writeZipEntry(zw *zip.Writer, name, srcPath string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// importBackup validates and installs a .cvbak file. The current database is
// automatically backed up first (IPC rule). The database is REPLACED wholesale;
// returns (imported entries, skipped).
func (a *App) importBackup(path string) (imported, skipped int, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, 0, fmt.Errorf("not a valid backup file: %w", err)
	}
	defer zr.Close()

	var meta backupMeta
	var dbData []byte
	for _, f := range zr.File {
		switch f.Name {
		case "meta.json":
			rc, err := f.Open()
			if err != nil {
				return 0, 0, err
			}
			raw, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return 0, 0, err
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return 0, 0, fmt.Errorf("bad backup metadata: %w", err)
			}
		case "clipvault.db":
			rc, err := f.Open()
			if err != nil {
				return 0, 0, err
			}
			dbData, err = io.ReadAll(io.LimitReader(rc, 2<<30))
			rc.Close()
			if err != nil {
				return 0, 0, err
			}
		}
	}
	if meta.App != backupMagicApp || meta.Format != backupFormat || len(dbData) == 0 {
		return 0, 0, fmt.Errorf("unsupported backup format (only ClipVault-Local .cvbak files are accepted)")
	}

	st := a.getStore()
	dbPath := st.Path()

	// Pre-import safety backup of the current database (best effort).
	if pre, err := a.autoBackupNow(); err != nil {
		logf("import: pre-import backup failed (continuing): %v", err)
	} else {
		logf("import: current database backed up to %s", pre)
	}

	// Swap the database file.
	a.mu.Lock()
	_ = st.Close()
	if err := os.WriteFile(dbPath, dbData, 0o644); err != nil {
		a.mu.Unlock()
		return 0, 0, fmt.Errorf("cannot restore database file: %w", err)
	}
	ns, err := OpenStore(dbPath)
	if err != nil {
		a.mu.Unlock()
		return 0, 0, fmt.Errorf("restored database is corrupt: %w", err)
	}
	a.store = ns
	a.mu.Unlock()

	total, _, _, _, err := ns.Stats()
	if err != nil {
		return 0, 0, err
	}
	logf("import: restored %d entries from %s", total, path)
	a.event("settings_changed", map[string]any{})
	return total, 0, nil
}

// vacuumInto runs VACUUM INTO for a consistent file-level snapshot.
func (s *Store) vacuumInto(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	esc := strings.ReplaceAll(path, "'", "''")
	_, err := s.db.Exec("VACUUM INTO '" + esc + "'")
	return err
}

// ---- JSON export ----

type jsonExportEntry struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	Text      string   `json:"text,omitempty"`
	HTML      string   `json:"html,omitempty"`
	Preview   string   `json:"preview"`
	Tags      []string `json:"tags"`
	Favorite  bool     `json:"favorite"`
	CreatedAt int64    `json:"created_at"`
	Sensitive bool     `json:"sensitive"`
	ImageW    int      `json:"image_w,omitempty"`
	ImageH    int      `json:"image_h,omitempty"`
	ImageFile string   `json:"image_file,omitempty"`
}

// exportJSON writes entries.json + images/ into dir. Returns file count.
func (a *App) exportJSON(dir string) (int, error) {
	st := a.getStore()
	if st.IsLocked() {
		return 0, ErrLocked
	}
	if err := ensureDir(dir); err != nil {
		return 0, err
	}
	imgDir := filepath.Join(dir, "images")
	entries, _, err := st.ListEntries(ListOptions{Limit: 100000})
	if err != nil {
		return 0, err
	}
	out := make([]jsonExportEntry, 0, len(entries))
	files := 1 // entries.json itself
	for _, sum := range entries {
		e, err := st.GetEntry(sum.ID)
		if err != nil {
			return 0, err
		}
		je := jsonExportEntry{
			ID: e.ID, Kind: e.Kind, Text: e.Text, HTML: e.HTML,
			Preview: e.Preview, Tags: e.Tags, Favorite: e.Favorite,
			CreatedAt: e.CreatedAt, Sensitive: e.Sensitive,
		}
		if e.Kind == KindImage && len(e.Image) > 0 {
			if err := ensureDir(imgDir); err != nil {
				return 0, err
			}
			name := fmt.Sprintf("%d.png", e.ID)
			if err := os.WriteFile(filepath.Join(imgDir, name), e.Image, 0o644); err != nil {
				return 0, err
			}
			je.ImageW, je.ImageH = e.ImageW, e.ImageH
			je.ImageFile = "images/" + name
			files++
		}
		out = append(out, je)
	}
	doc := map[string]any{
		"app":         backupMagicApp,
		"version":     coreVersion,
		"exported_at": time.Now().UnixMilli(),
		"entries":     out,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "entries.json"), raw, 0o644); err != nil {
		return 0, err
	}
	logf("export: %d entries -> %s (%d files)", len(out), dir, files)
	return files, nil
}

// saveImageToPath writes an entry's original image to path.
func (a *App) saveImageToPath(id int64, path string) error {
	e, err := a.getStore().GetEntry(id)
	if err != nil {
		return err
	}
	if e.Kind != KindImage || len(e.Image) == 0 {
		return fmt.Errorf("entry %d has no image", id)
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, e.Image, 0o644)
}

// ---- automatic backup ----

// autoBackupNow creates a timestamped .cvbak next to the database.
func (a *App) autoBackupNow() (string, error) {
	st := a.getStore()
	name := "clipvault-backup-" + time.Now().Format("20060102-150405") + ".cvbak"
	path := filepath.Join(filepath.Dir(st.Path()), name)
	if _, err := a.exportBackup(path); err != nil {
		return "", err
	}
	st.SetLastAutoBackupMs(time.Now().UnixMilli())
	return path, nil
}

// runAutoBackupLoop performs the scheduled backup check.
func (a *App) runAutoBackupLoop() {
	a.maybeAutoBackup()
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for range t.C {
		a.maybeAutoBackup()
	}
}

func (a *App) maybeAutoBackup() {
	st := a.getStore()
	settings, err := st.GetSettings()
	if err != nil || !settings.AutoBackup {
		return
	}
	last := st.GetLastAutoBackupMs()
	interval := time.Duration(settings.BackupIntervalDays) * 24 * time.Hour
	if last > 0 && time.Since(time.UnixMilli(last)) < interval {
		return
	}
	path, err := a.autoBackupNow()
	if err != nil {
		logf("auto backup failed: %v", err)
		return
	}
	logf("auto backup done: %s", path)
	a.event("backup_done", map[string]any{"path": path})
}
