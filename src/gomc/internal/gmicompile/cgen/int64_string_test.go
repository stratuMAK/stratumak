// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/check"
)

// int64StringAPI is a representative API exercising the 64-bit-as-JSON-string
// change: a response type with a scalar u64 field and a nested slice of structs
// (whose element also has an i64 field), plus a function that returns that type
// and takes an i64 request parameter.
func int64StringAPI() *ast.API {
	u64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimU64}
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}
	i32 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32}
	point := ast.TypeRef{Kind: ast.TypeNamed, Name: "Point"}
	pointSlice := ast.TypeRef{Kind: ast.TypeSlice, Elem: &point}
	stats := ast.TypeRef{Kind: ast.TypeNamed, Name: "Stats"}

	return &ast.API{
		Name:       "counters",
		Version:    1,
		Prefix:     "counters",
		RestExport: true,
		Types: []ast.Type{
			{Name: "Point", Fields: []ast.Field{
				{Name: "t_ms", Type: i64},
				{Name: "value", Type: i32},
			}},
			{Name: "Stats", Fields: []ast.Field{
				{Name: "tx_bytes", Type: u64},
				{Name: "bucket_ms", Type: i32},
				{Name: "points", Type: pointSlice},
			}},
		},
		Funcs: []ast.Func{
			{
				Name: "get_stats", Method: "GET", Path: "/stats",
				Return: &stats,
			},
			{
				Name: "set_period", Method: "POST", Path: "/period",
				Params: []ast.Param{{Name: "period_ns", Type: i64}},
			},
		},
	}
}

func TestPredicate64BitScalarInt(t *testing.T) {
	yes := []ast.TypeRef{
		{Kind: ast.TypePrimitive, Name: ast.PrimI64},
		{Kind: ast.TypePrimitive, Name: ast.PrimU64},
		{Kind: ast.TypePrimitive, Name: ast.PrimI64, Nullable: true},
	}
	no := []ast.TypeRef{
		{Kind: ast.TypePrimitive, Name: ast.PrimI32},
		{Kind: ast.TypePrimitive, Name: ast.PrimU32},
		{Kind: ast.TypePrimitive, Name: ast.PrimPtr},
		{Kind: ast.TypeNamed, Name: "Stats"},
	}
	for _, tr := range yes {
		if !is64BitScalarInt(tr) {
			t.Errorf("is64BitScalarInt(%s) = false, want true", tr.String())
		}
	}
	for _, tr := range no {
		if is64BitScalarInt(tr) {
			t.Errorf("is64BitScalarInt(%s) = true, want false", tr.String())
		}
	}
}

func TestJSONStringOptParamExcludesPtrAndOut(t *testing.T) {
	u64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimU64}
	if got := jsonStringOptParam(ast.Param{Name: "v", Type: u64}); got != ",string" {
		t.Errorf("plain u64 param opt = %q, want \",string\"", got)
	}
	// ptr/byref/out params must never be widened to a JSON string.
	for _, p := range []ast.Param{
		{Name: "v", Type: u64, IsPtr: true},
		{Name: "v", Type: u64, ByRef: true},
		{Name: "v", Type: u64, IsOut: true},
	} {
		if got := jsonStringOptParam(p); got != "" {
			t.Errorf("excluded param opt = %q, want \"\"", got)
		}
	}
}

