// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/comp"
)

// Header directories the generated file needs, relative to this package.
var fsmCCIncludes = []string{"../../../pkg/cmodule", "../../../../../include"}

// compileFSMC compiles a .comp's generated C the way modcompile builds a
// module (-Wall -Werror).  A text comparison cannot see what only the C
// compiler reports, such as a helper that is defined but never called.
// Skipped when there is no gcc or the headers are missing.
func compileFSMC(t *testing.T, name, src string) {
	t.Helper()
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("no gcc")
	}
	args := []string{"-c", "-fPIC", "-g", "-Os", "-Wall", "-Werror"}
	for _, inc := range fsmCCIncludes {
		if _, err := os.Stat(inc); err != nil {
			t.Skipf("no header directory %s", inc)
		}
		args = append(args, "-I"+inc)
	}
	pkg, err := comp.Parse(name+".comp", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dir := t.TempDir()
	cfile := filepath.Join(dir, name+".c")
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, cfile); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := os.WriteFile(cfile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-o", filepath.Join(dir, name+".o"), cfile)
	if out, err := exec.Command(gcc, args...).CombinedOutput(); err != nil {
		t.Errorf("gcc %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// An error in an fsm block is reported at its column in the .comp, counted
// in characters even after an umlaut: the fragment is indented per byte, and
// gcc converts the byte column using the .comp line it reads via #line.
func TestFSMErrorColumnAfterUmlaut(t *testing.T) {
	checkErrorColumn(t, `fsm m { /* ä ü */ state S { on_enter { o = zz_undeclared; } } };`)
}

// A unit literal is rewritten to text of another length; an error after it
// on the same line is still reported at its column in the .comp.
func TestFSMErrorColumnAfterUnitLiteral(t *testing.T) {
	checkErrorColumn(t, `fsm m { state S { timeout (250ms + zz_undeclared) -> S; } };`)
}

// checkErrorColumn compiles a component whose fsm line holds the undeclared
// identifier zz_undeclared and checks that gcc reports it at that line and
// character column of the .comp.
func checkErrorColumn(t *testing.T, line string) {
	t.Helper()
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("no gcc")
	}
	args := []string{"-c", "-fdiagnostics-column-unit=display"}
	for _, inc := range fsmCCIncludes {
		if _, err := os.Stat(inc); err != nil {
			t.Skipf("no header directory %s", inc)
		}
		args = append(args, "-I"+inc)
	}
	src := "component col \"column test\";\npin out s32 o;\nfunction _;\n" + line + "\n;;\nm();\n"
	dir := t.TempDir()
	compFile := filepath.Join(dir, "col.comp")
	cfile := filepath.Join(dir, "col.c")
	if err := os.WriteFile(compFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := comp.Parse(compFile, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, cfile); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := os.WriteFile(cfile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-o", filepath.Join(dir, "col.o"), cfile)
	out, _ := exec.Command(gcc, args...).CombinedOutput()
	col := len([]rune(line[:strings.Index(line, "zz_undeclared")])) + 1
	want := compFile + ":4:" + strconv.Itoa(col) + ":"
	if !strings.Contains(string(out), want) {
		t.Errorf("gcc did not report the error at %s:\n%s", want, out)
	}
}

// TestFSMGeneratedCCompiles builds the fsm components in the tree and the
// edge cases earlier reviews broke the build with.
func TestFSMGeneratedCCompiles(t *testing.T) {
	for _, f := range []string{
		"testdata/fsm.comp",
		"../../../../../tests/modcompile-fsm/fsm_test.comp",
		"../../../../../tests/modcompile-fsm-multi/fsm_multi.comp",
		"../../../../../tests/multiclick-fsm/multiclick_fsm.comp",
	} {
		name := strings.TrimSuffix(filepath.Base(f), ".comp")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Skipf("%v", err)
			}
			compileFSMC(t, name, string(src))
		})
	}

	const header = `component cc "fsm compile test";
pin in bit a;
pin in bit b;
pin out s32 o;
pin out s32 st;
function _;
`
	for _, tc := range []struct{ name, fsm, code string }{
		// Action helpers that no transition calls.
		{"unused any timeout action",
			`fsm m { any { timeout -> S { o = 1; } } state S { on (a) -> T; } state T { on (b) -> S; } };`,
			"m();"},
		{"any on to the only state",
			`fsm m { any { on (a) -> S { o = 2; } } state S { on (b) -> S; } };`,
			"m();"},
		{"on_exit without a way out",
			`fsm m { state S { on (a) -> T; } state T { on_exit { o = 3; } } };`,
			"m();"},
		// Blocks are functions of their own: return, period and fperiod.
		{"return and fperiod in blocks",
			`fsm m { outputs: o; latched: st;
			  state S { during { if (a) return; o = fperiod > 0; }
			            on (a) -> T { if (b) return; st = period; } }
			  state T { on_enter { if (b) return; } on_exit { return; } on (!a) -> S; timeout (1ms) -> S; } };`,
			"m();"},
		// Two machines, one enabled by the other's state, hidden state variable.
		{"two fsms",
			`fsm mode { state IDLE { on (a) -> RUN; } state RUN { on (!a) -> IDLE; } };
			 fsm seq { outputs: o; state_var: st; enable: (mode_in(RUN));
			  state A { during { o = 1; } on (b) -> B; } state B { on (!b) -> A; } };`,
			"FUNCTION(_) { mode(); seq(); }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compileFSMC(t, "cc", header+tc.fsm+"\n;;\n"+tc.code+"\n")
		})
	}
}
