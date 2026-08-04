// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"sync"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/tools"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/tooltable"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

func init() {
	apiserver.RegisterMeta(tools.ToolsMeta)
}

// spindleIdx is the tool table's spindle slot (2.9's tooldata index 0).
const spindleIdx int32 = 0

// Package-level reference to the tooltable client, set during registerTools().
// Used by the canon getters, which are called from the interp thread and have
// no module handle.
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
//
// The tooltable store is addressed by SLOT; this API is the operator-facing
// one and is addressed by TOOL NUMBER, which is what a UI and an operator
// think in. Every method here therefore resolves a tool number to a slot
// first (2.9's tooldata_find_index_for_tool), and the slot never leaves.
type toolsImpl struct {
	module *milltaskModule

	// writeMu serializes the write commands (PutTool/DeleteTool). PutTool's
	// new-tool path is check-then-act across two client calls — NextFreeIndex
	// (or the random-changer occupant check) and then the put — and the store
	// has no allocate-if-empty primitive, so two concurrent creates would both
	// be handed the same slot and the second would silently destroy the first
	// tool (a create's Updated==0 bypasses the stamp CAS by design).
	writeMu sync.Mutex
}

// checkToolConflict is the optimistic-concurrency pre-check for PutTool: the
// caller's non-zero baseline stamp must match the currently stored one. It is
// a pre-check for UX only — the in-process client shim flattens the tooltable
// module's authoritative (mutex-atomic) refusal to a bare rc, so the readable
// 409 must be produced here, before crossing the shim. A vanished slot means
// the tool is gone: recreating a deleted tool from a stale dialog is a
// conflict too, not an upsert.
func checkToolConflict(current *tools.ToolEntry, baseline int64, toolno int32) error {
	if baseline == 0 {
		return nil // last-write-wins caller (canon/G10/legacy)
	}
	if current == nil || current.Toolno != toolno {
		return apiserver.Faultf(apiserver.FaultState,
			"tool %d was deleted since it was read — reload the table", toolno)
	}
	if current.Updated != baseline {
		return apiserver.Faultf(apiserver.FaultState,
			"tool %d changed since it was read — reload and re-apply the edit", toolno)
	}
	return nil
}

// spindleSlotIsACopy reports whether slot 0 duplicates a tool that also has
// its own row. It does on a NON-RANDOM changer, where a tool change copies the
// tool's slot to slot 0 and the tool keeps its home slot; such a copy is not a
// tool and must never be listed or edited as one. On a RANDOM changer slot 0
// is pocket 0 — the tool in the spindle lives there and nowhere else, so it is
// an ordinary row that a UI must be able to see and edit.
func (t *toolsImpl) spindleSlotIsACopy() bool {
	return t.module.task == nil || !t.module.task.randomToolchanger
}

// ListTools reports the tool table as tools, not as slots.
func (t *toolsImpl) ListTools() ([]tools.ToolEntry, error) {
	entries, err := t.module.ttClient.ListTools()
	if err != nil {
		return nil, err
	}
	skipSpindle := t.spindleSlotIsACopy()
	result := make([]tools.ToolEntry, 0, len(entries))
	for i := range entries {
		if skipSpindle && entries[i].Idx == spindleIdx {
			continue
		}
		if entries[i].Toolno < 0 {
			continue // unoccupied slot
		}
		result = append(result, tooltableToToolEntry(&entries[i]))
	}
	return result, nil
}

// findToolIdx resolves a tool number to its table slot. -1 means the tool is
// not in the table. Where slot 0 is only a copy of a loaded tool it is not a
// hit: editing it would edit the mirror and leave the tool's own row stale.
func (t *toolsImpl) findToolIdx(toolno int32) (int32, error) {
	res, err := t.module.ttClient.FindIndexForTool(toolno)
	if err != nil {
		return -1, err
	}
	if res.Idx == spindleIdx && t.spindleSlotIsACopy() {
		return -1, nil
	}
	return res.Idx, nil
}