// TestServerGoStructFieldStringTag verifies response struct fields carry
// ",string" while request params do NOT (they arrive as JSON numbers via
// apiserver.encodeParams, which a ",string" field would reject).
func TestServerGoStructFieldStringTag(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateServerGo(&buf, int64StringAPI(), "counters"); err != nil {
		t.Fatalf("GenerateServerGo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "`json:\"tx_bytes,string\"`") {
		t.Errorf("u64 struct field missing ,string tag:\n%s", out)
	}
	if !strings.Contains(out, "`json:\"t_ms,string\"`") {
		t.Errorf("i64 nested struct field missing ,string tag")
	}
	if strings.Contains(out, "`json:\"bucket_ms,string\"`") {
		t.Errorf("i32 field must NOT get ,string tag")
	}
	// set_period's period_ns is a POST *body* param, so it IS string-encoded
	// (symmetric with the client); the check layer guarantees no 64-bit
	// path/query param reaches this struct.
	if !strings.Contains(out, "`json:\"period_ns,string\"`") {
		t.Errorf("64-bit POST body param should carry ,string:\n%s", out)
	}
}

func TestClientGoStructFieldStringTag(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientGo(&buf, int64StringAPI(), "counters"); err != nil {
		t.Fatalf("GenerateClientGo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "`json:\"tx_bytes,string\"`") {
		t.Errorf("Go client u64 field missing ,string tag:\n%s", out)
	}
}

// TestClientTSBigintAndRevive verifies the TS client types 64-bit fields as
// bigint, emits recursive revivers (including the nested slice), wires the
// reviver into the return path, and types+sends a 64-bit body param as
// bigint / String().
func TestClientTSBigintAndRevive(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientTS(&buf, int64StringAPI()); err != nil {
		t.Fatalf("GenerateClientTS: %v", err)
	}
	out := buf.String()
	checks := []string{
		"tx_bytes: bigint;",      // field typed bigint
		"t_ms: bigint;",          // nested field typed bigint
		"function __reviveStats", // reviver emitted
		"function __revivePoint", // nested reviver emitted
		"BigInt(o.tx_bytes as unknown as string)",
		"o.points?.forEach(__revivePoint)", // nested slice revived
		"__reviveStats(__res);",            // wired into the getter
		"periodNs: bigint",                 // 64-bit body param typed bigint
		"period_ns: String(periodNs)",      // ...serialized as a JSON string
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("TS client missing %q:\n%s", c, out)
		}
	}
}

// flat64API has a type with a direct u64 field (no nested 64-bit) plus a POST
// body i64 param — the shape the Python client fully supports.
func flat64API() *ast.API {
	u64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimU64}
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}
	i32 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32}
	stats := ast.TypeRef{Kind: ast.TypeNamed, Name: "Stats"}
	return &ast.API{
		Name: "flat", Version: 1, Prefix: "flat", RestExport: true,
		Types: []ast.Type{{Name: "Stats", Fields: []ast.Field{
			{Name: "tx_bytes", Type: u64},
			{Name: "bucket_ms", Type: i32},
		}}},
		Funcs: []ast.Func{
			{Name: "get_stats", Method: "GET", Path: "/stats", Return: &stats},
			{Name: "set_period", Method: "POST", Path: "/period",
				Params: []ast.Param{{Name: "period_ns", Type: i64}}},
		},
	}
}

func TestClientPyIntConversion(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientPython(&buf, flat64API()); err != nil {
		t.Fatalf("GenerateClientPython: %v", err)
	}
	out := buf.String()
	checks := []string{
		"def _gmi_to_int(v):",
		"def _gmi_from_int(v):",
		"tx_bytes=_gmi_to_int(d.get(\"tx_bytes\", 0))",
		"\"tx_bytes\": _gmi_from_int(self.tx_bytes)",
		"\"period_ns\": _gmi_from_int(period_ns)", // body param string-encoded
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Python client missing %q:\n%s", c, out)
		}
	}
}

// TestClientPyWSIntConversion covers the py-ws cell of the 64-bit matrix in
// both directions: struct fields arriving as wire strings must become Python
// ints in from_dict, and i64 command args must go out as JSON strings — on the
// async client AND the threaded wrapper command path.
func TestClientPyWSIntConversion(t *testing.T) {
	api := flat64API()
	// Make get_stats a watch so the API is the shape --client-python-ws
	// actually serves (at least one @watch function).
	api.Funcs[0].Watch = true

	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, api); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	out := buf.String()
	checks := []string{
		"def _gmi_to_int(v):",
		"def _gmi_from_int(v):",
		// Incoming: wire string -> int at the from_dict seam.
		"tx_bytes=_gmi_to_int(d.get(\"tx_bytes\", 0)),",
		// Outgoing: i64 command arg -> JSON string.
		"\"period_ns\": _gmi_from_int(period_ns),",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Python WS client missing %q:\n%s", c, out)
		}
	}
	// The conversion must exist on both command paths: the async client's
	// set_period and the threaded wrapper's fire-and-forget set_period.
	if n := strings.Count(out, "\"period_ns\": _gmi_from_int(period_ns),"); n != 2 {
		t.Errorf("outgoing i64 conversion appears %d times; want 2 (async + threaded wrapper):\n%s", n, out)
	}
	// The i32 field must stay untouched.
	if strings.Contains(out, "bucket_ms=_gmi_to_int") {
		t.Errorf("i32 field must not get the 64-bit conversion:\n%s", out)
	}
}

