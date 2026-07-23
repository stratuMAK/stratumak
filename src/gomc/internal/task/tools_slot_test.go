// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tools"
	gmitooltable "github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"

	// Real persistence + tool table behind the API boundary: what is under
	// test here is the tool-number <-> slot translation, and a stand-in store
	// would just re-state the translation instead of checking it.
	_ "github.com/sittner/linuxcnc/src/gomc/internal/persist_sqlite"
	_ "github.com/sittner/linuxcnc/src/gomc/internal/tooltable"
)

var slotTestInstance int

// newToolsImpl brings up persist_sqlite + tooltable and returns the
// operator-facing tools API wired to them through the real C callback table.
// The tooltable module itself is always constructed non-random (nil INI); the
// `random` flag drives only the task-layer behaviour under test.
func newToolsImpl(t *testing.T, random bool) *toolsImpl {
	t.Helper()
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	slotTestInstance++
	persistName := fmt.Sprintf("slottest_persist_%d", slotTestInstance)
	tableName := fmt.Sprintf("slottest_tt_%d", slotTestInstance)

	newPersist := gomc.GetFactory("persist_sqlite")
	if newPersist == nil {
		t.Fatal("persist_sqlite is not registered")
	}
	p, err := newPersist(nil, logger, persistName, []string{"dbpath=" + filepath.Join(dir, "db")})
	if err != nil {
		t.Fatalf("persist_sqlite: %v", err)
	}
	t.Cleanup(p.Destroy)
	if err := p.Start(); err != nil {
		t.Fatalf("persist_sqlite Start: %v", err)
	}

	newTT := gomc.GetFactory("tooltable")
	if newTT == nil {
		t.Fatal("tooltable is not registered")
	}
	tt, err := newTT(nil, logger, tableName, []string{"persist_instance=" + persistName})
	if err != nil {
		t.Fatalf("tooltable: %v", err)
	}
	t.Cleanup(tt.Destroy)
	if err := tt.Start(); err != nil {
		t.Fatalf("tooltable Start: %v", err)
	}

	cbs, err := apiserver.DefaultRegistry().GetAPIFor("slottest", "tooltable", tableName, 2)
	if err != nil {
		t.Fatalf("tooltable API lookup: %v", err)
	}
	client := gmitooltable.NewTooltableClient(unsafe.Pointer(cbs))
	mod := &milltaskModule{ttClient: client}
	if random {
		// Only randomToolchanger is read off task on these paths; io stays nil
		// so syncSpindleSlot self-disables either way.
		mod.task = &Task{randomToolchanger: true}
	}
	return &toolsImpl{module: mod}
}

// TestToolsAPIHidesTheSpindleSlot — the operator-facing API lists TOOLS. The
// spindle slot is a copy of a tool that also has its own row, so listing it
// showed every loaded tool twice; before the store was keyed by slot it showed
// up as a phantom tool 0 (its stored tool number having been clobbered with
// the key) in the tooledit UI and in every REST client.
func TestToolsAPIHidesTheSpindleSlot(t *testing.T) {
	ti := newToolsImpl(t, false)

	// An empty table lists no tools even though slot 0 exists.
	list, err := ti.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("ListTools on an empty table = %+v, want none", list)
	}

	if _, err := ti.PutTool(7, tools.ToolEntry{Pocketno: 3, ZOffset: -1.5}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}
	// Simulate a tool change: the changer copies tool 7 into the spindle slot.
	if _, err := ti.module.ttClient.PutTool(0, gmitooltable.ToolEntry{
		Toolno: 7, Pocketno: 3, ZOffset: -1.5,
	}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}

	list, err = ti.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list) != 1 || list[0].Toolno != 7 {
		t.Errorf("ListTools = %+v, want exactly tool 7 (the spindle copy must not appear)", list)
	}

	// And a lookup resolves to the tool's own slot, not the spindle's copy of
	// it — otherwise editing a loaded tool would edit the spindle mirror.
	got, err := ti.GetTool(7)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if got.Toolno != 7 || got.Pocketno != 3 {
		t.Errorf("GetTool(7) = %+v, want tool 7 in pocket 3", got)
	}
}

// TestPutToolAllocatesASlot — a new tool gets a free slot, never the spindle,
// and re-putting the same tool number updates in place instead of consuming
// another slot.
func TestPutToolAllocatesASlot(t *testing.T) {
	ti := newToolsImpl(t, false)

	for _, tno := range []int32{4, 9, 2} {
		if _, err := ti.PutTool(tno, tools.ToolEntry{Pocketno: tno * 2}); err != nil {
			t.Fatalf("PutTool(%d): %v", tno, err)
		}
	}

	slots, err := ti.module.ttClient.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	bySlot := map[int32]int32{}
	for _, s := range slots {
		bySlot[s.Idx] = s.Toolno
	}
	// Slot 0 stays the empty spindle; the three tools take 1, 2, 3 in order.
	if bySlot[0] != -1 {
		t.Errorf("spindle slot holds tool %d, want it left empty", bySlot[0])
	}
	if bySlot[1] != 4 || bySlot[2] != 9 || bySlot[3] != 2 {
		t.Errorf("slot assignment = %v, want 1:T4 2:T9 3:T2", bySlot)
	}

	// An update must not allocate a second slot for the same tool.
	if _, err := ti.PutTool(9, tools.ToolEntry{Pocketno: 18, ZOffset: -7}); err != nil {
		t.Fatalf("PutTool(9) update: %v", err)
	}
	slots, err = ti.module.ttClient.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(slots) != 4 { // spindle + 3 tools
		t.Errorf("after updating tool 9 the table has %d slots, want 4", len(slots))
	}
	got, err := ti.GetTool(9)
	if err != nil {
		t.Fatalf("GetTool(9): %v", err)
	}
	if got.ZOffset != -7 || got.Pocketno != 18 {
		t.Errorf("updated tool 9 = %+v, want z -7 / pocket 18", got)
	}
}

