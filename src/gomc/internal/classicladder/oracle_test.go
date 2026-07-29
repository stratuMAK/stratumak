// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Differential tests: the same project and the same script are run through the
// LinuxCNC 2.9 ClassicLadder engine (built headless from src/hal/classicladder
// by testdata/oracle/Makefile) and through the gomc RT engine, and the
// resulting variable state must match scan for scan.

const demoProject = "../../../../configs/sim/axis/classicladder/demo_sim_cl.clp"

// buildOracle compiles the 2.9 reference engine, skipping the test if it
// cannot be built here (no compiler, or the reference sources are gone).
func buildOracle(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs("testdata/oracle")
	if err != nil {
		t.Fatalf("resolve oracle dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
		t.Skipf("2.9 oracle not available: %v", err)
	}
	cmd := exec.Command("make", "-s")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the 2.9 oracle: %v\n%s", err, out)
	}
	return filepath.Join(dir, "cl-oracle")
}

// runOracle feeds the script to the reference engine and returns its dumps.
func runOracle(t *testing.T, bin, project, script string) string {
	t.Helper()

	abs, err := filepath.Abs(project)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	cmd := exec.Command(bin, abs)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle run failed: %v", err)
	}
	return extractDumps(string(out))
}

// extractDumps keeps only the dump blocks, dropping the banner the 2.9 loader
// prints when it allocates the PLC.
func extractDumps(s string) string {
	var b strings.Builder
	inDump := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "BITS ") {
			inDump = true
		}
		if inDump {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if line == "END" {
			inDump = false
		}
	}
	return b.String()
}

// runGomc loads the project into the gomc engine and runs the same script.
func runGomc(t *testing.T, project, script string) string {
	t.Helper()

	m := newTestModule(t)
	abs, err := filepath.Abs(project)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if err := m.loadCLPFile(abs); err != nil {
		t.Fatalf("load project: %v", err)
	}
	m.prepareRunForTest()

	out, err := m.runScript(script)
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return out
}

// diffDumps reports the first differing line, with context.
func diffDumps(t *testing.T, want, got string) {
	t.Helper()

	wantLines := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	n := len(wantLines)
	if len(gotLines) < n {
		n = len(gotLines)
	}
	for i := 0; i < n; i++ {
		if wantLines[i] != gotLines[i] {
			t.Errorf("dump line %d differs\n 2.9: %s\ngomc: %s", i+1, wantLines[i], gotLines[i])
			return
		}
	}
	if len(wantLines) != len(gotLines) {
		t.Errorf("dump length differs: 2.9 has %d lines, gomc has %d",
			len(wantLines), len(gotLines))
	}
}

func runDifferential(t *testing.T, project, script string) {
	t.Helper()

	bin := buildOracle(t)
	want := runOracle(t, bin, project, script)
	got := runGomc(t, project, script)
	if want == "" {
		t.Fatal("the oracle produced no dumps")
	}
	if want != got {
		diffDumps(t, want, got)
	}
}

// The estop chain of the shipped demo config: gui-estop (%I0), an external
// estop button (%I1) and the estop-reset strobe (%I2) driving estop-all-ok
// (%Q0). Walk it through a realistic reset sequence.
func TestOracle_DemoEstopChain(t *testing.T) {
	script := `dump
scan 1
dump
set 50 0 1
scan 1
dump
set 50 1 1
scan 1
dump
set 50 2 1
scan 1
dump
set 50 2 0
scan 1
dump
set 50 1 0
scan 1
dump
set 50 1 1
set 50 2 1
scan 1
dump
set 50 2 0
scan 1
dump
`
	runDifferential(t, demoProject, script)
}

// The demo's second rung is an intermittent lube pump built from three timers
// and a latch. Run it long enough for every timer to expire and recycle.
func TestOracle_DemoLubeTimers(t *testing.T) {
	var b strings.Builder
	// Bring the estop chain up, then turn the lube request on.
	b.WriteString("set 50 0 1\nset 50 1 1\nset 50 2 1\nscan 1\nset 50 2 0\nscan 1\n")
	b.WriteString("set 50 3 1\nset 50 4 1\n")
	// 60s of 10ms scans, dumping every second.
	for i := 0; i < 60; i++ {
		for j := 0; j < 100; j++ {
			b.WriteString("scan 10\n")
		}
		b.WriteString("dump\n")
	}
	runDifferential(t, demoProject, b.String())
}

// Drop the lube level sensor mid-cycle: the warning timer has to run and the
// low-lube output has to follow.
func TestOracle_DemoLubeSensorLost(t *testing.T) {
	var b strings.Builder
	b.WriteString("set 50 0 1\nset 50 1 1\nset 50 2 1\nscan 1\nset 50 2 0\nscan 1\n")
	b.WriteString("set 50 3 1\nset 50 4 1\n")
	for i := 0; i < 10; i++ {
		for j := 0; j < 100; j++ {
			b.WriteString("scan 10\n")
		}
		b.WriteString("dump\n")
	}
	// Sensor lost.
	b.WriteString("set 50 4 0\n")
	for i := 0; i < 30; i++ {
		for j := 0; j < 100; j++ {
			b.WriteString("scan 10\n")
		}
		b.WriteString("dump\n")
	}
	runDifferential(t, demoProject, b.String())
}

// Scan-period sensitivity: the same wall-clock time delivered in different
// slice sizes must land both engines in the same place.
func TestOracle_DemoVaryingScanPeriod(t *testing.T) {
	var b strings.Builder
	b.WriteString("set 50 0 1\nset 50 1 1\nset 50 2 1\nscan 1\nset 50 2 0\n")
	b.WriteString("set 50 3 1\nset 50 4 1\n")
	for i := 0; i < 200; i++ {
		b.WriteString("scan 1\nscan 7\nscan 20\nscan 3\n")
		b.WriteString("dump\n")
	}
	runDifferential(t, demoProject, b.String())
}