// TestClientPyWSNo64BitNoHelpers: an API without 64-bit scalars must not gain
// the helper functions (byte-stability for every other generated client).
func TestClientPyWSNo64BitNoHelpers(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, watchOnly64API()); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	if !strings.Contains(buf.String(), "_gmi_to_int") {
		t.Errorf("watch API with i64 field should emit converters")
	}

	// Strip the 64-bit field: no helpers may be emitted.
	api := watchOnly64API()
	api.Types[0].Fields = api.Types[0].Fields[1:] // drop boot_id (i64)
	buf.Reset()
	if err := GenerateClientPythonWS(&buf, api); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	if strings.Contains(buf.String(), "_gmi_") {
		t.Errorf("64-bit-free API gained conversion helpers:\n%s", buf.String())
	}
}

// TestClientPyWSCollection64BitRejected: a 64-bit int reachable only through a
// collection of named types (int64StringAPI: Stats.points -> []Point.t_ms) is a
// shape the WS client's from_dict cannot convert (collection elements stay raw
// dicts) — it must fail loud rather than emit a silently-wrong client.
func TestClientPyWSCollection64BitRejected(t *testing.T) {
	var buf bytes.Buffer
	err := GenerateClientPythonWS(&buf, int64StringAPI())
	if err == nil {
		t.Fatalf("expected error for 64-bit field inside a collection, got none")
	}
	if !strings.Contains(err.Error(), "collection") {
		t.Errorf("error should mention the collection shape, got: %v", err)
	}
}

// TestClientPyWSDirectNested64BitAccepted: unlike the REST Python client, the
// WS client's from_dict recurses into a DIRECT named-type field, so a 64-bit
// int there converts fine and must be accepted.
func TestClientPyWSDirectNested64BitAccepted(t *testing.T) {
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}
	point := ast.TypeRef{Kind: ast.TypeNamed, Name: "Point"}
	outer := ast.TypeRef{Kind: ast.TypeNamed, Name: "Outer"}
	api := &ast.API{
		Name: "nested", Version: 1, Prefix: "nested", RestExport: true,
		Types: []ast.Type{
			{Name: "Point", Fields: []ast.Field{{Name: "t_ms", Type: i64}}},
			{Name: "Outer", Fields: []ast.Field{{Name: "p", Type: point}}},
		},
		Funcs: []ast.Func{
			{Name: "get_outer", Method: "GET", Path: "/outer", Watch: true, Return: &outer},
		},
	}
	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, api); err != nil {
		t.Fatalf("direct nested 64-bit should be accepted (from_dict recurses): %v", err)
	}
	out := buf.String()
	// The recursion carries the conversion: Outer.from_dict delegates to
	// Point.from_dict, which converts t_ms.
	for _, c := range []string{
		"p=Point.from_dict(d[\"p\"]) if \"p\" in d else Point(),",
		"t_ms=_gmi_to_int(d.get(\"t_ms\", 0)),",
	} {
		if !strings.Contains(out, c) {
			t.Errorf("Python WS client missing %q:\n%s", c, out)
		}
	}
}

// TestPyNested64BitRejected verifies the Python client fails loud on a 64-bit
// field reachable only through a nested type (int64StringAPI: Stats.points ->
// Point.t_ms), which from_dict cannot convert.
func TestPyNested64BitRejected(t *testing.T) {
	var buf bytes.Buffer
	err := GenerateClientPython(&buf, int64StringAPI())
	if err == nil {
		t.Fatalf("expected error for nested 64-bit field, got none")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error should mention nesting, got: %v", err)
	}
}

