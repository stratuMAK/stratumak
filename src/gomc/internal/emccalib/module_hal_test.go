// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Behavioural tests for emccalib's REST-facing surface — GetTunables, SetPin,
// SaveIni and Revert — run against a real in-process HAL instance rather than a
// mock. Those four methods exist to move numbers between HAL pins and the INI
// file on disk, so a test that stubs out either end tests nothing: the reason
// this package sat at 43 % was precisely "it reads live HAL pins", and the
// keep-alive TestMain pattern is the answer to that.
//
// The value of the round trip is the reason to test it end to end. SaveIni
// writes the operator's tuning back over their INI file (with a .bak beside
// it), and Revert is what undoes a bad value on a running machine — E-1 was a
// pointer-aliasing bug that made Revert restore the *startup* value forever,
// which on a live spindle is the operator's careful tune silently thrown away.
package emccalib

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/calibreg"
	"github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	hal "github.com/sittner/linuxcnc/src/gomc/pkg/hal"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and cannot be re-initialised afterwards — see pkg/hal's TestMain.
func TestMain(m *testing.M) {
	// Initialise the RTAPI application environment exactly as the launcher
	// does, before any HAL init. emccalib needs no RT component of its own, so
	// RtapiInitializeApp (not RtapiAppInit) is enough.
	halcmd.RtapiInitializeApp()

	keep, err := hal.NewComponent("emccalib-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// uniq gives each test its own HAL namespace. HAL names are process-global and
// only freed on component exit, so sharing them across tests would make
// failures cascade.
var uniqCounter int

func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

// tunePins creates a ready component exporting one float in-pin per name and
// returns the component's HAL prefix. In-pins are what a tunable actually is:
// unlinked, so setp writes the pin's own dummy signal, which is how emccalib
// drives them.
func tunePins(t *testing.T, comp string, pins ...string) string {
	t.Helper()
	c, err := hal.NewComponent(comp)
	if err != nil {
		t.Fatalf("NewComponent(%q): %v", comp, err)
	}
	t.Cleanup(func() { _ = c.Exit() })
	for _, p := range pins {
		if _, err := hal.NewPin[float64](c, p, hal.In); err != nil {
			t.Fatalf("NewPin(%q.%q): %v", comp, p, err)
		}
	}
	if err := c.Ready(); err != nil {
		t.Fatalf("Ready(%q): %v", comp, err)
	}
	return comp + "."
}

// getPin reads a HAL pin as a float, failing the test if it cannot.
func getPin(t *testing.T, pin string) float64 {
	t.Helper()
	s, err := halcmd.GetP(pin)
	if err != nil {
		t.Fatalf("GetP(%q): %v", pin, err)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		t.Fatalf("GetP(%q) = %q, not a float: %v", pin, s, err)
	}
	return v
}

// setPin writes a HAL pin directly, standing in for whatever else on the
// machine moved the value (a GUI slider, another component).
func setPin(t *testing.T, pin string, v float64) {
	t.Helper()
	if err := halcmd.SetP(pin, strconv.FormatFloat(v, 'f', -1, 64)); err != nil {
		t.Fatalf("SetP(%q, %v): %v", pin, v, err)
	}
}

// newIniEmccalib builds a module over a real parsed INI file, so the tunables
// carry the provenance (source file + line) that SaveIni writes through. The
// returned path is the INI on disk; pathres is pointed at its directory so both
// the read and the write-back resolve.
func newIniEmccalib(t *testing.T, content string, mappings ...calibreg.IniPinMapping) (*emccalib, string) {
	t.Helper()
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	path := filepath.Join(dir, "test.ini")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ini, err := inifile.Parse(path)
	if err != nil {
		t.Fatalf("inifile.Parse: %v", err)
	}

	calibreg.Reset()
	t.Cleanup(calibreg.Reset)
	for _, m := range mappings {
		calibreg.Record(m)
	}
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	mod, err := newEmccalib(ini, testLogger(), uniq(t.Name()), nil)
	if err != nil {
		t.Fatalf("newEmccalib: %v", err)
	}
	return mod.(*emccalib), path
}

// --- Provenance discovery ---

// TestNewEmccalibProvenance covers the branch the nil-INI test cannot: with a
// real INI, every tunable must find the file and line its value came from.
// Without provenance SaveIni silently skips the entry (sourceFile == ""), so a
// lookup that quietly misses is a save that quietly does nothing.
func TestNewEmccalibProvenance(t *testing.T) {
	const ini = "[JOINT_0]\nP = 1\nI = 2\n\n[JOINT_1]\nP = 3\n"
	e, path := newIniEmccalib(t, ini,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: "pid.0.Pgain", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "I", Pin: "pid.0.Igain", IniValue: 2},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "P", Pin: "pid.1.Pgain", IniValue: 3},
		// Recorded as tunable but absent from the INI: provenance is simply
		// unavailable, and that must not derail the ones that have it.
		calibreg.IniPinMapping{Section: "SPINDLE_0", Key: "P", Pin: "pid.s.Pgain", IniValue: 4},
	)

	wantLine := map[string]int{"JOINT_0\x00P": 2, "JOINT_0\x00I": 3, "JOINT_1\x00P": 6}
	for key, line := range wantLine {
		sec, k, _ := strings.Cut(key, "\x00")
		got := lookupOne(e, sec, k)
		if got == nil {
			t.Fatalf("%s/%s missing from the index", sec, k)
		}
		if got.sourceFile != path {
			t.Errorf("%s/%s sourceFile = %q, want %q", sec, k, got.sourceFile, path)
		}
		if got.sourceLine != line {
			t.Errorf("%s/%s sourceLine = %d, want %d", sec, k, got.sourceLine, line)
		}
	}
	if got := lookupOne(e, "SPINDLE_0", "P"); got == nil {
		t.Fatal("SPINDLE_0/P missing from the index")
	} else if got.sourceFile != "" {
		t.Errorf("SPINDLE_0/P is not in the INI but got sourceFile %q", got.sourceFile)
	}
}

