// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/comp"
)

// gen parses a .comp source and returns the generated C, failing the test on
// any parse/generate error.
func gen(t *testing.T, src string) string {
	t.Helper()
	pkg, err := comp.Parse("test.comp", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return buf.String()
}

// MC-1: an array param with a default must get a per-element assignment inside
// the creation loop (params are stored by value → no dereference).
func TestGenerate_ArrayParamDefault(t *testing.T) {
	src := `component tc "t";
pin in bit ok;
param rw float scale[4] = 2.5;
function _;
license "GPL";
;;
FUNCTION(_) { }
`
	out := gen(t, src)
	if !strings.Contains(out, "inst->hal->scale[j] = 2.5;") {
		t.Errorf("array param default not emitted per-element; generated:\n%s", out)
	}
}

// MC-5: a non-default function name must be hyphenated in the HAL namespace
// (halcompile's to_hal), consistent with pin/param names — read_all → read-all.
func TestGenerate_FunctionNameHyphenated(t *testing.T) {
	src := `component tc "t";
pin in bit ok;
function read_all;
license "GPL";
;;
FUNCTION(read_all) { }
`
	out := gen(t, src)
	if !strings.Contains(out, "%s.read-all") {
		t.Errorf("function name not hyphenated; generated:\n%s", out)
	}
	if strings.Contains(out, "%s.read_all") {
		t.Errorf("function name still emitted with underscore; generated:\n%s", out)
	}
}

// MC-3: a string modparam default must be re-escaped as a C string literal. The
// scanner unescapes the source literal, so a backslash or embedded quote would
// otherwise corrupt the value or produce uncompilable C.
func TestGenerate_StringModparamEscaping(t *testing.T) {
	src := `component tc "t";
pin in bit ok;
modparam string dev = "c:\\ttyS0";
modparam string msg = "a\"b";
function _;
license "GPL";
;;
FUNCTION(_) { }
`
	out := gen(t, src)
	if !strings.Contains(out, `"c:\\ttyS0"`) {
		t.Errorf("backslash not re-escaped in string modparam default; generated:\n%s", out)
	}
	if !strings.Contains(out, `"a\"b"`) {
		t.Errorf("embedded quote not re-escaped in string modparam default; generated:\n%s", out)
	}
}

// MC-2: the New() error path must free the option-data block (mirroring
// inst_destroy) — otherwise every failed load leaks it.
func TestGenerate_OptionDataFreedOnError(t *testing.T) {
	src := `component tc "t";
pin in bit ok;
option data mystruct;
function _;
license "GPL";
;;
FUNCTION(_) { }
`
	out := gen(t, src)
	// The err: label region must contain the _data free.
	idx := strings.Index(out, "err:")
	if idx < 0 {
		t.Fatalf("no err: label in generated New(); generated:\n%s", out)
	}
	if !strings.Contains(out[idx:], "if (inst->_data) env->rtapi->free") {
		t.Errorf("option-data block not freed on the err: path; generated err region:\n%s", out[idx:])
	}
}

// MC-N: a component that declares M-codes (and has NO rt function) must
// auto-consume the mcode_handler API, define the MCODE macro, and register a
// trampoline per code in inst_init.
func TestGenerate_Mcode(t *testing.T) {
	src := `component test "test";
pin out bit m101_request;
mcode 101;
license "GPL";
;;
MCODE(101) {
  m101_request = 1;
  if (MCODE_ABORTED) return -2;
  return 32;
}
`
	out := gen(t, src)

	checks := []string{
		// auto gmi_consume mcode_handler + its worker-thread includes
		`#include <poll.h>`,
		`#include "mcode_handler_api.h"`,
		`const mcode_handler_callbacks_t *__gmi_mcode_handler;`,
		// the MCODE macro and abort helper
		`#define MCODE(num_)`,
		`stmak_mcode_aborted`,
		// api lookup + per-code registration in inst_init
		`inst->__gmi_mcode_handler = mcode_handler_api_get(`,
		`inst->__gmi_mcode_handler->register_handler(inst->__gmi_mcode_handler->ctx, 101, mcode_101, inst)`,
		// provider instance defaults to milltask, overridable at load
		`inst->__gmi_mcode_handler_instance = "milltask";`,
		`mcode_handler_instance=`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("generated C missing %q; generated:\n%s", want, out)
		}
	}

	// The mcode_handler API is provided by milltask in its Start(), so the
	// lookup+register must live in inst_start, not inst_init.  A comp that only
	// consumes mcode_handler must therefore emit no inst_init at all.
	if strings.Contains(out, "inst_init") {
		t.Errorf("mcode-only comp must not emit inst_init (lookup belongs in Start); generated:\n%s", out)
	}
	si := strings.Index(out, "inst_start")
	if si < 0 || !strings.Contains(out[si:], "mcode_handler_api_get(") {
		t.Errorf("mcode_handler lookup must be inside inst_start; generated:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// #line directives
// ---------------------------------------------------------------------------

// genTo is gen for the path that knows its output filename, which is what
// enables #line emission.
func genTo(t *testing.T, name, src, outName string) string {
	t.Helper()
	pkg, err := comp.Parse(name, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, outName); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return buf.String()
}

// lineSrc has its verbatim C starting at a known line, with an #include in the
// middle of the body. The include is hoisted to a different part of the output,
// which splits the body into two non-contiguous runs — the case that needs a
// second #line directive rather than one at the top.
const lineSrc = `component tc "t";
pin in bit ok;
function _;
license "GPL";
;;
FUNCTION(_) { int a = 1; (void)a;
#include <math.h>
(void)fabs(1.0); }
`

// The ";;" is line 5, so verbatim C starts at line 6.
const wantFirstUserLine = 6

func TestGenerate_LineDirectivesMapToComp(t *testing.T) {
	out := genTo(t, "hal/components/tc.comp", lineSrc, "objects/cmod/tc.c")

	want := `#line ` + strconv.Itoa(wantFirstUserLine) + ` "hal/components/tc.comp"`
	if !strings.Contains(out, want) {
		t.Errorf("generated C lacks %q\n--- output ---\n%s", want, out)
	}
	// The #include sits on line 7 and is emitted in the includes region.
	if want := `#line 7 "hal/components/tc.comp"`; !strings.Contains(out, want) {
		t.Errorf("hoisted #include not mapped: missing %q", want)
	}
	// Body resumes at line 8, after the hoisted include left a gap.
	if want := `#line 8 "hal/components/tc.comp"`; !strings.Contains(out, want) {
		t.Errorf("body after the hoisted include not re-mapped: missing %q", want)
	}
}

// TestGenerate_LineDirectivesResumeCorrectly checks the arithmetic that is
// easiest to get wrong and hardest to notice: every directive returning to the
// generated file must name the line that physically follows it, or every
// diagnostic in generated code after that point is misreported.
func TestGenerate_LineDirectivesResumeCorrectly(t *testing.T) {
	out := genTo(t, "hal/components/tc.comp", lineSrc, "objects/cmod/tc.c")

	found := 0
	for i, line := range strings.Split(out, "\n") {
		var n int
		if _, err := fmt.Sscanf(line, `#line %d "objects/cmod/tc.c"`, &n); err != nil {
			continue
		}
		found++
		// i is 0-based, so this directive is physical line i+1 and the
		// next line is i+2.
		if want := i + 2; n != want {
			t.Errorf("resume directive on physical line %d says line %d; want %d", i+1, n, want)
		}
	}
	if found == 0 {
		t.Fatalf("no resume directives found\n--- output ---\n%s", out)
	}
}

// Without an output name there is nothing to switch back to, so the generator
// must emit no directives at all rather than a one-way mapping that would
// attribute generated code to .comp lines that do not exist.
func TestGenerate_NoLineDirectivesWithoutOutputName(t *testing.T) {
	out := gen(t, lineSrc)
	if strings.Contains(out, "#line") {
		t.Errorf("Generate emitted #line directives without an output name:\n%s", out)
	}
}

// An int modparam must be parsed with strtol, not atoi: an I/O address is
// written ioaddr=0x378, and atoi stops at the 'x' and yields 0 silently — the
// module then runs against port 0 with nothing to say why.  Not base 0 though:
// that reads a zero-padded decimal (addr=0500) as octal, silently, which is
// the failure mode the hex fix exists to prevent.  The base comes from the
// prefix: 16 with 0x/0X, 10 otherwise.
func TestGenerate_IntModparamAcceptsHex(t *testing.T) {
	src := `component tc "t";
pin in bit ok;
modparam int ioaddr = 0 "I/O port address";
function _;
license "GPL";
;;
FUNCTION(_) { }
`
	out := gen(t, src)
	if strings.Contains(out, "atoi(argv[i]") {
		t.Errorf("int modparam still parsed with atoi (hex silently becomes 0); generated:\n%s", out)
	}
	if strings.Contains(out, `NULL, 0)`) {
		t.Errorf("int modparam parsed with strtol base 0 (zero-padded decimal becomes octal); generated:\n%s", out)
	}
	if !strings.Contains(out, `? 16 : 10`) {
		t.Errorf("int modparam not parsed as hex-or-decimal; generated:\n%s", out)
	}
}
