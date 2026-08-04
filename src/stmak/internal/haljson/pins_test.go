//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package haljson

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits and
// a subsequent hal_init then fails — see pkg/hal's TestMain for the full
// rationale. Keeping one component alive lets each test create/exit its own.
func TestMain(m *testing.M) {
	keep, err := hal.NewComponent("haljson-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	// The module factory registers its REST API in the default registry. In
	// production both launcher entry points (Run, RunHalFile) install one
	// before any module loads; do the same here.
	apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// compCounter gives every test its own HAL component name. HAL component names
// are process-global and a name is only freed on Exit, so reusing one across
// tests would collide when a test fails before its deferred Exit runs.
var compCounter int

// newTestComp creates a HAL component with a unique name, registered for exit at
// test end.
func newTestComp(t *testing.T) *hal.Component {
	t.Helper()
	compCounter++
	comp, err := hal.NewComponent(fmt.Sprintf("hjt%d", compCounter))
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	t.Cleanup(func() { _ = comp.Exit() })
	return comp
}

// buildRoots parses an XML config, creates its HAL pins on a fresh component and
// returns both. This is the same sequence newHaljsonModule runs, minus the
// registry wiring.
func buildRoots(t *testing.T, cfg string) ([]*jsonRoot, *hal.Component) {
	t.Helper()
	roots, err := parseConfig(writeTempConfig(t, cfg), nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	comp := newTestComp(t)
	for _, r := range roots {
		if err := r.createPins(comp); err != nil {
			t.Fatalf("createPins: %v", err)
		}
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return roots, comp
}

// decode unmarshals a JSON snapshot into a generic map.
func decode(t *testing.T, data json.RawMessage) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return obj
}

// findPin locates a pin item by its dotted path within a root (array elements
// are addressed by their numeric child name, e.g. "arr.0.v").
func findPin(t *testing.T, root *jsonRoot, path string) *jsonItem {
	t.Helper()
	for _, wp := range flattenPins(root.items, "") {
		if wp.path == path {
			return wp.item
		}
	}
	// flattenPins renders arrays as "name[i]"; fall back to a direct walk so
	// callers can use either spelling.
	t.Fatalf("pin %q not found in root %q", path, root.path)
	return nil
}

// The config used by most tests: one of every supported type and direction,
// plus a nested object and a templated array.
const mixedConfig = `<halJson>
  <halJsonRoot path="panel">
    <halJsonPin name="b" type="bit" dir="io"/>
    <halJsonPin name="f" type="float" dir="io"/>
    <halJsonPin name="s" type="s32" dir="io"/>
    <halJsonPin name="u" type="u32" dir="io"/>
    <halJsonPin name="ro" type="float" dir="in"/>
    <halJsonObject name="nested">
      <halJsonPin name="inner" type="bit" dir="out"/>
    </halJsonObject>
    <halJsonArray name="ax" size="2">
      <halJsonPin name="pos" type="float" dir="io"/>
    </halJsonArray>
  </halJsonRoot>
</halJson>`

// TestCreatePinsExportsHALPins verifies createPins exports one HAL pin per leaf,
// under the "<comp>.<rootPath>.<item>" name the REST/WS paths are derived from,
// and that arrays expand to "<name>-<index>" element prefixes.
func TestCreatePinsExportsHALPins(t *testing.T) {
	_, comp := buildRoots(t, mixedConfig)

	want := []string{
		"panel.b", "panel.f", "panel.s", "panel.u", "panel.ro",
		"panel.nested.inner",
		"panel.ax-0.pos", "panel.ax-1.pos",
	}
	for _, suffix := range want {
		name := comp.Name() + "." + suffix
		if _, ok := hal.LookupValue(name); !ok {
			t.Errorf("HAL pin %q not exported", name)
		}
	}
	// A name that must NOT exist: the array template itself is not a pin.
	if _, ok := hal.LookupValue(comp.Name() + ".panel.ax.pos"); ok {
		t.Error("array template must not create a bare (unindexed) pin")
	}
}

// TestBuildJSONShape verifies the read snapshot mirrors the config tree: scalars
// at the leaves, a nested object, and an array of objects in index order.
func TestBuildJSONShape(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	obj := decode(t, roots[0].buildJSON())

	for _, k := range []string{"b", "f", "s", "u", "ro", "nested", "ax"} {
		if _, ok := obj[k]; !ok {
			t.Fatalf("key %q missing from snapshot %v", k, obj)
		}
	}
	if b, ok := obj["b"].(bool); !ok || b {
		t.Errorf("b = %v (%T); want false", obj["b"], obj["b"])
	}
	nested, ok := obj["nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("nested = %T; want object", obj["nested"])
	}
	if _, ok := nested["inner"]; !ok {
		t.Errorf("nested.inner missing: %v", nested)
	}
	arr, ok := obj["ax"].([]interface{})
	if !ok || len(arr) != 2 {
		t.Fatalf("ax = %v (%T); want a 2-element array", obj["ax"], obj["ax"])
	}
	for i, elem := range arr {
		e, ok := elem.(map[string]interface{})
		if !ok {
			t.Fatalf("ax[%d] = %T; want object", i, elem)
		}
		if _, ok := e["pos"]; !ok {
			t.Errorf("ax[%d].pos missing: %v", i, e)
		}
	}
}

// TestApplyJSONRoundTrip is the core contract of the module: a POST body written
// through applyJSON must land on the HAL pins and read back identically from
// buildJSON, for every supported type including a nested object and an array
// element.
func TestApplyJSONRoundTrip(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]

	const body = `{
	  "b": true,
	  "f": -2.5,
	  "s": -7,
	  "u": 4000000000,
	  "nested": {"inner": true},
	  "ax": [{"pos": 1.5}, {"pos": 2.5}]
	}`
	if err := root.applyJSON(json.RawMessage(body)); err != nil {
		t.Fatalf("applyJSON: %v", err)
	}

	obj := decode(t, root.buildJSON())
	if obj["b"] != true {
		t.Errorf("b = %v; want true", obj["b"])
	}
	if obj["f"] != -2.5 {
		t.Errorf("f = %v; want -2.5", obj["f"])
	}
	if obj["s"] != float64(-7) {
		t.Errorf("s = %v; want -7", obj["s"])
	}
	// u32 must survive the top half of the range (a signed round-trip would
	// wrap this to a negative value).
	if obj["u"] != float64(4000000000) {
		t.Errorf("u = %v; want 4000000000", obj["u"])
	}
	nested := obj["nested"].(map[string]interface{})
	if nested["inner"] != true {
		t.Errorf("nested.inner = %v; want true", nested["inner"])
	}
	arr := obj["ax"].([]interface{})
	for i, want := range []float64{1.5, 2.5} {
		got := arr[i].(map[string]interface{})["pos"]
		if got != want {
			t.Errorf("ax[%d].pos = %v; want %v", i, got, want)
		}
	}
}

// TestApplyJSONIgnoresInputPins asserts the direction guard: a client POST must
// not be able to drive a dir="in" pin (that pin is owned by whatever HAL signal
// is linked to it).
func TestApplyJSONIgnoresInputPins(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]

	// Seed the input pin from the HAL side.
	findPin(t, root, "ro").fltPin.Set(42)

	if err := root.applyJSON(json.RawMessage(`{"ro": 99}`)); err != nil {
		t.Fatalf("applyJSON: %v", err)
	}
	if got := decode(t, root.buildJSON())["ro"]; got != float64(42) {
		t.Errorf("ro = %v; want 42 (write to an input pin must be ignored)", got)
	}
}

// TestApplyJSONPartialAndUnknownKeys verifies a POST need not carry the whole
// tree: absent keys are left untouched and unknown keys are ignored rather than
// rejected.
func TestApplyJSONPartialAndUnknownKeys(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]

	if err := root.applyJSON(json.RawMessage(`{"f": 1.25}`)); err != nil {
		t.Fatalf("applyJSON: %v", err)
	}
	if err := root.applyJSON(json.RawMessage(`{"s": 3, "nosuchpin": 1}`)); err != nil {
		t.Fatalf("applyJSON with unknown key: %v", err)
	}
	obj := decode(t, root.buildJSON())
	if obj["f"] != 1.25 {
		t.Errorf("f = %v; want 1.25 (untouched by the second POST)", obj["f"])
	}
	if obj["s"] != float64(3) {
		t.Errorf("s = %v; want 3", obj["s"])
	}
}