// TestCheck64BitPathQueryParam verifies the check layer rejects a 64-bit path or
// query param but accepts a 64-bit POST body param.
func TestCheck64BitPathQueryParam(t *testing.T) {
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}

	// GET query param — rejected.
	getAPI := &ast.API{Name: "a", Version: 1, Funcs: []ast.Func{
		{Name: "f", Method: "GET", Path: "/f", Params: []ast.Param{{Name: "id", Type: i64}}},
	}}
	if errs := check.Validate(getAPI); len(errs) == 0 {
		t.Errorf("expected error for 64-bit GET query param")
	}

	// Path param — rejected.
	pathAPI := &ast.API{Name: "a", Version: 1, Funcs: []ast.Func{
		{Name: "f", Method: "POST", Path: "/f/{id}", Params: []ast.Param{{Name: "id", Type: i64}}},
	}}
	if errs := check.Validate(pathAPI); len(errs) == 0 {
		t.Errorf("expected error for 64-bit path param")
	}

	// POST body param — accepted.
	bodyAPI := &ast.API{Name: "a", Version: 1, Funcs: []ast.Func{
		{Name: "f", Method: "POST", Path: "/f", Params: []ast.Param{{Name: "period_ns", Type: i64}}},
	}}
	if errs := check.Validate(bodyAPI); len(errs) != 0 {
		t.Errorf("64-bit POST body param should be accepted, got: %v", errs)
	}

	// Internal func (no @path) — unaffected.
	cgoAPI := &ast.API{Name: "a", Version: 1, Funcs: []ast.Func{
		{Name: "malloc", Params: []ast.Param{{Name: "size", Type: i64}}},
	}}
	if errs := check.Validate(cgoAPI); len(errs) != 0 {
		t.Errorf("non-REST 64-bit param should be accepted, got: %v", errs)
	}
}

func TestTSReviveSetTransitive(t *testing.T) {
	set := tsReviveSet(int64StringAPI())
	if !set["Point"] {
		t.Errorf("Point (direct i64 field) should need revive")
	}
	if !set["Stats"] {
		t.Errorf("Stats (direct u64 + slice of revivable Point) should need revive")
	}
}

// watchOnly64API returns its bigint-carrying type from a WATCH function only —
// the emcstat shape. The REST client emits no method for it, so it must emit no
// reviver for it either: an uncalled function is a hard error under the
// webapps' noUnusedLocals.
func watchOnly64API() *ast.API {
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}
	i32 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32}
	stat := ast.TypeRef{Kind: ast.TypeNamed, Name: "Stat"}
	info := ast.TypeRef{Kind: ast.TypeNamed, Name: "Info"}
	return &ast.API{
		Name: "livestat", Version: 1, Prefix: "livestat", RestExport: true,
		Types: []ast.Type{
			{Name: "Stat", Fields: []ast.Field{
				{Name: "boot_id", Type: i64},
				{Name: "heartbeat", Type: i32},
			}},
			{Name: "Info", Fields: []ast.Field{{Name: "joints", Type: i32}}},
		},
		Funcs: []ast.Func{
			{Name: "get_stat", Method: "GET", Path: "/stat", Watch: true, Return: &stat},
			{Name: "get_info", Method: "GET", Path: "/info", Return: &info},
		},
	}
}

func TestClientTSOmitsReviverForWatchOnlyType(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientTS(&buf, watchOnly64API()); err != nil {
		t.Fatalf("GenerateClientTS: %v", err)
	}
	out := buf.String()
	// The type and its bigint field still have to be declared — the WS client
	// imports nothing, but application code refers to the interface.
	if !strings.Contains(out, "boot_id: bigint;") {
		t.Errorf("REST client should still declare the bigint field:\n%s", out)
	}
	if strings.Contains(out, "function __reviveStat") {
		t.Errorf("REST client emitted a reviver for a watch-only type (nothing calls it):\n%s", out)
	}
}

func TestClientTSWSKeepsReviverForWatchOnlyType(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientTSWS(&buf, watchOnly64API()); err != nil {
		t.Fatalf("GenerateClientTSWS: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "function __reviveStat") {
		t.Errorf("WS client must emit the reviver for the type it subscribes to:\n%s", out)
	}
	if !strings.Contains(out, "__reviveStat(__d)") {
		t.Errorf("WS subscribe callback must revive before invoking the caller:\n%s", out)
	}
}
