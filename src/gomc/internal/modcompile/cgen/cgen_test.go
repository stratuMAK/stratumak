// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/modcompile/comp"
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
