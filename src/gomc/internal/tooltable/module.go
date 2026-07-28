// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package tooltable implements a tool table gomod using the persist API.
//
// It registers as "tooltable" and exposes the tooltable GMI API for CRUD
// operations on tool table SLOTS. Storage is delegated to the generic
// persistence service (looked up as "persistence" by default, overrideable
// via persist_instance=<instance>).
//
// The store is an array indexed by idx, mirroring 2.9's tooldata
// (emc/tooldata/tooldata_mmap.cc): idx 0 is the spindle slot and always
// exists, idx 1..N are the tool table proper. See gmi/idl/tooltable.gmi for
// why the key is the slot and not the tool number.
//
// On first run — an empty persist namespace — the store seeds itself from the
// legacy .tbl named by init_tbl=, if any.  See newTooltable for the full set
// of load parameters; the module reads no INI of its own.
package tooltable

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

const persistNamespace = "tooltable"

// SpindleIdx is the storage slot holding the tool currently in the spindle.
// 2.9 calls it tooldata index 0; it is a slot, never a tool row.
const SpindleIdx int32 = 0

// MaxIdx is the highest addressable slot: CANON_POCKETS_MAX-1 from
// emc/nml_intf/emctool.h, which bounds the interp's tool_table[] array.
const MaxIdx int32 = 1000

// emptyToolno marks a slot with nothing in it — 2.9's tooldata_entry_init,
// which sets toolno and pocketno to -1.
const emptyToolno int32 = -1

func init() {
	gomc.RegisterModule("tooltable", newTooltable)
}

type module struct {
	logger           *slog.Logger
	name             string
	initTbl          string
	persistInstance  string
	randomToolchange bool
	db               *persist.PersistClient
	dbHandle         int32
	mu               sync.RWMutex

	// spindleMem is the NON-RANDOM spindle slot (idx 0): a SESSION copy of
	// the mounted tool, held in memory and never persisted — 2.9 parity,
	// where the durable form omits it (tooldata_save starts at idx 1 for
	// non-random changers). Persisting it is how a restart came to apply a
	// phantom G43 offset from a tool io reported as absent: the copy
	// survived the power cycle while toolInSpindle initialised to 0. On a
	// RANDOM changer slot 0 is carousel pocket 0 — a real row that MUST
	// persist (startup restores toolInSpindle from it) — and this field is
	// unused. Guarded by mu; Idx/Updated are kept meaningful in place.
	spindleMem tooltable.ToolEntry
}

// newTooltable builds a store from the "load tooltable <name> k=v ..." line:
//
//	persist_instance=<name>   persist instance backing the store
//	init_tbl=<path>           legacy .tbl seeding this store on FIRST RUN only
//	                          (when the persist namespace is still empty);
//	                          unset = start empty
//	random_toolchanger=0|1    what an idx means here: carousel pocket (1) or
//	                          synthetic slot (0, default)
//
// The store reads no INI of its own.  Both settings are per-instance — which
// file seeds THIS store, whether THIS changer is random — and a HAL file names
// them with an ordinary INI reference, which is also what makes a second store
// on one INI possible at all:
//
//	load tooltable <tt2> init_tbl=[mill2:EMCIO]TOOL_TABLE persist_instance=persist2
//
// Reading [EMCIO] directly is what made a multi-instance config's second tool
// table a silent second copy of the first: every store resolved the same
// global section no matter which task it belonged to.
func newTooltable(_ *inifile.IniFile, logger *slog.Logger, name string, args []string) (gomc.Module, error) {
	m := &module{
		logger:          logger,
		name:            name,
		persistInstance: "persistence",
	}

	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		switch k {
		case "persist_instance":
			m.persistInstance = v
		case "init_tbl":
			m.initTbl = strings.TrimSpace(v)
		case "random_toolchanger":
			// The random/non-random distinction decides what an idx MEANS
			// (carousel pocket vs synthetic slot), so it drives both the
			// legacy import's slot assignment and the find_index_for_tool
			// spindle rule.
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("tooltable: random_toolchanger=%q is not a number", v)
			}
			m.randomToolchange = n != 0
		}
	}

	// Register tooltable API (actual persist lookup deferred to Start).
	reg := apiserver.DefaultRegistry()
	if err := tooltable.RegisterTooltableAPI(reg, name, m); err != nil {
		return nil, fmt.Errorf("tooltable: register API: %w", err)
	}

	logger.Info("tooltable: registered", "instance", name, "persistence", m.persistInstance,
		"init_tbl", m.initTbl, "random_toolchanger", m.randomToolchange)
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

	// The spindle slot is a structural invariant, not a row that happens to
	// exist: every reader of the table indexes slot 0 (the interp's
	// tool_table[0], stat.tool_table[0] in every UI) and an absent slot 0 is
	// what turned a fresh config with no tools into an IndexError crash
	// (issue #272). Materialise it once, empty, if the import did not.
	if err := m.ensureSpindleSlot(); err != nil {
		return err
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

// idxKey is the persist key for a slot. Slots are decimal, zero-padded to a
// fixed width so the store's lexicographic `ORDER BY key` is also numeric
// order — list_tools must hand back slots in idx order, and "10" sorts before
// "2" unpadded.
func idxKey(idx int32) string {
	return fmt.Sprintf("%04d", idx)
}

// parseIdxKey is idxKey's inverse, tolerating unpadded keys.
func parseIdxKey(key string) (int32, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(key), 10, 32)
	if err != nil {
		return 0, err
	}
	if n < 0 || n > int64(MaxIdx) {
		return 0, fmt.Errorf("slot %d out of range 0..%d", n, MaxIdx)
	}
	return int32(n), nil
}

