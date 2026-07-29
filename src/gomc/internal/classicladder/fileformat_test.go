// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"os"
	"path/filepath"
	"testing"
)

// Loading, saving and re-loading a project must land on the same program. This
// catches parser/emitter pairs that agree with each other but not with the
// file format — the timers_iec.csv field order was one of those.
func TestCLP_RoundTrip(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}

	first := newTestModule(t)
	if err := first.loadCLPFile(src); err != nil {
		t.Fatalf("load project: %v", err)
	}
	firstProg, err := first.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}

	saved := filepath.Join(t.TempDir(), "roundtrip.clp")
	if err := first.saveCLPFile(saved); err != nil {
		t.Fatalf("save project: %v", err)
	}

	second := newTestModule(t)
	if err := second.loadCLPFile(saved); err != nil {
		t.Fatalf("reload saved project: %v", err)
	}
	secondProg, err := second.GetProgram()
	if err != nil {
		t.Fatalf("get reloaded program: %v", err)
	}

	if len(firstProg.Rungs) != len(secondProg.Rungs) {
		t.Fatalf("rung count changed: %d -> %d", len(firstProg.Rungs), len(secondProg.Rungs))
	}
	for i := range firstProg.Rungs {
		a, b := firstProg.Rungs[i], secondProg.Rungs[i]
		if a.Used != b.Used || a.Label != b.Label || a.Comment != b.Comment ||
			a.PrevRung != b.PrevRung || a.NextRung != b.NextRung {
			t.Errorf("rung %d header changed:\n before %+v\n after  %+v", i, a, b)
			continue
		}
		for j := range a.Elements {
			if a.Elements[j] != b.Elements[j] {
				t.Errorf("rung %d element %d changed: %+v -> %+v",
					i, j, a.Elements[j], b.Elements[j])
				break
			}
		}
	}

	for i := range firstProg.Sections {
		if firstProg.Sections[i] != secondProg.Sections[i] {
			t.Errorf("section %d changed:\n before %+v\n after  %+v",
				i, firstProg.Sections[i], secondProg.Sections[i])
		}
	}
	for i := range firstProg.Timers {
		a, b := firstProg.Timers[i], secondProg.Timers[i]
		if a.Preset != b.Preset || a.Base != b.Base {
			t.Errorf("timer %d changed: preset %d base %d -> preset %d base %d",
				i, a.Preset, a.Base, b.Preset, b.Base)
		}
	}
	for i := range firstProg.Monostables {
		a, b := firstProg.Monostables[i], secondProg.Monostables[i]
		if a.Preset != b.Preset || a.Base != b.Base {
			t.Errorf("monostable %d changed: preset %d base %d -> preset %d base %d",
				i, a.Preset, a.Base, b.Preset, b.Base)
		}
	}
	for i := range firstProg.TimersIec {
		a, b := firstProg.TimersIec[i], secondProg.TimersIec[i]
		if a.Preset != b.Preset || a.Base != b.Base || a.Mode != b.Mode {
			t.Errorf("IEC timer %d changed: %+v -> %+v", i, a, b)
		}
	}
	for i := range firstProg.Counters {
		if firstProg.Counters[i].Preset != secondProg.Counters[i].Preset {
			t.Errorf("counter %d preset changed: %d -> %d",
				i, firstProg.Counters[i].Preset, secondProg.Counters[i].Preset)
		}
	}
	for i := range firstProg.Symbols {
		if firstProg.Symbols[i] != secondProg.Symbols[i] {
			t.Errorf("symbol %d changed: %+v -> %+v",
				i, firstProg.Symbols[i], secondProg.Symbols[i])
		}
	}
	for i := range firstProg.ArithmExprs {
		if firstProg.ArithmExprs[i] != secondProg.ArithmExprs[i] {
			t.Errorf("expression %d changed: %q -> %q",
				i, firstProg.ArithmExprs[i].Expr, secondProg.ArithmExprs[i].Expr)
		}
	}
}

// A project the port has written must still run identically under the 2.9
// engine — that is what makes a saved .clp portable rather than merely
// self-consistent.
func TestCLP_SavedProjectRunsTheSameUnder29(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}

	m := newTestModule(t)
	if err := m.loadCLPFile(src); err != nil {
		t.Fatalf("load project: %v", err)
	}
	saved := filepath.Join(t.TempDir(), "written.clp")
	if err := m.saveCLPFile(saved); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("saved project missing: %v", err)
	}

	script := "set 50 0 1\nset 50 1 1\nset 50 2 1\nscan 1\nset 50 2 0\nset 50 3 1\nset 50 4 1\n"
	for i := 0; i < 30; i++ {
		for j := 0; j < 100; j++ {
			script += "scan 10\n"
		}
		script += "dump\n"
	}

	bin := buildOracle(t)
	fromOriginal := runOracle(t, bin, src, script)
	fromSaved := runOracle(t, bin, saved, script)
	if fromOriginal == "" {
		t.Fatal("the oracle produced no dumps")
	}
	if fromOriginal != fromSaved {
		diffDumps(t, fromOriginal, fromSaved)
	}
}

// The timer sections of a project written by the port must be byte-compatible
// with what 2.9 writes: a base id, and a preset in base units.
func TestCLP_TimerSectionsUseBaseIDs(t *testing.T) {
	m := newTestModule(t)
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if err := m.loadCLPFile(src); err != nil {
		t.Fatalf("load project: %v", err)
	}

	// demo_sim_cl.clp opens timers.csv with "1,10" — base id 1 (seconds),
	// preset 10 seconds.
	got := m.emitTimers()
	const wantFirst = "1,10"
	if len(got) < len(wantFirst) || got[:len(wantFirst)] != wantFirst {
		t.Errorf("emitTimers starts with %q, want %q", firstLine(got), wantFirst)
	}
	// timers_iec.csv opens with "1,0,0" — base id, preset, mode.
	gotIEC := firstDataLine(m.emitTimersIEC())
	if gotIEC != "1,0,0" {
		t.Errorf("emitTimersIEC first entry = %q, want \"1,0,0\"", gotIEC)
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// firstDataLine skips the "#VER=" header a section may carry.
func firstDataLine(s string) string {
	for len(s) > 0 {
		line := firstLine(s)
		if len(line) > 0 && line[0] != '#' {
			return line
		}
		s = s[len(line):]
		if len(s) > 0 {
			s = s[1:]
		}
	}
	return ""
}
