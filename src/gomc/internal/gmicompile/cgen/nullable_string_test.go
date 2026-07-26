// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests that `string?` really is nullable on the provider side.
//
// It used not to be. Four separate copies of the Go type mapping existed and
// three of them excluded strings from the nullable-pointer rule, so `string?`
// was `*string` in the standalone Go client and a plain `string` everywhere a
// provider could see it. Two consequences, both real:
//
//   - A provider could not *report* absence. inirest's Query branched on
//     keyExists to return null for a missing INI key, and both branches
//     produced the same value.
//   - A provider could not *produce* NULL for C. GoToC turned every "" into a
//     non-NULL empty C string, so a C callee written to the documented contract
//     — `if (req->hostname)` in the EtherCAT EoE handler — read an omitted
//     field as a supplied one and cleared the slave's hostname.
//
// These tests pin the emitted code for all four positions a nullable string can
// occupy, so a fifth copy of the rule cannot quietly reintroduce the exclusion.
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
)

func nullableStringAPI() *ast.API {
	return &ast.API{
		Name:       "nulls",
		Version:    1,
		Prefix:     "nulls",
		RestExport: true,
		Types: []ast.Type{
			{
				Name: "Rec",
				Fields: []ast.Field{
					{Name: "req", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "opt", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string", Nullable: true}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:   "put",
				Method: "POST",
				Path:   "/put",
				Params: []ast.Param{
					{Name: "pattern", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string", Nullable: true}},
					{Name: "rec", Type: ast.TypeRef{Kind: ast.TypeNamed, Name: "Rec"}},
				},
				Return: &ast.TypeRef{Kind: ast.TypeNamed, Name: "Rec"},
			},
		},
	}
}

// TestNullableStringIsPointerEverywhere is the anti-divergence test: every
// emitter that names a provider-facing Go type must agree that `string?` is
// *string. The bug was that they did not.
func TestNullableStringIsPointerEverywhere(t *testing.T) {
	api := nullableStringAPI()

	for _, tc := range []struct {
		name string
		gen  func(*bytes.Buffer) error
	}{
		{"server-meta (dispatch + cgo types)", func(b *bytes.Buffer) error {
			return GenerateDispatchC(b, api, "nulls", "nulls_api.h")
		}},
		{"client-go", func(b *bytes.Buffer) error {
			return GenerateClientGo(b, api, "nullsclient")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.gen(&buf); err != nil {
				t.Fatalf("generate: %v", err)
			}
			out := buf.String()
			assertContains(t, out, "Opt *string")
			// The non-nullable sibling must stay a plain string, or the fix
			// would have been "make every string a pointer".
			assertContains(t, out, "Req string")
			if strings.Contains(out, "Opt string ") {
				t.Errorf("a nullable string was emitted as a plain string:\n%s", out)
			}
		})
	}
}

// TestNullableStringGoToCEmitsNull pins the direction that mattered to C: a nil
// Go value must leave the C pointer NULL rather than pointing at "". This is
// the EtherCAT hostname bug — a C callee's `if (req->field)` is the documented
// way to ask "was this supplied?", and it was answered wrong for every field.
func TestNullableStringGoToCEmitsNull(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateDispatchC(&buf, nullableStringAPI(), "nulls", "nulls_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	out := buf.String()

	// Struct field: guarded by a nil check, and the C field is only assigned
	// inside it, so nil leaves the zero value (NULL).
	assertContains(t, out, "if src.Opt != nil {")
	assertContains(t, out, "C.CString(*src.Opt)")
	// Parameter: declared first, assigned only when supplied.
	assertContains(t, out, "var cPattern *C.char")
	assertContains(t, out, "C.CString(*params.Pattern)")

	// The non-nullable field keeps the direct, unguarded conversion.
	assertContains(t, out, "C.CString(src.Req)")
}

// TestNullableStringCToGoPreservesNull pins the other direction: a NULL from C
// must arrive as nil, not as "". Collapsing it here is what made "absent" and
// "present but empty" the same value in Go.
func TestNullableStringCToGoPreservesNull(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateDispatchC(&buf, nullableStringAPI(), "nulls", "nulls_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	out := buf.String()

	assertContains(t, out, "if src.opt == nil { return nil }")
	if strings.Contains(out, `if src.opt != nil { return C.GoString(src.opt) }; return ""`) {
		t.Error("CToGo still collapses a NULL string to \"\"")
	}
	assertContains(t, out, "Req: C.GoString(src.req)")
}

// TestNullableStringBridgeParamPreservesNull covers the C-calls-Go direction:
// the provider bridge must hand the implementation nil for a NULL argument.
func TestNullableStringBridgeParamPreservesNull(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateBridgeGo(&buf, nullableStringAPI(), "nulls"); err != nil {
		t.Fatalf("GenerateBridgeGo: %v", err)
	}
	out := buf.String()

	assertContains(t, out, "var goPattern *string")
	assertContains(t, out, "if pattern != nil {")
	// And the callbacks interface must declare the pointer, or the bridge
	// would not compile against a conforming provider.
	assertContains(t, out, "*string")
}
