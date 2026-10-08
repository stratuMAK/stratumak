// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package docgen

import (
	"bytes"
	"strings"
	"testing"

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

func TestTroffTextLeadingDot(t *testing.T) {
	if got := troffText(".5 > x"); got != `\&.5 > x` {
		t.Errorf("got %q", got)
	}
	if got := troffText("'a'"); got != `\&'a'` {
		t.Errorf("got %q", got)
	}
}