// TestApplyJSONShortArrayAndOverlongArray covers the array bounds in
// applyItemsJSON: fewer elements than pins updates only the prefix, and extra
// elements are dropped instead of indexing past the expanded children.
func TestApplyJSONShortArrayAndOverlongArray(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]

	if err := root.applyJSON(json.RawMessage(`{"ax": [{"pos": 7}]}`)); err != nil {
		t.Fatalf("applyJSON short array: %v", err)
	}
	arr := decode(t, root.buildJSON())["ax"].([]interface{})
	if got := arr[0].(map[string]interface{})["pos"]; got != float64(7) {
		t.Errorf("ax[0].pos = %v; want 7", got)
	}
	if got := arr[1].(map[string]interface{})["pos"]; got != float64(0) {
		t.Errorf("ax[1].pos = %v; want 0 (not written by a short array)", got)
	}

	// More elements than pins must be dropped, not panic.
	if err := root.applyJSON(json.RawMessage(`{"ax": [{"pos": 1}, {"pos": 2}, {"pos": 3}]}`)); err != nil {
		t.Fatalf("applyJSON overlong array: %v", err)
	}
	arr = decode(t, root.buildJSON())["ax"].([]interface{})
	if len(arr) != 2 {
		t.Fatalf("ax has %d elements; want 2", len(arr))
	}
}

