// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package tooltable implements a tool table gomod using the persist API.
//
// It registers as "tooltable" and exposes the tooltable GMI API for CRUD
// operations on tools. Storage is delegated to the generic persistence
// service (looked up as "persistence" by default, overrideable via
// persistence=<instance>).
//
// On first run, if a legacy .tbl file exists, it is imported automatically.
package tooltable

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

const persistNamespace = "tooltable"

func init() {
	gomc.RegisterModule("tooltable", newTooltable)
}

type module struct {
	logger          *slog.Logger
	name            string
	ini             *inifile.IniFile
	persistInstance string
	db              *persist.PersistClient
	dbHandle        int32
	mu              sync.RWMutex
}

func newTooltable(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (gomc.Module, error) {
	persistInst := "persistence"
	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok && k == "persist_instance" {
			persistInst = v
		}
	}

	m := &module{
		logger:          logger,
		name:            name,
		ini:             ini,
		persistInstance: persistInst,
	}

	// Register tooltable API (actual persist lookup deferred to Start).
	reg := apiserver.DefaultRegistry()
	if err := tooltable.RegisterTooltableAPI(reg, name, m); err != nil {
		return nil, fmt.Errorf("tooltable: register API: %w", err)
	}

	logger.Info("tooltable: registered", "instance", name, "persistence", persistInst)
	return m, nil
}

func (m *module) Start() error {
	// Look up the persist API.
	reg := apiserver.DefaultRegistry()
	cbs, err := reg.GetAPIFor(m.name, "persist", m.persistInstance, 2)
	if err != nil {
		return fmt.Errorf("tooltable: persist API lookup (%s): %w", m.persistInstance, err)
	}
	db := persist.NewPersistClient(unsafe.Pointer(cbs))

	// Open the tooltable namespace.
	res, err := db.Open(persistNamespace)
	if err != nil {
		return fmt.Errorf("tooltable: persist open namespace: %w", err)
	}

	// Publish the client and its handle together, under the lock the API
	// methods read them through: on the runtime REST load path the API server
	// is already serving while Start runs, so an unsynchronised write here
	// races every handler. Nothing is published until the namespace is open,
	// so a caller that gets past ready() has a usable handle.
	m.mu.Lock()
	m.db, m.dbHandle = db, res.Handle
	m.mu.Unlock()

	// Import the legacy .tbl only when the namespace is genuinely empty.
	//
	// The error used to be discarded, which conflated "empty" with "could not
	// tell" — so a transient read failure (sqlite busy, say) re-ran the import
	// and upserted the shipped .tbl over the live table, silently reverting
	// every offset the operator had edited since the migration. A namespace we
	// cannot read is not a namespace we may overwrite.
	entries, err := db.GetEntries(res.Handle)
	if err != nil {
		return fmt.Errorf("tooltable: reading namespace %q: %w", persistNamespace, err)
	}
	if len(entries) == 0 {
		m.tryImportLegacy()
	}

	m.logger.Info("tooltable: ready", "persistence", m.persistInstance)
	return nil
}

func (m *module) Stop()    {}
func (m *module) Destroy() {}

// ready reports whether Start has bound the persist client.
//
// The API is registered in the constructor but the client is only resolved in
// Start, and on the runtime REST load path (launcher loadModuleNamed: construct
// -> register -> Start) the API server is already serving, so a request can
// arrive in between. Dereferencing the nil client there panicked the handler;
// net/http turns that into a 500, but an honest error beats an unwound
// goroutine. Callers must hold m.mu.
func (m *module) ready() error {
	if m.db == nil {
		return fmt.Errorf("tooltable: not started yet (persist instance %q not bound)", m.persistInstance)
	}
	return nil
}

// tryImportLegacy attempts to import a legacy .tbl file on first run.
func (m *module) tryImportLegacy() {
	// No INI (halrun mode) → no TOOL_TABLE to import from.
	if m.ini == nil {
		return
	}
	v := m.ini.Get("EMCIO", "TOOL_TABLE")
	if !strings.HasSuffix(v, ".tbl") {
		return
	}
	// [EMCIO]TOOL_TABLE is a configuration path: resolved server-side and
	// contained by the shared rule (internal/pathres).  A missing file is not
	// an error here — the legacy import is best-effort.
	tblPath, err := pathres.Resolve(v, pathres.Read)
	if err != nil {
		m.logger.Debug("tooltable: no legacy .tbl to import", "value", v, "err", err)
		return
	}
	if err := m.importTbl(tblPath); err != nil {
		m.logger.Warn("tooltable: import .tbl failed", "path", tblPath, "error", err)
	} else {
		m.logger.Info("tooltable: imported legacy .tbl", "path", tblPath)
	}
}

// --- TooltableCallbacks implementation ---

func (m *module) ListTools() ([]tooltable.ToolEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.ready(); err != nil {
		return nil, err
	}

	entries, err := m.db.GetEntries(m.dbHandle)
	if err != nil {
		return nil, err
	}

	tools := make([]tooltable.ToolEntry, 0, len(entries))
	for _, e := range entries {
		var t tooltable.ToolEntry
		if err := json.Unmarshal([]byte(e.Value), &t); err != nil {
			// A corrupt record must not take the whole listing down, but it
			// must not vanish without a word either — the tool is simply gone
			// from every UI until someone notices.
			m.logger.Warn("tooltable: skipping unreadable entry", "key", e.Key, "err", err)
			continue
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func (m *module) GetTool(toolno int32) (tooltable.ToolEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.ready(); err != nil {
		return tooltable.ToolEntry{}, err
	}

	key := strconv.FormatInt(int64(toolno), 10)
	entry, err := m.db.GetEntry(m.dbHandle, key)
	if err != nil {
		return tooltable.ToolEntry{}, err
	}

	// An absent tool reads back as the zero entry, which is what 2.9 does and
	// what the callers expect (they tell "no such tool" from Toolno == 0).
	//
	// "Absent" is the empty value, not an error: persist reports a missing row
	// that way on purpose, keeping its status channel for real storage
	// failures. Since persist.gmi became @rc_error those failures do arrive
	// here — the err above is no longer structurally unreachable — so a broken
	// database now surfaces instead of masquerading as an empty tool table.
	// tooltable only ever stores JSON, so an empty value is unambiguous.
	if entry.Value == "" {
		return tooltable.ToolEntry{}, nil
	}

	var t tooltable.ToolEntry
	if err := json.Unmarshal([]byte(entry.Value), &t); err != nil {
		return tooltable.ToolEntry{}, fmt.Errorf("tooltable: tool %d is corrupt: %w", toolno, err)
	}
	return t, nil
}

func (m *module) PutTool(toolno int32, entry tooltable.ToolEntry) (tooltable.PutToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ready(); err != nil {
		return tooltable.PutToolResult{}, err
	}

	entry.Toolno = toolno
	data, err := json.Marshal(entry)
	if err != nil {
		return tooltable.PutToolResult{}, err
	}

	key := strconv.FormatInt(int64(toolno), 10)
	if _, err := m.db.SetEntry(m.dbHandle, key, string(data)); err != nil {
		return tooltable.PutToolResult{}, err
	}
	return tooltable.PutToolResult{Ok: true, Index: toolno}, nil
}

func (m *module) DeleteTool(toolno int32) (tooltable.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ready(); err != nil {
		return tooltable.DeleteResult{}, err
	}

	key := strconv.FormatInt(int64(toolno), 10)
	res, err := m.db.DeleteEntry(m.dbHandle, key)
	if err != nil {
		return tooltable.DeleteResult{}, err
	}
	return tooltable.DeleteResult{Ok: res.Ok}, nil
}
