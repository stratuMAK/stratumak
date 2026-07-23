// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package persist_sqlite implements a generic persistence gomod backed by SQLite.
//
// It registers as "persist_sqlite" and exposes the persist GMI API for
// handle-based key-value storage. Each namespace gets its own <namespace>.db
// file. Consumers call Open(namespace) to get a handle, then use the handle
// for all subsequent operations.
//
// Load: load persist_sqlite <persistence> [dbpath=<dir>]
// Default db directory: db/ next to the INI file.
package persist_sqlite

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"

	_ "modernc.org/sqlite"
)

func init() {
	gomc.RegisterModule("persist_sqlite", newPersistSQLite)
}

type nsHandle struct {
	namespace string
	db        *sql.DB
}

type module struct {
	logger  *slog.Logger
	dbDir   string
	mu      sync.RWMutex
	handles []nsHandle // indexed by handle value
}

func newPersistSQLite(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (gomc.Module, error) {
	dbDir := ""
	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok && k == "dbpath" {
			dbDir = v
		}
	}

	// Default: db/ directory next to the INI file.  dbpath= is a configuration
	// path and a *write* target, so it resolves through the shared rule and
	// must land inside the allowed roots (internal/pathres).
	if dbDir == "" {
		dbDir = "db"
	}
	dbDir, err := pathres.Resolve(dbDir, pathres.Dir)
	if err != nil {
		return nil, fmt.Errorf("persist_sqlite: dbpath=: %w", err)
	}

	// Create directory if it doesn't exist.
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("persist_sqlite: create db dir %s: %w", dbDir, err)
	}

	m := &module{logger: logger, dbDir: dbDir}

	// Register API.
	reg := apiserver.DefaultRegistry()
	if err := persist.RegisterPersistAPI(reg, name, m); err != nil {
		return nil, fmt.Errorf("persist_sqlite: register API: %w", err)
	}

	logger.Info("persist_sqlite: ready", "dir", dbDir, "instance", name)
	return m, nil
}

func (m *module) Start() error { return nil }
func (m *module) Stop()        {}
func (m *module) Destroy() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.handles {
		if m.handles[i].db != nil {
			if err := m.handles[i].db.Close(); err != nil {
				m.logger.Warn("persist_sqlite: close db on destroy", "namespace", m.handles[i].namespace, "err", err)
			}
			m.handles[i].db = nil
		}
	}
}

// --- Validation ---

const (
	// maxNameLen bounds a namespace name. It becomes a filename, so the real
	// ceiling is the filesystem's NAME_MAX (255 on ext4); staying well under it
	// turns an obscure ENAMETOOLONG from deep inside sqlite into an honest
	// rejection at the door.
	maxNameLen = 64

	// maxNamespaces bounds how many namespaces one instance will hold open.
	//
	// open is REST-reachable (POST /{namespace}) and every distinct name that
	// reaches it creates a .db file plus its -wal/-shm sidecars, an *sql.DB
	// with its own connection pool, and a permanent slot in m.handles. Without
	// a ceiling a caller on the (unauthenticated) loopback surface can walk the
	// name space and exhaust file descriptors and disk. Real configs use a
	// handful of namespaces — one per consumer per instance, see
	// configs/sim/axis/multiinst — so this is far above any legitimate use.
	maxNamespaces = 256
)

