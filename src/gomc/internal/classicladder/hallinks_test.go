// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"fmt"
	"os"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	"github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// These tests run against a live in-process HAL, because the thing worth
// checking is that the pin names createHALPins invents are the names HAL ends up
// knowing the pins by — and a test that builds its own copy of that table proves
// nothing about the code that builds the real one.

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits and
// cannot be re-initialised afterwards — see pkg/hal's TestMain.
func TestMain(m *testing.M) {
	// Without the RTAPI app init, thread creation fails with EPERM in an
	// unprivileged process (see internal/halcmd's TestMain).
	halcmd.RtapiInitializeApp()

	// RtapiAppInit is what the launcher calls before any hal_init, and it is
	// required here specifically: it sets hal_lib's rtapi_pid, which is what
	// makes hal_init_ex(..., COMPONENT_TYPE_REALTIME) mark the component as
	// realtime. Without it hal_export_funct refuses classicladder.0.refresh with
	// EINVAL ("component is not realtime").
	if err := halcmd.RtapiAppInit(); err != nil {
		fmt.Fprintf(os.Stderr, "rtapi app init failed: %v\n", err)
		os.Exit(1)
	}

	keep, err := hal.NewComponent("classicladder-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	halcmd.RtapiAppCleanup()
	os.Exit(code)
}

var halUniqCounter int

// halUniq gives each test its own HAL namespace. HAL names are process-global
// and only freed on component exit, so sharing them would make failures cascade.
func halUniq(prefix string) string {
	halUniqCounter++
	return fmt.Sprintf("%s%d", prefix, halUniqCounter)
}

// newHalModule builds a module with real HAL pins, the way the loader does.
func newHalModule(t *testing.T, name string) *classicladder {
	t.Helper()

	m := newTestModule(t)
	m.name = name

	compID, err := halTestInit(name)
	if err != nil {
		t.Fatalf("hal_init_ex(%s): %v", name, err)
	}
	t.Cleanup(func() { halTestExit(compID) })

	pins, err := halTestCreatePins(m.rt, compID, name)
	if err != nil {
		t.Fatalf("createHALPins: %v", err)
	}
	m.halPins = pins
	halTestReady(compID)
	return m
}

// The pin names have to be the ones HAL knows, or the signal column is silently
// always empty. Asking HAL is the only way to know.
func TestHalLinks_PinNamesMatchWhatHalKnows(t *testing.T) {
	name := halUniq("cltest")
	m := newHalModule(t, name)

	links, err := m.GetHalLinks()
	if err != nil {
		t.Fatalf("get hal links: %v", err)
	}
	if len(links) == 0 {
		t.Fatal("no links returned")
	}

	res, err := halcmd.Show("pin", name+".*")
	if err != nil {
		t.Fatalf("show pins: %v", err)
	}
	known := map[string]bool{}
	for _, p := range res.Pins {
		known[p.Name] = true
	}

	for _, l := range links {
		if !known[l.Pin] {
			t.Errorf("variable %d/%d claims pin %q, which HAL does not have",
				l.VarType, l.Offset, l.Pin)
		}
	}

	// And every physical pin the component created must be accounted for, minus
	// hide_gui which carries no ladder variable.
	claimed := map[string]bool{}
	for _, l := range links {
		claimed[l.Pin] = true
	}
	for _, p := range res.Pins {
		if p.Name == name+".0.hide_gui" {
			continue
		}
		if !claimed[p.Name] {
			t.Errorf("pin %q exists but no variable claims it", p.Name)
		}
	}

	// The layout itself is a compatibility promise, not an internal detail:
	// configs written for 2.9 net these names, so the instance number and the
	// two-digit index have to stay. The recorded table cannot disagree with what
	// HAL has — both come from one string — but both can drift from the
	// convention together, which this catches and the checks above cannot.
	for _, want := range []string{
		name + ".0.in-00", name + ".0.in-09",
		name + ".0.out-00",
		name + ".0.s32in-00", name + ".0.s32out-00",
		name + ".0.floatin-00", name + ".0.floatout-00",
	} {
		if !claimed[want] {
			t.Errorf("no variable maps to %q; the pin naming convention changed", want)
		}
	}
}

// The point of the whole endpoint: which signal is this variable wired to.
func TestHalLinks_ReportsTheConnectedSignal(t *testing.T) {
	name := halUniq("cltest")
	m := newHalModule(t, name)

	sig := halUniq("estop-chain")
	if err := halcmd.NewSig(sig, hal.TypeBit); err != nil {
		t.Fatalf("newsig: %v", err)
	}
	if err := halcmd.LinkPS(name+".0.in-02", sig); err != nil {
		t.Fatalf("linkps: %v", err)
	}

	links, err := m.GetHalLinks()
	if err != nil {
		t.Fatalf("get hal links: %v", err)
	}

	var found bool
	for _, l := range links {
		switch {
		case int(l.VarType) == varPhysInput && l.Offset == 2:
			found = true
			if l.Signal != sig {
				t.Errorf("%%I2 signal = %q, want %q", l.Signal, sig)
			}
			if !l.IsInput {
				t.Error("%I2 should read from HAL, not write to it")
			}
		case l.Signal != "":
			t.Errorf("variable %d/%d reports signal %q, but nothing was wired to it",
				l.VarType, l.Offset, l.Signal)
		}
	}
	if !found {
		t.Fatal("%I2 was not in the returned links")
	}
}

// Direction is what tells a reader whether HAL drives the ladder or the other
// way round; getting it backwards makes the annotation actively misleading.
func TestHalLinks_DirectionFollowsThePin(t *testing.T) {
	name := halUniq("cltest")
	m := newHalModule(t, name)

	links, err := m.GetHalLinks()
	if err != nil {
		t.Fatalf("get hal links: %v", err)
	}

	want := map[int]bool{
		varPhysInput:      true,
		varPhysWordInput:  true,
		varPhysFloatIn:    true,
		varPhysOutput:     false,
		varPhysWordOutput: false,
		varPhysFloatOut:   false,
	}
	seenType := map[int]bool{}
	for _, l := range links {
		w, ok := want[int(l.VarType)]
		if !ok {
			t.Errorf("variable type %d has no HAL pin but was listed", l.VarType)
			continue
		}
		seenType[int(l.VarType)] = true
		if l.IsInput != w {
			t.Errorf("variable %d/%d isInput = %v, want %v",
				l.VarType, l.Offset, l.IsInput, w)
		}
	}
	for vt := range want {
		if !seenType[vt] {
			t.Errorf("no links returned for variable type %d", vt)
		}
	}
}

// Internal variables have no wiring, and their absence is the answer rather than
// a sentinel every client has to recognise.
func TestHalLinks_OmitsInternalVariables(t *testing.T) {
	name := halUniq("cltest")
	m := newHalModule(t, name)

	links, err := m.GetHalLinks()
	if err != nil {
		t.Fatalf("get hal links: %v", err)
	}
	for _, l := range links {
		switch int(l.VarType) {
		case varMemBit, varMemWord, varCounterValue, varStepTime, varTimerPreset:
			t.Errorf("internal variable type %d was listed as HAL-wired", l.VarType)
		}
	}
}

func TestHalLinks_EmptyWithoutPins(t *testing.T) {
	m := newTestModule(t)
	m.name = "cl-nopins"

	links, err := m.GetHalLinks()
	if err != nil {
		t.Fatalf("get hal links: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("got %d links for a module that created no pins", len(links))
	}
}