// GetTool answers an absent tool with the zero entry rather than an error —
// the wire contract callers already depend on (they tell "no such tool" from
// Toolno == 0).
func (t *toolsImpl) GetTool(toolno int32) (*tools.ToolEntry, error) {
	idx, err := t.findToolIdx(toolno)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		return &tools.ToolEntry{}, nil
	}
	entry, err := t.module.ttClient.GetTool(idx)
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
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if entry.Updated != 0 {
		current, err := t.GetTool(toolno)
		if err != nil {
			return nil, err
		}
		if err := checkToolConflict(current, entry.Updated, toolno); err != nil {
			return nil, err
		}
	}

	idx, err := t.findToolIdx(toolno)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		// A new tool. On a random changer the slot IS the carousel pocket, so
		// the operator picks it by typing the pocket; anywhere else the slot
		// is an implementation detail and the store hands out the next free
		// one.
		if t.module.task != nil && t.module.task.randomToolchanger {
			idx = entry.Pocketno
			if idx <= 0 {
				return nil, apiserver.Faultf(apiserver.FaultState,
					"tool %d needs a pocket number: on a random toolchanger the pocket is the tool's slot in the table", toolno)
			}
			occupant, err := t.module.ttClient.GetTool(idx)
			if err != nil {
				return nil, err
			}
			if occupant.Toolno >= 0 && occupant.Toolno != toolno {
				return nil, apiserver.Faultf(apiserver.FaultState,
					"pocket %d already holds tool %d", idx, occupant.Toolno)
			}
		} else {
			free, err := t.module.ttClient.NextFreeIndex()
			if err != nil {
				return nil, err
			}
			if free.Idx < 0 {
				return nil, apiserver.Faultf(apiserver.FaultCapacity, "the tool table is full")
			}
			idx = free.Idx
		}
	}

	entry.Toolno = toolno
	ttEntry := toolEntryToTooltable(&entry)
	res, err := t.module.ttClient.PutTool(idx, ttEntry)
	if err != nil {
		return nil, err
	}
	// Editing the loaded tool must move with it into the spindle slot, or the
	// interp keeps applying the pre-edit offsets until the next M6 — this is
	// what 2.9's interp does for G10 (tool_table[0] = tool_table[idx]) and
	// what the tooledit UI needs for a touch-off to take effect now.
	if err := t.syncSpindleSlot(toolno); err != nil {
		return nil, err
	}
	// Report the TOOL NUMBER: this API is tool-number addressed, and the slot
	// res.Index carries is an internal detail of the store.
	return &tools.PutToolResult{Ok: res.Ok, Index: toolno}, nil
}

func (t *toolsImpl) DeleteTool(toolno int32) (*tools.CmdResult, error) {
	if toolno <= 0 {
		return nil, fmt.Errorf("toolno must be > 0")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	idx, err := t.findToolIdx(toolno)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		return &tools.CmdResult{Ok: "true"}, nil // already gone
	}
	if idx == spindleIdx {
		// Random changer: the tool lives in pocket 0. The slot itself is a
		// structural invariant and cannot be removed, so empty it — which is
		// also what "there is no tool in the spindle pocket" means.
		empty := tooltable.ToolEntry{Toolno: -1, Pocketno: -1}
		if _, err := t.module.ttClient.PutTool(spindleIdx, empty); err != nil {
			return nil, err
		}
		return &tools.CmdResult{Ok: "true"}, nil
	}
	if _, err := t.module.ttClient.DeleteTool(idx); err != nil {
		return nil, err
	}
	return &tools.CmdResult{Ok: "true"}, nil
}

// syncSpindleSlot copies a tool's row into the spindle slot when that tool is
// the one in the spindle, on a non-random changer. On a random changer the
// spindle slot is the tool's only row, so there is nothing to copy.
func (t *toolsImpl) syncSpindleSlot(toolno int32) error {
	if t.module.task == nil || t.module.task.io == nil || !t.spindleSlotIsACopy() {
		return nil
	}
	tis, err := t.module.task.io.GetToolInSpindle()
	if err != nil || tis != toolno || tis <= 0 {
		return nil
	}
	idx, err := t.findToolIdx(toolno)
	if err != nil || idx < 0 {
		return err
	}
	entry, err := t.module.ttClient.GetTool(idx)
	if err != nil {
		return err
	}
	entry.Updated = 0 // unconditional: this is a mirror, not an edit
	_, err = t.module.ttClient.PutTool(spindleIdx, entry)
	return err
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

// getToolSlot reads one tool table slot for the canon getters. An unoccupied
// slot answers rc 0 with toolno -1 (2.9's GET_EXTERNAL_TOOL_TABLE hands back
// the empty entry, it does not fail); retval -1 is reserved for a slot that
// cannot be READ at all (no client, negative index, store error).
func getToolSlot(idx int32) (retval int32, toolno int32, pocketno int32, offset [9]float64, diameter, frontangle, backangle float64, orientation int32) {
	if pkgTTClient == nil || idx < 0 {
		return -1, 0, 0, [9]float64{}, 0, 0, 0, 0
	}
	entry, err := pkgTTClient.GetTool(idx)
	if err != nil {
		return -1, 0, 0, [9]float64{}, 0, 0, 0, 0
	}
	return 0, entry.Toolno, entry.Pocketno, toolOffsets(&entry), entry.Diameter, entry.Frontangle, entry.Backangle, entry.Orientation
}

// toolIdxFor resolves a tool number to its table slot for the canon getters,
// mirroring 2.9's tooldata_find_index_for_tool. -1 means absent, and also
// means "the lookup failed" — 2.9 could not tell those apart either, and the
// getters it feeds (#<_current_pocket>, #<_selected_pocket>) have -1 as their
// documented "unknown" value.
func toolIdxFor(toolno int32) int32 {
	if pkgTTClient == nil {
		return -1
	}
	res, err := pkgTTClient.FindIndexForTool(toolno)
	if err != nil {
		return -1
	}
	return res.Idx
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