// TestLifecycle pins that the module's lifecycle hooks are the no-ops they
// claim to be — emccalib owns no goroutines, files or HAL objects, so Stop and
// Destroy have nothing to release and must stay safe to call in any order the
// launcher chooses, including twice.
func TestLifecycle(t *testing.T) {
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: "pid.0.Pgain", IniValue: 1},
	)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	e.Stop()
	e.Stop()
	e.Destroy()
	e.Destroy()
}

// --- GetTunables ---

// TestGetTunables is the read path an operator's calibration panel sees: the
// live HAL value alongside the INI value it was loaded from, grouped by section
// in discovery order.
func TestGetTunables(t *testing.T) {
	comp := uniq("emccalib-get")
	pfx := tunePins(t, comp, "j0p", "j0i", "j1p")
	setPin(t, pfx+"j0p", 10.5)
	setPin(t, pfx+"j0i", 20)
	setPin(t, pfx+"j1p", 30)

	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "j0p", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "I", Pin: pfx + "j0i", IniValue: 2},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "P", Pin: pfx + "j1p", IniValue: 3},
	)

	secs, err := e.GetTunables()
	if err != nil {
		t.Fatalf("GetTunables: %v", err)
	}
	if len(secs) != 2 {
		t.Fatalf("got %d sections, want 2 (%+v)", len(secs), secs)
	}
	// Discovery order, not map order — the panel lays sections out in the order
	// the INI declares them, so a randomised order would reshuffle the UI on
	// every poll.
	if secs[0].Name != "JOINT_0" || secs[1].Name != "JOINT_1" {
		t.Errorf("section order = %q, %q; want JOINT_0, JOINT_1", secs[0].Name, secs[1].Name)
	}
	if len(secs[0].Items) != 2 || len(secs[1].Items) != 1 {
		t.Fatalf("items per section = %d, %d; want 2, 1", len(secs[0].Items), len(secs[1].Items))
	}

	first := secs[0].Items[0]
	if first.Section != "JOINT_0" || first.Key != "P" || first.HalPin != pfx+"j0p" {
		t.Errorf("first item identity = %+v", first)
	}
	if first.Value != 10.5 {
		t.Errorf("first item Value = %v, want the live HAL value 10.5", first.Value)
	}
	if first.IniValue != 1 {
		t.Errorf("first item IniValue = %v, want 1", first.IniValue)
	}
	if got := secs[1].Items[0].Value; got != 30 {
		t.Errorf("JOINT_1/P Value = %v, want 30", got)
	}
}

