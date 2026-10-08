// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// fsmHeader is the declaration part shared by the fsm tests: every name the
// design document's example refers to.
const fsmHeader = `component t "fsm test";
pin in bit in1;
pin in bit in2;
pin in bit estop;
pin in bit ack;
pin in bit reset_fsm;
pin in bit enable_fsm;
pin out bit out1;
pin out bit out2;
pin out bit out3;
pin out bit special_timeout;
pin out s32 fsm_state;
pin out float fsm_timer;
param rw float wait_s = 1;
variable int latched1;
variable int latched2;
function _;
`

// designExample is the example from MODCOMPILE_FSM_DESIGN.md.
const designExample = `fsm test_fsm {
    inputs:    in1, in2, estop, ack;
    outputs:   out1, out2, out3 = 1, special_timeout;
    latched:   latched1, latched2 = 3;
    reset:     (reset_fsm);
    enable:    (enable_fsm);
    state_var: fsm_state;
    timer_var: fsm_timer;
    initial:   IDLE;

    any {
        on (estop) -> FAULT;
        timeout -> TIMEOUT;
    }

    state IDLE {
        on_enter { out1 = 1; }
        on_exit  { out2 = 1; latched1 = 1; }
        on (in1 || in2 == 1) -> WAIT_RELEASE;
        timeout (60s) -> TIMEOUT { special_timeout = 1; }
    }

    state WAIT_RELEASE {
        during { out3 = 0; }
        on (!in1) -> IDLE;
        timeout (wait_s);           /* handled by any { timeout -> ... } */
    }

    state TIMEOUT {
        on (ack) -> IDLE;
    }

    state FAULT {
        on (!estop && ack) -> IDLE;
    }
};
`

const fsmCall = "\n;;\ntest_fsm();\n"