// TestDeleteToolLeavesTheSpindleSlot — deleting by tool number must free that
// tool's slot and nothing else. Tool numbers <= 0 are refused outright, so the
// spindle is not reachable through this API at all.
func TestDeleteToolLeavesTheSpindleSlot(t *testing.T) {
	ti := newToolsImpl(t, false)
	if _, err := ti.PutTool(3, tools.ToolEntry{Pocketno: 1}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}

	if _, err := ti.DeleteTool(0); err == nil {
		t.Error("DeleteTool(0) succeeded; tool numbers must be > 0")
	}
	if _, err := ti.DeleteTool(3); err != nil {
		t.Fatalf("DeleteTool(3): %v", err)
	}
	// Deleting a tool that is already gone is not an error (the UI may retry).
	if _, err := ti.DeleteTool(3); err != nil {
		t.Errorf("DeleteTool of an absent tool: %v", err)
	}

	slots, err := ti.module.ttClient.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(slots) != 1 || slots[0].Idx != 0 {
		t.Errorf("after deleting the only tool the table is %+v, want just the spindle slot", slots)
	}
}

// TestRandomChangerListsTheSpindlePocket — on a random toolchanger slot 0 is
// pocket 0, the tool in the spindle lives THERE and nowhere else. Treating it
// as a copy (as the non-random path must) would hide the loaded tool from
// every UI and make it uneditable.
func TestRandomChangerListsTheSpindlePocket(t *testing.T) {
	ti := newToolsImpl(t, true)

	// The changer put tool 8 in the spindle pocket.
	if _, err := ti.module.ttClient.PutTool(0, gmitooltable.ToolEntry{
		Toolno: 8, Pocketno: 0, ZOffset: -2,
	}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}

	list, err := ti.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list) != 1 || list[0].Toolno != 8 {
		t.Fatalf("ListTools = %+v, want the spindle-pocket tool 8", list)
	}
	got, err := ti.GetTool(8)
	if err != nil {
		t.Fatalf("GetTool(8): %v", err)
	}
	if got.Toolno != 8 || got.ZOffset != -2 {
		t.Errorf("GetTool(8) = %+v, want tool 8 z -2", got)
	}

	// Editing it must land in pocket 0, not allocate a new slot.
	if _, err := ti.PutTool(8, tools.ToolEntry{Pocketno: 0, ZOffset: -3}); err != nil {
		t.Fatalf("PutTool(8): %v", err)
	}
	slots, err := ti.module.ttClient.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(slots) != 1 || slots[0].Idx != 0 || slots[0].ZOffset != -3 {
		t.Errorf("after the edit the table is %+v, want tool 8 at slot 0 with z -3", slots)
	}

	// Deleting it empties the slot rather than removing it: slot 0 must exist.
	if _, err := ti.DeleteTool(8); err != nil {
		t.Fatalf("DeleteTool(8): %v", err)
	}
	slots, err = ti.module.ttClient.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(slots) != 1 || slots[0].Idx != 0 || slots[0].Toolno != -1 {
		t.Errorf("after the delete the table is %+v, want an empty slot 0", slots)
	}
}

// TestRandomChangerNeedsAPocketForANewTool — the slot IS the pocket there, so
// a new tool without one has nowhere to go, and one whose pocket is taken must
// be refused rather than silently overwriting the occupant.
func TestRandomChangerNeedsAPocketForANewTool(t *testing.T) {
	ti := newToolsImpl(t, true)

	if _, err := ti.PutTool(5, tools.ToolEntry{}); err == nil {
		t.Error("PutTool with no pocket succeeded on a random changer")
	}
	if _, err := ti.PutTool(5, tools.ToolEntry{Pocketno: 4}); err != nil {
		t.Fatalf("PutTool(5, pocket 4): %v", err)
	}
	if _, err := ti.PutTool(6, tools.ToolEntry{Pocketno: 4}); err == nil {
		t.Error("PutTool into an occupied pocket succeeded; want a conflict")
	}
	got, err := ti.GetTool(5)
	if err != nil || got.Toolno != 5 {
		t.Errorf("GetTool(5) = %+v, %v; the occupant must be untouched", got, err)
	}
}

// Two concurrent creates must land on distinct slots. The new-tool path is
// check-then-act (NextFreeIndex, then PutTool) with no allocate-if-empty
// primitive in the store, so without writeMu both creates are handed the same
// free slot and the second silently destroys the first tool — a create's
// Updated==0 bypasses the stamp CAS by design.
func TestPutToolConcurrentCreatesGetDistinctSlots(t *testing.T) {
	ti := newToolsImpl(t, false)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ti.PutTool(int32(100+i), tools.ToolEntry{ZOffset: float64(i) + 0.5})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create of tool %d failed: %v", 100+i, err)
		}
	}

	// Every tool must still exist, with its own geometry — no silent overwrite.
	for i := 0; i < n; i++ {
		got, err := ti.GetTool(int32(100 + i))
		if err != nil {
			t.Fatalf("GetTool(%d): %v", 100+i, err)
		}
		if got.Toolno != int32(100+i) || got.ZOffset != float64(i)+0.5 {
			t.Errorf("tool %d came back as toolno=%d z=%v — a concurrent create overwrote it",
				100+i, got.Toolno, got.ZOffset)
		}
	}
}