// validName checks that a namespace name is safe for use as a filename.
func validName(name string) bool {
	if name == "" || len(name) > maxNameLen {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// countOpen reports how many handle slots are live. Caller must hold m.mu.
func (m *module) countOpen() int {
	n := 0
	for i := range m.handles {
		if m.handles[i].db != nil {
			n++
		}
	}
	return n
}

// --- Handle management ---

func (m *module) getHandle(handle int32) (*nsHandle, error) {
	idx := int(handle)
	if idx < 0 || idx >= len(m.handles) || m.handles[idx].db == nil {
		// The handle names a namespace that was never opened, or was released.
		// That is a missing resource, not a broken controller.
		return nil, apiserver.Faultf(apiserver.FaultNotFound, "invalid handle: %d", handle)
	}
	return &m.handles[idx], nil
}

// --- Persist API implementation ---

func (m *module) Open(namespace string) (persist.OpenResult, error) {
	if !validName(namespace) {
		return persist.OpenResult{}, fmt.Errorf("invalid namespace: %q", namespace)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already open — return existing handle.
	for i, h := range m.handles {
		if h.namespace == namespace && h.db != nil {
			return persist.OpenResult{Handle: int32(i)}, nil
		}
	}

	if m.countOpen() >= maxNamespaces {
		// Full, not broken: the module is healthy and a delete_all frees a slot,
		// so this is 503 rather than a 500 that reads as a controller fault.
		return persist.OpenResult{}, apiserver.Faultf(apiserver.FaultCapacity,
			"persist_sqlite: too many open namespaces (limit %d)", maxNamespaces)
	}

	// Open new DB file.
	dbPath := filepath.Join(m.dbDir, namespace+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return persist.OpenResult{}, fmt.Errorf("open %s: %w", dbPath, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return persist.OpenResult{}, fmt.Errorf("set WAL on %s: %w", dbPath, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return persist.OpenResult{}, fmt.Errorf("set busy_timeout on %s: %w", dbPath, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS entries (
		key     TEXT PRIMARY KEY,
		value   TEXT NOT NULL DEFAULT '',
		updated INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		_ = db.Close()
		return persist.OpenResult{}, fmt.Errorf("create table in %s: %w", dbPath, err)
	}

	// Reuse a slot vacated by DeleteAll before growing the slice; otherwise a
	// delete_all/open cycle (both REST-reachable) grows m.handles without
	// bound even though the number of live namespaces never rises.
	handle := int32(-1)
	for i := range m.handles {
		if m.handles[i].db == nil {
			handle = int32(i)
			m.handles[i] = nsHandle{namespace: namespace, db: db}
			break
		}
	}
	if handle < 0 {
		handle = int32(len(m.handles))
		m.handles = append(m.handles, nsHandle{namespace: namespace, db: db})
	}
	m.logger.Debug("persist_sqlite: opened namespace", "namespace", namespace, "handle", handle)
	return persist.OpenResult{Handle: handle}, nil
}

// Close is deliberately a no-op; every handle is closed at Destroy().
//
// Handles are *shared*: Open returns the same handle to every caller asking
// for the same namespace, so honouring one consumer's Close would pull the
// database out from under the others (milltask, tooltable and ngcpreview can
// all hold the same namespace — see configs/sim/axis/multiinst). The API is
// also not REST-reachable — persist.gmi gives close no @method/@path, so only
// in-process GMI consumers can call it, and none of them do. Turning it into a
// real close would need refcounting, which buys nothing while the module's
// lifetime is the process's.
func (m *module) Close(handle int32) {
}

func (m *module) GetNamespaces() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]bool)
	for _, h := range m.handles {
		if h.db != nil {
			seen[h.namespace] = true
		}
	}
	// Also scan directory for namespaces not yet opened.
	entries, err := os.ReadDir(m.dbDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
				ns := strings.TrimSuffix(e.Name(), ".db")
				if validName(ns) {
					seen[ns] = true
				}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for ns := range seen {
		result = append(result, ns)
	}
	return result, nil
}

func (m *module) GetEntries(handle int32) ([]persist.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return nil, err
	}
	rows, err := h.db.Query(`SELECT key, value, updated FROM entries ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []persist.Entry
	for rows.Next() {
		var e persist.Entry
		if err := rows.Scan(&e.Key, &e.Value, &e.Updated); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (m *module) GetEntry(handle int32, key string) (persist.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return persist.Entry{}, err
	}
	var e persist.Entry
	err = h.db.QueryRow(`SELECT key, value, updated FROM entries WHERE key = ?`, key).
		Scan(&e.Key, &e.Value, &e.Updated)
	if err == sql.ErrNoRows {
		// A missing row is an answer, not a failure: it reads back as the zero
		// entry with no error. The @rc_error status channel carries one bit —
		// worked or did not — so folding "no such key" into it would make an
		// unwritten key indistinguishable from a broken database, which is the
		// confusion the conversion exists to remove. Consumers already tell the
		// two apart by the empty value (halscope's first start, tooltable's
		// unstored tool); now a non-nil error means storage really is broken.
		return persist.Entry{}, nil
	}
	if err != nil {
		return persist.Entry{}, err
	}
	return e, nil
}

func (m *module) SetEntry(handle int32, key, value string) (persist.SetResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return persist.SetResult{Ok: false}, err
	}
	now := time.Now().UnixNano()
	_, err = h.db.Exec(
		`INSERT INTO entries (key, value, updated) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated`,
		key, value, now,
	)
	if err != nil {
		return persist.SetResult{Ok: false}, err
	}
	return persist.SetResult{Ok: true}, nil
}

func (m *module) DeleteEntry(handle int32, key string) (persist.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return persist.DeleteResult{Ok: false}, err
	}
	res, err := h.db.Exec(`DELETE FROM entries WHERE key = ?`, key)
	if err != nil {
		return persist.DeleteResult{Ok: false}, err
	}
	n, _ := res.RowsAffected()
	return persist.DeleteResult{Ok: n > 0, Count: int32(n)}, nil
}

func (m *module) SetEntries(handle int32, entries []persist.Entry) (persist.SetResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return persist.SetResult{Ok: false}, err
	}
	tx, err := h.db.Begin()
	if err != nil {
		return persist.SetResult{Ok: false}, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	now := time.Now().UnixNano()
	stmt, err := tx.Prepare(
		`INSERT INTO entries (key, value, updated) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated`)
	if err != nil {
		return persist.SetResult{Ok: false}, err
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		ts := e.Updated
		if ts == 0 {
			ts = now
		}
		if _, err := stmt.Exec(e.Key, e.Value, ts); err != nil {
			return persist.SetResult{Ok: false}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return persist.SetResult{Ok: false}, err
	}
	return persist.SetResult{Ok: true}, nil
}

func (m *module) DeleteAll(handle int32) (persist.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, err := m.getHandle(handle)
	if err != nil {
		return persist.DeleteResult{Ok: false}, err
	}
	// Close the DB, remove files.
	if err := h.db.Close(); err != nil {
		return persist.DeleteResult{Ok: false}, fmt.Errorf("close %s before delete: %w", h.namespace, err)
	}
	h.db = nil
	dbPath := filepath.Join(m.dbDir, h.namespace+".db")
	// The -wal/-shm sidecars may legitimately not exist; ignore their removal errors.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return persist.DeleteResult{Ok: false}, fmt.Errorf("remove %s: %w", dbPath, err)
	}
	return persist.DeleteResult{Ok: true, Count: 1}, nil
}