// emptySlot is 2.9's tooldata_entry_init: an unoccupied slot, distinguishable
// from a real T0 entry (toolno 0) by its -1 tool number.
func emptySlot() tooltable.ToolEntry {
	return tooltable.ToolEntry{Toolno: emptyToolno, Pocketno: -1}
}

// ensureSpindleSlot materialises slot 0. Caller must not hold m.mu.
//
// RANDOM: slot 0 is carousel pocket 0, a real persisted row — created empty
// if absent, left alone if present (it holds the tool physically in the
// spindle across restarts).
//
// NON-RANDOM: slot 0 is session state — initialised empty in memory, and any
// persisted slot-0 row is DELETED, not just ignored: it can only be leftover
// from a store written before this rule (or from a config whose
// RANDOM_TOOLCHANGER flag was flipped), and a row that merely lingered would
// resurrect an ancient "tool in spindle" the moment the flag flips back.
func (m *module) ensureSpindleSlot() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(); err != nil {
		return err
	}
	if !m.randomToolchange {
		m.spindleMem = emptySlot()
		m.spindleMem.Idx = SpindleIdx
		if _, err := m.db.DeleteEntry(m.dbHandle, idxKey(SpindleIdx)); err != nil {
			return fmt.Errorf("tooltable: purging the persisted spindle slot: %w", err)
		}
		return nil
	}
	entry, err := m.db.GetEntry(m.dbHandle, idxKey(SpindleIdx))
	if err != nil {
		return fmt.Errorf("tooltable: reading the spindle slot: %w", err)
	}
	if entry.Value != "" {
		return nil
	}
	if err := m.writeSlot(SpindleIdx, emptySlot()); err != nil {
		return fmt.Errorf("tooltable: creating the spindle slot: %w", err)
	}
	return nil
}

// writeSlot marshals and stores one slot. Caller must hold m.mu for writing.
func (m *module) writeSlot(idx int32, entry tooltable.ToolEntry) error {
	// idx comes from the key and the stamp from the persist row; neither
	// belongs in the stored JSON, where a stale copy could contradict it.
	entry.Idx = 0
	entry.Updated = 0
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = m.db.SetEntry(m.dbHandle, idxKey(idx), string(data))
	return err
}

// decodeSlot rebuilds an entry from a persist row, restoring the two fields
// that live on the row rather than in the JSON.
func decodeSlot(key, value string, updated int64) (tooltable.ToolEntry, error) {
	idx, err := parseIdxKey(key)
	if err != nil {
		return tooltable.ToolEntry{}, fmt.Errorf("slot key %q: %w", key, err)
	}
	var t tooltable.ToolEntry
	if err := json.Unmarshal([]byte(value), &t); err != nil {
		return tooltable.ToolEntry{}, err
	}
	t.Idx = idx
	t.Updated = updated
	return t, nil
}

