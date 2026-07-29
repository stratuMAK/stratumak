// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// An array-backed tooltable for tests that need real tool data (T/M6/G43,
// G10 L1/L10). It implements the GMI provider interface, so it plugs in
// exactly where the real service does — the canon getters go through a
// TooltableClient built over these callbacks and cannot tell the difference.
// No persistence, no sqlite, no service.
//
// The slot semantics mirror internal/tooltable/module.go, which mirrors 2.9's
// tooldata: the store is keyed by SLOT, not by tool number, and slot 0 is the
// spindle rather than a tool slot.

import (
	"sync"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
)

// fakeToolSlots is the table size for tests. The real MaxIdx is 1000; nothing
// here needs more than a handful, and a small array keeps ListTools cheap.
const fakeToolSlots = 16

// fakeSpindleIdx is the spindle slot (internal/tooltable.SpindleIdx).
const fakeSpindleIdx int32 = 0

// fakeEmptyToolno is the "no tool" tool number (internal/tooltable.emptyToolno).
const fakeEmptyToolno int32 = -1

type fakeToolSlot struct {
	entry    tooltable.ToolEntry
	occupied bool
}

// fakeToolTable implements tooltable.TooltableCallbacks over a flat array.
//
// The mutex is not ceremony: the tool table is genuinely reached from two
// goroutines at once — the interpreter thread reads it through the canon
// getters while the sequencer writes it through io.ToolSetOffset — and the
// real store (internal/tooltable/module.go) guards itself the same way. The
// race detector catches this immediately without it.
type fakeToolTable struct {
	mu     sync.RWMutex
	slots  [fakeToolSlots]fakeToolSlot
	random bool // what GetInfo reports: idx is a carousel pocket
}

// setTool places a tool in a slot. Offsets beyond Z are left zero — the tests
// that use this only exercise the XYZ tool offsets.
func (f *fakeToolTable) setTool(idx, toolno int32, x, y, z, diameter float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots[idx] = fakeToolSlot{
		occupied: true,
		entry: tooltable.ToolEntry{
			Idx: idx, Toolno: toolno, Pocketno: idx,
			XOffset: x, YOffset: y, ZOffset: z,
			Diameter: diameter,
		},
	}
}

// GetInfo — the store is what a task asks about idx semantics, so the fake
// must answer too, and a test that wants a random changer sets f.random.
func (f *fakeToolTable) GetInfo() (tooltable.StoreInfo, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return tooltable.StoreInfo{RandomToolchanger: f.random}, nil
}

func (f *fakeToolTable) ListTools() ([]tooltable.ToolEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []tooltable.ToolEntry
	for i := range f.slots {
		if f.slots[i].occupied {
			out = append(out, f.slots[i].entry)
		}
	}
	return out, nil
}

// GetTool answers an unoccupied slot with toolno -1 rather than an error —
// 2.9's GET_EXTERNAL_TOOL_TABLE hands back the empty entry, it does not fail.
// getToolSlot() reserves its error return for a slot that cannot be read.
func (f *fakeToolTable) GetTool(idx int32) (tooltable.ToolEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if idx < 0 || int(idx) >= len(f.slots) || !f.slots[idx].occupied {
		return tooltable.ToolEntry{Idx: idx, Toolno: fakeEmptyToolno}, nil
	}
	return f.slots[idx].entry, nil
}

// FindIndexForTool mirrors module.go's rules (themselves 2.9's
// tooldata_find_index_for_tool): -1 for the empty tool number or a tool that
// is not in the table, the spindle slot for toolno 0, and otherwise the
// lowest NON-spindle slot holding that tool number — slot 0 is a copy of the
// loaded tool, so letting it win would resolve every loaded tool to the
// spindle.
func (f *fakeToolTable) FindIndexForTool(toolno int32) (tooltable.IndexResult, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if toolno == fakeEmptyToolno {
		return tooltable.IndexResult{Idx: -1}, nil
	}
	if toolno == 0 {
		return tooltable.IndexResult{Idx: fakeSpindleIdx}, nil
	}
	found := int32(-1)
	for i := range f.slots {
		if !f.slots[i].occupied || f.slots[i].entry.Toolno != toolno {
			continue
		}
		found = f.slots[i].entry.Idx
		if found == fakeSpindleIdx {
			continue // keep looking for the tool's own slot
		}
		break
	}
	return tooltable.IndexResult{Idx: found}, nil
}

// NextFreeIndex returns the lowest unoccupied slot >= 1; slot 0 is never
// offered, it is the spindle.
func (f *fakeToolTable) NextFreeIndex() (tooltable.IndexResult, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for idx := int32(1); int(idx) < len(f.slots); idx++ {
		if !f.slots[idx].occupied {
			return tooltable.IndexResult{Idx: idx}, nil
		}
	}
	return tooltable.IndexResult{Idx: -1}, nil
}

func (f *fakeToolTable) PutTool(idx int32, entry tooltable.ToolEntry) (tooltable.PutToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if idx < 0 || int(idx) >= len(f.slots) {
		return tooltable.PutToolResult{Ok: false, Index: -1}, nil
	}
	entry.Idx = idx
	f.slots[idx] = fakeToolSlot{entry: entry, occupied: true}
	return tooltable.PutToolResult{Ok: true, Index: idx}, nil
}

