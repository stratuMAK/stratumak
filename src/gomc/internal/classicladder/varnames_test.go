// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every variable type the name table knows about, plus a couple it does not, so
// the sweep covers refusals as well as conversions.
var sweepTypes = []int{
	varMemBit, 10, 11, 230, 231, // %B, %T.D, %T.R, %T.P, %T.V
	20, 240, 241, // %M.R, %M.P, %M.V
	25, 26, 27, 250, varCounterValue, // %C.D, %C.E, %C.F, %C.P, %C.V
	15, varTimerIECPreset, 261, // %TM.Q, %TM.P, %TM.V
	varPhysInput, varPhysOutput, varMemWord,
	varPhysWordInput, varPhysWordOutput,
	varPhysFloatIn, varPhysFloatOut,
	70,              // %E
	varStepTime, 30, // %X.V, %X
	199, 999, -1, // not in the table
}

// runOracleLines feeds a script to the reference engine and returns its output
// lines that start with the given prefix.
func runOracleLines(t *testing.T, bin, script, prefix string) []string {
	t.Helper()

	cmd := exec.Command(bin, mustAbs(t, demoProject))
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle run failed: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		}
	}
	return lines
}

// Rendering a variable must agree with 2.9 across the whole space — including
// which combinations it refuses.
func TestVarNames_FormatMatches29(t *testing.T) {
	bin := buildOracle(t)
	m := newTestModule(t)

	type probe struct{ varType, offset int }
	var probes []probe
	var script strings.Builder
	for _, vt := range sweepTypes {
		// Past the end of every region, so the bounds are compared too.
		for off := -1; off < 20; off++ {
			probes = append(probes, probe{vt, off})
			fmt.Fprintf(&script, "varname %d %d\n", vt, off)
		}
	}
	// The step regions are much larger than 20.
	for _, vt := range []int{30, varStepTime} {
		for _, off := range []int{100, 127, 128, 200} {
			probes = append(probes, probe{vt, off})
			fmt.Fprintf(&script, "varname %d %d\n", vt, off)
		}
	}

	want := runOracleLines(t, bin, script.String(), "NAME ")
	if len(want) != len(probes) {
		t.Fatalf("oracle returned %d names for %d probes", len(want), len(probes))
	}

	for i, p := range probes {
		name, ok := m.formatVarName(p.varType, p.offset)
		got := "-"
		if ok {
			got = name
		}
		if got != want[i] {
			t.Errorf("formatVarName(%d, %d) = %q, 2.9 says %q", p.varType, p.offset, got, want[i])
		}
	}
}

// And reading a name back must agree, including how many characters it consumed
// — an expression parser walks the string by that count.
func TestVarNames_ParseMatches29(t *testing.T) {
	bin := buildOracle(t)
	m := newTestModule(t)

	names := []string{
		"%B0", "%B99", "%B100",
		"%I0", "%I14", "%I15",
		"%Q0", "%Q14",
		"%W0", "%W99",
		"%IW0", "%IW9", "%IW10",
		"%QW0", "%QW9",
		"%IF0", "%IF9",
		"%QF0", "%QF9",
		"%E0", "%E9",
		"%T0.D", "%T0.R", "%T0.P", "%T0.V", "%T9.D", "%T10.D",
		"%M0.R", "%M0.P", "%M0.V",
		"%C0.D", "%C0.E", "%C0.F", "%C0.P", "%C0.V",
		"%TM0.Q", "%TM0.P", "%TM0.V", "%TM9.Q",
		"%X0", "%X127", "%X128",
		"%X0.V", "%X127.V",
		// Malformed or unknown: both sides must refuse.
		"%", "%%", "%Z0", "%B", "%B-1", "%Bx", "B0", "", "%T0", "%T0.X",
	}

	var script strings.Builder
	for _, n := range names {
		// The oracle reads one whitespace-delimited token; an empty name would
		// desynchronise the transcript, so send a placeholder it will refuse.
		tok := n
		if tok == "" {
			tok = "~"
		}
		fmt.Fprintf(&script, "varparse %s\n", tok)
	}

	want := runOracleLines(t, bin, script.String(), "PARSE ")
	if len(want) != len(names) {
		t.Fatalf("oracle returned %d results for %d names", len(want), len(names))
	}

	for i, n := range names {
		varType, offset, consumed, err := m.parseVarName(n)
		got := "-"
		if err == nil {
			got = fmt.Sprintf("%d %d %d", varType, offset, consumed)
		}
		if got != want[i] {
			t.Errorf("parseVarName(%q) = %q, 2.9 says %q", n, got, want[i])
		}
	}
}

