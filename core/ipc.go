package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
)

// JSON-RPC 2.0 over newline-delimited stdio (IPC.md).
//
// Requests (Tauri -> Go): {"jsonrpc":"2.0","id":7,"method":"list","params":{...}}
// Responses:              {"jsonrpc":"2.0","id":7,"result":{...}}
//                         {"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"..."}}
// Events (Go -> Tauri):   {"jsonrpc":"2.0","event":"entry_added","data":{"id":123,"kind":"text"}}
//
// Method and field names here MUST match IPC.md exactly.

const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
	errLocked         = -32001
	errApp            = -32000
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcEvent struct {
	JSONRPC string `json:"jsonrpc"`
	Event   string `json:"event"`
	Data    any    `json:"data"`
}

// emitter serializes all stdout writes (responses + events).
type emitter struct {
	mu  sync.Mutex
	out *json.Encoder
}

func newEmitter(enc *json.Encoder) *emitter { return &emitter{out: enc} }

func (e *emitter) emit(v any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.out.Encode(v); err != nil {
		logf("stdout write failed: %v", err)
	}
}

// event pushes a Go->Tauri event (no id).
func (a *App) event(name string, data any) {
	a.emitter.emit(rpcEvent{JSONRPC: "2.0", Event: name, Data: data})
}

func rpcErr(code int, msg string) *rpcError { return &rpcError{Code: code, Message: msg} }

func appErr(err error) *rpcError {
	if err == ErrLocked {
		return rpcErr(errLocked, "database is locked")
	}
	if err == errWrongPassword {
		return rpcErr(errApp, "incorrect password")
	}
	return rpcErr(errApp, err.Error())
}

// ---- param structs (field names per IPC.md) ----

type pSetRecording struct {
	Enabled bool `json:"enabled"`
}

type pList struct {
	Limit         *int   `json:"limit"`
	Offset        *int   `json:"offset"`
	Query         string `json:"query"`
	Tag           string `json:"tag"`
	Kind          string `json:"kind"`
	FavoritesOnly bool   `json:"favorites_only"`
}

type pID struct {
	ID int64 `json:"id"`
}

type pSetFavorite struct {
	ID       int64 `json:"id"`
	Favorite bool  `json:"favorite"`
}

type pTag struct {
	ID  int64  `json:"id"`
	Tag string `json:"tag"`
}

type pClearRecent struct {
	Minutes int `json:"minutes"`
}

type pSetSettings struct {
	Patch map[string]any `json:"patch"`
}

type pDir struct {
	Dir string `json:"dir"`
}

type pPath struct {
	Path string `json:"path"`
}

type pSaveImage struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

type pResolveSensitive struct {
	Token  string `json:"token"`
	Record bool   `json:"record"`
}

type pSetDBPassword struct {
	Password string `json:"password"`
}

func decodeParams(raw json.RawMessage, v any) *rpcError {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return rpcErr(errInvalidParams, "bad params: "+err.Error())
	}
	return nil
}

// dispatch runs one method and returns (result, rpcErr).
func (a *App) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {

	case "ping":
		return map[string]any{"version": coreVersion}, nil

	case "get_state":
		return a.mGetState()

	case "set_recording":
		var p pSetRecording
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mSetRecording(p.Enabled)

	case "list":
		var p pList
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mList(p)

	case "get":
		var p pID
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mGet(p.ID)

	case "copy":
		var p pID
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mCopy(p.ID)

	case "set_favorite":
		var p pSetFavorite
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mSetFavorite(p.ID, p.Favorite)

	case "add_tag":
		var p pTag
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mAddTag(p.ID, p.Tag)

	case "remove_tag":
		var p pTag
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mRemoveTag(p.ID, p.Tag)

	case "list_tags":
		return a.mListTags()

	case "delete":
		var p pID
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mDelete(p.ID)

	case "clear_all":
		return a.mClearAll()

	case "clear_recent":
		var p pClearRecent
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mClearRecent(p.Minutes)

	case "get_settings":
		return a.mGetSettings()

	case "set_settings":
		var p pSetSettings
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		if p.Patch == nil {
			return nil, rpcErr(errInvalidParams, "patch is required")
		}
		return a.mSetSettings(p.Patch)

	case "export_json":
		var p pDir
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mExportJSON(p.Dir)

	case "export_backup":
		var p pPath
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mExportBackup(p.Path)

	case "import_backup":
		var p pPath
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mImportBackup(p.Path)

	case "auto_backup_now":
		return a.mAutoBackupNow()

	case "save_image":
		var p pSaveImage
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mSaveImage(p.ID, p.Path)

	case "resolve_sensitive":
		var p pResolveSensitive
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mResolveSensitive(p.Token, p.Record)

	case "set_db_password":
		var p pSetDBPassword
		if e := decodeParams(params, &p); e != nil {
			return nil, e
		}
		return a.mSetDBPassword(p.Password)

	default:
		return nil, rpcErr(errMethodNotFound, "unknown method: "+method)
	}
}

