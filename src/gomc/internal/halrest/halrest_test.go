// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Behavioural tests for the halcmd REST implementation, run against a real
// in-process HAL instance (link_test.go pulls in the HAL C symbols).
//
// This layer is what a remote HMI reaches when it pokes HAL over REST, so the
// tests care as much about the failure shapes as the happy paths: a command
// that fails must come back as CmdResult{Success:false} carrying the reason
// (rendered as 200 with an error body), while a *lookup* that misses must come
// back as a Go error (rendered as 404/500). Confusing the two would either hide
// failures from the operator or turn ordinary "not found" into a 500.
package halrest

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	hal "github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and cannot be re-initialised afterwards — see pkg/hal's TestMain.
func TestMain(m *testing.M) {
	// Initialise the RTAPI application environment exactly as the launcher
	// does, before any HAL init — without it thread creation fails with EPERM
	// in an unprivileged process (see internal/halcmd's TestMain).
	halcmd.RtapiInitializeApp()

	keep, err := hal.NewComponent("halrest-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// sp makes an optional string argument from a literal; derefStr reads an
// optional string field, treating absent as empty.
func sp(s string) *string { return &s }

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// uniq gives each test its own HAL namespace. HAL names are process-global and
// only freed on component exit / delsig, so sharing them across tests would
// make failures cascade.
var uniqCounter int

func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

// testComp creates a ready component with one pin of each type: "<name>.b"
// (bit, out), "<name>.f" (float, in), "<name>.u" (u32, in).
func testComp(t *testing.T, name string) *hal.Component {
	t.Helper()
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = comp.Exit() })
	if _, err := hal.NewPin[bool](comp, "b", hal.Out); err != nil {
		t.Fatalf("NewPin bit: %v", err)
	}
	if _, err := hal.NewPin[float64](comp, "f", hal.In); err != nil {
		t.Fatalf("NewPin float: %v", err)
	}
	if _, err := hal.NewPin[uint32](comp, "u", hal.In); err != nil {
		t.Fatalf("NewPin u32: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return comp
}

// --- Pattern helper ---

func TestShowPatterns(t *testing.T) {
	// An empty pattern must become "no patterns" (list everything), not a
	// single empty pattern — halcmd.Show treats those differently. An absent
	// pattern means the same: for a glob filter there is no third answer.
	if got := showPatterns(sp("")); got != nil {
		t.Errorf("showPatterns(\"\") = %v, want nil", got)
	}
	if got := showPatterns(nil); got != nil {
		t.Errorf("showPatterns(nil) = %v, want nil", got)
	}
	if got := showPatterns(sp("axis.*")); len(got) != 1 || got[0] != "axis.*" {
		t.Errorf("showPatterns(\"axis.*\") = %v", got)
	}
}

func TestParseHalType(t *testing.T) {
	for s, want := range map[string]hal.PinType{
		"bit": hal.TypeBit, "float": hal.TypeFloat, "s32": hal.TypeS32, "u32": hal.TypeU32,
	} {
		got, err := parseHalType(s)
		if err != nil || got != want {
			t.Errorf("parseHalType(%q) = %v, %v; want %v, nil", s, got, err, want)
		}
	}
	for _, bad := range []string{"", "BIT", "double", "port"} {
		if _, err := parseHalType(bad); err == nil {
			t.Errorf("parseHalType(%q) should fail", bad)
		}
	}
}

// --- Listings ---

func TestListPinsAndGetPin(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hrcomp")
	testComp(t, name)

	pins, err := h.ListPins(sp(name + ".*"))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(pins) != 3 {
		t.Fatalf("ListPins returned %d pins, want 3: %+v", len(pins), pins)
	}
	byName := map[string]bool{}
	for _, p := range pins {
		byName[p.Name] = true
		if p.Owner != name {
			t.Errorf("pin %q owner = %q, want %q", p.Name, p.Owner, name)
		}
		if p.Linked {
			t.Errorf("pin %q reported linked before any net", p.Name)
		}
	}
	for _, want := range []string{name + ".b", name + ".f", name + ".u"} {
		if !byName[want] {
			t.Errorf("ListPins is missing %q", want)
		}
	}

	// The unfiltered listing must be a superset of the filtered one.
	all, err := h.ListPins(sp(""))
	if err != nil {
		t.Fatalf("ListPins(\"\"): %v", err)
	}
	if len(all) < len(pins) {
		t.Errorf("unfiltered listing (%d) smaller than filtered (%d)", len(all), len(pins))
	}

	pi, err := h.GetPin(name + ".b")
	if err != nil {
		t.Fatalf("GetPin: %v", err)
	}
	if pi.Name != name+".b" || pi.Type != "bit" || pi.Dir != "OUT" {
		t.Errorf("GetPin returned %+v", pi)
	}

	// A miss is an error (→ 404), not an empty result.
	if _, err := h.GetPin("no.such.pin"); err == nil {
		t.Error("GetPin on an unknown pin must fail")
	}
}

func TestGetPinReportsSignalLinkage(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hrlink")
	testComp(t, name)
	sig := uniq("hrsig")

	if _, err := h.NewSignal(sig, "bit"); err != nil {
		t.Fatalf("NewSignal: %v", err)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	res, err := h.Link(name+".b", sig)
	if err != nil || !res.Success {
		t.Fatalf("Link: %v / %+v", err, res)
	}

	pi, err := h.GetPin(name + ".b")
	if err != nil {
		t.Fatalf("GetPin: %v", err)
	}
	if !pi.Linked || derefStr(pi.Signal) != sig {
		t.Errorf("after Link, pin = %+v; want Linked with Signal=%q", pi, sig)
	}

	// The signal view must show the writer from the other side.
	si, err := h.GetSignal(sig)
	if err != nil {
		t.Fatalf("GetSignal: %v", err)
	}
	if len(si.Writers) != 1 || si.Writers[0] != name+".b" {
		t.Errorf("signal writers = %v, want [%s.b]", si.Writers, name)
	}
	if si.Type != "bit" {
		t.Errorf("signal type = %q, want bit", si.Type)
	}

	// Unlink clears it again.
	if res, err := h.Unlink(name + ".b"); err != nil || !res.Success {
		t.Fatalf("Unlink: %v / %+v", err, res)
	}
	pi, err = h.GetPin(name + ".b")
	if err != nil {
		t.Fatalf("GetPin after Unlink: %v", err)
	}
	if pi.Linked {
		t.Error("pin still reports linked after Unlink")
	}
}

func TestListSignalsAndGetSignalMiss(t *testing.T) {
	h := &halcmdImpl{}
	sig := uniq("hrlist")
	if _, err := h.NewSignal(sig, "float"); err != nil {
		t.Fatalf("NewSignal: %v", err)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	sigs, err := h.ListSignals(sp(sig))
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if len(sigs) != 1 || sigs[0].Name != sig {
		t.Fatalf("ListSignals = %+v", sigs)
	}
	// The connection slices must be empty, never nil — the JSON contract is
	// `"writers": []`, and a nil would serialise as null.
	if sigs[0].Writers == nil || sigs[0].Readers == nil || sigs[0].Bidirs == nil {
		t.Errorf("ListSignals left a nil connection slice: %+v", sigs[0])
	}

	if _, err := h.GetSignal("no.such.signal"); err == nil {
		t.Error("GetSignal on an unknown signal must fail")
	}
}

func TestListParamsAndGetParamMiss(t *testing.T) {
	h := &halcmdImpl{}
	// No component here exports params, so this only asserts the call shape and
	// the miss path; the listing itself may legitimately be empty.
	if _, err := h.ListParams(sp("")); err != nil {
		t.Fatalf("ListParams: %v", err)
	}
	if _, err := h.GetParam("no.such.param"); err == nil {
		t.Error("GetParam on an unknown param must fail")
	}
}

func TestListComponentsAndStatus(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hrcomps")
	testComp(t, name)

	comps, err := h.ListComponents(sp(name))
	if err != nil {
		t.Fatalf("ListComponents: %v", err)
	}
	if len(comps) != 1 || comps[0].Name != name {
		t.Fatalf("ListComponents = %+v", comps)
	}
	if comps[0].Id == 0 {
		t.Error("component id not reported")
	}

	st, err := h.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Components < 1 || st.Pins < 3 {
		t.Errorf("GetStatus undercounts: %+v", st)
	}
	if st.RtLock {
		t.Error("GetStatus reports HAL locked in an unlocked test run")
	}
}

// --- set / alias ---

func TestSetPinAndSetSignal(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hrset")
	testComp(t, name)

	// An unlinked input pin is directly settable.
	res, err := h.SetPin(name+".f", "2.5")
	if err != nil || !res.Success {
		t.Fatalf("SetPin: %v / %+v", err, res)
	}
	pi, err := h.GetPin(name + ".f")
	if err != nil {
		t.Fatalf("GetPin: %v", err)
	}
	if pi.Value != "2.5" {
		t.Errorf("pin value = %q, want 2.5", pi.Value)
	}

	// Failures come back as an unsuccessful CmdResult with the reason, NOT as
	// a Go error — the REST layer renders those as 200-with-error, and losing
	// the distinction would turn every bad value into a 500.
	for _, tc := range []struct{ name, value string }{
		{name + ".f", "notanumber"},
		{"no.such.pin", "1"},
	} {
		res, err := h.SetPin(tc.name, tc.value)
		if err != nil {
			t.Errorf("SetPin(%q,%q) returned a transport error: %v", tc.name, tc.value, err)
		}
		if res.Success || derefStr(res.Error) == "" {
			t.Errorf("SetPin(%q,%q) = %+v; want Success=false with a reason", tc.name, tc.value, res)
		}
	}

	sig := uniq("hrsetsig")
	if _, err := h.NewSignal(sig, "s32"); err != nil {
		t.Fatalf("NewSignal: %v", err)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	if res, err := h.SetSignal(sig, "-7"); err != nil || !res.Success {
		t.Fatalf("SetSignal: %v / %+v", err, res)
	}
	si, err := h.GetSignal(sig)
	if err != nil {
		t.Fatalf("GetSignal: %v", err)
	}
	if si.Value != "-7" {
		t.Errorf("signal value = %q, want -7", si.Value)
	}
	if res, _ := h.SetSignal(sig, "nope"); res.Success {
		t.Error("SetSignal with an unparsable value must not report success")
	}
	if res, _ := h.SetSignal("no.such.signal", "1"); res.Success {
		t.Error("SetSignal on an unknown signal must not report success")
	}
}

func TestSetParamAndUnknownParam(t *testing.T) {
	h := &halcmdImpl{}
	if res, _ := h.SetParam("no.such.param", "1"); res.Success {
		t.Error("SetParam on an unknown param must not report success")
	}
}

func TestNewAndDeleteSignalErrors(t *testing.T) {
	h := &halcmdImpl{}
	sig := uniq("hrnew")

	if res, err := h.NewSignal(sig, "bit"); err != nil || !res.Success {
		t.Fatalf("NewSignal: %v / %+v", err, res)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	// A bad type must be rejected before HAL is touched.
	res, err := h.NewSignal(uniq("hrbad"), "quaternion")
	if err != nil {
		t.Fatalf("NewSignal returned a transport error: %v", err)
	}
	if res.Success || !strings.Contains(derefStr(res.Error), "unknown HAL type") {
		t.Errorf("NewSignal with a bad type = %+v", res)
	}

	if res, _ := h.NewSignal(sig, "bit"); res.Success {
		t.Error("NewSignal with a duplicate name must not report success")
	}
	if res, _ := h.DeleteSignal("no.such.signal"); res.Success {
		t.Error("DeleteSignal on an unknown signal must not report success")
	}
}

func TestAliasRoundTrip(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hralias")
	testComp(t, name)
	alias := uniq("aliaspin")

	if res, err := h.AliasPin(name+".b", alias); err != nil || !res.Success {
		t.Fatalf("AliasPin: %v / %+v", err, res)
	}
	if _, err := h.GetPin(alias); err != nil {
		t.Errorf("the alias is not resolvable: %v", err)
	}
	if res, err := h.UnaliasPin(alias); err != nil || !res.Success {
		t.Fatalf("UnaliasPin: %v / %+v", err, res)
	}
	if _, err := h.GetPin(alias); err == nil {
		t.Error("the alias still resolves after UnaliasPin")
	}

	// Failure paths stay CmdResult-shaped.
	if res, _ := h.AliasPin("no.such.pin", "x"); res.Success {
		t.Error("AliasPin on an unknown pin must not report success")
	}
	if res, _ := h.UnaliasPin("no.such.pin"); res.Success {
		t.Error("UnaliasPin on an unknown pin must not report success")
	}
	if res, _ := h.AliasParam("no.such.param", "x"); res.Success {
		t.Error("AliasParam on an unknown param must not report success")
	}
	if res, _ := h.UnaliasParam("no.such.param"); res.Success {
		t.Error("UnaliasParam on an unknown param must not report success")
	}
}

func TestNetAndLinkPp(t *testing.T) {
	h := &halcmdImpl{}
	a := uniq("hrneta")
	b := uniq("hrnetb")
	testComp(t, a)
	testComp(t, b)
	sig := uniq("hrnetsig")

	// Two HAL_OUT pins on one signal is illegal (two writers) — HAL must reject
	// it, and the rejection has to arrive as a reason on the result rather than
	// as a transport error or a crash.
	res, err := h.Net(sig, []string{a + ".b", b + ".b"})
	if err != nil {
		t.Fatalf("Net: %v", err)
	}
	if res.Success {
		t.Error("Net joining two output pins must not report success")
	}

	// A legal net. It needs its own signal name: the rejected attempt above
	// already created `sig` as a bit signal, and reusing it for u32 pins would
	// fail on the type, not on the wiring.
	sig2 := uniq("hrnetsig")
	res, err = h.Net(sig2, []string{a + ".u", b + ".u"})
	if err != nil {
		t.Fatalf("Net: %v", err)
	}
	if !res.Success {
		t.Fatalf("Net joining two input pins failed: %s", derefStr(res.Error))
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig2) })

	// Both pins now report the signal.
	for _, pin := range []string{a + ".u", b + ".u"} {
		pi, err := h.GetPin(pin)
		if err != nil {
			t.Fatalf("GetPin(%q): %v", pin, err)
		}
		if !pi.Linked || derefStr(pi.Signal) != sig2 {
			t.Errorf("pin %q = %+v; want linked to %q", pin, pi, sig2)
		}
	}

	if res, _ := h.LinkPp("no.such.pin", "also.missing"); res.Success {
		t.Error("LinkPp on unknown pins must not report success")
	}
	if res, _ := h.Unlink("no.such.pin"); res.Success {
		t.Error("Unlink on an unknown pin must not report success")
	}
}

// --- Threads and functions ---

func TestThreadLifecycle(t *testing.T) {
	h := &halcmdImpl{}
	thread := uniq("hrthread")

	fp := false
	cpu := int32(-1)
	res, err := h.Newthread(thread, 1000000, &fp, &cpu)
	if err != nil {
		t.Fatalf("Newthread: %v", err)
	}
	if !res.Success {
		t.Fatalf("Newthread failed: %s", derefStr(res.Error))
	}
	t.Cleanup(func() { _, _ = h.Delthread(thread) })

	threads, err := h.ListThreads(sp(thread))
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 || threads[0].Name != thread {
		t.Fatalf("ListThreads = %+v", threads)
	}
	if threads[0].Period != 1000000 {
		t.Errorf("thread period = %d, want 1000000", threads[0].Period)
	}

	if threads[0].Fp {
		t.Errorf("explicit fp=false thread reports FP")
	}

	// Nil fp/cpuId must be accepted (the REST body may omit them), and an
	// omitted fp must mean FP — the .hal parser default. Defaulting to nofp
	// made every addf of a floating-point function fail on threads created at
	// runtime while the same HAL-file line worked.
	other := uniq("hrthread")
	if res, err := h.Newthread(other, 1000000, nil, nil); err != nil || !res.Success {
		t.Fatalf("Newthread with nil options: %v / %+v", err, res)
	}
	otherInfo, err := h.ListThreads(sp(other))
	if err != nil || len(otherInfo) != 1 {
		t.Fatalf("ListThreads(%s) = %+v / %v", other, otherInfo, err)
	}
	if !otherInfo[0].Fp {
		t.Errorf("thread created with omitted fp is not FP; want the .hal parser default (fp)")
	}
	if res, err := h.Delthread(other); err != nil || !res.Success {
		t.Fatalf("Delthread: %v / %+v", err, res)
	}

	// Functions listing must not error even when empty.
	if _, err := h.ListFunctions(sp("")); err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}

	// Failure paths.
	if res, _ := h.Delthread("no.such.thread"); res.Success {
		t.Error("Delthread on an unknown thread must not report success")
	}
	if res, _ := h.Addf(thread, "no.such.funct", nil); res.Success {
		t.Error("Addf with an unknown function must not report success")
	}
	if res, _ := h.Delf(thread, "no.such.funct"); res.Success {
		t.Error("Delf with an unknown function must not report success")
	}
	pos := int32(0)
	if res, _ := h.Addf("no.such.thread", "no.such.funct", &pos); res.Success {
		t.Error("Addf on an unknown thread must not report success")
	}
}

func TestStartStopThreads(t *testing.T) {
	h := &halcmdImpl{}
	if res, err := h.Start(); err != nil || !res.Success {
		t.Fatalf("Start: %v / %+v", err, res)
	}
	if res, err := h.Stop(); err != nil || !res.Success {
		t.Fatalf("Stop: %v / %+v", err, res)
	}
}

// --- Lock / debug / save ---

func TestLockUnlockDefaultsToAll(t *testing.T) {
	h := &halcmdImpl{}
	// An empty level means "all" on both sides. The test must leave HAL
	// unlocked or every later test would fail.
	defer func() { _, _ = h.Unlock(sp("all")) }()

	if res, err := h.Lock(sp("")); err != nil || !res.Success {
		t.Fatalf("Lock(\"\"): %v / %+v", err, res)
	}
	st, err := h.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.RtLock {
		t.Error("GetStatus does not report the lock taken by Lock(\"\")")
	}

	if res, err := h.Unlock(sp("")); err != nil || !res.Success {
		t.Fatalf("Unlock(\"\"): %v / %+v", err, res)
	}
	if st, _ := h.GetStatus(); st.RtLock {
		t.Error("HAL still locked after Unlock(\"\")")
	}

	if res, _ := h.Lock(sp("sideways")); res.Success {
		t.Error("Lock with an unknown level must not report success")
	}
	if res, _ := h.Unlock(sp("sideways")); res.Success {
		t.Error("Unlock with an unknown level must not report success")
	}
}

func TestSetDebugLevels(t *testing.T) {
	h := &halcmdImpl{}
	defer func() { _, _ = h.SetDebug(1) }() // restore INFO

	for _, lvl := range []int32{0, 1, 2, 3} {
		if res, err := h.SetDebug(lvl); err != nil || !res.Success {
			t.Errorf("SetDebug(%d): %v / %+v", lvl, err, res)
		}
	}
	if res, _ := h.SetDebug(99); res.Success {
		t.Error("SetDebug with an out-of-range level must not report success")
	}
}

func TestSaveDefaultsToAll(t *testing.T) {
	h := &halcmdImpl{}
	sig := uniq("hrsavesig")
	if res, err := h.NewSignal(sig, "float"); err != nil || !res.Success {
		t.Fatalf("NewSignal: %v / %+v", err, res)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	res, err := h.Save(sp(""))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !res.Success {
		t.Fatalf("Save failed: %s", derefStr(res.Error))
	}
	// "" means "all", so the signal just created has to appear in the dump.
	if !strings.Contains(derefStr(res.Output), sig) {
		t.Errorf("Save(\"\") output does not mention %q:\n%s", sig, derefStr(res.Output))
	}
	// Every emitted line must be newline-terminated so the output pastes
	// straight into a HAL file.
	if derefStr(res.Output) != "" && !strings.HasSuffix(derefStr(res.Output), "\n") {
		t.Error("Save output is not newline-terminated")
	}

	if res, _ := h.Save(sp("bogustype")); res.Success {
		t.Error("Save with an unknown type must not report success")
	}
}

func TestRetainUnretain(t *testing.T) {
	h := &halcmdImpl{}
	sig := uniq("hrretain")
	if res, err := h.NewSignal(sig, "float"); err != nil || !res.Success {
		t.Fatalf("NewSignal: %v / %+v", err, res)
	}
	t.Cleanup(func() { _, _ = h.DeleteSignal(sig) })

	if res, err := h.Retain(sig); err != nil || !res.Success {
		t.Fatalf("Retain: %v / %+v", err, res)
	}
	if res, err := h.Unretain(sig); err != nil || !res.Success {
		t.Fatalf("Unretain: %v / %+v", err, res)
	}
	if res, _ := h.Retain("no.such.signal"); res.Success {
		t.Error("Retain on an unknown signal must not report success")
	}
	if res, _ := h.Unretain("no.such.signal"); res.Success {
		t.Error("Unretain on an unknown signal must not report success")
	}
}

func TestWatchItemsListsPins(t *testing.T) {
	h := &halcmdImpl{}
	name := uniq("hrwatch")
	testComp(t, name)

	items, err := h.WatchItems([]string{name + ".b"})
	if err != nil {
		t.Fatalf("WatchItems: %v", err)
	}
	found := false
	for _, it := range items {
		if it.Name == name+".b" {
			found = true
		}
	}
	if !found {
		t.Errorf("WatchItems did not include %q", name+".b")
	}
}

// --- Load / unload hooks ---

func TestLoadUnloadWithoutLauncher(t *testing.T) {
	h := &halcmdImpl{}
	// The hooks are package globals the launcher installs. Restore whatever was
	// there so an ordering change cannot leak into another test.
	origLoad, origUnload := loadModuleHook, unloadModuleHook
	t.Cleanup(func() { loadModuleHook, unloadModuleHook = origLoad, origUnload })

	loadModuleHook, unloadModuleHook = nil, nil
	res, err := h.Load("somemod", nil)
	if err != nil {
		t.Fatalf("Load returned a transport error: %v", err)
	}
	if res.Success || !strings.Contains(derefStr(res.Error), "not supported") {
		t.Errorf("Load without a launcher = %+v", res)
	}
	res, err = h.Unload("somemod")
	if err != nil {
		t.Fatalf("Unload returned a transport error: %v", err)
	}
	if res.Success || !strings.Contains(derefStr(res.Error), "not supported") {
		t.Errorf("Unload without a launcher = %+v", res)
	}
}

func TestLoadUnloadDelegateToHooks(t *testing.T) {
	h := &halcmdImpl{}
	origLoad, origUnload := loadModuleHook, unloadModuleHook
	t.Cleanup(func() { loadModuleHook, unloadModuleHook = origLoad, origUnload })

	var gotModule string
	var gotArgs []string
	SetLoadModuleFunc(func(module string, args []string) error {
		gotModule, gotArgs = module, args
		return nil
	})
	var gotUnload string
	SetUnloadModuleFunc(func(name string) error {
		gotUnload = name
		return nil
	})

	if res, err := h.Load("mymod", []string{"a=1", "b=2"}); err != nil || !res.Success {
		t.Fatalf("Load: %v / %+v", err, res)
	}
	if gotModule != "mymod" || len(gotArgs) != 2 || gotArgs[0] != "a=1" {
		t.Errorf("hook saw module=%q args=%v", gotModule, gotArgs)
	}
	if res, err := h.Unload("mymod"); err != nil || !res.Success {
		t.Fatalf("Unload: %v / %+v", err, res)
	}
	if gotUnload != "mymod" {
		t.Errorf("unload hook saw %q", gotUnload)
	}

	// A hook error surfaces as an unsuccessful result, not a transport error.
	SetLoadModuleFunc(func(string, []string) error { return fmt.Errorf("boom") })
	SetUnloadModuleFunc(func(string) error { return fmt.Errorf("bang") })
	if res, err := h.Load("m", nil); err != nil || res.Success || derefStr(res.Error) != "boom" {
		t.Errorf("Load with a failing hook = %+v / %v", res, err)
	}
	if res, err := h.Unload("m"); err != nil || res.Success || derefStr(res.Error) != "bang" {
		t.Errorf("Unload with a failing hook = %+v / %v", res, err)
	}
}

// --- Watch factory ---

func TestWatchItemsFactoryRequiresNames(t *testing.T) {
	for _, args := range []string{``, `{}`, `{"names":[]}`, `not json`} {
		if _, err := watchItemsFactory(json.RawMessage(args)); err == nil {
			t.Errorf("watchItemsFactory(%q) must fail — an unbounded watch is not a valid subscription", args)
		}
	}
}

// TestWatchItemsFactoryUnknownName pins the deliberate behaviour for a name
// that does not resolve: the subscription is accepted and the item is carried
// as a dead entry, because a watched pin may legitimately appear later (its
// module is loaded after the HMI subscribed) — WatchSet re-resolves. Rejecting
// the whole subscription would break that.
func TestWatchItemsFactoryUnknownName(t *testing.T) {
	fn, err := watchItemsFactory(json.RawMessage(`{"names":["no.such.pin"]}`))
	if err != nil {
		t.Fatalf("watchItemsFactory: %v", err)
	}
	raw, err := fn()
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	var first watchItemsMeta
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("first poll is not a meta message: %v (%s)", err, raw)
	}
	if len(first.Meta) != 1 || first.Meta[0].Kind != "unknown" {
		t.Errorf("meta for an unresolvable name = %+v, want one entry of kind %q", first.Meta, "unknown")
	}
}

func TestWatchItemsFactoryFirstPollCarriesMeta(t *testing.T) {
	name := uniq("hrwf")
	testComp(t, name)

	// Watch the float input pin: an unlinked HAL_IN pin is the one this test can
	// drive with setp (setp on an output pin is refused by HAL).
	fn, err := watchItemsFactory(json.RawMessage(`{"names":["` + name + `.f"]}`))
	if err != nil {
		t.Fatalf("watchItemsFactory: %v", err)
	}

	// First poll: metadata plus the initial values.
	raw, err := fn()
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	var first watchItemsMeta
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("first poll is not a meta message: %v (%s)", err, raw)
	}
	if len(first.Meta) != 1 || first.Meta[0].Name != name+".f" {
		t.Fatalf("first poll meta = %+v", first.Meta)
	}
	if first.Meta[0].Kind != "pin" || first.Meta[0].Type != "float" {
		t.Errorf("meta entry = %+v", first.Meta[0])
	}
	if _, ok := first.Values[name+".f"]; !ok {
		t.Errorf("first poll carries no initial value: %+v", first.Values)
	}

	// Unchanged: nil, so pushLoop skips the tick instead of resending.
	if raw, err := fn(); err != nil || raw != nil {
		t.Errorf("unchanged poll = %s, %v; want nil, nil", raw, err)
	}

	// Changed: a bare name→value map, not another meta message.
	if err := halcmd.SetP(name+".f", "2.5"); err != nil {
		t.Fatalf("SetP: %v", err)
	}
	raw, err = fn()
	if err != nil {
		t.Fatalf("poll after change: %v", err)
	}
	if raw == nil {
		t.Fatal("poll after change returned nil")
	}
	var delta map[string]string
	if err := json.Unmarshal(raw, &delta); err != nil {
		t.Fatalf("delta unmarshal: %v (%s)", err, raw)
	}
	if delta[name+".f"] != "2.5" {
		t.Errorf("delta = %v, want the changed pin", delta)
	}
}

// --- Registration ---

func TestRegisterWiresRESTAndWatch(t *testing.T) {
	reg := apiserver.NewRegistry()
	if err := Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	api := reg.GetByAPI("halcmd", "halcmd")
	if api == nil {
		t.Fatal("Register did not add the halcmd instance to the registry")
	}
	// REST dispatch only works when the generated meta was attached; without it
	// every /api/v1/halcmd/... request would 404.
	if api.Meta == nil || !api.Meta.RESTExport || len(api.Meta.Funcs) == 0 {
		t.Fatalf("halcmd registered without usable REST meta: %+v", api.Meta)
	}
	if err := Register(reg); err == nil {
		t.Error("registering halcmd twice into one registry must fail")
	}

	wreg := apiserver.NewWatchRegistry()
	RegisterWatch(wreg, 0)
	wapi := wreg.Get("halcmd", "halcmd")
	if wapi == nil {
		t.Fatal("RegisterWatch did not register the halcmd watch API")
	}
	if len(wapi.Watches) != 1 || wapi.Watches[0].Name != "watch_items" {
		t.Fatalf("watch API = %+v", wapi.Watches)
	}
	// A non-positive interval must fall back to the default, never produce a
	// zero-period ticker (time.NewTicker panics on <= 0).
	if wapi.Watches[0].DefaultRate != 100*time.Millisecond {
		t.Errorf("default rate = %v, want 100ms", wapi.Watches[0].DefaultRate)
	}
	if wapi.Watches[0].Factory == nil {
		t.Error("watch_items registered without a per-connection factory")
	}

	wreg2 := apiserver.NewWatchRegistry()
	RegisterWatch(wreg2, 25*time.Millisecond)
	if got := wreg2.Get("halcmd", "halcmd").Watches[0].DefaultRate; got != 25*time.Millisecond {
		t.Errorf("configured rate = %v, want 25ms", got)
	}
}