// TestGetTunablesUnreadablePin covers the deliberate soft-fail: a tunable whose
// pin has gone away (its component was unloaded) must still be listed, with a
// zero live value, rather than failing the whole query. One dead pin must not
// blank the operator's entire calibration panel.
func TestGetTunablesUnreadablePin(t *testing.T) {
	comp := uniq("emccalib-dead")
	pfx := tunePins(t, comp, "alive")
	setPin(t, pfx+"alive", 7)

	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "alive", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "I", Pin: comp + ".nosuchpin", IniValue: 2},
	)

	secs, err := e.GetTunables()
	if err != nil {
		t.Fatalf("GetTunables with a missing pin: %v", err)
	}
	if len(secs) != 1 || len(secs[0].Items) != 2 {
		t.Fatalf("got %+v, want one section with both items", secs)
	}
	if got := secs[0].Items[0].Value; got != 7 {
		t.Errorf("the live pin reads %v, want 7", got)
	}
	if got := secs[0].Items[1].Value; got != 0 {
		t.Errorf("the missing pin reads %v, want 0", got)
	}
	// The INI value is still the truth about what the file says, dead pin or not.
	if got := secs[0].Items[1].IniValue; got != 2 {
		t.Errorf("the missing pin's IniValue = %v, want 2", got)
	}
}

func TestGetTunablesEmpty(t *testing.T) {
	e := newTestEmccalib(t)
	secs, err := e.GetTunables()
	if err != nil {
		t.Fatalf("GetTunables: %v", err)
	}
	if len(secs) != 0 {
		t.Errorf("got %d sections from an empty registry, want 0", len(secs))
	}
}

// --- SetPin ---

func TestSetPin(t *testing.T) {
	comp := uniq("emccalib-set")
	pfx := tunePins(t, comp, "p")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1},
	)

	ok, err := e.SetPin("JOINT_0", "P", 42.25)
	if err != nil || !ok {
		t.Fatalf("SetPin = %v, %v; want true, nil", ok, err)
	}
	if got := getPin(t, pfx+"p"); got != 42.25 {
		t.Errorf("pin holds %v after SetPin, want 42.25", got)
	}
	// Tuning a pin must not touch the INI value — that is what Revert restores
	// to and what SaveIni compares against to decide there is anything to save.
	if got := lookupOne(e, "JOINT_0", "P"); got.iniValue != 1 {
		t.Errorf("SetPin changed iniValue to %v; it must stay 1", got.iniValue)
	}
}

// TestSetPinFanOut pins the tandem/gantry contract: one [SECTION]KEY feeding
// two pins must write BOTH. The index used to keep only the last registration,
// so the first tandem PID silently kept its old gain — mismatched gains on a
// gantry while the panel reported success.
func TestSetPinFanOut(t *testing.T) {
	comp := uniq("emccalib-fanout")
	pfx := tunePins(t, comp, "p0", "p1")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p0", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p1", IniValue: 1},
	)

	ok, err := e.SetPin("JOINT_0", "P", 7.5)
	if err != nil || !ok {
		t.Fatalf("SetPin = %v, %v; want true, nil", ok, err)
	}
	if got := getPin(t, pfx+"p0"); got != 7.5 {
		t.Errorf("first tandem pin holds %v after SetPin, want 7.5", got)
	}
	if got := getPin(t, pfx+"p1"); got != 7.5 {
		t.Errorf("second tandem pin holds %v after SetPin, want 7.5", got)
	}
}