// handleLine processes one stdin line.
func (a *App) handleLine(line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		a.emitter.emit(rpcResponse{JSONRPC: "2.0", Error: rpcErr(errParseError, "parse error")})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		a.emitter.emit(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr(errInvalidRequest, "invalid request")})
		return
	}
	result, rerr := a.dispatch(req.Method, req.Params)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr}
	if len(req.ID) == 0 {
		// Notification: no id -> no response, but surface errors as events.
		if rerr != nil {
			a.event("core_error", map[string]any{"message": rerr.Message})
		}
		return
	}
	a.emitter.emit(resp)
}

// ---- method implementations ----

func (a *App) mGetState() (any, *rpcError) {
	st := a.store
	total, texts, images, imageBytes, err := st.Stats()
	if err != nil {
		return nil, appErr(err)
	}
	settings, err := st.GetSettings()
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{
		"recording":   settings.Recording,
		"entry_count": total,
		"text_count":  texts,
		"image_count": images,
		"image_bytes": imageBytes,
		"db_path":     st.Path(),
		"version":     coreVersion,
	}, nil
}

func (a *App) mSetRecording(enabled bool) (any, *rpcError) {
	if err := a.store.SetRecording(enabled); err != nil {
		return nil, appErr(err)
	}
	a.event("state_changed", map[string]any{"recording": enabled})
	return map[string]any{"recording": enabled}, nil
}

func (a *App) mList(p pList) (any, *rpcError) {
	opts := ListOptions{Limit: 50, Offset: 0, Query: p.Query, Tag: p.Tag, Kind: p.Kind, FavoritesOnly: p.FavoritesOnly}
	if p.Limit != nil {
		opts.Limit = *p.Limit
	}
	if p.Offset != nil {
		opts.Offset = *p.Offset
	}
	if opts.Limit < 0 || opts.Offset < 0 {
		return nil, rpcErr(errInvalidParams, "limit/offset must be >= 0")
	}
	entries, total, err := a.store.ListEntries(opts)
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"entries": entries, "total": total}, nil
}

func entryToDetail(e *Entry) EntryDetail {
	d := EntryDetail{
		EntrySummary: EntrySummary{
			ID: e.ID, Kind: e.Kind, Preview: e.Preview, CreatedAt: e.CreatedAt,
			Favorite: e.Favorite, Tags: e.Tags, HasImage: e.Kind == KindImage,
			TextLen: e.TextLen, Sensitive: e.Sensitive,
		},
		Text:   e.Text,
		ImageW: e.ImageW,
		ImageH: e.ImageH,
	}
	if len(e.Thumb) > 0 {
		d.ImageThumb = base64.StdEncoding.EncodeToString(e.Thumb)
	}
	if len(e.Image) > 0 {
		d.ImageFull = base64.StdEncoding.EncodeToString(e.Image)
	}
	return d
}

func (a *App) mGet(id int64) (any, *rpcError) {
	e, err := a.store.GetEntry(id)
	if err != nil {
		return nil, appErr(err)
	}
	return entryToDetail(e), nil
}

func (a *App) mCopy(id int64) (any, *rpcError) {
	if err := a.copyEntryToClipboard(id); err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) mSetFavorite(id int64, favorite bool) (any, *rpcError) {
	if err := a.store.SetFavorite(id, favorite); err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) mAddTag(id int64, tag string) (any, *rpcError) {
	tags, err := a.store.AddTag(id, tag)
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"tags": tags}, nil
}

func (a *App) mRemoveTag(id int64, tag string) (any, *rpcError) {
	tags, err := a.store.RemoveTag(id, tag)
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"tags": tags}, nil
}

func (a *App) mListTags() (any, *rpcError) {
	tags, err := a.store.ListTags()
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"tags": tags}, nil
}

func (a *App) mDelete(id int64) (any, *rpcError) {
	if err := a.store.DeleteEntry(id); err != nil {
		return nil, appErr(err)
	}
	a.event("entry_deleted", map[string]any{"id": id})
	return map[string]any{"ok": true}, nil
}

