// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// HAL-backed behavioural tests for the PyVCP module. These exercise the parts
// that only mean anything against real HAL pins: the periodic scan (changepin
// edge toggle, jogwheel clear, param_pin override), the server-authoritative
// timer accrual, and — most importantly — the unload teardown that closes the
// use-after-free window (Destroy must mark the panel closed under mu so an
// already-open watch pushLoop stops reading pins before the component is freed).
//
// Input pins are driven with Set(): with no signal netted to them, the local
// shadow persists, so scan() reads back what the test wrote. HAL names are
// process-global and only freed on component Exit, so every test uses uniq()
// names and exits its component.
package pyvcpmodule

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/pyvcp"
	"github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary;
// the in-process HAL data segment is torn down when the last component exits and
// cannot be re-initialised (see pkg/hal's TestMain). We deliberately do NOT pull
// in internal/halcmd (RtapiInitializeApp) — a plain component, like pkg/hal's
// own tests use, is all this package needs, and it keeps the test binary's HAL
// symbol set minimal.
func TestMain(m *testing.M) {
	keep, err := hal.NewComponent("pyvcp-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

var uniqCounter int

func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

// buildPanel writes an XML file, parses it, creates its HAL pins on a fresh
// component and marks it ready. The component is exited on test cleanup unless
// the test destroys it itself.
func buildPanel(t *testing.T, name, xml string) (*panel, *hal.Component) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.xml")
	if err := os.WriteFile(path, []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := parsePanel(name, path)
	if err != nil {
		t.Fatalf("parsePanel: %v", err)
	}
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	if err := p.createPins(comp); err != nil {
		t.Fatalf("createPins: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return p, comp
}

func TestScanCheckbuttonChangepinEdge(t *testing.T) {
	name := uniq("pyvcp")
	cb := uniq("cb")
	p, comp := buildPanel(t, name, fmt.Sprintf(`<pyvcp><checkbutton halpin="%s"/></pyvcp>`, cb))
	t.Cleanup(func() { _ = comp.Exit() })

	w := p.byID[cb]
	if w == nil {
		t.Fatalf("checkbutton %q not found", cb)
	}
	change := w.pins["changepin"]
	state := w.pins["state"]

	// Rising edge toggles state on.
	change.writeBit(true)
	p.scan()
	if !state.readBit() {
		t.Errorf("rising changepin edge should toggle state on")
	}
	// Holding high (no new edge) does not toggle again.
	p.scan()
	if !state.readBit() {
		t.Errorf("held changepin should not re-toggle")
	}
	// Falling then rising toggles off.
	change.writeBit(false)
	p.scan()
	change.writeBit(true)
	p.scan()
	if state.readBit() {
		t.Errorf("second rising edge should toggle state off")
	}
}

func TestScanJogwheelReset(t *testing.T) {
	name := uniq("pyvcp")
	jw := uniq("jw")
	p, comp := buildPanel(t, name, fmt.Sprintf(`<pyvcp><jogwheel halpin="%s" clear_pin="1"/></pyvcp>`, jw))
	t.Cleanup(func() { _ = comp.Exit() })

	w := p.byID[jw]
	w.value = 5
	w.pins["count"].writeFloat(5)
	w.pins["reset"].writeBit(true)
	p.scan()
	if w.value != 0 || w.pins["count"].readFloat() != 0 {
		t.Errorf("clear_pin should zero the count, got value=%v count=%v", w.value, w.pins["count"].readFloat())
	}
}

func TestScanScaleParamPinOverrideClamps(t *testing.T) {
	name := uniq("pyvcp")
	sc := uniq("sc")
	p, comp := buildPanel(t, name, fmt.Sprintf(
		`<pyvcp><scale halpin="%s" param_pin="1" min_="0" max_="10"/></pyvcp>`, sc))
	t.Cleanup(func() { _ = comp.Exit() })

	w := p.byID[sc]
	w.pins["param"].writeFloat(7)
	p.scan()
	if w.value != 7 {
		t.Errorf("param override to 7 = %v", w.value)
	}
	// Above max must clamp.
	w.pins["param"].writeFloat(20)
	p.scan()
	if w.value != 10 {
		t.Errorf("param override 20 should clamp to 10, got %v", w.value)
	}
}

// TestScanTimerServerAuthoritative verifies elapsed time is accrued by the
// server while run is high (the client no longer computes it) and zeroed on
// reset — so a client connecting mid-run sees correct elapsed time.
func TestScanTimerServerAuthoritative(t *testing.T) {
	name := uniq("pyvcp")
	tm := uniq("tm")
	p, comp := buildPanel(t, name, fmt.Sprintf(`<pyvcp><timer halpin="%s"/></pyvcp>`, tm))
	t.Cleanup(func() { _ = comp.Exit() })

	w := p.byID[tm]

	// Not running: no accrual.
	p.lastScan = time.Now().Add(-100 * time.Millisecond)
	p.scan()
	if w.value != 0 {
		t.Errorf("stopped timer should not accrue, got %v", w.value)
	}

	// Running: accrue ~100ms.
	w.pins["run"].writeBit(true)
	p.lastScan = time.Now().Add(-100 * time.Millisecond)
	p.scan()
	if w.value <= 0 || w.value > 1 {
		t.Errorf("running timer accrual = %v, want ~0.1s", w.value)
	}
	if s := w.readState(); s.Value != w.value || !s.State {
		t.Errorf("readState should surface elapsed value and run state, got %+v", s)
	}

	// Reset zeroes elapsed.
	w.pins["reset"].writeBit(true)
	p.scan()
	if w.value != 0 {
		t.Errorf("reset should zero elapsed, got %v", w.value)
	}
}

// TestDestroyClosesUseAfterFree is the regression guard for the unload
// use-after-free: after Destroy, the panel is marked closed, the watch callback
// returns an empty state without touching (freed) pins, and the panel is gone
// from the registry — even though an already-open pushLoop would still hold the
// WatchStateJSON closure.
func TestDestroyClosesUseAfterFree(t *testing.T) {
	name := uniq("pyvcp")
	bt := uniq("bt")
	p, comp := buildPanel(t, name, fmt.Sprintf(`<pyvcp><button halpin="%s"/></pyvcp>`, bt))

	panelRegistry.register(p)
	mod := &pyvcpModule{logger: slog.Default(), comp: comp, panel: p}
	if err := mod.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cb := &pyvcpCallbacks{panel: p, comp: comp, logger: slog.Default()}
	if js, err := cb.WatchStateJSON(); err != nil || string(js) == "{}" {
		t.Fatalf("live panel should return non-empty state, got %q err=%v", js, err)
	}

	mod.Destroy() // Stop() + closed + unregister + comp.Exit()

	if !p.closed {
		t.Errorf("Destroy must mark the panel closed")
	}
	if panelRegistry.get(name) != nil {
		t.Errorf("Destroy must remove the panel from the registry")
	}
	// The dangerous path: a pushLoop calling the captured closure after unload.
	// It must return an empty map and never touch a freed pin.
	js, err := cb.WatchStateJSON()
	if err != nil {
		t.Errorf("WatchStateJSON after close errored: %v", err)
	}
	if string(js) != "{}" {
		t.Errorf("closed panel WatchStateJSON = %q, want {}", js)
	}
	// A widget event after close must be rejected, not panic.
	ev := pyvcp.WidgetEvent{Widget: bt, Event: pyvcp.EventType(evPress)}
	if ok, _ := cb.WidgetEvent(ev); ok {
		t.Errorf("WidgetEvent on closed panel should be rejected")
	}
}