// TestApplyJSONErrors covers the malformed-payload paths: these run on an
// untrusted REST/WS body, so each must surface as an error rather than a panic
// or a silent partial write.
func TestApplyJSONErrors(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]

	for _, tc := range []struct {
		name string
		body string
	}{
		{"not an object", `[1,2,3]`},
		{"truncated", `{"f":`},
		{"wrong scalar type", `{"f": "hello"}`},
		{"bool for s32", `{"s": true}`},
		{"negative for u32", `{"u": -1}`},
		{"object is a scalar", `{"nested": 5}`},
		{"array is an object", `{"ax": {"0": {"pos": 1}}}`},
		{"array element is a scalar", `{"ax": [3]}`},
		{"nested child type mismatch", `{"nested": {"inner": 7}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := root.applyJSON(json.RawMessage(tc.body)); err == nil {
				t.Errorf("applyJSON(%s) = nil; want an error", tc.body)
			}
		})
	}
}

// TestRESTMetaGetAndPost drives the same dispatch functions the REST server
// calls, confirming GET returns the snapshot and POST applies the body and
// reports the in-band error for a bad one.
func TestRESTMetaGetAndPost(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	meta := buildRESTMeta("panelapi", roots)

	if meta.Name != "panelapi" || !meta.RESTExport {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	byName := map[string]int{}
	for i, f := range meta.Funcs {
		byName[f.Name] = i
	}
	get, ok := byName["get_panel"]
	if !ok {
		t.Fatalf("no get_panel func in %v", byName)
	}
	set, ok := byName["set_panel"]
	if !ok {
		t.Fatalf("no set_panel func in %v", byName)
	}
	if m := meta.Funcs[get].Method; m != "GET" {
		t.Errorf("get_panel method = %q; want GET", m)
	}
	if m := meta.Funcs[set].Method; m != "POST" {
		t.Errorf("set_panel method = %q; want POST", m)
	}

	if _, err := meta.Funcs[set].Dispatch(nil, []byte(`{"f": 3.5}`)); err != nil {
		t.Fatalf("POST dispatch: %v", err)
	}
	body, err := meta.Funcs[get].Dispatch(nil, nil)
	if err != nil {
		t.Fatalf("GET dispatch: %v", err)
	}
	if got := decode(t, body)["f"]; got != 3.5 {
		t.Errorf("f = %v after POST; want 3.5", got)
	}
	if _, err := meta.Funcs[set].Dispatch(nil, []byte(`{"f":`)); err == nil {
		t.Error("POST with a malformed body must return an error")
	}
}

// TestMultipleRootsAreIndependent checks that two roots in one config keep
// separate pin trees and separate REST endpoints.
func TestMultipleRootsAreIndependent(t *testing.T) {
	const cfg = `<halJson>
  <halJsonRoot path="one">
    <halJsonPin name="v" type="s32" dir="io"/>
  </halJsonRoot>
  <halJsonRoot path="two">
    <halJsonPin name="v" type="s32" dir="io"/>
  </halJsonRoot>
</halJson>`
	roots, comp := buildRoots(t, cfg)
	if len(roots) != 2 {
		t.Fatalf("got %d roots; want 2", len(roots))
	}
	for _, suffix := range []string{"one.v", "two.v"} {
		if _, ok := hal.LookupValue(comp.Name() + "." + suffix); !ok {
			t.Errorf("HAL pin %q not exported", comp.Name()+"."+suffix)
		}
	}
	if err := roots[0].applyJSON(json.RawMessage(`{"v": 11}`)); err != nil {
		t.Fatalf("applyJSON: %v", err)
	}
	if got := decode(t, roots[0].buildJSON())["v"]; got != float64(11) {
		t.Errorf("one.v = %v; want 11", got)
	}
	if got := decode(t, roots[1].buildJSON())["v"]; got != float64(0) {
		t.Errorf("two.v = %v; want 0 (roots must not share pins)", got)
	}
}

// TestCreatePinsDuplicateNameFails covers the createPin error path and its
// propagation out of a nested object and an array element. A config that names
// the same HAL pin twice must fail the module load rather than leave a
// half-exported component behind.
func TestCreatePinsDuplicateNameFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"top level", `
      <halJsonPin name="dup" type="bit" dir="in"/>
      <halJsonPin name="dup" type="bit" dir="in"/>`},
		{"inside an object", `
      <halJsonObject name="o">
        <halJsonPin name="dup" type="bit" dir="in"/>
        <halJsonPin name="dup" type="bit" dir="in"/>
      </halJsonObject>`},
		{"inside an array element", `
      <halJsonArray name="a" size="2">
        <halJsonPin name="dup" type="bit" dir="in"/>
        <halJsonPin name="dup" type="bit" dir="in"/>
      </halJsonArray>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := `<halJson><halJsonRoot path="pins">` + tc.body + `</halJsonRoot></halJson>`
			roots, err := parseConfig(writeTempConfig(t, cfg), nil)
			if err != nil {
				t.Fatalf("parseConfig: %v", err)
			}
			if err := roots[0].createPins(newTestComp(t)); err == nil {
				t.Error("createPins with a duplicate pin name = nil error; want a rejection")
			}
		})
	}
}

