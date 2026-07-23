// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tools"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

func init() {
	apiserver.RegisterMeta(tools.ToolsMeta)
}

// Package-level reference to the tooltable client, set during registerTools().
// Used by getToolByPocket (called from canon getters).
var pkgTTClient *tooltable.TooltableClient

func tooltableToToolEntry(t *tooltable.ToolEntry) tools.ToolEntry {
	return tools.ToolEntry{
		Toolno:      t.Toolno,
		Pocketno:    t.Pocketno,
		XOffset:     t.XOffset,
		YOffset:     t.YOffset,
		ZOffset:     t.ZOffset,
		AOffset:     t.AOffset,
		BOffset:     t.BOffset,
		COffset:     t.COffset,
		UOffset:     t.UOffset,
		VOffset:     t.VOffset,
		WOffset:     t.WOffset,
		Diameter:    t.Diameter,
		Frontangle:  t.Frontangle,
		Backangle:   t.Backangle,
		Orientation: t.Orientation,
		Comment:     t.Comment,
		Updated:     t.Updated,
	}
}

func toolEntryToTooltable(e *tools.ToolEntry) tooltable.ToolEntry {
	return tooltable.ToolEntry{
		Toolno:      e.Toolno,
		Pocketno:    e.Pocketno,
		XOffset:     e.XOffset,
		YOffset:     e.YOffset,
		ZOffset:     e.ZOffset,
		AOffset:     e.AOffset,
		BOffset:     e.BOffset,
		COffset:     e.COffset,
		UOffset:     e.UOffset,
		VOffset:     e.VOffset,
		WOffset:     e.WOffset,
		Diameter:    e.Diameter,
		Frontangle:  e.Frontangle,
		Backangle:   e.Backangle,
		Orientation: e.Orientation,
		Comment:     e.Comment,
		Updated:     e.Updated,
	}
}

// toolsImpl implements tools.ToolsCallbacks via the tooltable GMI client.
type toolsImpl struct {
	module *milltaskModule
}

// checkToolConflict is the optimistic-concurrency pre-check for PutTool: the
// caller's non-zero baseline stamp must match the currently stored one. It is
// a pre-check for UX only — the in-process client shim flattens the tooltable
// module's authoritative (mutex-atomic) refusal to a bare rc, so the readable
// 409 must be produced here, before crossing the shim. current.Toolno == 0
// means the tool is gone: recreating a deleted tool from a stale dialog is a
// conflict too, not an upsert.
func checkToolConflict(current *tools.ToolEntry, baseline int64, toolno int32) error {
	if baseline == 0 {
		return nil // last-write-wins caller (canon/G10/legacy)
	}
	if current == nil || current.Toolno == 0 {
		return apiserver.Faultf(apiserver.FaultState,
			"tool %d was deleted since it was read — reload the table", toolno)
	}
	if current.Updated != baseline {
		return apiserver.Faultf(apiserver.FaultState,
			"tool %d changed since it was read — reload and re-apply the edit", toolno)
	}
	return nil
}

func (t *toolsImpl) ListTools() ([]tools.ToolEntry, error) {
	entries, err := t.module.ttClient.ListTools()
	if err != nil {
		return nil, err
	}
	result := make([]tools.ToolEntry, len(entries))
	for i := range entries {
		result[i] = tooltableToToolEntry(&entries[i])
	}
	return result, nil
}

func (t *toolsImpl) GetTool(toolno int32) (*tools.ToolEntry, error) {
	entry, err := t.module.ttClient.GetTool(toolno)
	if err != nil {
		return nil, err
	}
	te := tooltableToToolEntry(&entry)
	return &te, nil
}

func (t *toolsImpl) PutTool(toolno int32, entry tools.ToolEntry) (*tools.PutToolResult, error) {
	if toolno <= 0 {
		return nil, fmt.Errorf("toolno must be > 0")
	}
	if entry.Updated != 0 {
		current, err := t.GetTool(toolno)
		if err != nil {
			return nil, err
		}
		if err := checkToolConflict(current, entry.Updated, toolno); err != nil {
			return nil, err
		}
	}
	entry.Toolno = toolno
	ttEntry := toolEntryToTooltable(&entry)
	res, err := t.module.ttClient.PutTool(toolno, ttEntry)
	if err != nil {
		return nil, err
	}
	// The edit may have moved the prepped tool to another pocket — the
	// memoized stat.pocket_prepped must be recomputed from the new table.
	if t.module.task != nil {
		t.module.task.invalidatePrepPocket()
	}
	return &tools.PutToolResult{Ok: res.Ok, Index: res.Index}, nil
}

func (t *toolsImpl) DeleteTool(toolno int32) (*tools.CmdResult, error) {
	_, err := t.module.ttClient.DeleteTool(toolno)
	if err != nil {
		return nil, err
	}
	if t.module.task != nil {
		t.module.task.invalidatePrepPocket()
	}
	return &tools.CmdResult{Ok: "true"}, nil
}

func (t *toolsImpl) ReloadTools() (*tools.CmdResult, error) {
	_, err := t.module.LoadToolTable("")
	if err != nil {
		return nil, fmt.Errorf("failed to reload tool table: %v", err)
	}
	return &tools.CmdResult{Ok: "true"}, nil
}

// GetUnits reports the machine's linear-unit scale. The tool table is stored
// in mm; a UI multiplies a stored length by linearScale to display it in the
// operator's units and divides typed input by it to store. linearUnits is
// machine-units-per-mm (1.0 for mm, 1/25.4 for inch); an unset/degenerate
// value falls back to mm, matching machineCanonUnits' INIT_CANON default.
func (t *toolsImpl) GetUnits() (*tools.ToolUnits, error) {
	scale := 1.0
	if t.module.task != nil && t.module.task.linearUnits > 0 {
		scale = t.module.task.linearUnits
	}
	return &tools.ToolUnits{
		LinearScale: scale,
		Metric:      machineCanonUnits(scale) == CanonUnitsMM,
	}, nil
}

