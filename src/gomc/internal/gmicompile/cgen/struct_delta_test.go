// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
)

// structDeltaAPI is the emcstat shape: a watch-only func returning a named
// STRUCT with @watch_delta, whose struct carries a 64-bit field.
//
// This is the case that broke a real client. After the first push the server
// sends only the changed top-level keys, so frame two is `{"heartbeat":N}` —
// not a valid Stat. The generated 64-bit reviver ran BigInt(o.boot_id) on the
// missing field and threw inside the WebSocket message handler, so the app
// froze on its last full frame while the connection stayed healthy and the
// status badge stayed green.
func structDeltaAPI() *ast.API {
	i64 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI64}
	i32 := ast.TypeRef{Kind: ast.TypePrimitive, Name: ast.PrimI32}
	stat := ast.TypeRef{Kind: ast.TypeNamed, Name: "Stat"}
	return &ast.API{
		Name: "livestat", Version: 1, Prefix: "livestat", RestExport: true,
		Types: []ast.Type{
			{Name: "Stat", Fields: []ast.Field{
				{Name: "boot_id", Type: i64},
				{Name: "heartbeat", Type: i32},
			}},
		},
		Funcs: []ast.Func{
			{
				Name: "get_stat", Method: "GET", Path: "/stat",
				Watch: true, WatchDelta: true, WatchDefaultRate: "50ms",
				Return: &stat,
			},
		},
	}
}

func TestClientTSWSMergesStructDelta(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientTSWS(&buf, structDeltaAPI()); err != nil {
		t.Fatalf("GenerateClientTSWS: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"private deltaState = new Map<string, Record<string, unknown>>();",
		"private mergeDelta<T>(funcName: string, raw: unknown): T {",
		// The merge must happen BEFORE the reviver, which is what throws on a
		// partial object.
		"const __d = this.mergeDelta<Stat>('get_stat', raw); __reviveStat(__d); callback(__d);",
		// A resubscribe must not merge onto the previous connection's base.
		"this.deltaState.delete(funcName);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WS client missing %q:\n%s", want, out)
		}
	}

	// The raw cast is the pre-fix shape: it hands the callback a partial object.
	if strings.Contains(out, "const __d = raw as Stat;") {
		t.Errorf("WS client still replaces instead of merging a delta:\n%s", out)
	}
}

func TestClientTSWSLeavesMapDeltaAlone(t *testing.T) {
	// A @watch_delta MAP watch stays as it was: a map of the changed keys is
	// still a well-formed map of that type, and the apps consuming these
	// (classicladder rung states, pyvcp widget state) merge into their own
	// keyed store by design. Merging in the client would change their contract.
	var buf bytes.Buffer
	if err := GenerateClientTSWS(&buf, mapWatchAPI()); err != nil {
		t.Fatalf("GenerateClientTSWS: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "mergeDelta") {
		t.Errorf("map delta watch should not get the struct merge:\n%s", out)
	}
	if strings.Contains(out, "deltaState") {
		t.Errorf("map-only API should not carry delta state:\n%s", out)
	}
}

func TestClientPyWSMergesStructDelta(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, structDeltaAPI()); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"self._delta_state: dict[str, dict] = {}",
		"def _merge_delta(self, func_name: str, data):",
		`data = self._merge_delta("get_stat", data)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Python WS client missing %q:\n%s", want, out)
		}
	}
	// Both the async and the threaded subscribe wrappers must merge.
	if n := strings.Count(out, `data = self._merge_delta("get_stat", data)`); n != 2 {
		t.Errorf("merge appears %d times; want 2 (async + threaded wrapper)", n)
	}
}

func TestClientPyWSLeavesMapDeltaAlone(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, mapWatchAPI()); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "_merge_delta") {
		t.Errorf("map delta watch should not get the struct merge:\n%s", out)
	}
}

func TestClientTSWSNoDeltaStateWithoutDelta(t *testing.T) {
	// An API with no @watch_delta at all must be byte-identical to before.
	api := structDeltaAPI()
	api.Funcs[0].WatchDelta = false

	var buf bytes.Buffer
	if err := GenerateClientTSWS(&buf, api); err != nil {
		t.Fatalf("GenerateClientTSWS: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "deltaState") || strings.Contains(out, "mergeDelta") {
		t.Errorf("non-delta API gained delta machinery:\n%s", out)
	}
	if !strings.Contains(out, "const __d = raw as Stat;") {
		t.Errorf("non-delta watch should keep the plain cast:\n%s", out)
	}
}
