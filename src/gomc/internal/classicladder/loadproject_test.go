// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// REST load_project rewrites the whole program in place, so it carries 2.9's
// StopRunIfRunning/RunBackIfStopped bracket. These tests pin the observable
// contract: the run state survives a load, a running scan is waited out, the
// dynamic state is prepared before running again, and a failed load leaves
// the project untouched.

func TestLoadProject_RestoresRunStateAndPrepares(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatal(err)
	}

	m := newTestModule(t)
	m.setState(stateRun)
	if rv, err := m.loadProjectResolved(src); err != nil || rv != 0 {
		t.Fatalf("load: rv=%d err=%v", rv, err)
	}
	if m.getState() != stateRun {
		t.Errorf("state after load-while-RUN = %d, want RUN", m.getState())
	}
	// cl_prepare_all_datas_before_run must have run: an old-style timer sits
	// at its preset before the first scan (demo timer 0 is 10s = 10000ms).
	if got := int(rtTimers(m.rt)[0].value); got != int(rtTimers(m.rt)[0].preset) || got == 0 {
		t.Errorf("timer 0 value = %d, preset = %d: load did not prepare the dynamic state",
			got, int(rtTimers(m.rt)[0].preset))
	}
	if m.projectFile != src {
		t.Errorf("projectFile = %q, want %q", m.projectFile, src)
	}

	m.setState(stateStop)
	if _, err := m.loadProjectResolved(src); err != nil {
		t.Fatal(err)
	}
	if m.getState() != stateStop {
		t.Error("a load must not start a stopped PLC")
	}
}

func TestLoadProject_WaitsForTheScanInFlight(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatal(err)
	}

	m := newTestModule(t)
	m.setState(stateRun)
	m.setScanningForTest(1)

	done := make(chan struct{})
	go func() {
		_, _ = m.loadProjectResolved(src)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("load went ahead while a scan was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	m.setScanningForTest(0)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("load never proceeded after the scan settled")
	}
	if m.getState() != stateRun {
		t.Error("run state not restored")
	}
}

func TestLoadProject_FailureLeavesTheProjectIntact(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatal(err)
	}

	m := newTestModule(t)
	if _, err := m.loadProjectResolved(src); err != nil {
		t.Fatal(err)
	}
	m.setState(stateRun)
	before, err := m.GetProgram()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.loadProjectResolved(filepath.Join(t.TempDir(), "missing.clp")); err == nil {
		t.Fatal("loading a missing file succeeded")
	}
	if m.getState() != stateRun {
		t.Error("failed load left the PLC stopped")
	}
	if m.projectFile != src {
		t.Errorf("failed load rewrote projectFile to %q", m.projectFile)
	}
	after, err := m.GetProgram()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Rungs) != len(after.Rungs) || len(before.Sections) != len(after.Sections) {
		t.Fatal("failed load altered the program")
	}
	for i := range before.Rungs {
		if before.Rungs[i].Used != after.Rungs[i].Used {
			t.Fatalf("rung %d used-flag changed across a failed load", i)
		}
	}
}

// A truncated #NAME line in sections.csv used to panic the parser after the
// old program was already wiped — the whole controller came down with it.
func TestLoadProject_ShortNameLineLoadsLeniently(t *testing.T) {
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	mangled := filepath.Join(t.TempDir(), "mangled.clp")
	// Prepend degenerate #NAME variants to sections.csv: bare marker, no
	// number, number but nothing after the equals sign.
	content := string(raw)
	content = replaceOnce(t, content,
		"_FILE-sections.csv\n",
		"_FILE-sections.csv\n#NAME\n#NAME=\n#NAME7\n")
	if err := os.WriteFile(mangled, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModule(t)
	if _, err := m.loadProjectResolved(mangled); err != nil {
		t.Fatalf("lenient load failed: %v", err)
	}
	prog, err := m.GetProgram()
	if err != nil {
		t.Fatal(err)
	}
	used := 0
	for _, s := range prog.Sections {
		if s.Used {
			used++
		}
	}
	if used == 0 {
		t.Error("mangled #NAME lines kept the sections from loading")
	}
}

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, new, 1)
}
