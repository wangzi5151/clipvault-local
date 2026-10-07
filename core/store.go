package main

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry kinds.
const (
	KindText  = "text"
	KindImage = "image"
)

// Settings keys in the settings table.
const (
	skRecording          = "recording"
	skMaxEntries         = "max_entries"
	skRecordImages       = "record_images"
	skImageSpaceMB       = "image_space_mb"
	skRecordText         = "record_text"
	skHotkey             = "hotkey"
	skTheme              = "theme"
	skAutostart          = "autostart"
	skSkipSensitive      = "skip_sensitive"
	skDBDir              = "db_dir"
	skAutoBackup         = "auto_backup"
	skBackupIntervalDays = "backup_interval_days"
	skLockAction         = "lock_action"
	skOnboarded          = "onboarded"
	skCryptoSalt         = "crypto_salt"
	skCryptoCheck        = "crypto_check"
	skLastAutoBackup     = "last_auto_backup_ms"
)

// Settings mirrors the IPC Settings object 1:1 (JSON field names must not change).
type Settings struct {
	Recording          bool   `json:"recording"`
	MaxEntries         int    `json:"max_entries"`
	RecordImages       bool   `json:"record_images"`
	ImageSpaceMB       int    `json:"image_space_mb"`
	RecordText         bool   `json:"record_text"`
	Hotkey             string `json:"hotkey"`
	Theme              string `json:"theme"`
	Autostart          bool   `json:"autostart"`
	SkipSensitive      string `json:"skip_sensitive"`
	DBDir              string `json:"db_dir"`
	AutoBackup         bool   `json:"auto_backup"`
	BackupIntervalDays int    `json:"backup_interval_days"`
	LockAction         string `json:"lock_action"`
	DBPasswordSet      bool   `json:"db_password_set"`
	Onboarded          bool   `json:"onboarded"`
}

// DefaultSettings returns the IPC-specified defaults.
func DefaultSettings() Settings {
	return Settings{
		Recording:          true,
		MaxEntries:         200,
		RecordImages:       true,
		ImageSpaceMB:       200,
		RecordText:         true,
		Hotkey:             "Shift+Super+V",
		Theme:              "auto",
		Autostart:          false,
		SkipSensitive:      "ask",
		DBDir:              "",
		AutoBackup:         true,
		BackupIntervalDays: 7,
		LockAction:         "none",
		DBPasswordSet:      false,
		Onboarded:          false,
	}
}

// Entry is a full clipboard history record.
type Entry struct {
	ID        int64
	Kind      string
	Text      string // "" for images
	HTML      string // captured "HTML Format" (Windows), "" otherwise
	Preview   string
	Image     []byte // original PNG bytes
	Thumb     []byte // thumbnail PNG bytes (longest side <= 320)
	ImageW    int
	ImageH    int
	ImageSize int64 // original PNG byte length, plaintext (quota accounting)
	SHA256    string
	CreatedAt int64 // ms
	Favorite  bool
	Tags      []string
	TextLen   int // rune count
	Sensitive bool
}

// EntrySummary mirrors the IPC EntrySummary object.
type EntrySummary struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	Preview   string   `json:"preview"`
	CreatedAt int64    `json:"created_at"`
	Favorite  bool     `json:"favorite"`
	Tags      []string `json:"tags"`
	HasImage  bool     `json:"has_image"`
	TextLen   int      `json:"text_len"`
	Sensitive bool     `json:"sensitive"`
}

// EntryDetail mirrors the IPC EntryDetail object.
type EntryDetail struct {
	EntrySummary
	Text       string `json:"text"`
	ImageThumb string `json:"image_thumb"` // base64 PNG, longest side <= 320
	ImageFull  string `json:"image_full"`  // base64 original PNG
	ImageW     int    `json:"image_w"`
	ImageH     int    `json:"image_h"`
}

// ListOptions mirrors the IPC list params.
type ListOptions struct {
	Limit         int
	Offset        int
	Query         string
	Tag           string
	Kind          string
	FavoritesOnly bool
}

// ErrLocked is returned when the database is encrypted but no key is loaded.
var ErrLocked = errors.New("database is locked")