// TestModuleLifecycle drives the stmak.Module surface. Start/Stop are no-ops
// (haljson has no goroutine of its own — the watch scheduler is the
// apiserver's), and Destroy must release the HAL component so the instance name
// is free for a reload.
func TestModuleLifecycle(t *testing.T) {
	comp := newTestComp(t)
	name := comp.Name()
	m := &haljsonModule{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), comp: comp}

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	m.Destroy()

	// The name is only freed HAL-side if Destroy really exited the component.
	comp2, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("Destroy did not release the HAL component: %v", err)
	}
	_ = comp2.Exit()

	// Destroy on a module that never got a component must not panic — the
	// unload path runs it regardless of how far the load got.
	(&haljsonModule{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Destroy()
}

// TestNewHaljsonModule drives the real module factory end-to-end: config
// resolution through pathres, XML parse, HAL pin export, and registration of
// the REST + watch APIs. This is the path a `load haljson config=...` line
// takes, so it is the one that must not silently half-succeed.
func TestNewHaljsonModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "panel.xml"), []byte(mixedConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	pathres.SetDefaultForTest(t, dir)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A unique instance per invocation: the REST registration outlives
	// Destroy (unregistering is the launcher's unregisterModuleAPIs step, not
	// the module's), so a re-run under -count=N would otherwise hit EEXIST.
	compCounter++
	inst := fmt.Sprintf("hjmod%d", compCounter)
	mod, err := newHaljsonModule(nil, logger, inst, []string{"config=panel.xml", "rate=25"})
	if err != nil {
		t.Fatalf("newHaljsonModule: %v", err)
	}
	t.Cleanup(mod.Destroy)

	// The HAL pins are live under the instance name.
	if _, ok := hal.LookupValue(inst + ".panel.f"); !ok {
		t.Errorf("HAL pin %q not exported by the module factory", inst+".panel.f")
	}

	// The watch API is registered with the requested rate and a per-connection
	// factory (not a shared WatchFunc — see TestWatchPerConnectionState).
	api := apiserver.DefaultWatchRegistry().Get(inst, inst)
	if api == nil {
		t.Fatal("watch API not registered")
	}
	if len(api.Watches) != 1 || api.Watches[0].Name != "panel" {
		t.Fatalf("unexpected watches: %+v", api.Watches)
	}
	if got := api.Watches[0].DefaultRate; got != 25*time.Millisecond {
		t.Errorf("DefaultRate = %v; want 25ms", got)
	}
	if api.Watches[0].Factory == nil {
		t.Error("watch must be registered as a per-connection Factory")
	}

	// The write command applies to the pins.
	if len(api.Commands) != 1 || api.Commands[0].Name != "panel" {
		t.Fatalf("unexpected commands: %+v", api.Commands)
	}
	if _, err := api.Commands[0].Handler(json.RawMessage(`{"f": 6.25}`)); err != nil {
		t.Fatalf("command handler: %v", err)
	}
	if v, ok := hal.LookupValue(inst + ".panel.f"); !ok || v != 6.25 {
		t.Errorf("panel.f = %v, %v after the command; want 6.25", v, ok)
	}
	if _, err := api.Commands[0].Handler(json.RawMessage(`{"f":`)); err == nil {
		t.Error("command handler must reject a malformed body")
	}
}