func (f *fakeToolTable) DeleteTool(idx int32) (tooltable.DeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if idx < 0 || int(idx) >= len(f.slots) {
		return tooltable.DeleteResult{Ok: false}, nil
	}
	f.slots[idx] = fakeToolSlot{}
	return tooltable.DeleteResult{Ok: true}, nil
}

// installFakeToolTable publishes the fake as the package-level tooltable
// client the canon getters read, and restores the previous value on cleanup.
//
// pkgTTClient is a package global (the canon getters run on the interp thread
// and have no module handle), so tests using this cannot run in parallel —
// the same constraint setActiveCanon already imposes on the fixture.
func installFakeToolTable(t *testing.T, f *fakeToolTable) {
	t.Helper()

	cbs := tooltable.BuildTooltableCallbacks(f)
	t.Cleanup(func() { tooltable.FreeTooltableCallbacks(cbs) })

	prev := pkgTTClient
	pkgTTClient = tooltable.NewTooltableClient(cbs)
	t.Cleanup(func() { pkgTTClient = prev })
}

// fakeToolIO models the part of the IO controller a tool change actually
// depends on. mockIO answers every tool query with a constant, which makes M6
// spin forever: the interpreter returns INTERP_EXECUTE_FINISH and waits for
// tool-in-spindle to change, and with a constant 0 it never does.
//
// The handshake mirrored here is the classic iocontrol one: T preps a tool,
// M6 loads the prepped tool into the spindle and clears the prep, M61 sets
// the spindle tool directly, and "nothing prepped" reads as -1.
type fakeToolIO struct {
	mockIO
	tt *fakeToolTable

	mu        sync.Mutex
	prepped   int32 // prepped TOOL number, -1 = nothing prepped
	inSpindle int32 // TOOL number in the spindle, 0 = empty
}

func newFakeToolIO(tt *fakeToolTable) *fakeToolIO {
	return &fakeToolIO{tt: tt, prepped: -1}
}

// ToolPrepare receives a TOOL number (Canon.SelectTool passes the T word).
func (io *fakeToolIO) ToolPrepare(toolno int32) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	io.prepped = toolno
	return nil
}

// ToolLoad moves the prepped tool into the spindle and clears the prep.
//
// It also copies the tool's entry into slot 0. That copy is not bookkeeping:
// slot 0 IS the spindle in the 2.9 tooldata model, it is what iocontrol
// maintains on a non-random changer, and it is where the interpreter reads
// the current tool from — #5400 and the G43 offsets both come from slot 0,
// not from the tool's home slot. Without it M6 completes and G43 silently
// applies zero offsets.
func (io *fakeToolIO) ToolLoad() error {
	io.mu.Lock()
	prepped := io.prepped
	if prepped >= 0 {
		io.inSpindle = prepped
	}
	io.prepped = -1
	io.mu.Unlock()

	if prepped < 0 {
		return nil
	}
	res, err := io.tt.FindIndexForTool(prepped)
	if err != nil || res.Idx < 0 {
		return nil
	}
	entry, err := io.tt.GetTool(res.Idx)
	if err != nil {
		return nil
	}
	_, err = io.tt.PutTool(fakeSpindleIdx, entry)
	return err
}

// ToolUnload empties the spindle, slot 0 included.
func (io *fakeToolIO) ToolUnload() error {
	io.mu.Lock()
	io.inSpindle = 0
	io.mu.Unlock()

	_, err := io.tt.DeleteTool(fakeSpindleIdx)
	return err
}

// ToolSetOffset writes a tool table entry (G10 L1/L10/L11, and the tool
// setter). The IO controller owns the tool table write in this architecture —
// SetToolTableEntryCmd dispatches straight to it — so a no-op here means G10
// L1 updates only the interpreter's in-memory copy and the store silently
// keeps the old values.
//
// pocket is a SLOT: the interpreter resolves the P word to a table slot
// before sending it.
func (io *fakeToolIO) ToolSetOffset(pocket, toolno int32, x, y, z, a, b, c, u, v, w,
	diameter, frontangle, backangle float64, orientation int32,
) error {
	_, err := io.tt.PutTool(pocket, tooltable.ToolEntry{
		Idx: pocket, Toolno: toolno, Pocketno: pocket,
		XOffset: x, YOffset: y, ZOffset: z,
		AOffset: a, BOffset: b, COffset: c,
		UOffset: u, VOffset: v, WOffset: w,
		Diameter: diameter, Frontangle: frontangle, Backangle: backangle,
		Orientation: orientation,
	})
	return err
}

// ToolSetNumber is M61 — set the spindle tool without a change cycle.
func (io *fakeToolIO) ToolSetNumber(toolno int32) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	io.inSpindle = toolno
	return nil
}

func (io *fakeToolIO) GetToolInSpindle() (int32, error) {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.inSpindle, nil
}

// GetPocketPrepped answers the prepped SLOT, not the tool number — that is
// what the tool-prep-index HAL pin carries, and what GetExternalSelectedToolSlot
// reads straight through. -1 means nothing is prepped.
func (io *fakeToolIO) GetPocketPrepped() (int32, error) {
	io.mu.Lock()
	prepped := io.prepped
	io.mu.Unlock()

	if prepped < 0 {
		return -1, nil
	}
	res, err := io.tt.FindIndexForTool(prepped)
	if err != nil {
		return -1, nil
	}
	return res.Idx, nil
}
