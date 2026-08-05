// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// rcErrorAPI is the storage-API shape: a struct payload and a slice payload,
// both carried by an out param with the i32 return as the status channel.
func rcErrorAPI() *ast.API {
	entry := ast.TypeRef{Kind: ast.TypeNamed, Name: "Entry"}
	return &ast.API{
		Name:       "store",
		Version:    1,
		Prefix:     "store",
		RestExport: true,
		Types: []ast.Type{
			{
				Name: "Entry",
				Fields: []ast.Field{
					{Name: "key", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "value", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:    "get_entry",
				Method:  "GET",
				Path:    "/{key}",
				RcError: true,
				Params: []ast.Param{
					{Name: "key", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "string"}},
					{Name: "entry", Type: entry, IsOut: true},
				},
				Return: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32},
			},
			{
				Name:    "get_entries",
				Method:  "GET",
				Path:    "/",
				RcError: true,
				Params: []ast.Param{
					{Name: "entries", Type: ast.TypeRef{Kind: ast.TypeSlice, Elem: &entry}, IsOut: true},
				},
				Return: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32},
			},
		},
	}
}

// The whole point of the shape: a provider failure must reach the caller
// instead of arriving as a zero-valued payload.
func TestRcErrorDispatchReportsFailure(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateDispatchC(&buf, rcErrorAPI(), "store", "store_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	out := buf.String()

	// The rc is checked, and only then is the payload marshaled.
	assertContains(t, out, "rc := int32(C.call_store_get_entry(cb, cKey, &cEntry))")
	assertContains(t, out, `return nil, fmt.Errorf("get_entry: rc=%d", rc)`)
	assertContains(t, out, "result := entryCToGo(&cEntry)")
	assertContains(t, out, "return json.Marshal(result)")

	// The out param is the reply, so the request must not carry a field for it
	// — reading one would let a caller supply the provider's answer.
	getEntry := out[strings.Index(out, "func storeDispatchGetEntry"):]
	getEntry = getEntry[:strings.Index(getEntry, "\n}\n")]
	if strings.Contains(getEntry, `json:"entry"`) {
		t.Errorf("out param appears in the request struct:\n%s", getEntry)
	}

	// Slice payload: the owning {data, len} struct, converted and freed.
	assertContains(t, out, "var cEntries C.store_entry_slice_t")
	assertContains(t, out, "n := int(cEntries.len)")
	assertContains(t, out, "C.free(unsafe.Pointer(cEntries.data))")
}

// The conversion must be invisible to Go code on both sides: consumer and
// provider keep the signatures they had as value-returning functions.
func TestRcErrorKeepsGoSignatures(t *testing.T) {
	var client bytes.Buffer
	if err := GenerateClientCgo(&client, rcErrorAPI(), "store"); err != nil {
		t.Fatalf("GenerateClientCgo: %v", err)
	}
	assertContains(t, client.String(), "func (cl *StoreClient) GetEntry(key string) (Entry, error) {")
	assertContains(t, client.String(), "func (cl *StoreClient) GetEntries() ([]Entry, error) {")

	var bridge bytes.Buffer
	if err := GenerateBridgeGo(&bridge, rcErrorAPI(), "store"); err != nil {
		t.Fatalf("GenerateBridgeGo: %v", err)
	}
	out := bridge.String()
	assertContains(t, out, "GetEntry(key string) (Entry, error)")
	assertContains(t, out, "GetEntries() ([]Entry, error)")
	// The provider never spells out an rc — the trampoline encodes its error.
	assertContains(t, out, "_out0, _err := impl.GetEntry(goKey)")
	assertContains(t, out, "\t\treturn -1\n")
	assertContains(t, out, "\treturn 0\n")
}

// A REST-routed @rc_error func must stay a REST command, or the server would
// serve a route its clients no longer emit (or the reverse).
func TestRcErrorStaysRESTCommand(t *testing.T) {
	for _, fn := range rcErrorAPI().Funcs {
		if !isRESTCommandFunc(fn) {
			t.Errorf("%s: excluded from REST dispatch", fn.Name)
		}
		view := restView(fn)
		if view.Return == nil || view.Return.Kind == ast.TypePrimitive {
			t.Errorf("%s: REST view returns the status, not the payload: %+v", fn.Name, view.Return)
		}
		for _, p := range view.Params {
			if p.IsOut {
				t.Errorf("%s: out param %s survived into the REST view", fn.Name, p.Name)
			}
		}
	}
}

// An out param on a plain (non-@rc_error) func is still unmarshalable, so it
// must stay out of the REST surface — the rule the shape narrows, not removes.
func TestPlainOutParamStillNotRESTCommand(t *testing.T) {
	fn := ast.Func{
		Name:   "get_status",
		Params: []ast.Param{{Name: "status", Type: ast.TypeRef{Kind: ast.TypeNamed, Name: "Status"}, IsOut: true}},
		Return: &ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32},
	}
	if isRESTCommandFunc(fn) {
		t.Error("a plain out-param func was admitted to the REST surface")
	}
}
