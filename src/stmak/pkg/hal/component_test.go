// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal_test

import (
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/hallib/halnettest"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestComponentLifecycle covers the Component Ready/Exit lifecycle and the
// accessor state around it: NewComponent starts not-ready, Ready() flips
// IsReady and is not idempotent (a second Ready returns an error), and the
// accessors (Name/ID/String) report consistent state.
func TestComponentLifecycle(t *testing.T) {
	comp, err := hal.NewComponent("test-lifecycle")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	if comp.Name() != "test-lifecycle" {
		t.Errorf("Name: got %q, want %q", comp.Name(), "test-lifecycle")
	}
	if comp.ID() <= 0 {
		t.Errorf("ID: got %d, want a positive HAL component id", comp.ID())
	}
	if comp.IsReady() {
		t.Error("IsReady: got true before Ready(), want false")
	}

	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if !comp.IsReady() {
		t.Error("IsReady: got false after Ready(), want true")
	}

	// Ready() is not idempotent: hal_ready() on an already-ready component
	// errors, and the wrapper guards against the double call.
	if err := comp.Ready(); err == nil {
		t.Error("second Ready(): got nil, want ErrAlreadyReady")
	}
	// A failed second Ready() must not clear the ready state.
	if !comp.IsReady() {
		t.Error("IsReady: got false after a rejected second Ready(), want true")
	}

	if got := comp.String(); got == "" {
		t.Error("String: got empty, want a non-empty representation")
	}
}

// TestComponentExit verifies Exit() releases the component cleanly. It uses a
// dedicated component (not the keep-alive) so the count never drops to zero
// (see TestMain on why a full HAL teardown cannot be re-initialised).
func TestComponentExit(t *testing.T) {
	comp, err := hal.NewComponent("test-exit")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	if _, err := hal.NewPin[float64](comp, "p", hal.Out); err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if err := comp.Exit(); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	// After Exit the name is freed HAL-side; a fresh component may reuse it.
	comp2, err := hal.NewComponent("test-exit")
	if err != nil {
		t.Fatalf("NewComponent after Exit (name should be free): %v", err)
	}
	_ = comp2.Exit()
}

// TestLookupValue exercises the by-name value lookup across the pin, signal, and
// not-found paths. LookupValue walks pin -> signal -> param and returns the
// value as float64, or (0, false) when the name resolves to nothing.
func TestLookupValue(t *testing.T) {
	comp, err := hal.NewComponent("test-lookup")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	fp, err := hal.NewPin[float64](comp, "speed", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[float64]: %v", err)
	}
	sp, err := hal.NewPin[int32](comp, "count", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[int32]: %v", err)
	}
	bp, err := hal.NewPin[bool](comp, "enable", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[bool]: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	fp.Set(12.5)
	sp.Set(-7)
	bp.Set(true)

	if v, ok := hal.LookupValue("test-lookup.speed"); !ok || v != 12.5 {
		t.Errorf("LookupValue(float pin): got (%v, %v), want (12.5, true)", v, ok)
	}
	if v, ok := hal.LookupValue("test-lookup.count"); !ok || v != -7 {
		t.Errorf("LookupValue(s32 pin): got (%v, %v), want (-7, true)", v, ok)
	}
	if v, ok := hal.LookupValue("test-lookup.enable"); !ok || v != 1 {
		t.Errorf("LookupValue(bool pin): got (%v, %v), want (1, true)", v, ok)
	}

	// A signal value is reachable by the same lookup (second search arm).
	if err := halnettest.NewSignal("test-lookup-sig", int(hal.TypeFloat)); err != nil {
		t.Fatalf("NewSignal: %v", err)
	}
	if err := halnettest.Link("test-lookup.speed", "test-lookup-sig"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	fp.Set(3.25)
	if v, ok := hal.LookupValue("test-lookup-sig"); !ok || v != 3.25 {
		t.Errorf("LookupValue(signal): got (%v, %v), want (3.25, true)", v, ok)
	}

	// An unknown name resolves to nothing.
	if v, ok := hal.LookupValue("test-lookup.nonexistent"); ok || v != 0 {
		t.Errorf("LookupValue(unknown): got (%v, %v), want (0, false)", v, ok)
	}
}

// TestPinLinkedScalarRoundTrip nets an output pin and an input pin to the same
// scalar signal and verifies a value written on the writer is read on the
// reader. This is the reason Get/Set dereference the HAL pointer at access time:
// hal_link repoints each pin's data-pointer slot at the signal's data cell, so a
// pin that cached the pre-link cell would read stale data.
func TestPinLinkedScalarRoundTrip(t *testing.T) {
	wc, err := hal.NewComponent("test-link-w")
	if err != nil {
		t.Fatalf("NewComponent(writer): %v", err)
	}
	defer func() { _ = wc.Exit() }()
	rc, err := hal.NewComponent("test-link-r")
	if err != nil {
		t.Fatalf("NewComponent(reader): %v", err)
	}
	defer func() { _ = rc.Exit() }()

	out, err := hal.NewPin[float64](wc, "out", hal.Out)
	if err != nil {
		t.Fatalf("NewPin(out): %v", err)
	}
	in, err := hal.NewPin[float64](rc, "in", hal.In)
	if err != nil {
		t.Fatalf("NewPin(in): %v", err)
	}
	if err := wc.Ready(); err != nil {
		t.Fatalf("Ready(writer): %v", err)
	}
	if err := rc.Ready(); err != nil {
		t.Fatalf("Ready(reader): %v", err)
	}

	if err := halnettest.NewSignal("test-link-sig", int(hal.TypeFloat)); err != nil {
		t.Fatalf("NewSignal: %v", err)
	}
	if err := halnettest.Link("test-link-w.out", "test-link-sig"); err != nil {
		t.Fatalf("Link(out): %v", err)
	}
	if err := halnettest.Link("test-link-r.in", "test-link-sig"); err != nil {
		t.Fatalf("Link(in): %v", err)
	}

	out.Set(42.5)
	if got := in.Get(); got != 42.5 {
		t.Errorf("linked scalar: reader got %v, want 42.5 (did the pin follow the netted cell?)", got)
	}
	out.Set(-1.0)
	if got := in.Get(); got != -1.0 {
		t.Errorf("linked scalar (second write): reader got %v, want -1.0", got)
	}
}

// TestPinLinkedPortRoundTrip nets an output string pin and an input string pin
// to the same HAL_PORT signal sized with a real buffer, then verifies a string
// written on the writer is read back on the reader through the length-prefix
// framing. Unlike the unlinked case (covered in pin_test.go, where Set drops and
// Get returns ""), this exercises the full framed write/peek path over a backing
// buffer that belongs to the signal.
func TestPinLinkedPortRoundTrip(t *testing.T) {
	wc, err := hal.NewComponent("test-port-w")
	if err != nil {
		t.Fatalf("NewComponent(writer): %v", err)
	}
	defer func() { _ = wc.Exit() }()
	rc, err := hal.NewComponent("test-port-r")
	if err != nil {
		t.Fatalf("NewComponent(reader): %v", err)
	}
	defer func() { _ = rc.Exit() }()

	out, err := hal.NewPin[string](wc, "out", hal.Out)
	if err != nil {
		t.Fatalf("NewPin(out): %v", err)
	}
	in, err := hal.NewPin[string](rc, "in", hal.In)
	if err != nil {
		t.Fatalf("NewPin(in): %v", err)
	}
	if err := wc.Ready(); err != nil {
		t.Fatalf("Ready(writer): %v", err)
	}
	if err := rc.Ready(); err != nil {
		t.Fatalf("Ready(reader): %v", err)
	}

	if err := halnettest.NewSignal("test-port-sig", int(hal.TypePort)); err != nil {
		t.Fatalf("NewSignal(port): %v", err)
	}
	if err := halnettest.AllocPortSignal("test-port-sig", 256); err != nil {
		t.Fatalf("AllocPortSignal: %v", err)
	}
	// Writer (output) first, then the single reader (input).
	if err := halnettest.Link("test-port-w.out", "test-port-sig"); err != nil {
		t.Fatalf("Link(out): %v", err)
	}
	if err := halnettest.Link("test-port-r.in", "test-port-sig"); err != nil {
		t.Fatalf("Link(in): %v", err)
	}

	// Now that the port pin is backed by the signal's buffer, TrySet succeeds
	// and the framed value round-trips to the reader.
	if err := out.TrySet("hello, port"); err != nil {
		t.Fatalf("TrySet on linked port pin: %v", err)
	}
	if got := in.Get(); got != "hello, port" {
		t.Errorf("linked port: reader got %q, want %q", got, "hello, port")
	}

	// Latest-value semantics: a second write clears and reframes.
	if err := out.TrySet("second"); err != nil {
		t.Fatalf("TrySet (second): %v", err)
	}
	if got := in.Get(); got != "second" {
		t.Errorf("linked port (second write): reader got %q, want %q", got, "second")
	}
}
