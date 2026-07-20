// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
)

func TestGenerateDispatchC(t *testing.T) {
	api := &ast.API{
		Name:       "testapi",
		Version:    1,
		Prefix:     "testapi",
		RestExport: true,
		Enums: []ast.Enum{
			{
				Name: "Color",
				Values: []ast.EnumValue{
					{Name: "RED", Value: 0},
					{Name: "GREEN", Value: 1},
					{Name: "BLUE", Value: 2},
				},
			},
		},
		Types: []ast.Type{
			{
				Name: "Item",
				Fields: []ast.Field{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "value", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "f64"}},
					{Name: "color", Type: ast.TypeRef{Kind: ast.TypeNamed, Name: "Color"}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:   "list_items",
				Method: "GET",
				Path:   "/items",
				Params: []ast.Param{
					{Name: "pattern", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
				Return: &ast.TypeRef{Kind: ast.TypeSlice, Elem: &ast.TypeRef{Kind: ast.TypeNamed, Name: "Item"}},
			},
			{
				Name:   "get_item",
				Method: "GET",
				Path:   "/item/{name}",
				Params: []ast.Param{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
				Return: &ast.TypeRef{Kind: ast.TypeNamed, Name: "Item"},
			},
			{
				Name:   "delete_item",
				Method: "DELETE",
				Path:   "/item/{name}",
				Params: []ast.Param{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := GenerateDispatchC(&buf, api, "testpkg", "testapi_api.h")
	if err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}

	out := buf.String()

	// -- Package + cgo preamble --
	assertContains(t, out, "package testpkg")
	assertContains(t, out, `#include "testapi_api.h"`)
	assertContains(t, out, `#include <stdlib.h>`)
	assertContains(t, out, `import "C"`)

	// -- Static call wrappers (with ctx) --
	assertContains(t, out, "static testapi_list_items_result_t call_testapi_list_items(const testapi_callbacks_t *cbs,")
	assertContains(t, out, "static testapi_item_t call_testapi_get_item(const testapi_callbacks_t *cbs,")
	assertContains(t, out, "static void call_testapi_delete_item(const testapi_callbacks_t *cbs,")

	// -- Go imports --
	assertContains(t, out, `"encoding/json"`)
	assertContains(t, out, `"syscall"`)
	assertContains(t, out, `"unsafe"`)
	assertContains(t, out, `"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"`)

	// -- Enums --
	assertContains(t, out, "type Color int32")
	assertContains(t, out, "RED Color = 0")
	assertContains(t, out, "GREEN Color = 1")

	// -- Types --
	assertContains(t, out, "type Item struct {")
	assertContains(t, out, `Name string `+"`"+`json:"name"`+"`")
	assertContains(t, out, `Value float64 `+"`"+`json:"value"`+"`")
	assertContains(t, out, `Color Color `+"`"+`json:"color"`+"`")

	// -- CToGo converters --
	assertContains(t, out, "func itemCToGo(src *C.testapi_item_t) Item")
	assertContains(t, out, "C.GoString(src.name)")
	assertContains(t, out, "float64(src.value)")
	assertContains(t, out, "Color(src.color)")

	// -- Dispatch functions --
	assertContains(t, out, "func testapiDispatchListItems(callbacks unsafe.Pointer, req []byte) ([]byte, error)")
	assertContains(t, out, "func testapiDispatchGetItem(callbacks unsafe.Pointer, req []byte) ([]byte, error)")
	assertContains(t, out, "func testapiDispatchDeleteItem(callbacks unsafe.Pointer, req []byte) ([]byte, error)")

	// Check JSON param unmarshal
	assertContains(t, out, `Pattern string `+"`"+`json:"pattern"`+"`")

	// Check C callback type usage
	assertContains(t, out, "(*C.testapi_callbacks_t)(callbacks)")

	// Check C.CString for string params
	assertContains(t, out, "C.CString(params.Pattern)")
	assertContains(t, out, "C.free(unsafe.Pointer(")

	// Check dispatch return conversions
	assertContains(t, out, "itemCToGo(&cSlice[i])") // slice return
	assertContains(t, out, "itemCToGo(&out)")       // struct return
	assertContains(t, out, "return nil, nil")       // void return

	// -- APIMeta --
	assertContains(t, out, "var TestapiMeta = &apiserver.APIMeta{")
	assertContains(t, out, `Name:       "testapi"`)
	assertContains(t, out, "Version:    1")
	assertContains(t, out, "RESTExport: true")
	assertContains(t, out, "testapiDispatchListItems,")
	assertContains(t, out, "testapiDispatchGetItem,")
	assertContains(t, out, "testapiDispatchDeleteItem,")

	// -- Meta Registration --
	assertContains(t, out, "func init() {")
	assertContains(t, out, "apiserver.RegisterMeta(TestapiMeta)")
}

func TestGenerateDispatchCKeywordFields(t *testing.T) {
	api := &ast.API{
		Name:    "kwapi",
		Version: 1,
		Prefix:  "kwapi",
		Types: []ast.Type{
			{
				Name: "TypeInfo",
				Fields: []ast.Field{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "type", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "range", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "i32"}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:   "get_info",
				Method: "GET",
				Path:   "/info/{name}",
				Params: []ast.Param{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
				Return: &ast.TypeRef{Kind: ast.TypeNamed, Name: "TypeInfo"},
			},
		},
	}

	var buf bytes.Buffer
	err := GenerateDispatchC(&buf, api, "kwpkg", "kwapi_api.h")
	if err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}

	out := buf.String()

	// cgo prefixes Go keywords with _ when accessing C struct fields
	assertContains(t, out, "src._type")
	assertContains(t, out, "src._range")
	assertContains(t, out, "src.name") // non-keyword, no prefix
}

func TestCgoFieldAccess(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"name", "name"},
		{"type", "_type"},
		{"func", "_func"},
		{"var", "_var"},
		{"range", "_range"},
		{"value", "value"},
		{"go", "_go"},
	}

	for _, tt := range tests {
		got := cgoFieldAccess(tt.input)
		if got != tt.want {
			t.Errorf("cgoFieldAccess(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateDispatchCVoidReturn(t *testing.T) {
	api := &ast.API{
		Name:    "voidapi",
		Version: 1,
		Prefix:  "voidapi",
		Funcs: []ast.Func{
			{
				Name:   "do_action",
				Method: "POST",
				Path:   "/action",
				Params: []ast.Param{
					{Name: "value", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "i32"}},
				},
				// No return type
			},
		},
	}

	var buf bytes.Buffer
	err := GenerateDispatchC(&buf, api, "voidpkg", "voidapi_api.h")
	if err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}

	out := buf.String()

	// No out param in call wrapper — void return (with ctx)
	assertContains(t, out, "static void call_voidapi_do_action(const voidapi_callbacks_t *cbs, int32_t value)")
	assertContains(t, out, "cbs->do_action(cbs->ctx, value);")

	// Dispatch function returns nil, nil
	assertContains(t, out, "return nil, nil")
}

// TestGenerateDispatchCArrayElemFree guards the free path for a fixed array of
// structs whose element type owns a string. The array is inline (no buffer of
// its own), but each element's string must still be freed under "caller owns
// returned data" — a case emitFreeCAllocs/typeHasCAllocs originally skipped.
func TestGenerateDispatchCArrayElemFree(t *testing.T) {
	api := &ast.API{
		Name:    "arrapi",
		Version: 1,
		Prefix:  "arrapi",
		Types: []ast.Type{
			{
				Name: "Item",
				Fields: []ast.Field{
					{Name: "name", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
			},
			{
				Name: "Bundle",
				Fields: []ast.Field{
					{Name: "items", Type: ast.TypeRef{
						Kind:     ast.TypeArray,
						Elem:     &ast.TypeRef{Kind: ast.TypeNamed, Name: "Item"},
						ArrayLen: 3,
					}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:   "get_bundle",
				Method: "GET",
				Path:   "/bundle",
				Return: &ast.TypeRef{Kind: ast.TypeNamed, Name: "Bundle"},
			},
		},
	}

	var buf bytes.Buffer
	if err := GenerateDispatchC(&buf, api, "arrpkg", "arrapi_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	out := buf.String()

	// The bundle's array elements must be walked and their strings freed.
	assertContains(t, out, "for _i0 := 0; _i0 < 3; _i0++ {")
	assertContains(t, out, "C.free(unsafe.Pointer(out.items[_i0].name))")
}

func TestGenerateDispatchCPrimitiveReturn(t *testing.T) {
	api := &ast.API{
		Name:    "primapi",
		Version: 2,
		Prefix:  "primapi",
		Funcs: []ast.Func{
			{
				Name:   "get_count",
				Method: "GET",
				Path:   "/count",
				Return: &ast.TypeRef{Kind: ast.TypePrimitive, Name: "i32"},
			},
		},
	}

	var buf bytes.Buffer
	err := GenerateDispatchC(&buf, api, "primpkg", "primapi_api.h")
	if err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}

	out := buf.String()

	// Primitive return: direct return (with ctx)
	assertContains(t, out, "static int32_t call_primapi_get_count(const primapi_callbacks_t *cbs)")
	assertContains(t, out, "int32(out)")
	assertContains(t, out, "Version:    2")
}

// --- Fail-fast guards for unsupported type shapes (G-L4 / G-L6 residual) ---
//
// Both shapes are latent today (no committed IDL uses them) but previously
// emitted silently-wrong code: cTypeForAPICgo fell through to C.int (truncating
// a real pointer/array at the FFI boundary), and emitFieldGoToC ran a
// fixed-array-of-string through the scalar path, emitting non-compiling C with
// no CString alloc or free. The guards convert both into loud generator panics.

func mustPanic(t *testing.T, contains string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", contains)
		}
		if msg, _ := r.(string); !bytes.Contains([]byte(msg), []byte(contains)) {
			t.Fatalf("panic %q does not contain %q", r, contains)
		}
	}()
	fn()
}

func TestCTypeForAPICgoRejectsUnsupportedShape(t *testing.T) {
	// A fixed array reaches cTypeForAPICgo with no case → must panic, not C.int.
	arr := ast.TypeRef{Kind: ast.TypeArray, ArrayLen: 4, Elem: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimString}}
	mustPanic(t, "unsupported type shape", func() { cTypeForAPICgo("testapi", arr) })
}

func TestEmitFieldGoToCRejectsFixedArrayOfString(t *testing.T) {
	g := &dispatchCGen{w: new(bytes.Buffer), api: &ast.API{Name: "testapi"}}
	fixedStrArr := ast.TypeRef{Kind: ast.TypeArray, ArrayLen: 4, Elem: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimString}}
	mustPanic(t, "fixed-array-of-string", func() { g.emitFieldGoToC("out.names", "in.Names", fixedStrArr) })
}