const schema = `
CREATE TABLE IF NOT EXISTS entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT '',
	html TEXT NOT NULL DEFAULT '',
	preview TEXT NOT NULL DEFAULT '',
	image BLOB,
	thumb BLOB,
	image_w INTEGER NOT NULL DEFAULT 0,
	image_h INTEGER NOT NULL DEFAULT 0,
	image_size INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	favorite INTEGER NOT NULL DEFAULT 0,
	text_len INTEGER NOT NULL DEFAULT 0,
	sensitive INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_entries_created ON entries(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_entries_kind ON entries(kind);
CREATE TABLE IF NOT EXISTS entry_tags (
	entry_id INTEGER NOT NULL,
	tag TEXT NOT NULL,
	PRIMARY KEY (entry_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_entry_tags_tag ON entry_tags(tag);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);
`

// Store is a concurrency-safe wrapper around the single SQLite file.
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	path   string
	key    []byte // nil = no key loaded (plaintext DB, or locked encrypted DB)
	salt   []byte // nil = not encrypted
	closed bool
}

// OpenStore opens (creating if needed) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema init: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.loadCryptoState(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.key = nil
	return s.db.Close()
}

// IsLocked reports whether the DB is encrypted at rest but no key is loaded.
func (s *Store) IsLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.salt != nil && s.key == nil
}

// IsEncrypted reports whether the DB is encrypted at rest.
func (s *Store) IsEncrypted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.salt != nil
}

func (s *Store) loadCryptoState() error {
	saltB64, _ := s.getSettingRaw(skCryptoSalt)
	if saltB64 == "" {
		s.salt = nil
		return nil
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return fmt.Errorf("bad crypto salt: %w", err)
	}
	s.salt = salt
	s.key = nil // key must be (re-)provided via set_db_password
	return nil
}

// ---- settings ----

