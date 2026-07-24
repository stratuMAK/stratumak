// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gmitooltable "github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"

	// Registers the persist_sqlite gomod so the tooltable under test can bind
	// to a real persistence backend instead of a stand-in. That matters here:
	// the interesting behaviour (the legacy import, the empty-namespace
	// decision) lives in the interaction between the two, not in either alone.
	_ "github.com/sittner/linuxcnc/src/gomc/internal/persist_sqlite"
)

var testInstance int

// newBoundTooltable brings up a persist_sqlite instance and a tooltable bound
// to it, both rooted in dir. Instance names are unique per call because the API
// registry is process-global.
func newBoundTooltable(t *testing.T, dir string, ini *inifile.IniFile) *module {
	t.Helper()
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	testInstance++
	persistName := fmt.Sprintf("persist_%d", testInstance)
	tableName := fmt.Sprintf("tooltable_%d", testInstance)

	newPersist := gomc.GetFactory("persist_sqlite")
	if newPersist == nil {
		t.Fatal("persist_sqlite is not registered")
	}
	p, err := newPersist(ini, logger, persistName, []string{"dbpath=" + filepath.Join(dir, "db")})
	if err != nil {
		t.Fatalf("persist_sqlite: %v", err)
	}
	t.Cleanup(p.Destroy)
	if err := p.Start(); err != nil {
		t.Fatalf("persist_sqlite Start: %v", err)
	}

	mod, err := newTooltable(ini, logger, tableName, []string{"persist_instance=" + persistName})
	if err != nil {
		t.Fatalf("newTooltable: %v", err)
	}
	t.Cleanup(mod.Destroy)
	return mod.(*module)
}

// TestNotStartedIsAnError pins the ready() guard. The API is registered in the
// constructor but the persist client is only bound in Start, and on the runtime
// REST load path the API server is already serving in between — so a request
// can land on an unstarted module. It used to dereference the nil client.
func TestNotStartedIsAnError(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil) // constructed, deliberately not Started

	if _, err := m.ListTools(); err == nil {
		t.Error("ListTools on an unstarted module succeeded, want an error")
	}
	if _, err := m.GetTool(1); err == nil {
		t.Error("GetTool on an unstarted module succeeded, want an error")
	}
	if _, err := m.PutTool(1, gmitooltable.ToolEntry{}); err == nil {
		t.Error("PutTool on an unstarted module succeeded, want an error")
	}
	if _, err := m.DeleteTool(1); err == nil {
		t.Error("DeleteTool on an unstarted module succeeded, want an error")
	}
}

// TestStorageFailureIsReported is the end-to-end proof of the @rc_error
// conversion (G-1): tooltable reaches persist through the C callback table, so
// before the conversion a storage failure arrived as a zeroed payload and an
// unreadable tool table was indistinguishable from an empty one — a silent
// zero-offset tool on the tool-change path. Deleting the namespace out from
// under the handle makes every persist call fail; each tooltable method must
// now say so.
func TestStorageFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.PutTool(3, gmitooltable.ToolEntry{Toolno: 3, Pocketno: 3}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}

	// DeleteAll closes the database and invalidates the handle.
	if _, err := m.db.DeleteAll(m.dbHandle); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	tools, err := m.ListTools()
	if err == nil {
		t.Errorf("ListTools over broken storage = %+v, nil; want an error", tools)
	} else if !strings.Contains(err.Error(), "rc=") {
		// The rc is what proves the failure crossed the C callback boundary
		// rather than being caught locally.
		t.Errorf("ListTools error = %v, want the persist rc", err)
	}
	if got, err := m.GetTool(3); err == nil {
		t.Errorf("GetTool over broken storage = %+v, nil; want an error", got)
	}
	if got, err := m.PutTool(4, gmitooltable.ToolEntry{}); err == nil {
		t.Errorf("PutTool over broken storage = %+v, nil; want an error", got)
	}
	if got, err := m.DeleteTool(3); err == nil {
		t.Errorf("DeleteTool over broken storage = %+v, nil; want an error", got)
	}
}

