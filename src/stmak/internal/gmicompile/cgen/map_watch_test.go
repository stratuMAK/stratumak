// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// mapWatchAPI is the pyvcp shape reduced to its essentials: one plain REST
// func (so the C surface is non-empty and its emission is provably intact)
// plus one watch-only func returning map[string]State with @watch_delta.
func mapWatchAPI() *ast.API {
	return &ast.API{
		Name:       "mapi",
		Version:    1,
		Prefix:     "mapi",
		RestExport: true,
		Types: []ast.Type{
			{
				Name: "State",
				Fields: []ast.Field{
					{Name: "value", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "f64"}},
					{Name: "on", Type: ast.TypeRef{Kind: ast.TypePrimitive, Name: "bool"}},
				},
			},
		},
		Funcs: []ast.Func{
			{
				Name:   "ping",
				Method: "GET",
				Path:   "/ping",
				Return: &ast.TypeRef{Kind: ast.TypePrimitive, Name: "bool"},
			},
			{
				Name:             "watch_state",
				Watch:            true,
				WatchDefaultRate: "100ms",
				WatchDelta:       true,
				Return: &ast.TypeRef{
					Kind: ast.TypeMap,
					Elem: &ast.TypeRef{Kind: ast.TypeNamed, Name: "State"},
				},
			},
		},
	}
}

// TestMapWatchGoProviderSurface — the Go side must carry the map watch:
// a typed WatchCallbacks method and a delta-enabled registration.
func TestMapWatchGoProviderSurface(t *testing.T) {
	var buf bytes.Buffer
	if err := GenerateServerGoExtra(&buf, mapWatchAPI(), "mapi"); err != nil {
		t.Fatalf("GenerateServerGoExtra: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"WatchState() (map[string]State, error)",
		`{Name: "watch_state", DefaultRate: 100 * time.Millisecond, Delta: true, Watch: func() (json.RawMessage, error) {`,
		"result, err := watchImpl.WatchState()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("server-go output missing %q\n---\n%s", want, out)
		}
	}
}

// TestMapWatchDeltaOmittedWhenUnset — a watch without @watch_delta must keep
// its registration byte-identical to the pre-map-support output.
func TestMapWatchDeltaOmittedWhenUnset(t *testing.T) {
	api := mapWatchAPI()
	api.Funcs[1].WatchDelta = false
	var buf bytes.Buffer
	if err := GenerateServerGoExtra(&buf, api, "mapi"); err != nil {
		t.Fatalf("GenerateServerGoExtra: %v", err)
	}
	if strings.Contains(buf.String(), "Delta:") {
		t.Errorf("Delta emitted without @watch_delta:\n%s", buf.String())
	}
}

// TestMapWatchHasNoCABI — the C-provider surface must skip the map watch
// entirely (header vtable, call wrapper, dispatch, FuncMeta) while keeping
// every other func, and must say WHY in the header.
func TestMapWatchHasNoCABI(t *testing.T) {
	api := mapWatchAPI()

	var hdr bytes.Buffer
	if err := GenerateServerHeader(&hdr, api); err != nil {
		t.Fatalf("GenerateServerHeader: %v", err)
	}
	h := hdr.String()
	if strings.Contains(h, "mapi_watch_state_fn") {
		t.Errorf("header emitted a C typedef for the map watch:\n%s", h)
	}
	if !strings.Contains(h, "JSON-only watch (map return)") {
		t.Errorf("header missing the skip explanation:\n%s", h)
	}
	if !strings.Contains(h, "mapi_ping_fn") {
		t.Errorf("header lost the ordinary func:\n%s", h)
	}

	var dis bytes.Buffer
	if err := GenerateDispatchC(&dis, api, "mapi", "mapi_api.h"); err != nil {
		t.Fatalf("GenerateDispatchC: %v", err)
	}
	d := dis.String()
	for _, absent := range []string{
		"call_mapi_watch_state",
		"mapiDispatchWatchState",
		`Name:     "watch_state"`,
	} {
		if strings.Contains(d, absent) {
			t.Errorf("C dispatch emitted %q for the map watch\n---\n%s", absent, d)
		}
	}
	if !strings.Contains(d, "mapiDispatchPing") {
		t.Errorf("C dispatch lost the ordinary func:\n%s", d)
	}
}

// TestMapWatchClientTypes — TS renders Record<string, T>, Python dict[str, T].
func TestMapWatchClientTypes(t *testing.T) {
	var ts bytes.Buffer
	if err := GenerateClientTSWS(&ts, mapWatchAPI()); err != nil {
		t.Fatalf("GenerateClientTSWS: %v", err)
	}
	if !strings.Contains(ts.String(), "Record<string, State>") {
		t.Errorf("TS WS client missing Record<string, State>:\n%s", ts.String())
	}

	var py bytes.Buffer
	if err := GenerateClientPythonWS(&py, mapWatchAPI()); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	if !strings.Contains(py.String(), "State.from_dict(v)") {
		t.Errorf("Python WS client missing per-value from_dict:\n%s", py.String())
	}
}