func (a *App) mClearAll() (any, *rpcError) {
	n, err := a.store.ClearAll()
	if err != nil {
		return nil, appErr(err)
	}
	a.event("entries_cleared", map[string]any{"deleted": n})
	return map[string]any{"deleted": n}, nil
}

func (a *App) mClearRecent(minutes int) (any, *rpcError) {
	if minutes < 0 {
		return nil, rpcErr(errInvalidParams, "minutes must be >= 0")
	}
	n, err := a.store.ClearRecent(minutes)
	if err != nil {
		return nil, appErr(err)
	}
	a.event("entries_cleared", map[string]any{"deleted": n})
	return map[string]any{"deleted": n}, nil
}

func (a *App) mGetSettings() (any, *rpcError) {
	st, err := a.store.GetSettings()
	if err != nil {
		return nil, appErr(err)
	}
	return st, nil
}

func (a *App) mSetSettings(patch map[string]any) (any, *rpcError) {
	old, err := a.store.GetSettings()
	if err != nil {
		return nil, appErr(err)
	}
	st, err := a.store.UpdateSettings(patch)
	if err != nil {
		return nil, rpcErr(errInvalidParams, err.Error())
	}
	// db_dir change -> reopen the database at the new location.
	if st.DBDir != old.DBDir {
		if rerr := a.reopenDatabase(st.DBDir); rerr != nil {
			return nil, rerr
		}
		// re-read settings from the new DB for an accurate merged view
		if st2, err := a.store.GetSettings(); err == nil {
			st = st2
		}
	}
	if st.Recording != old.Recording {
		a.event("state_changed", map[string]any{"recording": st.Recording})
	}
	a.event("settings_changed", map[string]any{})
	return st, nil
}

func (a *App) mExportJSON(dir string) (any, *rpcError) {
	if dir == "" {
		return nil, rpcErr(errInvalidParams, "dir is required")
	}
	files, err := a.exportJSON(dir)
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"dir": dir, "files": files}, nil
}

func (a *App) mExportBackup(path string) (any, *rpcError) {
	if path == "" {
		return nil, rpcErr(errInvalidParams, "path is required")
	}
	n, err := a.exportBackup(path)
	if err != nil {
		return nil, appErr(err)
	}
	a.event("backup_done", map[string]any{"path": path})
	return map[string]any{"path": path, "bytes": n}, nil
}

func (a *App) mImportBackup(path string) (any, *rpcError) {
	if path == "" {
		return nil, rpcErr(errInvalidParams, "path is required")
	}
	imported, skipped, err := a.importBackup(path)
	if err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"imported": imported, "skipped": skipped}, nil
}

func (a *App) mAutoBackupNow() (any, *rpcError) {
	path, err := a.autoBackupNow()
	if err != nil {
		return nil, appErr(err)
	}
	a.event("backup_done", map[string]any{"path": path})
	return map[string]any{"path": path}, nil
}

func (a *App) mSaveImage(id int64, path string) (any, *rpcError) {
	if path == "" {
		return nil, rpcErr(errInvalidParams, "path is required")
	}
	if err := a.saveImageToPath(id, path); err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"path": path}, nil
}

func (a *App) mResolveSensitive(token string, record bool) (any, *rpcError) {
	if token == "" {
		return nil, rpcErr(errInvalidParams, "token is required")
	}
	if err := a.resolveSensitive(token, record); err != nil {
		return nil, appErr(err)
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) mSetDBPassword(password string) (any, *rpcError) {
	// NOTE: password never goes to logs; keep it out of error strings too.
	if err := a.store.SetPassword(password); err != nil {
		return nil, appErr(err)
	}
	set := password != ""
	a.event("settings_changed", map[string]any{})
	return map[string]any{"ok": true, "db_password_set": set}, nil
}

// reopenDatabase switches the store to a new directory (settings.db_dir change).
func (a *App) reopenDatabase(dbDir string) *rpcError {
	dir := dbDir
	if dir == "" {
		dir = defaultDBDir()
	}
	if err := ensureDir(dir); err != nil {
		return rpcErr(errApp, fmt.Sprintf("cannot create db dir: %v", err))
	}
	newPath := filepath.Join(dir, "clipvault.db")
	old := a.store
	ns, err := OpenStore(newPath)
	if err != nil {
		return rpcErr(errApp, fmt.Sprintf("cannot open database at %s: %v", newPath, err))
	}
	a.swapStore(old, ns)
	logf("database moved to %s", newPath)
	return nil
}