// TestSetPinFanOutPartialFailure: with one live and one dead pin under the same
// key, the live pin is still written and the dead one is reported — a partial
// tandem write must not masquerade as success.
func TestSetPinFanOutPartialFailure(t *testing.T) {
	comp := uniq("emccalib-fanoutdead")
	pfx := tunePins(t, comp, "p0")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p0", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "nosuchpin", IniValue: 1},
	)

	ok, err := e.SetPin("JOINT_0", "P", 3.25)
	if err == nil || ok {
		t.Fatalf("SetPin with a dead tandem pin = %v, %v; want false and an error", ok, err)
	}
	if got := getPin(t, pfx+"p0"); got != 3.25 {
		t.Errorf("live tandem pin holds %v, want 3.25 (partial failure must still write it)", got)
	}
}

// TestSetPinUnknownKey: the tunable list is the allow-list. A REST caller
// naming anything else must be refused rather than have it forwarded to setp,
// which would let the endpoint write *any* pin on the machine.
func TestSetPinUnknownKey(t *testing.T) {
	comp := uniq("emccalib-setunknown")
	pfx := tunePins(t, comp, "p", "untunable")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1},
	)
	setPin(t, pfx+"untunable", 5)

	ok, err := e.SetPin("JOINT_0", "NOPE", 99)
	if err == nil || ok {
		t.Fatalf("SetPin of an untunable key = %v, %v; want false and an error", ok, err)
	}
	if got := getPin(t, pfx+"untunable"); got != 5 {
		t.Errorf("the untunable pin was written anyway (%v)", got)
	}
}

// TestSetPinHalError: a tunable whose pin no longer exists must surface the
// setp failure, not report success. Reporting true here tells the panel the
// value landed when nothing on the machine changed.
func TestSetPinHalError(t *testing.T) {
	comp := uniq("emccalib-setdead")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: comp + ".nosuchpin", IniValue: 1},
	)
	ok, err := e.SetPin("JOINT_0", "P", 1)
	if err == nil || ok {
		t.Fatalf("SetPin onto a missing pin = %v, %v; want false and an error", ok, err)
	}
}

// --- Revert ---

func TestRevert(t *testing.T) {
	comp := uniq("emccalib-revert")
	pfx := tunePins(t, comp, "p")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1.5},
	)

	if _, err := e.SetPin("JOINT_0", "P", 99); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	ok, err := e.Revert("JOINT_0", "P")
	if err != nil || !ok {
		t.Fatalf("Revert = %v, %v; want true, nil", ok, err)
	}
	if got := getPin(t, pfx+"p"); got != 1.5 {
		t.Errorf("pin holds %v after Revert, want the INI value 1.5", got)
	}
}

// TestRevertFanOut: revert must restore every pin the key feeds, same tandem
// contract as TestSetPinFanOut.
func TestRevertFanOut(t *testing.T) {
	comp := uniq("emccalib-revertfanout")
	pfx := tunePins(t, comp, "p0", "p1")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p0", IniValue: 2.5},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p1", IniValue: 2.5},
	)

	if _, err := e.SetPin("JOINT_0", "P", 99); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	ok, err := e.Revert("JOINT_0", "P")
	if err != nil || !ok {
		t.Fatalf("Revert = %v, %v; want true, nil", ok, err)
	}
	if got := getPin(t, pfx+"p0"); got != 2.5 {
		t.Errorf("first tandem pin holds %v after Revert, want 2.5", got)
	}
	if got := getPin(t, pfx+"p1"); got != 2.5 {
		t.Errorf("second tandem pin holds %v after Revert, want 2.5", got)
	}
}

func TestRevertUnknownKey(t *testing.T) {
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: "pid.0.Pgain", IniValue: 1},
	)
	if ok, err := e.Revert("JOINT_0", "NOPE"); err == nil || ok {
		t.Fatalf("Revert of an untunable key = %v, %v; want false and an error", ok, err)
	}
}

func TestRevertHalError(t *testing.T) {
	comp := uniq("emccalib-revertdead")
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: comp + ".nosuchpin", IniValue: 1},
	)
	if ok, err := e.Revert("JOINT_0", "P"); err == nil || ok {
		t.Fatalf("Revert onto a missing pin = %v, %v; want false and an error", ok, err)
	}
}

