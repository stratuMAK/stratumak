// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// nullableParamAPI has one func exercising every param shape that matters for
// the nullable-scalar-as-pointer ABI:
//   - id: i32?    → nullable scalar (must become a pointer)
//   - flag: bool? → nullable scalar (must become a pointer)
//   - tag: string? → nullable string (already char*; must NOT be double-pointered)
//   - n: u32      → non-nullable scalar (must stay a plain value)
func nullableParamAPI() *ast.API {
	return &ast.API{
		Name: "nptest", Version: 1, Prefix: "nptest", RestExport: true,
		Types: []ast.Type{{
			Name:   "Res",
			Fields: []ast.Field{{Name: "ok", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "bool"}}},
		}},
		Funcs: []ast.Func{{
			Name: "set_thing", Method: "POST", Path: "/thing",
			Params: []ast.Param{
				{Name: "id", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "i32", Nullable: true}},
				{Name: "flag", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "bool", Nullable: true}},
				{Name: "tag", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string", Nullable: true}},
				{Name: "n", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "u32"}},
			},
			Return: &ast.TypeRef{Kind: ast.TypeNamed, Name: "Res"},
		}},
	}
}

func TestNullableScalarParam_ABIHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateServerHeader(&buf, nullableParamAPI()); err != nil {
		t.Fatalf("GenerateServerHeader: %v", err)
	}
	out := buf.String()
	// Nullable scalars → pointer; nullable string stays a single char*; plain
	// scalar stays by value.
	for _, want := range []string{"const int32_t *id", "const bool *flag", "uint32_t n"} {
		if !strings.Contains(out, want) {
			t.Errorf("api.h missing %q; got:\n%s", want, out)
		}
	}
	// A nullable string must NOT be double-pointered.
	if strings.Contains(out, "char * *tag") || strings.Contains(out, "char **tag") {
		t.Errorf("nullable string was double-pointered; got:\n%s", out)
	}
}

func TestNullableScalarParam_Dispatch(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateDispatchC(&buf, nullableParamAPI(), "nptest", "nptest_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	out := buf.String()
	// call_X wrapper takes the pointer; dispatch marshals a nullable scalar into
	// a malloc'd *C.int32_t (NULL when absent), freed via _freeList.
	for _, want := range []string{
		"const int32_t *id",
		"var cId *C.int32_t",
		"cId = (*C.int32_t)(C.malloc(",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dispatch missing %q; got:\n%s", want, out)
		}
	}
}

func TestNullableScalarParam_BridgeTrampoline(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateBridgeGo(&buf, nullableParamAPI(), "nptest"); err != nil {
		t.Fatalf("GenerateBridgeGo: %v", err)
	}
	out := buf.String()
	// Trampoline receives nullable scalars as pointers and a nullable string as a
	// single *C.char, and reconstructs a nil-preserving Go *int32 (NOT a &local).
	for _, want := range []string{
		"id *C.int32_t",
		"flag *C.bool",
		"tag *C.char",
		"var goId *int32",
		"if id != nil {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bridge missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tag **C.char") {
		t.Errorf("nullable string trampoline param was double-pointered; got:\n%s", out)
	}
	// The extern C decl for the nullable scalar is a non-const pointer (matches
	// the cgo //export; the callbacks builder casts to the const typedef).
	if !strings.Contains(out, "int32_t *id") {
		t.Errorf("bridge extern decl missing pointer for nullable scalar; got:\n%s", out)
	}
}
