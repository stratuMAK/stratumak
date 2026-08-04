// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
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
		// A non-object frame must not be spread (mirrors Python's dict guard).
		"if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {",
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
		// Unsubscribe must not carry the merge base into a resubscription
		// (which gets a fresh full frame) — same rule as the TS client.
		"self._delta_state.pop(func_name, None)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Python WS client missing %q:\n%s", want, out)
		}
	}

	// The class boundary is where this broke once: _merge_delta/_delta_state
	// are members of the async WatchClient class only, but the threaded
	// wrapper's callback also called self._merge_delta — an AttributeError on
	// frame two, raised inside the client's _recv_loop, killing every
	// subscription. Assert each emitted merge call's receiver actually holds
	// the method: split the module at the wrapper class header and check each
	// side separately.
	wrapperIdx := strings.Index(out, "class LivestatWatchThread:")
	if wrapperIdx < 0 {
		t.Fatalf("threaded wrapper class not emitted:\n%s", out)
	}
	clientPart, wrapperPart := out[:wrapperIdx], out[wrapperIdx:]

	// _merge_delta is defined once, on the async client class.
	if n := strings.Count(clientPart, "def _merge_delta"); n != 1 {
		t.Errorf("async client defines _merge_delta %d times; want 1", n)
	}
	if strings.Contains(wrapperPart, "def _merge_delta") {
		t.Errorf("threaded wrapper must not define its own _merge_delta (the base must be shared with the client)")
	}

	// Async client's typed subscribe merges via its own method...
	if n := strings.Count(clientPart, `data = self._merge_delta("get_stat", data)`); n != 1 {
		t.Errorf("async subscribe merge appears %d times; want 1:\n%s", n, clientPart)
	}
	// ...the wrapper's queued callback merges via the wrapped client, which is
	// the object that has the method and the state.
	if n := strings.Count(wrapperPart, `data = self._client._merge_delta("get_stat", data)`); n != 1 {
		t.Errorf("wrapper merge via self._client appears %d times; want 1:\n%s", n, wrapperPart)
	}
	// No bare self._merge_delta may survive in the wrapper — that is the
	// AttributeError shape.
	if strings.Contains(wrapperPart, "self._merge_delta(") {
		t.Errorf("threaded wrapper still calls self._merge_delta (undefined on the wrapper class):\n%s", wrapperPart)
	}
}

// pyWSDeltaHarness drives the generated module with a stubbed websockets
// package and no event loop: it feeds a full frame then a partial one through
// BOTH subscribe paths (async client and threaded wrapper) and asserts the
// second frame arrives merged and 64-bit-revived. Before the class-boundary
// fix, the wrapper path raised AttributeError on the first frame.
const pyWSDeltaHarness = `
import asyncio
import py_compile
import sys
import types

# Stub the websockets dependency: the generated module imports it at the top,
# but nothing here opens a real connection.
ws_mod = types.ModuleType("websockets")
class _ConnClosed(Exception):
    pass
ws_mod.ConnectionClosed = _ConnClosed
async def _connect(url):
    raise RuntimeError("harness never connects")
ws_mod.connect = _connect
sys.modules["websockets"] = ws_mod

path = sys.argv[1]
py_compile.compile(path, doraise=True)

import importlib.util
spec = importlib.util.spec_from_file_location("genclient", path)
mod = importlib.util.module_from_spec(spec)
sys.modules["genclient"] = mod  # dataclasses resolves cls.__module__ here
spec.loader.exec_module(mod)

class FakeWS:
    async def send(self, msg):
        pass

# --- Async client path: typed subscribe merges on the client itself. ---
client = mod.LivestatWatchClient("ws://test")
client._ws = FakeWS()
got = []
asyncio.run(client.subscribe_get_stat(callback=got.append))
cb = client._callbacks["get_stat"]
cb({"boot_id": "7", "heartbeat": 1})   # full frame; i64 arrives as a string
cb({"heartbeat": 2})                    # delta frame: only the changed key
assert len(got) == 2, got
assert got[1].boot_id == 7, got[1]      # merged AND converted to int
assert got[1].heartbeat == 2, got[1]

# Unsubscribe clears the merge base.
asyncio.run(client.unsubscribe("get_stat"))
assert "get_stat" not in client._delta_state, client._delta_state

# --- Threaded wrapper path: queued callback must reach the client's base. ---
wrapper = mod.LivestatWatchThread("ws://test")
wgot = []
wrapper.subscribe_get_stat(callback=wgot.append)
# _connect_and_subscribe assigns _client before any frame can arrive; the
# harness performs the same step without opening a socket.
wrapper._client = mod.LivestatWatchClient("ws://test")
(fname, rate, wcb) = wrapper._pending_subs[0]
assert fname == "get_stat", fname
wcb({"boot_id": "9", "heartbeat": 1})
wcb({"heartbeat": 3})
assert len(wgot) == 2, wgot
assert wgot[1].boot_id == 9, wgot[1]
assert wgot[1].heartbeat == 3, wgot[1]
# The base lives on the wrapped client, exactly once.
assert "get_stat" in wrapper._client._delta_state

# A non-dict frame passes through and resets the base.
wcb(None)
assert wgot[2] is None, wgot[2]
assert "get_stat" not in wrapper._client._delta_state
wcb({"boot_id": "1", "heartbeat": 4})   # fresh base afterwards
assert wgot[3].boot_id == 1 and wgot[3].heartbeat == 4, wgot[3]

print("OK")
`

// TestClientPyWSDeltaMergeRuntime executes the generated Python against two
// frames on both subscribe paths. Structural checks above catch the known
// shapes; this catches whatever they miss.
func TestClientPyWSDeltaMergeRuntime(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	var buf bytes.Buffer
	if err := GenerateClientPythonWS(&buf, structDeltaAPI()); err != nil {
		t.Fatalf("GenerateClientPythonWS: %v", err)
	}
	dir := t.TempDir()
	modPath := filepath.Join(dir, "genclient.py")
	if err := os.WriteFile(modPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(dir, "harness.py")
	if err := os.WriteFile(harnessPath, []byte(pyWSDeltaHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, harnessPath, modPath).CombinedOutput()
	if err != nil {
		t.Fatalf("python harness failed: %v\n%s\n--- generated module ---\n%s", err, out, buf.String())
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("harness did not report OK:\n%s", out)
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