// --- SaveIni, and the E-1 regression at the API level ---

// TestSaveIniRoundTrip is the whole operator workflow in one test: tune a pin,
// save, and find the new number in the INI file with the old one in the .bak.
func TestSaveIniRoundTrip(t *testing.T) {
	comp := uniq("emccalib-save")
	pfx := tunePins(t, comp, "p", "i")
	const original = "[JOINT_0]\nP = 1\nI = 2 # do not lose me\n"
	e, path := newIniEmccalib(t, original,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "I", Pin: pfx + "i", IniValue: 2},
	)
	setPin(t, pfx+"p", 1) // unchanged — must not be rewritten
	setPin(t, pfx+"i", 2)

	if _, err := e.SetPin("JOINT_0", "P", 7.25); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	ok, err := e.SaveIni()
	if err != nil || !ok {
		t.Fatalf("SaveIni = %v, %v; want true, nil", ok, err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "[JOINT_0]\nP = 7.25\nI = 2 # do not lose me\n"
	if string(got) != want {
		t.Errorf("saved INI:\n got %q\nwant %q", got, want)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(bak) != original {
		t.Errorf("backup holds %q, want the pre-save %q", bak, original)
	}
	// The in-memory INI value must now agree with what is on disk, or the next
	// save thinks the value is still dirty and Revert undoes to a stale number.
	if v := lookupOne(e, "JOINT_0", "P").iniValue; v != 7.25 {
		t.Errorf("in-memory iniValue = %v after saving, want 7.25", v)
	}
}

// TestRevertAfterSaveIni is the E-1 regression on the real API surface, which
// is where the bug actually bit: the index used to hold pointers taken during
// the append loop, so SaveIni's write-back landed in the live slice while
// Revert read the orphaned copy — the operator saved a tune, nudged the pin,
// hit revert, and got the value the machine had booted with. Four tunables
// force the append growth that strands the early entries.
func TestRevertAfterSaveIni(t *testing.T) {
	comp := uniq("emccalib-e1")
	pfx := tunePins(t, comp, "a", "b", "c", "d")
	const original = "[JOINT_0]\nA = 1\nB = 2\n\n[JOINT_1]\nC = 3\nD = 4\n"
	e, _ := newIniEmccalib(t, original,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "A", Pin: pfx + "a", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "B", Pin: pfx + "b", IniValue: 2},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "C", Pin: pfx + "c", IniValue: 3},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "D", Pin: pfx + "d", IniValue: 4},
	)

	// Tune the *first* tunable — the one whose pointer went stale earliest — and
	// persist it.
	if _, err := e.SetPin("JOINT_0", "A", 11); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if _, err := e.SaveIni(); err != nil {
		t.Fatalf("SaveIni: %v", err)
	}

	// Now nudge it again and revert. "Revert" means back to what the INI holds
	// *now* (11), not what it held at startup (1).
	if _, err := e.SetPin("JOINT_0", "A", 99); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if _, err := e.Revert("JOINT_0", "A"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if got := getPin(t, pfx+"a"); got != 11 {
		t.Errorf("Revert restored %v; want the saved 11, not the startup value", got)
	}
}

// TestSaveIniNoProvenance: tunables with no source file (an INI-less launcher,
// or a mapping naming a section the INI does not have) are skipped rather than
// erroring. There is nowhere to write them, and the tunables that *do* have a
// home must still be saved.
func TestSaveIniNoProvenance(t *testing.T) {
	comp := uniq("emccalib-noprov")
	pfx := tunePins(t, comp, "known", "orphan")
	e, path := newIniEmccalib(t, "[JOINT_0]\nP = 1\n",
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "known", IniValue: 1},
		calibreg.IniPinMapping{Section: "GHOST", Key: "P", Pin: pfx + "orphan", IniValue: 5},
	)
	if lookupOne(e, "GHOST", "P").sourceFile != "" {
		t.Fatal("the ghost tunable unexpectedly has provenance")
	}

	setPin(t, pfx+"orphan", 55) // dirty, but unsaveable
	if _, err := e.SetPin("JOINT_0", "P", 2); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	ok, err := e.SaveIni()
	if err != nil || !ok {
		t.Fatalf("SaveIni = %v, %v; want true, nil", ok, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[JOINT_0]\nP = 2\n" {
		t.Errorf("saved INI = %q, want the one saveable tunable written", got)
	}
}