// TestSlotCRUD exercises the store in its own terms: slots, not tool numbers.
func TestSlotCRUD(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := m.PutTool(3, gmitooltable.ToolEntry{
		Toolno: 7, Pocketno: 3, ZOffset: -2.5, Diameter: 6.35, Comment: "6mm end mill",
	}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}

	got, err := m.GetTool(3)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	// The tool number is DATA now, not the key: slot 3 holds tool 7. Keying by
	// toolno forced entry.Toolno = key on every write, which is what made the
	// spindle slot unrepresentable.
	if got.Toolno != 7 || got.Idx != 3 || got.ZOffset != -2.5 || got.Diameter != 6.35 {
		t.Errorf("GetTool = %+v, want idx 3 / toolno 7 / z -2.5 / dia 6.35", got)
	}
	if got.Comment != "6mm end mill" {
		t.Errorf("comment = %q, want it preserved", got.Comment)
	}

	// An unoccupied slot reads back as the empty entry (toolno -1), 2.9's
	// tooldata_entry_init — NOT as a zero-numbered tool, which would be
	// indistinguishable from a real T0 marker.
	missing, err := m.GetTool(99)
	if err != nil {
		t.Fatalf("GetTool of an empty slot: %v", err)
	}
	if missing.Toolno != -1 || missing.Idx != 99 {
		t.Errorf("empty slot returned %+v, want idx 99 toolno -1", missing)
	}

	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// Slot 0 is always there; slot 3 is the tool we wrote.
	if len(tools) != 2 || tools[0].Idx != 0 || tools[1].Idx != 3 {
		t.Errorf("ListTools = %+v, want the spindle slot then slot 3", tools)
	}

	if _, err := m.DeleteTool(3); err != nil {
		t.Fatalf("DeleteTool: %v", err)
	}
	tools, err = m.ListTools()
	if err != nil {
		t.Fatalf("ListTools after delete: %v", err)
	}
	if len(tools) != 1 || tools[0].Idx != 0 {
		t.Errorf("ListTools after delete = %+v, want only the spindle slot", tools)
	}
}

// TestSpindleSlotAlwaysExists is issue #272 in one assertion: a config with no
// tools at all must still produce a table whose slot 0 is readable, because
// every classic UI subscripts stat.tool_table[0] unguarded.
func TestSpindleSlotAlwaysExists(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Idx != 0 || tools[0].Toolno != -1 {
		t.Fatalf("empty table = %+v, want exactly the empty spindle slot", tools)
	}

	// On a NON-random changer the spindle slot is SESSION state: what the
	// changer put there must be readable now — and must NOT survive a
	// restart. 2.9's durable form (.tbl) never contained this row
	// (tooldata_save starts at idx 1); persisting it is how a power cycle
	// came to apply a phantom G43 offset from a tool io reported as absent.
	if _, err := m.PutTool(0, gmitooltable.ToolEntry{Toolno: 4, ZOffset: -9}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}
	sp, err := m.GetTool(0)
	if err != nil {
		t.Fatalf("GetTool(0): %v", err)
	}
	if sp.Toolno != 4 || sp.ZOffset != -9 {
		t.Fatalf("live spindle slot = %+v, want tool 4 z -9", sp)
	}

	m2 := newBoundTooltable(t, dir, nil)
	if err := m2.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	sp, err = m2.GetTool(0)
	if err != nil {
		t.Fatalf("GetTool(0) after restart: %v", err)
	}
	if sp.Toolno != emptyToolno {
		t.Errorf("non-random spindle slot after restart = %+v, want empty "+
			"(the session copy must not be resurrected from the store)", sp)
	}
}

