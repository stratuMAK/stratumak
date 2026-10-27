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
		"../../../../../tests/modcompile-fsm-restore/fsm_restore.comp",
		"../../../../../tests/modcompile-fsm-internal/fsm_internal.comp",
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
			  state S { output { if (a) return; o = fperiod > 0; } during { if (b) return; st = fperiod > 0; }
			            on (a) -> T { if (b) return; st = period; } }
			  state T { on_enter { if (b) return; } on_exit { return; } on (!a) -> S; timeout (1ms) -> S; } };`,
			"m();"},
		// Two machines, one enabled by the other's state, hidden state variable.
		{"two fsms",
			`fsm mode { state IDLE { on (a) -> RUN; } state RUN { on (!a) -> IDLE; } };
			 fsm seq { outputs: o; state_var: st; enable: (mode_in(RUN));
			  state A { output { o = 1; } on (b) -> B; } state B { on (!b) -> A; } };`,
			"FUNCTION(_) { mode(); seq(); }"},
		// io state pins of both signs, restored on the first run; output
		// blocks without during; reset_outputs from user code; an out pin
		// as input.
		{"io state, output, reset_outputs",
			`pin io s32 ios; pin io u32 iou;
			 fsm m { inputs: o; outputs: o2 = 1; state_var: ios; enable: (a);
			  state S { output { o2 = 0; } on (o) -> T; } state T { output { int k = 2; o2 = k; } on (!o) -> S; } };
			 fsm n { state_var: iou; reset: (b);
			  state X { on_enter { o = 4; } on (a) -> Y; } state Y { on (!a) -> X; } };
			 pin out s32 o2;`,
			"FUNCTION(_) { o = a; m(); n(); if (!b) { m_reset_outputs(); n_reset_outputs(); } }"},
		{"internal on",
			`variable int n;
			 fsm m { inputs: a, b; latched: n; timer_var: tv;
			  state S { on (a) { n++; } on (b) { if (n > 2) return; n = 0; } on (!a) -> T; timeout (1) -> T; }
			  state T { on (b) -> S; } };
			 variable double tv;`,
			"FUNCTION(_) { m(); }"},
		{"internal timeout",
			`variable int n;
			 fsm m { inputs: a; latched: n;
			  state S { on (a) -> T; timeout (2ms) { if (n > 5) return; n++; } }
			  state T { on (!a) -> S; timeout (1) -> S; } };`,
			"FUNCTION(_) { m(); }"},
		{"persistent",
			`pin io u32 model; variable double pv;
			 fsm m { inputs: a; persistent: model, pv = 0.5; reset: (b);
			  state S { on (a && model) -> T { model = 0; pv += 1; } } state T { on (!a) -> S { model = 7; } } };`,
			"FUNCTION(_) { m(); }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compileFSMC(t, "cc", header+tc.fsm+"\n;;\n"+tc.code+"\n")
		})
	}
}

// An output pin is written once per run, by <fsm>_publish at its end, never
// with the default first and the state's value after: a reader outside the
// thread (a userspace task, halui) would see the default in between, as a
// pulse low on a held output.  Checked on the preprocessed C, so the macro
// redirection is what is tested, for a scalar pin and a pin array element.
func TestFSMOutputPinsWrittenOnce(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("no gcc")
	}
	const src = `component once "fsm output write test";
pin in bit a;
pin out bit o;
pin out bit arr-#[2];
pin out bit u;
function _;
fsm m {
    inputs: a;
    outputs: o, arr(1);
    state IDLE { on (a) -> RUN { o = 1; } }
    state RUN { output { o = 1; arr(1) = 1; } on (!a) -> IDLE; }
};
;;
FUNCTION(_) { m(); u = o; arr(0) = 1; }
`
	pkg, err := comp.Parse("once.comp", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dir := t.TempDir()
	cfile := filepath.Join(dir, "once.c")
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, cfile); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := os.WriteFile(cfile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"-E", "-P"}
	for _, inc := range fsmCCIncludes {
		if _, err := os.Stat(inc); err != nil {
			t.Skipf("no header directory %s", inc)
		}
		args = append(args, "-I"+inc)
	}
	out, err := exec.Command(gcc, append(args, cfile)...).Output()
	if err != nil {
		t.Fatalf("gcc -E: %v", err)
	}
	pp := string(out)
	// The body of a function: from its definition -- the occurrence whose
	// line opens a brace -- to its closing brace, or the end of its line
	// for a one-liner.
	body := func(name string) string {
		for i := 0; ; {
			j := strings.Index(pp[i:], name+"(inst_t *__comp_inst")
			if j < 0 {
				t.Fatalf("%s: no definition", name)
			}
			i += j
			line := pp[i:]
			if k := strings.IndexByte(line, '\n'); k >= 0 {
				line = line[:k]
			}
			if strings.Contains(line, "{") {
				if strings.HasSuffix(strings.TrimSpace(line), "}") {
					return line
				}
				return pp[i : i+strings.Index(pp[i:], "\n}")]
			}
			i += len(name)
		}
	}
	for _, w := range []struct{ store, what string }{
		{"(*(__comp_inst->hal->o)) =", "o"},
		{"(*(__comp_inst->hal->arr[1])) =", "arr(1)"},
	} {
		if n := strings.Count(pp, w.store); n != 1 {
			t.Errorf("%s is stored %d times, want once (in m's publish)", w.what, n)
		}
		if !strings.Contains(body("__fsm_m_publish"), w.store) {
			t.Errorf("%s is not stored by __fsm_m_publish", w.what)
		}
	}
	run := body("m_run")
	if strings.Count(run, "__fsm_m_publish(__comp_inst)") != 1 {
		t.Errorf("m_run does not publish exactly once:\n%s", run)
	}
	// User code sees the pins: u reads o's pin, arr(0) is not an output.
	fn := body("funct___body")
	for _, want := range []string{"(*(__comp_inst->hal->o))", "(*(__comp_inst->hal->arr[0])) = 1"} {
		if !strings.Contains(fn, want) {
			t.Errorf("user code does not use the pin %q:\n%s", want, fn)
		}
	}
}