// TestSaveIniSkipsUnreadablePin: a tunable whose pin has vanished cannot be
// read, so there is no new value to save. It must be skipped — writing the
// stale in-memory value back would be fine, but writing a parse-failure zero
// would put "0" where a feed limit used to be. Nothing about the file changes.
func TestSaveIniSkipsUnreadablePin(t *testing.T) {
	comp := uniq("emccalib-savedead")
	const original = "[JOINT_0]\nP = 1\n"
	e, path := newIniEmccalib(t, original,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: comp + ".nosuchpin", IniValue: 1},
	)
	ok, err := e.SaveIni()
	if err != nil || !ok {
		t.Fatalf("SaveIni = %v, %v; want true, nil", ok, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("an unreadable pin rewrote the INI: %q", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a no-op save created a backup (stat err = %v)", err)
	}
	if v := lookupOne(e, "JOINT_0", "P").iniValue; v != 1 {
		t.Errorf("an unreadable pin overwrote iniValue with %v, want 1 kept", v)
	}
}

// TestSaveIniWriteError: when the rewrite fails, SaveIni must report it. A save
// that swallows the error tells the operator their tune is persisted when the
// file on disk still holds the old numbers, and the next restart quietly
// reverts the machine.
func TestSaveIniWriteError(t *testing.T) {
	comp := uniq("emccalib-saveerr")
	pfx := tunePins(t, comp, "p")
	e, path := newIniEmccalib(t, "[JOINT_0]\nP = 1\n",
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1},
	)
	if _, err := e.SetPin("JOINT_0", "P", 2); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	// Remove the file out from under the save: the read-back in updateINIFile
	// fails, which is the same shape as a permission or I/O failure.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	ok, err := e.SaveIni()
	if err == nil || ok {
		t.Fatalf("SaveIni over a missing file = %v, %v; want false and an error", ok, err)
	}
	// The failed save must not have advanced the in-memory INI value, or a
	// retry would decide there is nothing left to save.
	if v := lookupOne(e, "JOINT_0", "P").iniValue; v != 1 {
		t.Errorf("a failed save advanced iniValue to %v, want 1", v)
	}
}

// TestSaveIniOutsideRoot pins the containment check. The path SaveIni writes
// comes from INI provenance, and an INI can be assembled from includes, so the
// write goes through pathres in Write mode — a source file outside the allowed
// roots must be refused rather than rewritten in place (with a .bak dropped
// beside it).
func TestSaveIniOutsideRoot(t *testing.T) {
	comp := uniq("emccalib-escape")
	pfx := tunePins(t, comp, "p")
	e, _ := newIniEmccalib(t, "[JOINT_0]\nP = 1\n",
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: pfx + "p", IniValue: 1},
	)

	// Point the tunable at a file outside the resolver root.
	outside := filepath.Join(t.TempDir(), "elsewhere.ini")
	if err := os.WriteFile(outside, []byte("[JOINT_0]\nP = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.tunables[0].sourceFile = outside
	e.tunables[0].sourceLine = 2

	if _, err := e.SetPin("JOINT_0", "P", 2); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	ok, err := e.SaveIni()
	if err == nil || ok {
		t.Fatalf("SaveIni to a path outside the root = %v, %v; want false and an error", ok, err)
	}
	// Name the reason. The unchanged-file assertion below already fails if
	// containment stops working, but an "any error" check would also pass on a
	// typo'd path, which proves nothing about the check under test.
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("refused with %q, want the containment error", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[JOINT_0]\nP = 1\n" {
		t.Errorf("the out-of-root file was rewritten: %q", got)
	}
	if _, err := os.Stat(outside + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a refused save still dropped a .bak (stat err = %v)", err)
	}
}
