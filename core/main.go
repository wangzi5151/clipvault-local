package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// clipvault-core: Tauri sidecar for ClipVault-Local.
//
// Protocol: newline-delimited JSON-RPC 2.0 on stdin/stdout (see IPC.md).
// Logging goes to stderr ONLY. Stdout carries nothing but JSON-RPC
// responses and events, one JSON object per line.
// This program performs ZERO network activity.

const coreVersion = "0.1.0"

// logf writes diagnostics to stderr; stdout is reserved for JSON-RPC.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[clipvault-core] "+format+"\n", args...)
}

// App wires the store, the JSON-RPC emitter, the clipboard watcher and
// the sensitive-prompt flow together.
type App struct {
	emitter *emitter

	mu    sync.RWMutex // guards store swaps (import_backup / db_dir change)
	store *Store

	// clipboard watcher state
	clipMu         sync.Mutex
	lastHash       string // hash of last clipboard content seen
	lastStoredHash string // hash of last entry actually stored (dedup)

	// pending sensitive captures: token -> pendingCapture
	pendMu  sync.Mutex
	pending map[string]*pendingCapture
}

func (a *App) getStore() *Store {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.store
}

func (a *App) swapStore(old, ns *Store) {
	a.mu.Lock()
	a.store = ns
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// defaultDBDir resolves the default database directory:
//   - Windows/macOS: %USERPROFILE%/Documents/ClipVault-Local
//   - Linux: ~/Documents/ClipVault-Local, falling back to
//     ~/.local/share/ClipVault-Local when Documents is absent.
func defaultDBDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	docs := filepath.Join(home, "Documents")
	if isWindows() || isDarwin() {
		return filepath.Join(docs, "ClipVault-Local")
	}
	if st, err := os.Stat(docs); err == nil && st.IsDir() {
		return filepath.Join(docs, "ClipVault-Local")
	}
	return filepath.Join(home, ".local", "share", "ClipVault-Local")
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func main() {
	dbFlag := flag.String("db", "", "override database directory (testing only)")
	flag.Parse()

	logf("clipvault-core %s starting (pid %d)", coreVersion, os.Getpid())

	dbDir := *dbFlag
	settingsDir := ""
	if dbDir == "" {
		dbDir = defaultDBDir()
	}
	if err := ensureDir(dbDir); err != nil {
		logf("cannot create db dir %s: %v", dbDir, err)
		os.Exit(1)
	}
	dbPath := filepath.Join(dbDir, "clipvault.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		logf("cannot open store %s: %v", dbPath, err)
		os.Exit(1)
	}

	// settings.db_dir overrides the default location (but not the -db flag).
	if *dbFlag == "" {
		if st, err := store.GetSettings(); err == nil && st.DBDir != "" && st.DBDir != dbDir {
			settingsDir = st.DBDir
			if err := ensureDir(settingsDir); err == nil {
				if ns, err := OpenStore(filepath.Join(settingsDir, "clipvault.db")); err == nil {
					_ = store.Close()
					store = ns
					dbDir = settingsDir
					logf("using settings db_dir: %s", dbDir)
				} else {
					logf("cannot open settings db_dir %s: %v (keeping %s)", settingsDir, err, dbPath)
				}
			}
		}
	}
	logf("database: %s", store.Path())

	app := &App{
		emitter: newEmitter(json.NewEncoder(os.Stdout)),
		store:   store,
		pending: make(map[string]*pendingCapture),
	}

	// Graceful shutdown on SIGINT/SIGTERM: Tauri kills the sidecar on exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logf("signal received, shutting down")
		_ = app.getStore().Close()
		os.Exit(0)
	}()

	// Background workers.
	go app.runWatcher()
	go app.runLockMonitor()
	go app.runAutoBackupLoop()

	// stdin: newline-delimited JSON-RPC requests. bufio.Scanner with a
	// generous buffer (requests can carry base64 images in theory).
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// copy: handleLine may outlive the scanner buffer reuse
		buf := make([]byte, len(line))
		copy(buf, line)
		app.handleLine(buf)
	}
	if err := scanner.Err(); err != nil {
		logf("stdin read error: %v", err)
	}

	// EOF on stdin -> parent is gone; shut down.
	logf("stdin closed, exiting")
	_ = app.getStore().Close()
	time.Sleep(50 * time.Millisecond)
}
