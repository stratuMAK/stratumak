//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package haljson

import (
	"encoding/json"
	"math"
	"testing"
)

// tick calls a WatchFunc and decodes its result, reporting whether the tick was
// suppressed (nil payload = nothing changed).
func tick(t *testing.T, fn func() (json.RawMessage, error)) (map[string]interface{}, bool) {
	t.Helper()
	data, err := fn()
	if err != nil {
		t.Fatalf("watch tick: %v", err)
	}
	if data == nil {
		return nil, false
	}
	return decode(t, data), true
}

// TestFlattenPinsPaths pins the WS delta key format: dotted for nested objects,
// bracketed index for arrays. Clients key their bindings off these strings, so
// the spelling is part of the wire contract.
func TestFlattenPinsPaths(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)

	var got []string
	for _, wp := range flattenPins(roots[0].items, "") {
		got = append(got, wp.path)
	}
	want := []string{"b", "f", "s", "u", "ro", "nested.inner", "ax[0].pos", "ax[1].pos"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q; want %q", i, got[i], want[i])
		}
	}
}

// TestWatchFirstTickIsFullSnapshot verifies the first tick sends the whole
// structured tree (so a fresh subscriber has complete state) and that a
// subsequent unchanged tick is suppressed entirely.
//
// Regression: the first tick used to return the snapshot without priming the
// shadows (which were pre-set to an impossible value), so the very next poll
// re-sent every pin as a flat delta — a duplicate full send on every subscribe.
func TestWatchFirstTickIsFullSnapshot(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	fn, err := newWatchFactory(roots[0])(nil)
	if err != nil {
		t.Fatalf("watch factory: %v", err)
	}

	first, ok := tick(t, fn)
	if !ok {
		t.Fatal("first tick was suppressed; want a full snapshot")
	}
	// Structured, not flat: the nested object is an object, not a "nested.inner" key.
	if _, ok := first["nested"].(map[string]interface{}); !ok {
		t.Errorf("first tick is not the structured snapshot: %v", first)
	}
	if _, ok := first["ax"].([]interface{}); !ok {
		t.Errorf("first tick array missing/!array: %v", first)
	}

	if _, ok := tick(t, fn); ok {
		t.Error("second tick with no pin change must be suppressed")
	}
}

// TestWatchDeltaOnlyChangedPins is the point of the shadow state: after the
// first snapshot, a tick carries only the pins that actually moved, keyed by
// their flat path.
func TestWatchDeltaOnlyChangedPins(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]
	fn, err := newWatchFactory(root)(nil)
	if err != nil {
		t.Fatalf("watch factory: %v", err)
	}
	if _, ok := tick(t, fn); !ok {
		t.Fatal("first tick was suppressed")
	}

	findPin(t, root, "f").fltPin.Set(1.5)
	findPin(t, root, "ax[1].pos").fltPin.Set(-3)

	delta, ok := tick(t, fn)
	if !ok {
		t.Fatal("tick after a pin change was suppressed")
	}
	if len(delta) != 2 {
		t.Fatalf("delta = %v; want exactly the 2 changed pins", delta)
	}
	if delta["f"] != 1.5 {
		t.Errorf("delta[f] = %v; want 1.5", delta["f"])
	}
	if delta["ax[1].pos"] != float64(-3) {
		t.Errorf("delta[ax[1].pos] = %v; want -3", delta["ax[1].pos"])
	}

	// The shadow is now up to date — re-reading must suppress again.
	if _, ok := tick(t, fn); ok {
		t.Error("tick after the delta was sent must be suppressed")
	}
}

