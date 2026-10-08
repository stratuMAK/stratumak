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
	text, pos, err := sc.CaptureC(",;")
	if err != nil {
		t.Fatal(err)
	}
	if text != " \n  f(1, 2) " || pos.Line != 1 || pos.Col != 4 {
		t.Errorf("got %q at %v", text, pos)
	}
}