func (s *Store) getSettingRaw(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) setSettingRaw(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func parseBool(v string, def bool) bool {
	switch strings.ToLower(v) {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return def
}

func parseInt(v string, def int) int {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// GetSettings reads the full Settings, applying IPC defaults.
func (s *Store) GetSettings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DefaultSettings()
	get := func(k string) string { v, _ := s.getSettingRaw(k); return v }
	st.Recording = parseBool(get(skRecording), true)
	st.MaxEntries = parseInt(get(skMaxEntries), 200)
	st.RecordImages = parseBool(get(skRecordImages), true)
	st.ImageSpaceMB = parseInt(get(skImageSpaceMB), 200)
	st.RecordText = parseBool(get(skRecordText), true)
	if v := get(skHotkey); v != "" || hasKey(s, skHotkey) {
		st.Hotkey = v
	}
	if v := get(skTheme); v != "" {
		st.Theme = v
	}
	st.Autostart = parseBool(get(skAutostart), false)
	if v := get(skSkipSensitive); v != "" {
		st.SkipSensitive = v
	}
	st.DBDir = get(skDBDir)
	st.AutoBackup = parseBool(get(skAutoBackup), true)
	st.BackupIntervalDays = parseInt(get(skBackupIntervalDays), 7)
	if v := get(skLockAction); v != "" {
		st.LockAction = v
	}
	st.DBPasswordSet = s.salt != nil
	st.Onboarded = parseBool(get(skOnboarded), false)
	return st, nil
}

func hasKey(s *Store, k string) bool {
	var c int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, k).Scan(&c)
	return c > 0
}

// UpdateSettings applies a patch map (from set_settings params) with
// validation. Unknown keys and illegal values return an error.
func (s *Store) UpdateSettings(patch map[string]any) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DefaultSettings()
	// load current into st first
	cur, err := s.getSettingsLocked()
	if err != nil {
		return st, err
	}
	st = cur

	setStr := func(k string, v any) (string, error) {
		sv, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("setting %s must be a string", k)
		}
		return sv, nil
	}
	setBool := func(k string, v any) (bool, error) {
		b, ok := v.(bool)
		if !ok {
			return false, fmt.Errorf("setting %s must be a boolean", k)
		}
		return b, nil
	}
	setInt := func(k string, v any) (int, error) {
		switch n := v.(type) {
		case float64:
			return int(n), nil
		case int:
			return n, nil
		default:
			return 0, fmt.Errorf("setting %s must be a number", k)
		}
	}

	changedMax, changedImg := false, false
	for k, v := range patch {
		switch k {
		case skRecording:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.Recording = b
			_ = s.setSettingRaw(k, boolStr(b))
		case skMaxEntries:
			n, err := setInt(k, v)
			if err != nil {
				return st, err
			}
			if n < 1 || n > 2000 {
				return st, fmt.Errorf("max_entries must be between 1 and 2000")
			}
			st.MaxEntries = n
			changedMax = true
			_ = s.setSettingRaw(k, fmt.Sprint(n))
		case skRecordImages:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.RecordImages = b
			_ = s.setSettingRaw(k, boolStr(b))
		case skImageSpaceMB:
			n, err := setInt(k, v)
			if err != nil {
				return st, err
			}
			if n < 0 {
				return st, fmt.Errorf("image_space_mb must be >= 0")
			}
			st.ImageSpaceMB = n
			changedImg = true
			_ = s.setSettingRaw(k, fmt.Sprint(n))
		case skRecordText:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.RecordText = b
			_ = s.setSettingRaw(k, boolStr(b))
		case skHotkey:
			sv, err := setStr(k, v)
			if err != nil {
				return st, err
			}
			st.Hotkey = sv
			_ = s.setSettingRaw(k, sv)
		case skTheme:
			sv, err := setStr(k, v)
			if err != nil {
				return st, err
			}
			if sv != "auto" && sv != "dark" && sv != "light" {
				return st, fmt.Errorf("theme must be auto, dark or light")
			}
			st.Theme = sv
			_ = s.setSettingRaw(k, sv)
		case skAutostart:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.Autostart = b
			_ = s.setSettingRaw(k, boolStr(b))
		case skSkipSensitive:
			sv, err := setStr(k, v)
			if err != nil {
				return st, err
			}
			if sv != "ask" && sv != "auto" && sv != "off" {
				return st, fmt.Errorf("skip_sensitive must be ask, auto or off")
			}
			st.SkipSensitive = sv
			_ = s.setSettingRaw(k, sv)
		case skDBDir:
			sv, err := setStr(k, v)
			if err != nil {
				return st, err
			}
			st.DBDir = sv
			_ = s.setSettingRaw(k, sv)
		case skAutoBackup:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.AutoBackup = b
			_ = s.setSettingRaw(k, boolStr(b))
		case skBackupIntervalDays:
			n, err := setInt(k, v)
			if err != nil {
				return st, err
			}
			if n < 1 {
				return st, fmt.Errorf("backup_interval_days must be >= 1")
			}
			st.BackupIntervalDays = n
			_ = s.setSettingRaw(k, fmt.Sprint(n))
		case skLockAction:
			sv, err := setStr(k, v)
			if err != nil {
				return st, err
			}
			if sv != "none" && sv != "pause" && sv != "clear" {
				return st, fmt.Errorf("lock_action must be none, pause or clear")
			}
			st.LockAction = sv
			_ = s.setSettingRaw(k, sv)
		case skOnboarded:
			b, err := setBool(k, v)
			if err != nil {
				return st, err
			}
			st.Onboarded = b
			_ = s.setSettingRaw(k, boolStr(b))
		default:
			return st, fmt.Errorf("unknown setting: %s", k)
		}
	}
	if changedMax {
		s.enforceCountLimitLocked(st.MaxEntries)
	}
	if changedImg {
		s.enforceImageLimitLocked(int64(st.ImageSpaceMB) * 1024 * 1024)
	}
	st.DBPasswordSet = s.salt != nil
	return st, nil
}