// TestNewHaljsonModuleErrors covers the load-time rejections: no config=, a
// config path that does not resolve, and a config file that fails to parse.
// Each must return an error rather than a half-built module.
func TestNewHaljsonModuleErrors(t *testing.T) {
	// Lay the config root and the escape target out as known siblings. The
	// escaping case used to be "config=../../etc/passwd" from a temp dir, which
	// reaches nothing — so it failed with "not found" and would have passed
	// with containment deleted outright, making it a duplicate of the
	// unresolvable case below it. The target here is real, reachable by the
	// relative path, and *parseable*: if containment stopped working the module
	// would build and this test would fail loudly.
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.xml"), []byte(`<halJson></halJson>`), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "escape.xml"), []byte(`<halJson></halJson>`), 0o644); err != nil {
		t.Fatalf("writing the out-of-root config: %v", err)
	}
	pathres.SetDefaultForTest(t, dir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for i, tc := range []struct {
		name string
		args []string
		// wantReason, when set, must appear in the error: an "any error will
		// do" assertion cannot tell a containment refusal from a typo.
		wantReason string
	}{
		{name: "missing config=", args: []string{"rate=10"}},
		{name: "unresolvable config", args: []string{"config=nosuchfile.xml"}},
		{
			name:       "config escaping the roots",
			args:       []string{"config=../outside/escape.xml"},
			wantReason: "outside the allowed directories",
		},
		{name: "unparsable config", args: []string{"config=bad.xml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, err := newHaljsonModule(nil, logger, fmt.Sprintf("hjerr%d", i), tc.args)
			if err == nil {
				t.Error("want an error, got a module")
				mod.Destroy()
				return
			}
			if tc.wantReason != "" && !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("refused with %q, want the reason %q — the target exists and is "+
					"parseable, so any other reason means containment was not what stopped it",
					err, tc.wantReason)
			}
		})
	}
}