// On a RANDOM changer slot 0 IS carousel pocket 0 — the tool in the spindle
// lives there and nowhere else, so it MUST persist: iocontrol restores
// toolInSpindle from it at startup.
func TestRandomSpindleSlotPersists(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	ini, err := inifile.ParseString("[EMCIO]\nRANDOM_TOOLCHANGER = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.PutTool(0, gmitooltable.ToolEntry{Toolno: 4, ZOffset: -9}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}

	m2 := newBoundTooltable(t, dir, ini)
	if err := m2.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	sp, err := m2.GetTool(0)
	if err != nil {
		t.Fatalf("GetTool(0): %v", err)
	}
	if sp.Toolno != 4 || sp.ZOffset != -9 {
		t.Errorf("random spindle slot after restart = %+v, want tool 4 z -9", sp)
	}
}

// A store written before the session-state rule (or under a config whose
// RANDOM_TOOLCHANGER flag was since flipped off) may hold a persisted slot-0
// row. A non-random Start must PURGE it, not just mask it: a row that merely
// lingered would resurrect an ancient "tool in spindle" the moment the flag
// flips back to random.
func TestNonRandomStartPurgesPersistedSpindleRow(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	randomIni, err := inifile.ParseString("[EMCIO]\nRANDOM_TOOLCHANGER = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	m := newBoundTooltable(t, dir, randomIni)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.PutTool(0, gmitooltable.ToolEntry{Toolno: 7, ZOffset: 3}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}

	// Reopen the same store non-random: slot 0 reads empty...
	m2 := newBoundTooltable(t, dir, nil)
	if err := m2.Start(); err != nil {
		t.Fatalf("non-random Start: %v", err)
	}
	if sp, err := m2.GetTool(0); err != nil || sp.Toolno != emptyToolno {
		t.Fatalf("non-random slot 0 over a random store = %+v, %v; want empty", sp, err)
	}

	// ...and the persisted row is genuinely gone: flipping back to random
	// starts with an empty spindle instead of tool 7 from another era.
	m3 := newBoundTooltable(t, dir, randomIni)
	if err := m3.Start(); err != nil {
		t.Fatalf("random re-Start: %v", err)
	}
	if sp, err := m3.GetTool(0); err != nil || sp.Toolno != emptyToolno {
		t.Errorf("random slot 0 after a non-random session = %+v, %v; "+
			"want empty (the stale row must have been purged, not masked)", sp, err)
	}
}

// TestSpindleSlotCannotBeDeleted — the invariant has to hold against the
// in-process C client too, which reaches these methods without the REST
// validator that enforces the IDL's idx >= 1 on the delete route.
func TestSpindleSlotCannotBeDeleted(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.DeleteTool(0); err == nil {
		t.Error("DeleteTool(0) succeeded; the spindle slot must not be removable")
	}
	if _, err := m.GetTool(0); err != nil {
		t.Fatalf("the spindle slot went missing: %v", err)
	}
}

// TestFindIndexForTool pins 2.9's tooldata_find_index_for_tool, including the
// rule that decides whether a loaded tool resolves to its own slot or to the
// spindle's copy of it.
func TestFindIndexForTool(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil) // non-random
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := m.PutTool(1, gmitooltable.ToolEntry{Toolno: 5, Pocketno: 2}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}
	// Tool 5 is in the spindle, so slot 0 carries a copy of it.
	if _, err := m.PutTool(0, gmitooltable.ToolEntry{Toolno: 5, Pocketno: 2}); err != nil {
		t.Fatalf("PutTool(spindle): %v", err)
	}

	// The tool's own slot must win over the spindle copy, or #<_current_pocket>
	// reads 0 for every tool that is loaded.
	if got, err := m.FindIndexForTool(5); err != nil || got.Idx != 1 {
		t.Errorf("FindIndexForTool(5) = %+v, %v; want idx 1", got, err)
	}
	// Non-random toolno 0 is the empty spindle itself.
	if got, err := m.FindIndexForTool(0); err != nil || got.Idx != 0 {
		t.Errorf("FindIndexForTool(0) = %+v, %v; want idx 0", got, err)
	}
	// -1 is 2.9's "no tool", and an unknown tool is simply absent.
	if got, err := m.FindIndexForTool(-1); err != nil || got.Idx != -1 {
		t.Errorf("FindIndexForTool(-1) = %+v, %v; want idx -1", got, err)
	}
	if got, err := m.FindIndexForTool(42); err != nil || got.Idx != -1 {
		t.Errorf("FindIndexForTool(42) = %+v, %v; want idx -1", got, err)
	}
}

// TestFindIndexForToolRandom — on a random changer toolno 0 is an ordinary
// empty-pocket marker, so it must NOT short-circuit to the spindle slot.
func TestFindIndexForToolRandom(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	ini, err := inifile.ParseString("[EMCIO]\nRANDOM_TOOLCHANGER = 1\n")
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !m.randomToolchange {
		t.Fatal("[EMCIO]RANDOM_TOOLCHANGER=1 was not picked up")
	}
	if _, err := m.PutTool(3, gmitooltable.ToolEntry{Toolno: 0, Pocketno: 3}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}
	if got, err := m.FindIndexForTool(0); err != nil || got.Idx != 3 {
		t.Errorf("FindIndexForTool(0) = %+v, %v; want the T0 marker at idx 3", got, err)
	}
}

// TestNextFreeIndex — the slot handed to a newly created tool. Slot 0 is never
// offered: it is the spindle, not a place to put a tool.
func TestNextFreeIndex(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, err := m.NextFreeIndex(); err != nil || got.Idx != 1 {
		t.Errorf("NextFreeIndex on an empty table = %+v, %v; want 1", got, err)
	}
	for _, idx := range []int32{1, 2, 4} {
		if _, err := m.PutTool(idx, gmitooltable.ToolEntry{Toolno: idx * 10}); err != nil {
			t.Fatalf("PutTool(%d): %v", idx, err)
		}
	}
	if got, err := m.NextFreeIndex(); err != nil || got.Idx != 3 {
		t.Errorf("NextFreeIndex = %+v, %v; want the hole at 3", got, err)
	}
}

// TestPutToolOptimisticConcurrency pins the stale-write refusal: a caller that
// echoes the stamp it read must be refused once the tool moved on, while a
// zero stamp keeps the classic last-write-wins for canon/G10/legacy writers.
func TestPutToolOptimisticConcurrency(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := m.PutTool(5, gmitooltable.ToolEntry{Toolno: 5, ZOffset: 1}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}
	base, err := m.GetTool(5)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if base.Updated == 0 {
		t.Fatal("stored tool reads back with a zero stamp — nothing to compare against")
	}

	// Matching stamp: the conditional write lands and produces a NEW stamp
	// (nanosecond stamps must never repeat, or the check is blind).
	upd := base
	upd.ZOffset = 2
	if _, err := m.PutTool(5, upd); err != nil {
		t.Fatalf("PutTool with the matching stamp: %v", err)
	}
	cur, err := m.GetTool(5)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if cur.ZOffset != 2 {
		t.Errorf("conditional write did not land (z = %v)", cur.ZOffset)
	}
	if cur.Updated == base.Updated {
		t.Errorf("stamp did not change across a write (still %d)", cur.Updated)
	}

	// Stale stamp (the pre-rewrite baseline): refused, value untouched.
	stale := base
	stale.ZOffset = 99
	if _, err := m.PutTool(5, stale); err == nil {
		t.Fatal("PutTool with a stale stamp succeeded; want a conflict error")
	}
	after, _ := m.GetTool(5)
	if after.ZOffset != 2 {
		t.Errorf("stale write modified the tool anyway (z = %v)", after.ZOffset)
	}

	// Baseline against a deleted slot: recreating it from a stale dialog is a
	// conflict, not an upsert.
	if _, err := m.DeleteTool(5); err != nil {
		t.Fatalf("DeleteTool: %v", err)
	}
	ghost := cur
	if _, err := m.PutTool(5, ghost); err == nil {
		t.Fatal("PutTool with a baseline onto a deleted tool succeeded; want a conflict error")
	}

	// Zero stamp: unconditional, recreates the tool (last-write-wins).
	if _, err := m.PutTool(5, gmitooltable.ToolEntry{Toolno: 5, ZOffset: 7}); err != nil {
		t.Fatalf("unconditional PutTool: %v", err)
	}

	// The stamp must not leak into the stored JSON (it lives on the persist
	// row): a fresh read's stamp comes from the row of THIS write, and a
	// second unconditional write refreshes it.
	fresh, err := m.GetTool(5)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if fresh.Updated == 0 || fresh.Updated == base.Updated {
		t.Errorf("recreated tool has stamp %d (base was %d); want a fresh non-zero stamp",
			fresh.Updated, base.Updated)
	}
}

// writeIni builds an INI naming a tool table, plus the .tbl itself.
func writeIni(t *testing.T, dir, tbl string) *inifile.IniFile {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "tool.tbl"), []byte(tbl), 0o644); err != nil {
		t.Fatal(err)
	}
	ini, err := inifile.ParseString("[EMCIO]\nTOOL_TABLE = tool.tbl\n")
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	return ini
}

