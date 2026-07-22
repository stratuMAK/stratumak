// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func TestToolCRUD(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	m := newBoundTooltable(t, dir, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := m.PutTool(3, gmitooltable.ToolEntry{
		Pocketno: 3, ZOffset: -2.5, Diameter: 6.35, Comment: "6mm end mill",
	}); err != nil {
		t.Fatalf("PutTool: %v", err)
	}

	got, err := m.GetTool(3)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	// PutTool stamps the tool number from the argument, not from the body.
	if got.Toolno != 3 || got.ZOffset != -2.5 || got.Diameter != 6.35 {
		t.Errorf("GetTool = %+v, want toolno 3 / z -2.5 / dia 6.35", got)
	}

	// A tool that was never stored reads back as the zero entry, not an error —
	// callers distinguish by Toolno.
	missing, err := m.GetTool(99)
	if err != nil {
		t.Fatalf("GetTool of a missing tool: %v", err)
	}
	if missing.Toolno != 0 {
		t.Errorf("missing tool returned %+v, want the zero entry", missing)
	}

	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Toolno != 3 {
		t.Errorf("ListTools = %+v, want just tool 3", tools)
	}

	if _, err := m.DeleteTool(3); err != nil {
		t.Fatalf("DeleteTool: %v", err)
	}
	tools, err = m.ListTools()
	if err != nil {
		t.Fatalf("ListTools after delete: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("ListTools after delete = %+v, want empty", tools)
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

// TestLegacyImport covers the one-shot migration end to end, including that a
// malformed line is dropped rather than imported with zeroed offsets.
func TestLegacyImport(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	ini := writeIni(t, dir, ""+
		"; a comment line\n"+
		"\n"+
		"T1 P1 Z-1.5 D6 ;six mil\n"+
		"t2 p2 Z-2.5 D8\n"+ // lowercase must import, as in the C parser
		"T3 P3 Zgarbage D10\n"+ // malformed: must be skipped entirely
		"T4 P4 Z-4.5\n")

	m := newBoundTooltable(t, dir, ini)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tools, err := m.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byNo := map[int32]gmitooltable.ToolEntry{}
	for _, tool := range tools {
		byNo[tool.Toolno] = tool
	}
	if len(byNo) != 3 {
		t.Fatalf("imported %d tools (%v), want 3 — tool 3 is malformed and must be dropped", len(byNo), tools)
	}
	if _, ok := byNo[3]; ok {
		t.Error("the malformed line was imported; a zeroed Z offset is a crash")
	}
	if got := byNo[1]; got.ZOffset != -1.5 || got.Diameter != 6 || got.Comment != "six mil" {
		t.Errorf("tool 1 = %+v, want z -1.5 / dia 6 / comment \"six mil\"", got)
	}
	if got := byNo[2]; got.ZOffset != -2.5 || got.Diameter != 8 {
		t.Errorf("tool 2 (lowercase keys) = %+v, want z -2.5 / dia 8", got)
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
	if _, err := m.PutTool(1, gmitooltable.ToolEntry{Pocketno: 1, ZOffset: -1.75}); err != nil {
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
	if len(tools) != 0 {
		t.Errorf("ListTools = %+v, want empty", tools)
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