func (s *Store) getSettingsLocked() (Settings, error) {
	st := DefaultSettings()
	get := func(k string) string { v, _ := s.getSettingRaw(k); return v }
	st.Recording = parseBool(get(skRecording), true)
	st.MaxEntries = parseInt(get(skMaxEntries), 200)
	st.RecordImages = parseBool(get(skRecordImages), true)
	st.ImageSpaceMB = parseInt(get(skImageSpaceMB), 200)
	st.RecordText = parseBool(get(skRecordText), true)
	if v := get(skHotkey); v != "" || hasKey(s, skHotkey) {
		st.Hotkey = v
	}
	if v := get(skTheme); v != "" {
		st.Theme = v
	}
	st.Autostart = parseBool(get(skAutostart), false)
	if v := get(skSkipSensitive); v != "" {
		st.SkipSensitive = v
	}
	st.DBDir = get(skDBDir)
	st.AutoBackup = parseBool(get(skAutoBackup), true)
	st.BackupIntervalDays = parseInt(get(skBackupIntervalDays), 7)
	if v := get(skLockAction); v != "" {
		st.LockAction = v
	}
	st.DBPasswordSet = s.salt != nil
	st.Onboarded = parseBool(get(skOnboarded), false)
	return st, nil
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ---- entries ----

// encryptEntryFields encrypts content columns when a key is loaded.
func (s *Store) encryptEntryFields(e *Entry) (text, html, preview string, image, thumb []byte, err error) {
	if s.key == nil {
		return e.Text, e.HTML, e.Preview, e.Image, e.Thumb, nil
	}
	if text, err = encryptText(s.key, e.Text); err != nil {
		return
	}
	if html, err = encryptText(s.key, e.HTML); err != nil {
		return
	}
	if preview, err = encryptText(s.key, e.Preview); err != nil {
		return
	}
	if image, err = encryptValue(s.key, e.Image); err != nil {
		return
	}
	if thumb, err = encryptValue(s.key, e.Thumb); err != nil {
		return
	}
	return text, html, preview, image, thumb, nil
}

// AddEntry inserts a new entry and enforces count/image quotas.
// Returns the new row id.
func (s *Store) AddEntry(e *Entry) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt != nil && s.key == nil {
		return 0, ErrLocked
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().UnixMilli()
	}
	text, html, preview, image, thumb, err := s.encryptEntryFields(e)
	if err != nil {
		return 0, err
	}
	fav := 0
	if e.Favorite {
		fav = 1
	}
	sens := 0
	if e.Sensitive {
		sens = 1
	}
	res, err := s.db.Exec(`INSERT INTO entries
		(kind,text,html,preview,image,thumb,image_w,image_h,image_size,sha256,created_at,favorite,text_len,sensitive)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Kind, text, html, preview, image, thumb, e.ImageW, e.ImageH, e.ImageSize,
		e.SHA256, e.CreatedAt, fav, e.TextLen, sens)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	for _, t := range e.Tags {
		if t = cleanTag(t); t != "" {
			_, _ = s.db.Exec(`INSERT OR IGNORE INTO entry_tags(entry_id,tag) VALUES(?,?)`, id, t)
		}
	}
	st, _ := s.getSettingsLocked()
	s.enforceCountLimitLocked(st.MaxEntries)
	s.enforceImageLimitLocked(int64(st.ImageSpaceMB) * 1024 * 1024)
	return id, nil
}

// enforceCountLimitLocked deletes oldest non-favorite entries over maxEntries.
func (s *Store) enforceCountLimitLocked(maxEntries int) {
	for {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil || count <= maxEntries {
			return
		}
		res, err := s.db.Exec(`DELETE FROM entries WHERE id = (
			SELECT id FROM entries WHERE favorite=0 ORDER BY created_at ASC, id ASC LIMIT 1)`)
		if err != nil {
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return // everything left is favorited; keep it
		}
	}
}

// enforceImageLimitLocked deletes oldest non-favorite image entries over the byte budget.
func (s *Store) enforceImageLimitLocked(maxBytes int64) {
	for {
		var total sql.NullInt64
		if err := s.db.QueryRow(`SELECT SUM(image_size) FROM entries WHERE kind='image'`).Scan(&total); err != nil || !total.Valid || total.Int64 <= maxBytes {
			return
		}
		res, err := s.db.Exec(`DELETE FROM entries WHERE id = (
			SELECT id FROM entries WHERE kind='image' AND favorite=0 ORDER BY created_at ASC, id ASC LIMIT 1)`)
		if err != nil {
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return
		}
	}
}

// cleanTag normalizes a tag.
func cleanTag(t string) string {
	t = strings.TrimSpace(t)
	if len([]rune(t)) > 20 {
		t = string([]rune(t)[:20])
	}
	return t
}

// decryptRow converts a DB row into an Entry, decrypting when a key is loaded.
func (s *Store) decryptRow(id int64, kind, text, html, preview string, image, thumb []byte,
	imageW, imageH int, imageSize int64, sha256 string, createdAt int64,
	favorite, textLen, sensitive int) (*Entry, error) {
	e := &Entry{
		ID: id, Kind: kind, ImageW: imageW, ImageH: imageH,
		ImageSize: imageSize, SHA256: sha256, CreatedAt: createdAt,
		Favorite: favorite != 0, TextLen: textLen, Sensitive: sensitive != 0,
	}
	if s.key != nil {
		var err error
		if e.Text, err = decryptText(s.key, text); err != nil {
			return nil, err
		}
		if e.HTML, err = decryptText(s.key, html); err != nil {
			return nil, err
		}
		if e.Preview, err = decryptText(s.key, preview); err != nil {
			return nil, err
		}
		if e.Image, err = decryptBlob(s.key, image); err != nil {
			return nil, err
		}
		if e.Thumb, err = decryptBlob(s.key, thumb); err != nil {
			return nil, err
		}
	} else {
		e.Text, e.HTML, e.Preview = text, html, preview
		e.Image, e.Thumb = image, thumb
	}
	return e, nil
}

func (s *Store) loadTags(id int64) []string {
	rows, err := s.db.Query(`SELECT tag FROM entry_tags WHERE entry_id=? ORDER BY tag`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			tags = append(tags, t)
		}
	}
	if tags == nil {
		tags = []string{}
	}
	return tags
}

// GetEntry returns a full entry by id.
func (s *Store) GetEntry(id int64) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt != nil && s.key == nil {
		return nil, ErrLocked
	}
	var e Entry
	var text, html, preview string
	var image, thumb []byte
	var fav, sens int
	err := s.db.QueryRow(`SELECT id,kind,text,html,preview,image,thumb,image_w,image_h,
		image_size,sha256,created_at,favorite,text_len,sensitive FROM entries WHERE id=?`, id).
		Scan(&e.ID, &e.Kind, &text, &html, &preview, &image, &thumb, &e.ImageW, &e.ImageH,
			&e.ImageSize, &e.SHA256, &e.CreatedAt, &fav, &e.TextLen, &sens)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	full, err := s.decryptRow(e.ID, e.Kind, text, html, preview, image, thumb, e.ImageW, e.ImageH,
		e.ImageSize, e.SHA256, e.CreatedAt, fav, e.TextLen, sens)
	if err != nil {
		return nil, err
	}
	full.Tags = s.loadTags(id)
	return full, nil
}

// ListEntries returns summaries matching opts plus the total match count.
func (s *Store) ListEntries(opts ListOptions) ([]EntrySummary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt != nil && s.key == nil {
		return nil, 0, ErrLocked
	}
	var where []string
	var args []any
	if opts.Kind == KindText || opts.Kind == KindImage {
		where = append(where, "e.kind=?")
		args = append(args, opts.Kind)
	}
	if opts.FavoritesOnly {
		where = append(where, "e.favorite=1")
	}
	if opts.Tag != "" {
		where = append(where, "EXISTS(SELECT 1 FROM entry_tags t WHERE t.entry_id=e.id AND t.tag=?)")
		args = append(args, opts.Tag)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}
	query := `SELECT e.id,e.kind,e.preview,e.created_at,e.favorite,e.text_len,e.sensitive,
		e.image_w,e.image_h,e.text,e.html
		FROM entries e ` + whereClause + ` ORDER BY e.created_at DESC, e.id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	// NOTE: rows are fully materialized and closed BEFORE any nested
	// query (loadTags) runs: with MaxOpenConns(1) a nested Query while
	// rows are open would deadlock.
	type listRow struct {
		id, createdAt      int64
		kind, preview      string
		text, html         string
		fav, textLen, sens int
		iw, ih             int
	}
	var allRows []listRow
	for rows.Next() {
		var r listRow
		if err := rows.Scan(&r.id, &r.kind, &r.preview, &r.createdAt, &r.fav,
			&r.textLen, &r.sens, &r.iw, &r.ih, &r.text, &r.html); err != nil {
			rows.Close()
			return nil, 0, err
		}
		allRows = append(allRows, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()

	q := strings.ToLower(opts.Query)
	var out []EntrySummary
	total := 0
	for _, r := range allRows {
		preview, text := r.preview, r.text
		if s.key != nil {
			var err error
			if preview, err = decryptText(s.key, preview); err != nil {
				return nil, 0, err
			}
			// query match needs plaintext text
			if text, err = decryptText(s.key, text); err != nil {
				return nil, 0, err
			}
		}
		if q != "" {
			hay := strings.ToLower(text + "\n" + preview)
			matched := strings.Contains(hay, q)
			if !matched {
				for _, t := range s.loadTags(r.id) {
					if strings.Contains(strings.ToLower(t), q) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}
		total++
		if opts.Offset > 0 && total <= opts.Offset {
			continue
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			continue
		}
		sum := EntrySummary{
			ID: r.id, Kind: r.kind, Preview: preview, CreatedAt: r.createdAt,
			Favorite: r.fav != 0, Tags: s.loadTags(r.id), HasImage: r.kind == KindImage,
			TextLen: r.textLen, Sensitive: r.sens != 0,
		}
		out = append(out, sum)
	}
	if out == nil {
		out = []EntrySummary{}
	}
	return out, total, nil
}

// DeleteEntry removes one entry.
func (s *Store) DeleteEntry(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM entries WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("entry %d not found", id)
	}
	_, _ = s.db.Exec(`DELETE FROM entry_tags WHERE entry_id=?`, id)
	return nil
}

// ClearAll deletes everything, favorites included. Returns rows deleted.
func (s *Store) ClearAll() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM entries`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, _ = s.db.Exec(`DELETE FROM entry_tags`)
	return int(n), nil
}

// ClearRecent deletes entries created within the last minutes. Returns rows deleted.
func (s *Store) ClearRecent(minutes int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	res, err := s.db.Exec(`DELETE FROM entries WHERE created_at>=?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, _ = s.db.Exec(`DELETE FROM entry_tags WHERE entry_id NOT IN (SELECT id FROM entries)`)
	return int(n), nil
}

// SetFavorite toggles the favorite flag.
func (s *Store) SetFavorite(id int64, favorite bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fav := 0
	if favorite {
		fav = 1
	}
	res, err := s.db.Exec(`UPDATE entries SET favorite=? WHERE id=?`, fav, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("entry %d not found", id)
	}
	return nil
}

// AddTag adds a tag to an entry and returns its current tag list.
func (s *Store) AddTag(id int64, tag string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tag = cleanTag(tag)
	if tag == "" {
		return nil, errors.New("tag must not be empty")
	}
	var c int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE id=?`, id).Scan(&c); err != nil || c == 0 {
		return nil, fmt.Errorf("entry %d not found", id)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO entry_tags(entry_id,tag) VALUES(?,?)`, id, tag); err != nil {
		return nil, err
	}
	return s.loadTags(id), nil
}

// RemoveTag removes a tag from an entry and returns its current tag list.
func (s *Store) RemoveTag(id int64, tag string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM entry_tags WHERE entry_id=? AND tag=?`, id, tag)
	if err != nil {
		return nil, err
	}
	return s.loadTags(id), nil
}

// ListTags returns all tags ordered by usage frequency.
func (s *Store) ListTags() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT tag, COUNT(*) c FROM entry_tags GROUP BY tag ORDER BY c DESC, tag ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err == nil {
			tags = append(tags, t)
		}
	}
	if tags == nil {
		tags = []string{}
	}
	return tags, nil
}

// Stats returns entry counts and image bytes for get_state.
func (s *Store) Stats() (total, texts, images int, imageBytes int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&total); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE kind='text'`).Scan(&texts); err != nil {
		return
	}
	var nb sql.NullInt64
	if err = s.db.QueryRow(`SELECT COUNT(*), SUM(image_size) FROM entries WHERE kind='image'`).Scan(&images, &nb); err != nil {
		return
	}
	if nb.Valid {
		imageBytes = nb.Int64
	}
	return
}

// SetRecording flips the recording setting.
func (s *Store) SetRecording(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setSettingRaw(skRecording, boolStr(enabled))
}

// GetLastAutoBackupMs returns the last auto-backup timestamp (ms), 0 if never.
func (s *Store) GetLastAutoBackupMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.getSettingRaw(skLastAutoBackup)
	return int64(parseInt(v, 0))
}