// Whether a program may assign to a variable decides whether an operate
// expression targeting it is a mistake; disagreeing with 2.9 here would either
// block valid programs or admit invalid ones.
func TestVarNames_WritabilityMatches29(t *testing.T) {
	bin := buildOracle(t)

	var script strings.Builder
	var probes []int
	for _, vt := range sweepTypes {
		probes = append(probes, vt)
		fmt.Fprintf(&script, "varrw %d 0\n", vt)
	}

	want := runOracleLines(t, bin, script.String(), "RW ")
	if len(want) != len(probes) {
		t.Fatalf("oracle returned %d results for %d probes", len(want), len(probes))
	}

	for i, vt := range probes {
		got := "0"
		if varIsWritable(vt) {
			got = "1"
		}
		if got != want[i] {
			t.Errorf("varIsWritable(%d) = %s, 2.9 says %s", vt, got, want[i])
		}
	}
}

// --- Expression rendering, which has no 2.9 counterpart to compare against ---

func TestVarNames_ExpressionRoundTrip(t *testing.T) {
	m := newTestModule(t)

	for _, tc := range []struct{ stored, written string }{
		{"@200/0@>5", "%W0>5"},
		{"@200/1@:=@200/0@+1", "%W1:=%W0+1"},
		{"@270/2@:=@300/1@*2", "%IW2:=%IF1*2"},
		{"@0/3@", "%B3"},
		{"@200/0[200/1]@:=7", "%W0[%W1]:=7"},
		{"MINI(@200/0@,@200/1@)", "MINI(%W0,%W1)"},
	} {
		if got := m.exprToNames(tc.stored); got != tc.written {
			t.Errorf("exprToNames(%q) = %q, want %q", tc.stored, got, tc.written)
		}
		back, err := m.namesToExpr(tc.written)
		if err != nil {
			t.Errorf("namesToExpr(%q): %v", tc.written, err)
			continue
		}
		if back != tc.stored {
			t.Errorf("namesToExpr(%q) = %q, want %q", tc.written, back, tc.stored)
		}
	}
}

// An expression an editor sends must be rejected when it names something that
// does not exist, rather than compiled into a reference to nowhere.
func TestVarNames_ExpressionRejectsUnknownNames(t *testing.T) {
	m := newTestModule(t)

	for _, bad := range []string{
		"%Z0:=1",
		"%W999:=1",
		"%W0:=%Q99",
		"%W0[%Zx]:=1",
	} {
		if got, err := m.namesToExpr(bad); err == nil {
			t.Errorf("namesToExpr(%q) = %q, want an error", bad, got)
		}
	}
}

// A reference this implementation cannot render is left in its stored form
// rather than dropped: an expression must not quietly lose a term.
func TestVarNames_UnknownRefSurvivesRendering(t *testing.T) {
	m := newTestModule(t)

	const stored = "@999/0@:=@200/0@"
	got := m.exprToNames(stored)
	if !strings.Contains(got, "@999/0@") {
		t.Errorf("exprToNames(%q) = %q, want the unrenderable reference kept", stored, got)
	}
	if !strings.Contains(got, "%W0") {
		t.Errorf("exprToNames(%q) = %q, want the rest still converted", stored, got)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("resolve %s: %v", p, err)
	}
	return abs
}