// lookupTool is the single presence-aware tool-table lookup. The store
// returns a zero entry (with Toolno clobbered to the key) for missing keys,
// so presence must be derived here and nowhere else: found=false with
// err==nil means the tool is genuinely absent from the table; err!=nil means
// the lookup itself was degraded (client unavailable or service error) and
// says nothing about presence — callers must not treat it as "empty tool".
func lookupTool(toolno int32) (entry tooltable.ToolEntry, found bool, err error) {
	if pkgTTClient == nil {
		return tooltable.ToolEntry{}, false, fmt.Errorf("tooltable client not available")
	}
	if toolno == 0 {
		// A real T0 entry's Toolno is 0 too, so the zero-entry miss is
		// indistinguishable by key — decide presence via the entry list.
		entries, lerr := pkgTTClient.ListTools()
		if lerr != nil {
			return tooltable.ToolEntry{}, false, lerr
		}
		for i := range entries {
			if entries[i].Toolno == 0 {
				return entries[i], true, nil
			}
		}
		return tooltable.ToolEntry{}, false, nil
	}
	e, gerr := pkgTTClient.GetTool(toolno)
	if gerr != nil {
		return tooltable.ToolEntry{}, false, gerr
	}
	if e.Toolno != toolno {
		return tooltable.ToolEntry{}, false, nil
	}
	return e, true, nil
}

// toolPocketFor resolves an io tool reference (toolno-keyed; -1 idle, 0 =
// empty/unload) to the tool's live table pocket number. Serves the classic
// pocket-index semantics of GET_EXTERNAL_(SELECTED_)TOOL_SLOT and
// stat.pocket_prepped — gomc has no tooldata array index, the pocket number
// is the stable equivalent. ok=false means the lookup was degraded by a
// service error: the returned value is the same fallback a missing tool
// gets, but it must not be cached.
func toolPocketFor(ref int32) (pocket int32, ok bool) {
	if ref < 0 {
		return ref, true // -1 = idle/unknown
	}
	entry, found, err := lookupTool(ref)
	if err != nil {
		if ref == 0 {
			return 0, false
		}
		return -1, false
	}
	if !found {
		if ref == 0 {
			// T0 with no table entry: the non-random spindle slot.
			return 0, true
		}
		return -1, true
	}
	// ref==0 resolves through the entry too: on a random toolchanger the T0
	// empty-pocket marker lives at a real pocket; the non-random key-0
	// spindle snapshot always carries pocketno 0.
	return entry.Pocketno, true
}

// pocketPreppedFor is toolPocketFor with a memo for the status path: the
// lookup is re-run per stat snapshot while a tool is prepped, but the result
// only changes on prep/change/table-edit (every tool-table mutation path
// calls invalidatePrepPocket after its write lands).
func (t *Task) pocketPreppedFor(toolno int32) int32 {
	t.mu.Lock()
	if t.prepPocketValid && t.prepPocketToolno == toolno {
		p := t.prepPocket
		t.mu.Unlock()
		return p
	}
	t.mu.Unlock()
	p, ok := toolPocketFor(toolno) // service round-trip — outside t.mu
	if !ok {
		// Degraded lookup (transient tooltable-service error): return the
		// fallback but do NOT memoize it, or every later snapshot would
		// keep reporting it until the next tool command invalidates.
		return p
	}
	t.mu.Lock()
	t.prepPocketToolno, t.prepPocket, t.prepPocketValid = toolno, p, true
	t.mu.Unlock()
	return p
}

// invalidatePrepPocket drops the memoized prepped-tool pocket. Called by the
// commands that can move a tool between pockets or rewrite the table.
func (t *Task) invalidatePrepPocket() {
	t.mu.Lock()
	t.prepPocketValid = false
	t.mu.Unlock()
}

// toolOffsets extracts a tool entry's offset tuple in the canonical
// X,Y,Z,A,B,C,U,V,W order — the single source for every getter that hands
// offsets to the interp.
func toolOffsets(e *tooltable.ToolEntry) [9]float64 {
	return [9]float64{
		e.XOffset, e.YOffset, e.ZOffset,
		e.AOffset, e.BOffset, e.COffset,
		e.UOffset, e.VOffset, e.WOffset,
	}
}

// getToolByPocket returns tool data for a given pocket index (pocket>0),
// scanning the table for a matching pocketno. Used by the canon getter
// GetExternalToolTable. pocket=0 (the spindle record) is deliberately NOT
// handled here: it must be resolved via io's tool-in-spindle (see
// GetExternalToolTable) — the key-0 snapshot the store holds reads back with
// Toolno clobbered to 0, which is exactly the "#5400 stuck at 0 after M6"
// bug this resolution replaced.
func getToolByPocket(pocket int32) (retval int32, toolno int32, offset [9]float64, diameter, frontangle, backangle float64, orientation int32) {
	if pkgTTClient == nil || pocket <= 0 {
		return -1, 0, [9]float64{}, 0, 0, 0, 0
	}

	// Scan all tools for matching pocketno.
	entries, err := pkgTTClient.ListTools()
	if err != nil {
		return -1, 0, [9]float64{}, 0, 0, 0, 0
	}
	for i := range entries {
		if entries[i].Pocketno == pocket {
			return 0, entries[i].Toolno, toolOffsets(&entries[i]), entries[i].Diameter, entries[i].Frontangle, entries[i].Backangle, entries[i].Orientation
		}
	}
	return -1, 0, [9]float64{}, 0, 0, 0, 0
}