// TestWatchDetectsChangePerType exercises readPinRaw for every pin type,
// including the two that a naive float/int comparison gets wrong: a u32 above
// the int32 range and a float whose change is only in the mantissa.
func TestWatchDetectsChangePerType(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]
	fn, err := newWatchFactory(root)(nil)
	if err != nil {
		t.Fatalf("watch factory: %v", err)
	}
	if _, ok := tick(t, fn); !ok {
		t.Fatal("first tick was suppressed")
	}

	findPin(t, root, "b").bitPin.Set(true)
	findPin(t, root, "s").s32Pin.Set(math.MinInt32)
	findPin(t, root, "u").u32Pin.Set(math.MaxUint32)
	findPin(t, root, "f").fltPin.Set(math.SmallestNonzeroFloat64)
	findPin(t, root, "nested.inner").bitPin.Set(true)

	delta, ok := tick(t, fn)
	if !ok {
		t.Fatal("tick after changes was suppressed")
	}
	for k, want := range map[string]interface{}{
		"b":            true,
		"s":            float64(math.MinInt32),
		"u":            float64(math.MaxUint32),
		"f":            math.SmallestNonzeroFloat64,
		"nested.inner": true,
	} {
		if delta[k] != want {
			t.Errorf("delta[%s] = %v; want %v", k, delta[k], want)
		}
	}
}

// TestWatchRevertedValueIsSuppressed: the shadow compares against the last
// *sent* value, so a pin that moves and comes back within one poll interval
// produces no delta. This is the intended behaviour (the client's state is
// already correct) and is worth pinning so a future refactor to edge-counting
// does not change it silently.
func TestWatchRevertedValueIsSuppressed(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]
	fn, err := newWatchFactory(root)(nil)
	if err != nil {
		t.Fatalf("watch factory: %v", err)
	}
	if _, ok := tick(t, fn); !ok {
		t.Fatal("first tick was suppressed")
	}

	pin := findPin(t, root, "f").fltPin
	pin.Set(5)
	pin.Set(0)
	if _, ok := tick(t, fn); ok {
		t.Error("a pin that returned to its shadow value must not emit a delta")
	}
}

// TestWatchPerConnectionState is the reason the API is a Factory rather than a
// shared WatchFunc: every subscriber must get its own first-full-snapshot and
// its own shadows, so one connection's poll cannot consume another's change.
func TestWatchPerConnectionState(t *testing.T) {
	roots, _ := buildRoots(t, mixedConfig)
	root := roots[0]
	factory := newWatchFactory(root)

	a, err := factory(nil)
	if err != nil {
		t.Fatalf("factory (a): %v", err)
	}
	if _, ok := tick(t, a); !ok {
		t.Fatal("a: first tick suppressed")
	}

	findPin(t, root, "s").s32Pin.Set(9)

	// A second subscriber attaches after the change: it must still get a full
	// snapshot carrying the current value.
	b, err := factory(nil)
	if err != nil {
		t.Fatalf("factory (b): %v", err)
	}
	snap, ok := tick(t, b)
	if !ok {
		t.Fatal("b: first tick suppressed")
	}
	if snap["s"] != float64(9) {
		t.Errorf("b snapshot s = %v; want 9", snap["s"])
	}

	// b's poll must not have consumed the change for a.
	delta, ok := tick(t, a)
	if !ok {
		t.Fatal("a: change was consumed by the other subscriber")
	}
	if delta["s"] != float64(9) {
		t.Errorf("a delta s = %v; want 9", delta["s"])
	}
}

// TestWatchEmptyRoot covers a root with no pins at all: the first tick is an
// empty object and every later tick is suppressed, with no nil-slice panic.
func TestWatchEmptyRoot(t *testing.T) {
	roots, _ := buildRoots(t, `<halJson><halJsonRoot path="empty"/></halJson>`)
	fn, err := newWatchFactory(roots[0])(nil)
	if err != nil {
		t.Fatalf("watch factory: %v", err)
	}
	first, ok := tick(t, fn)
	if !ok {
		t.Fatal("first tick suppressed")
	}
	if len(first) != 0 {
		t.Errorf("first tick = %v; want an empty object", first)
	}
	if _, ok := tick(t, fn); ok {
		t.Error("empty root must suppress every tick after the first")
	}
}