// TestLegacyImport covers the one-shot migration end to end: that a malformed
// line is dropped rather than imported with zeroed offsets, and that slots are
// assigned the way 2.9 assigns them on a non-random changer — a "fakepocket"
// counter from 1, with the file's P value kept as the carousel pocket.
func TestLegacyImport(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	ini := writeIni(t, dir, ""+
		"; a comment line\n"+
		"\n"+
		"T1 P1 Z-1.5 D6 ;six mil\n"+
		"t2 p2 Z-2.5 D8\n"+ // lowercase must import, as in the C parser
		"T3 P3 Zgarbage D10\n"+ // malformed: must be skipped entirely
		"T4 P7 Z-4.5\n")

	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byIdx := map[int32]gmitooltable.ToolEntry{}
	for _, tool := range tools {
		byIdx[tool.Idx] = tool
	}
	// Slot 0 (spindle) plus three imported tools; the malformed line is gone.
	if len(byIdx) != 4 {
		t.Fatalf("imported %d slots (%v), want the spindle plus 3 tools — tool 3 is malformed and must be dropped", len(byIdx), tools)
	}
	if got := byIdx[0]; got.Toolno != -1 {
		t.Errorf("spindle slot = %+v, want it left empty by the import", got)
	}
	if got := byIdx[1]; got.Toolno != 1 || got.Pocketno != 1 || got.ZOffset != -1.5 || got.Diameter != 6 || got.Comment != "six mil" {
		t.Errorf("slot 1 = %+v, want tool 1 / pocket 1 / z -1.5 / dia 6 / comment \"six mil\"", got)
	}
	if got := byIdx[2]; got.Toolno != 2 || got.ZOffset != -2.5 || got.Diameter != 8 {
		t.Errorf("slot 2 (lowercase keys) = %+v, want tool 2 / z -2.5 / dia 8", got)
	}
	// The skipped line must not consume a slot, and P is the carousel pocket,
	// not the slot: tool 4 lands in slot 3 while keeping pocket 7.
	if got := byIdx[3]; got.Toolno != 4 || got.Pocketno != 7 || got.ZOffset != -4.5 {
		t.Errorf("slot 3 = %+v, want tool 4 / pocket 7 / z -4.5", got)
	}
}

