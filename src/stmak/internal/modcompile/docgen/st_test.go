// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package docgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st/sema"
)

// document renders the man page for one first-party ST fixture.
func document(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("../st/testdata/accept", name+".st")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	file, perr := st.Parse(path, string(src))
	if perr != nil {
		t.Fatalf("%s does not parse:\n%v", name, perr)
	}
	pkg, cerr := sema.Check(name, file)
	if cerr != nil {
		t.Fatalf("%s does not check:\n%v", name, cerr)
	}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatalf("%s: generating documentation: %v", name, err)
	}
	return buf.String()
}

// TestSTManPageIsComplete is the phase 7 gate: a man page generated from an
// .st carries everything one generated from a .comp does.  docgen is shared,
// so what this really checks is that every field it reads is reachable from ST.
func TestSTManPageIsComplete(t *testing.T) {
	out := document(t, "documented")

	for _, section := range []string{
		".SH NAME", ".SH SYNOPSIS", ".SH DESCRIPTION", ".SH FUNCTIONS",
		".SH PINS", ".SH EXAMPLES", ".SH SEE ALSO", ".SH NOTES",
		".SH AUTHOR", ".SH LICENSE",
	} {
		if !strings.Contains(out, section) {
			t.Errorf("man page has no %s section:\n%s", section, out)
		}
	}

	for _, text := range []string{
		"documented \\- Every documentation field, exercised",
		"as complete as one generated from a .comp", // description
		"Latches the raw input when strobed",        // a function's doc
		"Scales the latched value",                  // the other function's
		"Value to latch.",                           // a pin's doc
		"Rising level latches \\fBraw\\fR.",         // troff passes through
		"\\fBscale\\fR(9), \\fBand2\\fR(9).",        // see also
		"stratuMAK",                                 // author
		"GPL",                                       // licence
	} {
		if !strings.Contains(out, text) {
			t.Errorf("man page is missing %q", text)
		}
	}

	// Each PROGRAM is documented as its own HAL function, and only the one
	// that uses floating point says so.
	fns := between(out, ".SH FUNCTIONS", ".SH PINS")
	if !strings.Contains(fns, "documented.sample") || !strings.Contains(fns, "documented.update") {
		t.Errorf("both functions are not listed:\n%s", fns)
	}
	if strings.Count(fns, "requires a floating-point thread") != 1 {
		t.Errorf("exactly one function should need a floating-point thread:\n%s", fns)
	}

	// A doc attribute may span lines: EXAMPLES needs them, and ST spells a
	// newline $N.
	ex := between(out, ".SH EXAMPLES", ".SH SEE ALSO")
	if strings.Contains(ex, "$N") {
		t.Error("the $N escape was not decoded in a pragma value")
	}
	if strings.Count(strings.TrimSpace(ex), "\n") < 2 {
		t.Errorf("EXAMPLES did not keep its line breaks:\n%q", ex)
	}
}

// TestSTPinDocsStayWithTheirPins guards the attachment rule: a pragma inside a
// VAR section documents the variable it precedes, and one heading the PROGRAM
// documents the function.  Getting that backwards silently moves a pin's
// description onto another pin.
func TestSTPinDocsStayWithTheirPins(t *testing.T) {
	out := document(t, "documented")
	pins := between(out, ".SH PINS", ".SH EXAMPLES")

	for _, pair := range [][2]string{
		{"raw\\fR s32 in", "Value to latch."},
		{"strobe\\fR bit in", "Rising level latches"},
		{"valid\\fR bit out", "TRUE once a value has been latched."},
		{"gain\\fR float in", "Multiplier applied to the latched value."},
		{"scaled\\fR float out", "The latched value times"},
	} {
		i := strings.Index(pins, pair[0])
		j := strings.Index(pins, pair[1])
		if i < 0 || j < 0 {
			t.Errorf("pin %q or its doc %q is missing", pair[0], pair[1])
			continue
		}
		if j < i {
			t.Errorf("the doc %q appears before its pin %q", pair[1], pair[0])
		}
	}

	// A function's doc must not leak into the pin table.
	if strings.Contains(pins, "Latches the raw input when strobed") {
		t.Error("a PROGRAM's doc attribute landed on a pin")
	}
}

// TestSTDefaultsAreDocumented checks a pin's declared default reaches the page,
// which is how someone reading it learns what happens if they never set it.
func TestSTDefaultsAreDocumented(t *testing.T) {
	out := document(t, "documented")
	if !strings.Contains(out, "(default: \\fI1.0\\fR)") {
		t.Errorf("a pin default did not reach the man page:\n%s", out)
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}