func parseFSMSrc(t *testing.T, src string) *ast.Package {
	t.Helper()
	pkg, err := Parse("t.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	return pkg
}

func TestParseFSMDesignExample(t *testing.T) {
	pkg := parseFSMSrc(t, fsmHeader+designExample+fsmCall)
	if len(pkg.Component.FSMs) != 1 {
		t.Fatalf("got %d FSMs, want 1", len(pkg.Component.FSMs))
	}
	f := pkg.Component.FSMs[0]
	f.Pos = ast.Pos{}
	want := ast.FSM{
		Name:     "test_fsm",
		Initial:  "IDLE",
		Inputs:   []string{"in1", "in2", "estop", "ack"},
		Outputs:  []string{"out1", "out2", "out3 = 1", "special_timeout"},
		Latched:  []string{"latched1", "latched2 = 3"},
		Reset:    "reset_fsm",
		Enable:   "enable_fsm",
		StateVar: "fsm_state",
		TimerVar: "fsm_timer",
		Any: []ast.FSMTransition{
			{Cond: "estop", Target: "FAULT"},
			{Timeout: true, Target: "TIMEOUT"},
		},
		States: []ast.FSMState{
			{Name: "IDLE", Number: 0, OnEnter: true, OnExit: true, Transitions: []ast.FSMTransition{
				{Cond: "in1 || in2 == 1", Target: "WAIT_RELEASE"},
				{Timeout: true, Cond: "60s", Target: "TIMEOUT", Action: true},
			}},
			{Name: "WAIT_RELEASE", Number: 1, During: true, Transitions: []ast.FSMTransition{
				{Cond: "!in1", Target: "IDLE"},
				{Timeout: true, Cond: "wait_s"},
			}},
			{Name: "TIMEOUT", Number: 2, Transitions: []ast.FSMTransition{
				{Cond: "ack", Target: "IDLE"},
			}},
			{Name: "FAULT", Number: 3, Transitions: []ast.FSMTransition{
				{Cond: "!estop && ack", Target: "IDLE"},
			}},
		},
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("FSM description:\n got %+v\nwant %+v", f, want)
	}
}

// The header after an fsm block must be tokenised as before: HAL names with
// '-' and '.' still scan as one identifier.
func TestParseFSMRestoresHeaderScanning(t *testing.T) {
	src := fsmHeader + `fsm m { state A { on (in1) -> A; } };
pin out bit my-pin.x;
` + "\n;;\nm();\n"
	pkg := parseFSMSrc(t, src)
	last := pkg.Component.Pins[len(pkg.Component.Pins)-1]
	if last.Name != "my-pin.x" {
		t.Errorf("pin after fsm block parsed as %q", last.Name)
	}
}

func TestParseFSMCaptures(t *testing.T) {
	src := fsmHeader + `fsm m {
    outputs: out1 = fmin(wait_s, 2), out2 = (a ? b : c);
    state A {
        on (strchr("),;}", ')') != 0 /* ) */) -> B { /* } */ out1 = 1; // }
        }
    }
    state B { on(in1)->A; timeout(1)->A; }
};` + fsmCall
	pkg := parseFSMSrc(t, src)
	f := pkg.Component.FSMs[0]
	if got := f.Outputs; !reflect.DeepEqual(got, []string{"out1 = fmin(wait_s, 2)", "out2 = (a ? b : c)"}) {
		t.Errorf("outputs = %q", got)
	}
	if got := f.States[0].Transitions[0].Cond; got != `strchr("),;}", ')') != 0 /* ) */` {
		t.Errorf("condition = %q", got)
	}
	if got := f.States[1].Transitions; len(got) != 2 || got[0].Target != "A" || got[1].Target != "A" {
		t.Errorf("unspaced transitions = %+v", got)
	}
}

func TestParseFSMArrayRefs(t *testing.T) {
	src := `component t "x";
pin in bit in_arr#[4];
pin out bit out_arr#[4];
variable int buf[3];
function _;
fsm m {
    inputs: in_arr(1), buf[2];
    outputs: out_arr(0) = 1;
    state A { on (in_arr(1)) -> A; }
};` + "\n;;\nm();\n"
	pkg := parseFSMSrc(t, src)
	f := pkg.Component.FSMs[0]
	if !reflect.DeepEqual(f.Inputs, []string{"in_arr(1)", "buf[2]"}) || !reflect.DeepEqual(f.Outputs, []string{"out_arr(0) = 1"}) {
		t.Errorf("inputs %q outputs %q", f.Inputs, f.Outputs)
	}
}

func TestParseFSMErrors(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"no states", `fsm m { inputs: in1; };`, "has no states"},
		{"dup state", `fsm m { state A { } state A { } };`, "duplicate state A"},
		{"dup item", `fsm m { inputs: in1; inputs: in2; state A { } };`, "duplicate inputs"},
		{"dup any", `fsm m { any { } any { } state A { } };`, "more than one any block"},
		{"dup any timeout", `fsm m { any { timeout -> A; timeout -> A; } state A { } };`, "more than one timeout in any"},
		{"dup during", `fsm m { state A { during { } during { } } };`, "duplicate during"},
		{"dup timeout", `fsm m { state A { timeout (1); timeout (2); } };`, "duplicate timeout"},
		{"unknown item", `fsm m { output: out1; state A { } };`, `unknown item "output"`},
		{"unknown state item", `fsm m { state A { on_entry { } } };`, `unknown item "on_entry"`},
		{"missing semi", `fsm m { state A { } }`, "expected ';' after '}'"},
		{"computed index", `fsm m { inputs: in1(i); state A { } };`, "integer literal"},
		{"unbalanced", `fsm m { state A { on (a]) -> A; } };`, "unbalanced"},
		{"empty cond", `fsm m { state A { on ( ) -> A; } };`, "empty expression"},
		{"empty default", `fsm m { outputs: out1 = ; state A { } };`, "empty default"},
		{"unterminated", `fsm m { state A { on ("x) -> A; } };`, "unterminated string"},
		{"any item", `fsm m { any { during { } } state A { } };`, "expected 'on' or 'timeout'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.comp", fsmHeader+tc.body+fsmCall)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestCaptureCPosition(t *testing.T) {
	sc := NewScanner("f", "a = \n  f(1, 2) , y;")
	sc.Next() // a
	sc.Next() // =
	c, err := sc.CaptureC(",;")
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != " \n  f(1, 2) " || c.Pos.Line != 1 || c.Pos.Col != 4 || c.Indent != "   " {
		t.Errorf("got %+v", c)
	}
	sc = NewScanner("f", "\tx (y);")
	sc.Next() // x
	sc.Next() // (
	if c, _ = sc.CaptureC(")"); c.Indent != "\t   " {
		t.Errorf("indent %q, want tab kept", c.Indent)
	}
}

func TestFSMDesignExampleClean(t *testing.T) {
	pkg := parseFSMSrc(t, fsmHeader+designExample+fsmCall)
	if len(pkg.Warnings) != 0 {
		t.Errorf("unexpected warnings:\n%s", strings.Join(pkg.Warnings, "\n"))
	}
}

// checkHeader adds declarations of every kind the list rules distinguish.
const checkHeader = fsmHeader + `pin io bit iob;
pin out u32 ustate;
pin out bit outc if personality & 1;
pin out bit outa#[2 : personality];
pin in bit ina#[3];
pin out bit outs#[3];
param rw s32 prm;
variable double dvar;
variable float fvar;
variable int ivar;
variable int iarr[2];
variable double *dptr;
`

func TestFSMCheckErrors(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"unknown name", `fsm m { inputs: nope; state A { } };`, "nope is not a declared pin or variable"},
		{"param listed", `fsm m { inputs: prm; state A { } };`, "prm is a param"},
		{"out pin as input", `fsm m { inputs: out1; state A { } };`, "inputs: out1 is an out pin"},
		{"in pin as output", `fsm m { outputs: in1; state A { } };`, "outputs: in1 is an in pin"},
		{"in pin latched", `fsm m { latched: in1; state A { } };`, "latched: in1 is an in pin"},
		{"conditional pin", `fsm m { outputs: outc; state A { } };`, "may not exist"},
		{"personality array", `fsm m { outputs: outa(0); state A { } };`, "may not exist"},
		{"whole array", `fsm m { outputs: outs; state A { } };`, "outs is an array; list single elements (outs(0))"},
		{"whole var array", `fsm m { latched: iarr; state A { } };`, "iarr[0]"},
		{"pin index style", `fsm m { outputs: outs[1]; state A { } };`, "pin array elements are written outs(1)"},
		{"var index style", `fsm m { latched: iarr(1); state A { } };`, "variable array elements are written iarr[1]"},
		{"index range", `fsm m { outputs: outs(3); state A { } };`, "index 3 out of range for outs[3]"},
		{"scalar indexed", `fsm m { outputs: out1(0); state A { } };`, "out1 is not an array"},
		{"pointer", `fsm m { latched: dptr; state A { } };`, "pointer variable"},
		{"state_var float", `fsm m { state_var: dvar; state A { } };`, "state_var: dvar is a double variable"},
		{"state_var bit", `fsm m { state_var: out1; state A { } };`, "state_var: out1 is a out bit pin"},
		{"timer_var int", `fsm m { timer_var: ivar; state A { } };`, "timer_var: ivar is a int variable"},
		{"timer_var in", `fsm m { timer_var: in1; state A { } };`, "timer_var: in1 is an in pin"},
		{"two lists", `fsm m { outputs: out1; latched: out1; state A { } };`, "more than one of outputs"},
		{"same list twice", `fsm m { outputs: out1, out1; state A { } };`, "more than one of outputs"},
		{"two fsms", `fsm m { outputs: out1; state A { } }; fsm n { latched: out1; state A { } };`,
			"out1 is already written by fsm m"},
		{"unknown target", `fsm m { state A { on (in1) -> B; } };`, "unknown target state B"},
		{"unknown any target", `fsm m { any { on (in1) -> B; } state A { } };`, "unknown target state B"},
		{"unknown timeout target", `fsm m { state A { timeout (1) -> B; } };`, "unknown target state B"},
		{"unknown initial", `fsm m { initial: B; state A { } };`, "initial state B is not declared"},
		{"timeout no any", `fsm m { state A { timeout (1); } };`, "needs 'any { timeout -> STATE; }'"},
		{"reads output", `fsm m { outputs: out1; state A { on (out1) -> A; } };`, "condition reads output out1"},
		{"reads output elem", `fsm m { outputs: outs(1); state A { on (outs(1) && in1) -> A; } };`, "condition reads output outs"},
		{"dup fsm", `fsm m { state A { } }; fsm m { state A { } };`, "fsm m already declared"},
		{"fsm named like pin", `fsm in1 { state A { } };`, "has the name of a in bit pin"},
		{"fsm named like function", `fsm _ { state A { } };`, "has the name of a function"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.comp", checkHeader+tc.body+"\n;;\nm();\n")
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestFSMCheckAccepts(t *testing.T) {
	src := checkHeader + `fsm m {
    inputs: in1, ina(1), ivar, iarr[0];
    outputs: out1 = prm, outs(0), dvar;
    latched: outs(1), iarr[1], iob;
    state_var: ustate;
    timer_var: fvar;
    state A { on (in1 && iob && ina(1) && ivar && iarr[0] && prm) -> B; }
    state B { on (ustate == 1 && fvar > 1.0 && outs(1) && iarr[1]) -> A; }
};
fsm n { state_var: ivar; timer_var: fsm_timer; state X { on (in2) -> Y; } state Y { on (!in2) -> X; } };
` + "\n;;\nm(); n();\n"
	pkg := parseFSMSrc(t, src)
	if len(pkg.Warnings) != 0 {
		t.Errorf("unexpected warnings:\n%s", strings.Join(pkg.Warnings, "\n"))
	}
}

func TestFSMCheckWarnings(t *testing.T) {
	cases := []struct {
		name, body, code string
		want             []string // each must appear in some warning
	}{
		{"sensitivity", `fsm m { inputs: in1; state A { on (in1 && in2 && prm && fabs(x.in2) > 0 && p->ack) -> A; } };`, "m();",
			[]string{"t.comp:30:43: fsm m: condition reads in2, which is not in inputs"}},
		{"no inputs list, no check", `fsm m { state A { on (in2) -> A; } };`, "m();", nil},
		{"element sensitivity", `fsm m { inputs: ina(1); state A { on (ina(1) || ina(2)) -> A; } };`, "m();",
			[]string{"condition reads ina, which is not in inputs"}},
		{"computed index", `fsm m { inputs: ina(1); state A { on (ina(i)) -> A; } };`, "m();",
			[]string{"reads ina with a computed index"}},
		{"unread input", `fsm m { inputs: in1, in2, ina(2); state A { on (in1 && ina(1)) -> A; } };`, "m();",
			[]string{"input in2 is never read", "input ina(2) is never read"}},
		{"read in action", `fsm m { inputs: in1, in2; state A { on (in1) -> A { out1 = in2; } } };`, "m();", nil},
		{"write outside", `fsm m { outputs: out1, outs(1); state_var: fsm_state; state A { on (in1) -> A; } };`,
			"m();\nout1 = 1; out2 = 1; outs(1) |= 1; outs(2) = 1; outs(i) = 0; fsm_state++; --fsm_state; x.out1 = 1;",
			[]string{"t.comp:33:1: out1 is written by an fsm", "33:21: outs is written by an fsm", "33:48: outs may overwrite",
				"33:61: fsm_state is written by an fsm", "33:76: fsm_state is written by an fsm"}},
		{"unreachable", `fsm m { state A { on (in1) -> A; } state B { on (in1) -> A; } };`, "m();",
			[]string{"state B is unreachable", "state A has no way out"}},
		{"self loop only", `fsm m { state A { on (in1) -> B; } state B { on (in1) -> B; } };`, "m();",
			[]string{"state B has no way out"}},
		{"any is a way out", `fsm m { any { on (in1) -> A; timeout -> A; } state A { on (in2) -> B; } state B { timeout (1); } };`, "m();", nil},
		{"never called", `fsm m { state A { on (in1) -> B; } state B { on (in1) -> A; } };`, "mm(); /* m() */ \"m()\";",
			[]string{"m() is never called"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := parseFSMSrc(t, checkHeader+tc.body+"\n;;\n"+tc.code+"\n")
			// Most cases use a one-state machine; its "no way out" is only
			// of interest where a case asks for it.
			var ws []string
			for _, w := range pkg.Warnings {
				if !strings.Contains(w, "no way out") || strings.Contains(strings.Join(tc.want, "|"), "no way out") {
					ws = append(ws, w)
				}
			}
			got := strings.Join(ws, "\n")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("no warning containing %q in:\n%s", w, got)
				}
			}
			if tc.want == nil && got != "" {
				t.Errorf("unexpected warnings:\n%s", got)
			}
		})
	}
}

func TestRewriteTimeUnits(t *testing.T) {
	cases := map[string]string{
		"60":                     "60",
		"60s":                    "(60)",
		"1.5ms":                  "(1.5 * 1e-3)",
		"wait_ms * 1ms":          "wait_ms * (1 * 1e-3)",
		"2us + 3ns":              "(2 * 1e-6) + (3 * 1e-9)",
		"1e3us":                  "(1e3 * 1e-6)",
		".5s":                    "(.5)",
		"x.s + f(\"5s\") /*5s*/": "x.s + f(\"5s\") /*5s*/",
		"0x5s + 5min + 1.0f":     "0x5s + 5min + 1.0f",
		"a\n  + 5ms":             "a\n  + (5 * 1e-3)",
	}
	for in, want := range cases {
		if got := rewriteTimeUnits(in); got != want {
			t.Errorf("rewriteTimeUnits(%q) = %q, want %q", in, got, want)
		}
	}
}