// TestLegacyImportRandom — on a random changer the .tbl's P value IS the slot,
// so a P0 line is the spindle record 2.9 writes first for random changers.
func TestLegacyImportRandom(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "tool.tbl"),
		[]byte("T5 P0 Z-1\nT6 P3 Z-2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ini, err := inifile.ParseString("[EMCIO]\nTOOL_TABLE = tool.tbl\nRANDOM_TOOLCHANGER = 1\n")
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sp, err := m.GetTool(0)
	if err != nil {
		t.Fatalf("GetTool(0): %v", err)
	}
	if sp.Toolno != 5 {
		t.Errorf("spindle slot = %+v, want the P0 line's tool 5", sp)
	}
	six, err := m.GetTool(3)
	if err != nil {
		t.Fatalf("GetTool(3): %v", err)
	}
	if six.Toolno != 6 {
		t.Errorf("slot 3 = %+v, want tool 6 (P3)", six)
	}
}

// TestLegacyImportOnlyOnce pins that the import is a *migration*, not a reload:
// once the namespace holds anything, the .tbl must never be replayed over it.
// Edited offsets silently reverting to the shipped file is the failure mode.
func TestLegacyImportOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	ini := writeIni(t, dir, "T1 P1 Z-1.5\n")

	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The operator retouches the tool.
	if _, err := m.PutTool(1, gmitooltable.ToolEntry{Toolno: 1, Pocketno: 1, ZOffset: -1.75}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}

	// Restart against the same db directory: the .tbl still says -1.5.
	m2 := newBoundTooltable(t, dir, ini)
	if err := m2.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	got, err := m2.GetTool(1)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if got.ZOffset != -1.75 {
		t.Errorf("Z offset = %v after restart, want the edited -1.75 — the legacy .tbl was replayed", got.ZOffset)
	}
}

// TestNoLegacyImportWithoutIni — halrun mode passes a nil INI, so there is no
// [EMCIO]TOOL_TABLE to import from and Start must still succeed.
func TestNoLegacyImportWithoutIni(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start with a nil INI: %v", err)
	}
	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// Just the spindle slot: nothing was imported, but the table is still a
	// table.
	if len(tools) != 1 || tools[0].Idx != 0 {
		t.Errorf("ListTools = %+v, want only the spindle slot", tools)
	}
}

// TestStartWithoutPersistInstance — a config naming a persistence instance that
// was never loaded must fail Start loudly, not come up half-wired.
func TestStartWithoutPersistInstance(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testInstance++
	mod, err := newTooltable(nil, logger, fmt.Sprintf("tt_missing_%d", testInstance), []string{"persist_instance=nosuchthing"})
	if err != nil {
		t.Fatalf("newTooltable: %v", err)
	}
	t.Cleanup(mod.Destroy)
	if err := mod.Start(); err == nil {
		t.Error("Start with an unknown persist instance succeeded, want an error")
	}
}
