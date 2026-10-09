// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package docgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/comp"
)

func TestFSMStateTable(t *testing.T) {
	src := `component t "x";
pin in bit go;
pin in bit stop;
pin out s32 step;
param rw float delay = 1;
function _;
fsm seq {
    inputs: go, stop;
    state_var: step;
    reset: (stop);
    any { on (stop) -> IDLE; timeout -> IDLE; }
    state IDLE { on (go) -> RUN { step = 0; } }
    state RUN { on (.5 < delay && strchr("\\x", 'a')) -> DONE; timeout (delay); }
    state DONE { }
};
;;
seq();
`
	pkg, err := comp.Parse("t.comp", src)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		".SH STATE MACHINES\n.SS seq\n",
		`Initial state: \fBIDLE\fR. The state number is on \fBt.\fIN\fB.step\fR.`,
		".br\nReset while: \\fIstop\\fR\n",
		"\t\\fIany\\fR\tT{\non stop\nT}\tIDLE\n",
		"\t\tT{\ntimeout of a state whose timeout has no target\nT}\tIDLE\n",
		"0\tIDLE\tT{\non go\nT}\tRUN *\n",
		// A backslash in the condition is escaped; the row's leading text is not a request.
		"1\tRUN\tT{\non .5 < delay && strchr(\"\\e\\ex\", 'a')\nT}\tDONE\n",
		"\t\tT{\ntimeout delay\nT}\tIDLE\n",
		"2\tDONE\t\t\n",
		".TE\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("man page lacks %q\n--- output ---\n%s", want, out)
		}
	}
}

// state_var and timer_var that are array elements name the element's pin.
func TestFSMElementPins(t *testing.T) {
	src := `component t "x";
pin in bit go;
pin out s32 st-##[4];
pin out float tm_#[2];
function _;
fsm seq { state_var: st(3); timer_var: tm(0); state A { on (go) -> B; } state B { timeout (1) -> A; } };
;;
seq();
`
	pkg, err := comp.Parse("t.comp", src)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatal(err)
	}
	want := `Initial state: \fBA\fR. The state number is on \fBt.\fIN\fB.st-03\fR.` +
		` The time in the current state, in seconds, is on \fBt.\fIN\fB.tm-0\fR.`
	if out := buf.String(); !strings.Contains(out, want) {
		t.Errorf("man page lacks %q\n--- output ---\n%s", want, out)
	}
}

func TestTroffTextLeadingDot(t *testing.T) {
	if got := troffText(".5 > x"); got != `\&.5 > x` {
		t.Errorf("got %q", got)
	}
	if got := troffText("'a'"); got != `\&'a'` {
		t.Errorf("got %q", got)
	}
}

// The man page names an array's elements the way cgen registers them, and
// refuses an array whose name has no run of '#' to put the index in: with
// none, every element had the same name (docgen used to append the index,
// cgen did not).
func TestArrayElementNames(t *testing.T) {
	src := `component t "x";
pin in bit in_##[12];
param rw s32 p-#[2:personality];
function _;
;;
`
	pkg, err := comp.Parse("t.comp", src)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`.B t.\fIN\fB.in-\fIM\fB\fIM\fB\fR bit in (M=00..11)`,
		`.B t.\fIN\fB.p-\fIM\fB\fR s32 rw (M=0..personality)`,
	} {
		if out := buf.String(); !strings.Contains(out, want) {
			t.Errorf("man page lacks %q\n--- output ---\n%s", want, out)
		}
	}
	if got := elementName("in-##", 7); got != "in-07" {
		t.Errorf("elementName(in-##, 7) = %q, want in-07", got)
	}

	pkg = &ast.Package{Component: ast.Component{Name: "t",
		Params: []ast.Param{{Pos: ast.Pos{File: "t.comp", Line: 3, Col: 14}, Name: "gain", ArraySize: 3}}}}
	err = Generate(&bytes.Buffer{}, pkg)
	if want := `t.comp:3:14: param array name "gain" has no '#'`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Generate error = %v, want it to contain %q", err, want)
	}
}
