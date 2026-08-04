// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// TestCallbackRTSafeAnnotation verifies the _cb typedef carries
// STMAK_API_NONBLOCKING iff the callback is @rt_safe — mirroring the @rt_safe/_fn
// provider-typedef precedent. Task/worker-level callbacks (the default) must
// stay blocking-capable and therefore un-annotated.
func TestCallbackRTSafeAnnotation(t *testing.T) {
	i32 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32}
	ptr := ast.TypeRef{Kind: ast.TypePrimitive, Name: "ptr"}
	api := &ast.API{
		Name: "rttest", Version: 1,
		Callbacks: []ast.Callback{
			{Name: "tick_fn", RTSafe: true, Params: []ast.Param{{Name: "ctx", Type: ptr}}, Return: &i32},
			{Name: "job_fn", RTSafe: false, Params: []ast.Param{{Name: "ctx", Type: ptr}}, Return: &i32},
		},
	}
	var buf bytes.Buffer
	if err := GenerateServerHeader(&buf, api); err != nil {
		t.Fatalf("GenerateServerHeader: %v", err)
	}
	out := buf.String()

	// The rt_safe callback's typedef must be stamped nonblocking.
	if !strings.Contains(out, "rttest_tick_fn_cb)") {
		t.Fatalf("missing tick_fn _cb typedef:\n%s", out)
	}
	if !rtSafeTypedefAnnotated(out, "rttest_tick_fn_cb") {
		t.Errorf("rt_safe callback tick_fn should close with STMAK_API_NONBLOCKING:\n%s", out)
	}
	// The default callback's typedef must NOT be annotated.
	if rtSafeTypedefAnnotated(out, "rttest_job_fn_cb") {
		t.Errorf("non-rt_safe callback job_fn must not be nonblocking-annotated:\n%s", out)
	}
}

// rtSafeTypedefAnnotated reports whether the typedef named cbName closes its
// declaration with STMAK_API_NONBLOCKING (i.e. ") STMAK_API_NONBLOCKING;" appears
// after the typedef's opening line).
func rtSafeTypedefAnnotated(out, cbName string) bool {
	i := strings.Index(out, cbName+")")
	if i < 0 {
		return false
	}
	rest := out[i:]
	end := strings.Index(rest, ";")
	if end < 0 {
		return false
	}
	return strings.Contains(rest[:end], "STMAK_API_NONBLOCKING")
}