// SetLastAutoBackupMs records the last auto-backup timestamp.
func (s *Store) SetLastAutoBackupMs(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.setSettingRaw(skLastAutoBackup, fmt.Sprint(ms))
}

// ---- whole-database password encryption ----

// SetPassword encrypts the whole DB with password, or decrypts when password
// is "". When the DB is already encrypted and the password verifies against
// crypto_check, it acts as an unlock (loads the key) without rewriting rows.
func (s *Store) SetPassword(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if password == "" {
		if s.salt == nil {
			return nil // nothing to do
		}
		return s.decryptAllLocked()
	}
	if s.salt != nil {
		key, err := deriveKey(password, s.salt)
		if err != nil {
			return err
		}
		checkB64, _ := s.getSettingRaw(skCryptoCheck)
		raw, err := base64.StdEncoding.DecodeString(checkB64)
		if err != nil {
			return fmt.Errorf("bad crypto check: %w", err)
		}
		plain, err := decryptValue(key, raw)
		if err != nil || string(plain) != checkPlaintext {
			return errWrongPassword
		}
		s.key = key // unlock
		return nil
	}
	// First-time encryption.
	salt, err := newSalt()
	if err != nil {
		return err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	if err := s.rewriteAllLocked(key, true); err != nil {
		return err
	}
	check, err := encryptValue(key, []byte(checkPlaintext))
	if err != nil {
		return err
	}
	if err := s.setSettingRaw(skCryptoSalt, base64.StdEncoding.EncodeToString(salt)); err != nil {
		return err
	}
	if err := s.setSettingRaw(skCryptoCheck, base64.StdEncoding.EncodeToString(check)); err != nil {
		return err
	}
	s.salt = salt
	s.key = key
	return nil
}

// decryptAllLocked decrypts every row and clears crypto state. Caller holds s.mu.
func (s *Store) decryptAllLocked() error {
	if s.key == nil {
		return ErrLocked
	}
	if err := s.rewriteAllLocked(nil, false); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM settings WHERE key IN (?,?)`, skCryptoSalt, skCryptoCheck)
	s.salt = nil
	s.key = nil
	return nil
}

// rewriteAllLocked re-encrypts (encrypt=true) or decrypts (encrypt=false)
// every entry row in a transaction. Caller holds s.mu.
func (s *Store) rewriteAllLocked(key []byte, encrypt bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,text,html,preview,image,thumb FROM entries`)
	if err != nil {
		return err
	}
	type row struct {
		id           int64
		text, html   string
		preview      string
		image, thumb []byte
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text, &r.html, &r.preview, &r.image, &r.thumb); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range all {
		var text, html, preview string
		var image, thumb []byte
		if encrypt {
			if text, err = encryptText(key, r.text); err != nil {
				return err
			}
			if html, err = encryptText(key, r.html); err != nil {
				return err
			}
			if preview, err = encryptText(key, r.preview); err != nil {
				return err
			}
			if image, err = encryptValue(key, r.image); err != nil {
				return err
			}
			if thumb, err = encryptValue(key, r.thumb); err != nil {
				return err
			}
		} else {
			if text, err = decryptText(s.key, r.text); err != nil {
				return fmt.Errorf("decrypt row %d: %w", r.id, err)
			}
			if html, err = decryptText(s.key, r.html); err != nil {
				return fmt.Errorf("decrypt row %d: %w", r.id, err)
			}
			if preview, err = decryptText(s.key, r.preview); err != nil {
				return fmt.Errorf("decrypt row %d: %w", r.id, err)
			}
			if image, err = decryptBlob(s.key, r.image); err != nil {
				return fmt.Errorf("decrypt row %d: %w", r.id, err)
			}
			if thumb, err = decryptBlob(s.key, r.thumb); err != nil {
				return fmt.Errorf("decrypt row %d: %w", r.id, err)
			}
		}
		if _, err := tx.Exec(`UPDATE entries SET text=?,html=?,preview=?,image=?,thumb=? WHERE id=?`,
			text, html, preview, image, thumb, r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
