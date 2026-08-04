// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Pure-logic tests for the widget-centric PyVCP server: XML→widget extraction,
// auto-name counter behaviour (must match the client), constraint clamping, and
// the event handler's accept/reject decisions (the untrusted-wire surface). None
// of these touch HAL — handleEvent's pin writes go through the nil-safe pinRef
// accessors, so widgets are built with pinless *pinRef stubs and only the
// server-authoritative value/state/index bookkeeping is asserted. HAL-backed
// behaviour (scan edge detection, timer accrual, unload teardown) lives in
// module_hal_test.go.
package pyvcpmodule

import (
	"math"
	"testing"
)

func TestToS32Saturates(t *testing.T) {
	cases := []struct {
		in   float64
		want int32
	}{
		{0, 0},
		{5.9, 5}, // truncates toward zero, like int32()
		{-5.9, -5},
		{math.NaN(), 0},
		{1e12, math.MaxInt32},  // would be implementation-defined via int32()
		{-1e12, math.MinInt32}, // ditto
		{math.Inf(1), math.MaxInt32},
		{math.Inf(-1), math.MinInt32},
		{math.MaxInt32, math.MaxInt32},
	}
	for _, c := range cases {
		if got := toS32(c.in); got != c.want {
			t.Errorf("toS32(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampNaNSentinel(t *testing.T) {
	// NaN min/max means "no limit"; a real limit of 0 must still clamp.
	noLimit := &widgetDef{min: math.NaN(), max: math.NaN()}
	if got := noLimit.clamp(-1e9); got != -1e9 {
		t.Errorf("no-limit clamp changed value: %v", got)
	}

	zeroFloor := &widgetDef{min: 0, max: math.NaN()}
	if got := zeroFloor.clamp(-5); got != 0 {
		t.Errorf("clamp(-5) with min=0 = %v, want 0 (0 is a real limit)", got)
	}

	bounded := &widgetDef{min: 0, max: 10}
	if got := bounded.clamp(-1); got != 0 {
		t.Errorf("clamp(-1) = %v, want 0", got)
	}
	if got := bounded.clamp(11); got != 10 {
		t.Errorf("clamp(11) = %v, want 10", got)
	}
	if got := bounded.clamp(5); got != 5 {
		t.Errorf("clamp(5) = %v, want 5", got)
	}
}

func TestQuantize(t *testing.T) {
	continuous := &widgetDef{resolution: 0}
	if got := continuous.quantize(3.14159); got != 3.14159 {
		t.Errorf("resolution 0 should be identity, got %v", got)
	}
	stepped := &widgetDef{resolution: 0.25}
	if got := stepped.quantize(1.1); got != 1.0 {
		t.Errorf("quantize(1.1, .25) = %v, want 1.0", got)
	}
	if got := stepped.quantize(1.2); got != 1.25 {
		t.Errorf("quantize(1.2, .25) = %v, want 1.25", got)
	}
}

func TestParseHelpers(t *testing.T) {
	if got := parseList("[a, b, c]"); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("parseList bracketed = %v", got)
	}
	if got := parseList("(1, 2)"); len(got) != 2 || got[1] != "2" {
		t.Errorf("parseList paren = %v", got)
	}
	if got := parseList(""); got != nil {
		t.Errorf("parseList empty = %v, want nil", got)
	}
	if got := parseList(`"x", 'y'`); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("parseList quoted = %v", got)
	}
	if v, ok := parseFloat("2.5"); !ok || v != 2.5 {
		t.Errorf("parseFloat 2.5 = %v,%v", v, ok)
	}
	if _, ok := parseFloat("notanum"); ok {
		t.Errorf("parseFloat should reject non-numeric")
	}
}

// TestAutoNameCountersMatchClient locks in the exact counter semantics the
// Python client must mirror: most widgets advance the counter only when halpin
// is empty, but scale/spinbox/dial/jogwheel advance it unconditionally (the
// server always computes an autoBase for param-pin naming). A regression here
// silently mis-numbers auto-named widgets and breaks the ID handshake.
func TestAutoNameCountersMatchClient(t *testing.T) {
	counters := map[string]int{}
	mk := func(elem string, attrs map[string]string) *widgetDef {
		if attrs == nil {
			attrs = map[string]string{}
		}
		return extractWidget(elem, map[string]string{}, attrs, counters)
	}

	// led / rectled share the "led" counter and only advance when auto-named.
	if w := mk("led", nil); w.id != "led.0" {
		t.Errorf("first led = %q, want led.0", w.id)
	}
	if w := mk("rectled", nil); w.id != "led.1" {
		t.Errorf("rectled shares led counter = %q, want led.1", w.id)
	}
	// Explicit halpin must NOT advance the only-when-empty counter.
	if w := mk("led", map[string]string{"halpin": "myled"}); w.id != "myled" {
		t.Errorf("explicit led = %q, want myled", w.id)
	}
	if w := mk("led", nil); w.id != "led.2" {
		t.Errorf("led after explicit = %q, want led.2 (counter unchanged by explicit)", w.id)
	}

	// number / u32 / s32 share the "number" counter.
	c2 := map[string]int{}
	if w := extractWidget("number", map[string]string{}, map[string]string{}, c2); w.id != "number.0" {
		t.Errorf("number = %q", w.id)
	}
	if w := extractWidget("u32", map[string]string{}, map[string]string{}, c2); w.id != "number.1" {
		t.Errorf("u32 shares number counter = %q, want number.1", w.id)
	}
	if w := extractWidget("s32", map[string]string{}, map[string]string{}, c2); w.id != "number.2" {
		t.Errorf("s32 shares number counter = %q, want number.2", w.id)
	}

	// scale advances the counter EVEN with an explicit halpin (unconditional
	// autoName) — the client must do the same.
	c3 := map[string]int{}
	if w := extractWidget("scale", map[string]string{}, map[string]string{"halpin": "myscale"}, c3); w.id != "myscale" {
		t.Errorf("explicit scale id = %q, want myscale", w.id)
	}
	if w := extractWidget("scale", map[string]string{}, map[string]string{}, c3); w.id != "scale.1" {
		t.Errorf("auto scale after explicit = %q, want scale.1 (counter advanced by explicit)", w.id)
	}
}

func TestExtractScaleDefaultsAndParamPin(t *testing.T) {
	counters := map[string]int{}
	w := extractWidget("scale", map[string]string{}, map[string]string{
		"halpin":    "s",
		"param_pin": "1",
	}, counters)
	if w.min != 0 || w.max != 10 || w.resolution != 1 {
		t.Errorf("scale defaults min/max/res = %v/%v/%v, want 0/10/1", w.min, w.max, w.resolution)
	}
	if _, ok := w.pins["param"]; !ok {
		t.Errorf("param_pin=1 should create a param pin")
	}
	if _, ok := w.pins["-f"]; !ok {
		t.Errorf("scale should have -f pin")
	}
	if _, ok := w.pins["-i"]; !ok {
		t.Errorf("scale should have -i pin")
	}
}

func TestExtractRadioChoices(t *testing.T) {
	counters := map[string]int{}
	w := extractWidget("radiobutton", map[string]string{}, map[string]string{
		"halpin":  "r",
		"choices": "[a, b, c]",
		"initval": "1",
	}, counters)
	if len(w.choices) != 3 {
		t.Fatalf("choices = %v", w.choices)
	}
	if w.index != 1 {
		t.Errorf("initval index = %d, want 1", w.index)
	}
	for i := 0; i < 3; i++ {
		if _, ok := w.pins["choice."+string(rune('0'+i))]; !ok {
			t.Errorf("missing choice pin %d", i)
		}
	}
}

// TestParamPinHalparamName guards the halparam regression: an explicit
// halparam="..." must name the param pin, falling back to "<autoBase>.param_pin"
// when absent.
func TestParamPinHalparamName(t *testing.T) {
	counters := map[string]int{}
	var p panel

	w := extractWidget("dial", map[string]string{}, map[string]string{
		"halpin": "dial-c-out", "param_pin": "1", "halparam": "dial-c-in",
	}, counters)
	if w.paramName != "dial-c-in" {
		t.Errorf("paramName = %q, want dial-c-in", w.paramName)
	}
	if got := p.pinName(w, "param"); got != "dial-c-in" {
		t.Errorf("param pin name with halparam = %q, want dial-c-in", got)
	}

	// No halparam → fall back to autoBase.param_pin (second dial → dial.1).
	w2 := extractWidget("dial", map[string]string{}, map[string]string{
		"halpin": "d2", "param_pin": "1",
	}, counters)
	if got := p.pinName(w2, "param"); got != "dial.1.param_pin" {
		t.Errorf("fallback param name = %q, want dial.1.param_pin", got)
	}

	// A dial ALWAYS has a param pin (Python pyvcp parity), even with no
	// param_pin attribute — the migration regressed this to opt-in.
	wBare := extractWidget("dial", map[string]string{}, map[string]string{"halpin": "d3"}, counters)
	if _, ok := wBare.pins["param"]; !ok {
		t.Errorf("dial must always have a param pin")
	}
}

// newTestWidget builds a widget with pinless *pinRef stubs for every role, so
// handleEvent runs its full logic (the nil-safe accessors no-op the writes).
func newTestWidget(wt widgetType, roles ...string) *widgetDef {
	w := &widgetDef{wtype: wt, min: math.NaN(), max: math.NaN(), pins: map[string]*pinRef{}}
	for _, r := range roles {
		w.pins[r] = &pinRef{}
	}
	return w
}

func TestHandleEventButton(t *testing.T) {
	w := newTestWidget(wtButton, "state")
	if !w.handleEvent(evPress, 0, 0, 0) || !w.state {
		t.Errorf("PRESS should set state true and accept")
	}
	if !w.handleEvent(evRelease, 0, 0, 0) || w.state {
		t.Errorf("RELEASE should clear state and accept")
	}
	if w.handleEvent(evToggle, 0, 0, 0) {
		t.Errorf("button should reject TOGGLE")
	}
}

func TestHandleEventRadioSelectBounds(t *testing.T) {
	w := newTestWidget(wtRadiobutton)
	w.choices = []string{"a", "b"}
	w.pins["choice.0"] = &pinRef{}
	w.pins["choice.1"] = &pinRef{}
	if !w.handleEvent(evSelect, 0, 0, 1) || w.index != 1 {
		t.Errorf("valid SELECT should accept and set index=1, got index=%d", w.index)
	}
	if w.handleEvent(evSelect, 0, 0, 2) {
		t.Errorf("out-of-range SELECT (2) must be rejected")
	}
	if w.handleEvent(evSelect, 0, 0, -1) {
		t.Errorf("negative SELECT must be rejected")
	}
}

func TestHandleEventScale(t *testing.T) {
	w := newTestWidget(wtScale, "-f", "-i")
	w.min, w.max, w.resolution = 0, 10, 1

	if w.handleEvent(evSet, math.NaN(), 0, 0) {
		t.Errorf("SET NaN must be rejected")
	}
	if !w.handleEvent(evSet, 20, 0, 0) || w.value != 10 {
		t.Errorf("SET 20 should clamp to max 10, got %v", w.value)
	}
	if w.handleEvent(evIncrement, 0, 0, 0) {
		t.Errorf("increment==0 must be rejected")
	}
	// resolution 0 → INCREMENT rejected.
	w.resolution = 0
	if w.handleEvent(evIncrement, 1, 1, 0) {
		t.Errorf("resolution==0 INCREMENT must be rejected")
	}
	// Fresh widget: INCREMENT by 3 steps of 2 from 0 → 6.
	w2 := newTestWidget(wtScale, "-f", "-i")
	w2.min, w2.max, w2.resolution = 0, 100, 2
	if !w2.handleEvent(evIncrement, 0, 3, 0) || w2.value != 6 {
		t.Errorf("INCREMENT 3*2 = %v, want 6", w2.value)
	}
}

func TestHandleEventSpinboxQuantize(t *testing.T) {
	w := newTestWidget(wtSpinbox, "value")
	w.min, w.max, w.resolution = 0, 100, 0.5
	if !w.handleEvent(evSet, 1.3, 0, 0) || w.value != 1.5 {
		t.Errorf("SET 1.3 with res .5 should quantize to 1.5, got %v", w.value)
	}
}

func TestHandleEventJogwheelIncrement(t *testing.T) {
	w := newTestWidget(wtJogwheel, "count")
	if !w.handleEvent(evIncrement, 0, 5, 0) || w.value != 5 {
		t.Errorf("jogwheel INCREMENT 5 from 0 = %v, want 5", w.value)
	}
	if w.handleEvent(evIncrement, 0, 0, 0) {
		t.Errorf("jogwheel increment==0 must be rejected")
	}
}

// TestHandleEventUnknownWidgetNoPanic exercises the nil-safe accessors: a widget
// whose expected pin is missing must not panic the controller on a wire event.
func TestHandleEventUnknownWidgetNoPanic(t *testing.T) {
	w := &widgetDef{wtype: wtButton, pins: map[string]*pinRef{}} // no "state" pin
	// w.pins["state"] is a nil *pinRef; writeBit must no-op, not panic.
	if !w.handleEvent(evPress, 0, 0, 0) {
		t.Errorf("PRESS should still be accepted")
	}
}