// --- TooltableCallbacks implementation ---

func (m *module) ListTools() ([]tooltable.ToolEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listLocked()
}

// listLocked is ListTools without the lock, for the methods that scan the
// table as part of a larger locked operation. Caller must hold m.mu.
func (m *module) listLocked() ([]tooltable.ToolEntry, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}

	entries, err := m.db.GetEntries(m.dbHandle)
	if err != nil {
		return nil, err
	}

	tools := make([]tooltable.ToolEntry, 0, len(entries)+1)
	if !m.randomToolchange {
		// The session spindle slot leads the listing (idx order; persisted
		// keys are all >= 1 after Start's purge).
		tools = append(tools, m.spindleMem)
	}
	for _, e := range entries {
		t, err := decodeSlot(e.Key, e.Value, e.Updated)
		if err != nil {
			// A corrupt record must not take the whole listing down, but it
			// must not vanish without a word either — the tool is simply gone
			// from every UI until someone notices.
			m.logger.Warn("tooltable: skipping unreadable slot", "key", e.Key, "err", err)
			continue
		}
		if !m.randomToolchange && t.Idx == SpindleIdx {
			continue // a persisted slot-0 row is stale by definition here
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func (m *module) GetTool(idx int32) (tooltable.ToolEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.ready(); err != nil {
		return tooltable.ToolEntry{}, err
	}

	if !m.randomToolchange && idx == SpindleIdx {
		return m.spindleMem, nil
	}

	entry, err := m.db.GetEntry(m.dbHandle, idxKey(idx))
	if err != nil {
		return tooltable.ToolEntry{}, err
	}

	// An unoccupied slot reads back as the empty entry (toolno -1), which is
	// what 2.9's tooldata_get yields for a slot that was never filled.
	//
	// "Absent" is the empty value, not an error: persist reports a missing row
	// that way on purpose, keeping its status channel for real storage
	// failures. Since persist.gmi became @rc_error those failures do arrive
	// here — the err above is no longer structurally unreachable — so a broken
	// database now surfaces instead of masquerading as an empty tool table.
	// tooltable only ever stores JSON, so an empty value is unambiguous.
	if entry.Value == "" {
		e := emptySlot()
		e.Idx = idx
		return e, nil
	}

	t, err := decodeSlot(idxKey(idx), entry.Value, entry.Updated)
	if err != nil {
		return tooltable.ToolEntry{}, fmt.Errorf("tooltable: slot %d is corrupt: %w", idx, err)
	}
	return t, nil
}

func (m *module) PutTool(idx int32, entry tooltable.ToolEntry) (tooltable.PutToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ready(); err != nil {
		return tooltable.PutToolResult{}, err
	}

	if !m.randomToolchange && idx == SpindleIdx {
		// Session state: same CAS contract as a persisted row (a stale
		// baseline refuses), same fresh stamp on every accepted write, no
		// storage — see spindleMem.
		if entry.Updated != 0 && entry.Updated != m.spindleMem.Updated {
			return tooltable.PutToolResult{}, fmt.Errorf(
				"tooltable: slot %d changed since it was read (stamp %d, caller had %d)",
				idx, m.spindleMem.Updated, entry.Updated)
		}
		entry.Idx = SpindleIdx
		entry.Updated = time.Now().UnixNano()
		m.spindleMem = entry
		return tooltable.PutToolResult{Ok: true, Index: idx}, nil
	}

	// Optimistic concurrency: a non-zero stamp is the caller's read baseline;
	// refuse when the stored row moved on (or vanished) since. Atomic with the
	// write below under m.mu, which makes it the authoritative backstop for
	// the task layer's pre-check — that pre-check exists because this error is
	// flattened to a bare rc by the in-process client shim, so the friendly
	// 409 message cannot originate here.
	if entry.Updated != 0 {
		cur, err := m.db.GetEntry(m.dbHandle, idxKey(idx))
		if err != nil {
			return tooltable.PutToolResult{}, err
		}
		if cur.Value == "" || cur.Updated != entry.Updated {
			return tooltable.PutToolResult{}, fmt.Errorf(
				"tooltable: slot %d changed since it was read (stamp %d, caller had %d)",
				idx, cur.Updated, entry.Updated)
		}
	}

	if err := m.writeSlot(idx, entry); err != nil {
		return tooltable.PutToolResult{}, err
	}
	return tooltable.PutToolResult{Ok: true, Index: idx}, nil
}

func (m *module) DeleteTool(idx int32) (tooltable.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ready(); err != nil {
		return tooltable.DeleteResult{}, err
	}

	// The IDL constrains idx >= 1 on this route, but the in-process C client
	// reaches these methods without the REST validator, so hold the invariant
	// here too: the spindle slot is emptied, never removed.
	if idx == SpindleIdx {
		return tooltable.DeleteResult{}, fmt.Errorf(
			"tooltable: the spindle slot (idx 0) cannot be deleted; unload the tool instead")
	}

	res, err := m.db.DeleteEntry(m.dbHandle, idxKey(idx))
	if err != nil {
		return tooltable.DeleteResult{}, err
	}
	return tooltable.DeleteResult{Ok: res.Ok}, nil
}

// FindIndexForTool mirrors 2.9's tooldata_find_index_for_tool
// (tooldata_mmap.cc): -1 for the empty tool number or a tool not in the
// table, 0 for toolno 0 on a non-random changer (the empty spindle IS slot
// 0), and otherwise the lowest non-zero slot holding that tool number.
//
// The spindle slot deliberately loses to a real table slot: on a non-random
// changer slot 0 is a COPY of the loaded tool, so a plain first-match scan
// would resolve a loaded tool to the spindle rather than to its home slot,
// and #<_current_pocket> would read 0 for every tool in the spindle.
func (m *module) FindIndexForTool(toolno int32) (tooltable.IndexResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.ready(); err != nil {
		return tooltable.IndexResult{}, err
	}
	if toolno == emptyToolno {
		return tooltable.IndexResult{Idx: -1}, nil
	}
	if !m.randomToolchange && toolno == 0 {
		return tooltable.IndexResult{Idx: SpindleIdx}, nil
	}

	entries, err := m.listLocked()
	if err != nil {
		return tooltable.IndexResult{}, err
	}
	found := int32(-1)
	for i := range entries {
		if entries[i].Toolno != toolno {
			continue
		}
		found = entries[i].Idx
		if found == SpindleIdx {
			continue // keep looking for the tool's own slot
		}
		break
	}
	return tooltable.IndexResult{Idx: found}, nil
}

// NextFreeIndex returns the lowest unoccupied slot >= 1, or -1 when the table
// is full. Slot 0 is never offered: it is the spindle, not a tool slot.
func (m *module) NextFreeIndex() (tooltable.IndexResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.ready(); err != nil {
		return tooltable.IndexResult{}, err
	}
	entries, err := m.listLocked()
	if err != nil {
		return tooltable.IndexResult{}, err
	}
	used := make(map[int32]bool, len(entries))
	for i := range entries {
		used[entries[i].Idx] = true
	}
	for idx := int32(1); idx <= MaxIdx; idx++ {
		if !used[idx] {
			return tooltable.IndexResult{Idx: idx}, nil
		}
	}
	return tooltable.IndexResult{Idx: -1}, nil
}

// tryImportLegacy seeds an empty store from the init_tbl= file, once.
func (m *module) tryImportLegacy() {
	// No init_tbl= (halrun, or a config that keeps no legacy table) → the
	// store simply starts empty.
	if m.initTbl == "" {
		return
	}
	// init_tbl= is a configuration path: resolved server-side and contained by
	// the shared rule (internal/pathres).  A missing file is not an error — a
	// config may name the .tbl it will eventually export — but it IS logged,
	// because the seeding the operator asked for did not happen.
	tblPath, err := pathres.Resolve(m.initTbl, pathres.Read)
	if err != nil {
		m.logger.Info("tooltable: init_tbl not readable, starting empty",
			"init_tbl", m.initTbl, "err", err)
		return
	}
	if err := m.importTbl(tblPath); err != nil {
		m.logger.Warn("tooltable: import .tbl failed", "path", tblPath, "error", err)
	} else {
		m.logger.Info("tooltable: imported legacy .tbl", "path", tblPath)
	}
}
