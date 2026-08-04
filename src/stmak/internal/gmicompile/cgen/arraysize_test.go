// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// TestCArraySizeStr covers the single source of truth for C array bounds: it
// emits the #define symbol when the length came from a named const, else the
// literal — so no C copy of an array can drift to a bare number (or a "[0]").
func TestCArraySizeStr(t *testing.T) {
	named := ast.TypeRef{Kind: ast.TypeArray, ArrayLenName: "MAX_JOINTS", ArrayLen: 16,
		Elem: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimF64}}
	if got := cArraySizeStr("motstat", named); got != "MOTSTAT_MAX_JOINTS" {
		t.Errorf("named length = %q, want MOTSTAT_MAX_JOINTS", got)
	}
	literal := ast.TypeRef{Kind: ast.TypeArray, ArrayLen: 9,
		Elem: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimF64}}
	if got := cArraySizeStr("canon", literal); got != "9" {
		t.Errorf("literal length = %q, want 9", got)
	}
}

// TestArraySizeDefineConsistency verifies the C header emits the #define symbol
// (not the resolved literal) for a named-length array field, matching every
// other C emitter routed through cArraySizeStr.
func TestArraySizeDefineConsistency(t *testing.T) {
	f64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimF64}
	api := &ast.API{
		Name: "motstat", Version: 1,
		Consts: []ast.Const{{Name: "MAX_JOINTS", Value: 16}},
		Types: []ast.Type{{Name: "Status", Fields: []ast.Field{
			{Name: "joints", Type: ast.TypeRef{Kind: ast.TypeArray, ArrayLenName: "MAX_JOINTS", ArrayLen: 16, Elem: &f64}},
		}}},
	}
	var buf bytes.Buffer
	if err := GenerateServerHeader(&buf, api); err != nil {
		t.Fatalf("GenerateServerHeader: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "joints[MOTSTAT_MAX_JOINTS]") {
		t.Errorf("header array field should use the #define symbol, not a literal:\n%s", out)
	}
}
