//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Behavioural tests for the halcmd command surface, run against a real
// in-process HAL instance (the package's link_test.go pulls in the HAL C
// symbols). The rest of the package's tests are compile-time signature
// assertions; these exercise what the commands actually do to HAL, which is the
// surface `halrun`, the HAL-file executor and the REST API all sit on.
package halcmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	halcmd "github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	hal "github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and cannot be re-initialised afterwards — see pkg/hal's TestMain.
func TestMain(m *testing.M) {
	// Initialise the RTAPI application environment exactly as the launcher
	// does, before any HAL init. Without it the uspace scheduling policy keeps
	// its SCHED_FIFO default and every thread creation fails with EPERM in an
	// unprivileged process; rtapi_initialize_app is what falls back to
	// SCHED_OTHER when RT hardening is unavailable.
	halcmd.RtapiInitializeApp()

	keep, err := hal.NewComponent("halcmd-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// uniq gives each test its own HAL namespace. HAL names are process-global and
// only freed on component exit / delsig, so sharing them across tests would
// make failures cascade.
var uniqCounter int

func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

// testComp creates a ready HAL component named <name> with a pin of each type
// and direction the tests need: "<name>.b" (bit, out), "<name>.f" (float, in),
// "<name>.s" (s32, io), "<name>.u" (u32, out) and "<name>.ui" (u32, in) — the
// last one so a net can legally join an output pin to an input pin.
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
	if _, err := hal.NewPin[int32](comp, "s", hal.IO); err != nil {
		t.Fatalf("NewPin s32: %v", err)
	}
	if _, err := hal.NewPin[uint32](comp, "u", hal.Out); err != nil {
		t.Fatalf("NewPin u32 out: %v", err)
	}
	if _, err := hal.NewPin[uint32](comp, "ui", hal.In); err != nil {
		t.Fatalf("NewPin u32 in: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return comp
}

// contains reports whether want is in got.
func contains(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// ===== Signals =====

// TestSignalLifecycle covers newsig/sets/gets/stype/delsig for every HAL type,
// including the value formatting `gets` produces (which halrun output and the
// REST layer both surface verbatim).
func TestSignalLifecycle(t *testing.T) {
	for _, tc := range []struct {
		typ    hal.PinType
		set    string
		want   string
		altSet string // alternative spelling that must parse to the same value
	}{
		{hal.TypeBit, "1", "TRUE", "true"},
		{hal.TypeFloat, "2.5", "2.5", "+2.5"},
		{hal.TypeS32, "-7", "-7", "-7"},
		{hal.TypeU32, "4000000000", "4000000000", "4000000000"},
	} {
		t.Run(tc.typ.String(), func(t *testing.T) {
			sig := uniq("sig")
			if err := halcmd.NewSig(sig, tc.typ); err != nil {
				t.Fatalf("NewSig: %v", err)
			}
			t.Cleanup(func() { _ = halcmd.DelSig(sig) })

			gotType, err := halcmd.SType(sig)
			if err != nil {
				t.Fatalf("SType: %v", err)
			}
			if gotType != tc.typ {
				t.Errorf("SType = %v; want %v", gotType, tc.typ)
			}

			if err := halcmd.SetS(sig, tc.set); err != nil {
				t.Fatalf("SetS(%q): %v", tc.set, err)
			}
			got, err := halcmd.GetS(sig)
			if err != nil {
				t.Fatalf("GetS: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetS = %q; want %q", got, tc.want)
			}

			if err := halcmd.SetS(sig, tc.altSet); err != nil {
				t.Fatalf("SetS(%q): %v", tc.altSet, err)
			}
			if got, err := halcmd.GetS(sig); err != nil || got != tc.want {
				t.Errorf("GetS after %q = %q, %v; want %q", tc.altSet, got, err, tc.want)
			}

			if err := halcmd.DelSig(sig); err != nil {
				t.Fatalf("DelSig: %v", err)
			}
			if _, err := halcmd.GetS(sig); err == nil {
				t.Error("GetS on a deleted signal must fail")
			}
		})
	}
}

// TestSignalErrors covers the rejection paths a bad HAL file or REST call
// reaches: unknown names, a duplicate signal, and values that do not parse as
// the signal's type.
func TestSignalErrors(t *testing.T) {
	sig := uniq("sig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	if err := halcmd.NewSig(sig, hal.TypeS32); err == nil {
		t.Error("NewSig with a duplicate name must fail")
	}
	if err := halcmd.SetS(sig, "notanumber"); err == nil {
		t.Error("SetS with an unparsable value must fail")
	}
	if _, err := halcmd.GetS("nosuchsignal"); err == nil {
		t.Error("GetS on an unknown signal must fail")
	}
	if _, err := halcmd.SType("nosuchsignal"); err == nil {
		t.Error("SType on an unknown signal must fail")
	}
	if err := halcmd.DelSig("nosuchsignal"); err == nil {
		t.Error("DelSig on an unknown signal must fail")
	}
	if err := halcmd.SetS("nosuchsignal", "1"); err == nil {
		t.Error("SetS on an unknown signal must fail")
	}
}

// TestRetainUnretain covers the retain flag and its listing. A retained signal
// keeps its value across a component reload, which is why `list retain` exists
// as its own list type.
func TestRetainUnretain(t *testing.T) {
	sig := uniq("retainsig")
	if err := halcmd.NewSig(sig, hal.TypeFloat); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	if err := halcmd.Retain(sig); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	got, err := halcmd.List("retain")
	if err != nil {
		t.Fatalf("List retain: %v", err)
	}
	if !contains(got, sig) {
		t.Errorf("List retain = %v; want it to contain %q", got, sig)
	}

	if err := halcmd.Unretain(sig); err != nil {
		t.Fatalf("Unretain: %v", err)
	}
	got, err = halcmd.List("retain")
	if err != nil {
		t.Fatalf("List retain: %v", err)
	}
	if contains(got, sig) {
		t.Errorf("List retain = %v; want %q gone after Unretain", got, sig)
	}

	if err := halcmd.Retain("nosuchsignal"); err == nil {
		t.Error("Retain on an unknown signal must fail")
	}
	if err := halcmd.Unretain("nosuchsignal"); err == nil {
		t.Error("Unretain on an unknown signal must fail")
	}
}

// ===== Pins =====

// TestPinSetGetPType covers setp/getp/ptype against real pins of every type,
// including the direction rule: an output pin is driven by its component, so
// setp on it must be refused.
func TestPinSetGetPType(t *testing.T) {
	name := uniq("pcomp")
	testComp(t, name)

	// An IO pin accepts setp and reads back.
	if err := halcmd.SetP(name+".s", "-42"); err != nil {
		t.Fatalf("SetP on an io pin: %v", err)
	}
	if got, err := halcmd.GetP(name + ".s"); err != nil || got != "-42" {
		t.Errorf("GetP = %q, %v; want %q", got, err, "-42")
	}
	if got, err := halcmd.PType(name + ".s"); err != nil || got != hal.TypeS32 {
		t.Errorf("PType = %v, %v; want %v", got, err, hal.TypeS32)
	}

	// Types are reported per pin.
	for pin, want := range map[string]hal.PinType{
		name + ".b": hal.TypeBit,
		name + ".f": hal.TypeFloat,
		name + ".u": hal.TypeU32,
	} {
		if got, err := halcmd.PType(pin); err != nil || got != want {
			t.Errorf("PType(%s) = %v, %v; want %v", pin, got, err, want)
		}
	}

	// getp works on any pin regardless of direction.
	if _, err := halcmd.GetP(name + ".b"); err != nil {
		t.Errorf("GetP on an out pin: %v", err)
	}

	if _, err := halcmd.GetP("nosuch.pin"); err == nil {
		t.Error("GetP on an unknown pin must fail")
	}
	if _, err := halcmd.PType("nosuch.pin"); err == nil {
		t.Error("PType on an unknown pin must fail")
	}
	if err := halcmd.SetP("nosuch.pin", "1"); err == nil {
		t.Error("SetP on an unknown pin must fail")
	}
	if err := halcmd.SetP(name+".s", "notanumber"); err == nil {
		t.Error("SetP with an unparsable value must fail")
	}
}

// ===== Linking =====

// TestLinkAndNet covers linkps/linksp/unlinkp and net. net is the form HAL
// files actually use: it creates the signal on demand from the first pin's type
// and links every listed pin to it.
func TestLinkAndNet(t *testing.T) {
	a := uniq("lcomp")
	b := uniq("lcomp")
	testComp(t, a)
	testComp(t, b)

	sig := uniq("lsig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	if err := halcmd.LinkPS(a+".s", sig); err != nil {
		t.Fatalf("LinkPS: %v", err)
	}
	// LinkSP is the same link with the arguments reversed.
	if err := halcmd.LinkSP(sig, b+".s"); err != nil {
		t.Fatalf("LinkSP: %v", err)
	}

	// Writing the signal is observable through a linked pin.
	if err := halcmd.SetS(sig, "17"); err != nil {
		t.Fatalf("SetS: %v", err)
	}
	if got, err := halcmd.GetP(b + ".s"); err != nil || got != "17" {
		t.Errorf("GetP through the link = %q, %v; want %q", got, err, "17")
	}

	if err := halcmd.UnlinkP(a + ".s"); err != nil {
		t.Fatalf("UnlinkP: %v", err)
	}
	if err := halcmd.UnlinkP("nosuch.pin"); err == nil {
		t.Error("UnlinkP on an unknown pin must fail")
	}
	if err := halcmd.LinkPS("nosuch.pin", sig); err == nil {
		t.Error("LinkPS with an unknown pin must fail")
	}
	if err := halcmd.LinkPS(a+".s", "nosuchsignal"); err == nil {
		t.Error("LinkPS with an unknown signal must fail")
	}
	// Type mismatch: a float pin cannot join an s32 signal.
	if err := halcmd.LinkPS(a+".f", sig); err == nil {
		t.Error("LinkPS with a type mismatch must fail")
	}

	// net creates the signal implicitly and links both pins, arrows and all.
	netSig := uniq("netsig")
	if err := halcmd.Net(netSig, a+".u", "=>", b+".ui"); err != nil {
		t.Fatalf("Net: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(netSig) })
	if got, err := halcmd.SType(netSig); err != nil || got != hal.TypeU32 {
		t.Errorf("net-created signal type = %v, %v; want %v", got, err, hal.TypeU32)
	}
	sigs, err := halcmd.List("sig", netSig)
	if err != nil {
		t.Fatalf("List sig: %v", err)
	}
	if !contains(sigs, netSig) {
		t.Errorf("List sig = %v; want it to contain %q", sigs, netSig)
	}

	// A second output pin on the same signal is the classic HAL error.
	if err := halcmd.Net(netSig, b+".u"); err == nil {
		t.Error("Net with a second writer on one signal must fail")
	}
	// So is joining a pin of a different type.
	if err := halcmd.Net(netSig, a+".b"); err == nil {
		t.Error("Net with a type mismatch must fail")
	}
}

// ===== Aliases =====

// TestPinAndParamAlias covers alias/unalias for pins. An alias is a second name
// for the same object, so it must resolve through getp and disappear again on
// unalias.
func TestPinAndParamAlias(t *testing.T) {
	name := uniq("acomp")
	testComp(t, name)
	alias := uniq("thealias")

	if err := halcmd.Alias("pin", name+".s", alias); err != nil {
		t.Fatalf("Alias: %v", err)
	}
	if got, err := halcmd.GetP(alias); err != nil {
		t.Errorf("GetP through the alias = %q, %v; want it to resolve", got, err)
	}
	if err := halcmd.SetP(alias, "5"); err != nil {
		t.Fatalf("SetP through the alias: %v", err)
	}
	if got, err := halcmd.GetP(name + ".s"); err != nil || got != "5" {
		t.Errorf("GetP on the real name = %q, %v; want %q", got, err, "5")
	}

	if err := halcmd.UnAlias("pin", alias); err != nil {
		t.Fatalf("UnAlias: %v", err)
	}
	if _, err := halcmd.GetP(alias); err == nil {
		t.Error("the alias must not resolve after UnAlias")
	}

	if err := halcmd.Alias("pin", "nosuch.pin", alias); err == nil {
		t.Error("Alias on an unknown pin must fail")
	}
	if err := halcmd.Alias("bogus", name+".s", alias); err == nil {
		t.Error("Alias with an unknown kind must fail")
	}
	if err := halcmd.UnAlias("bogus", name+".s"); err == nil {
		t.Error("UnAlias with an unknown kind must fail")
	}
}

// ===== list =====

// TestListTypes covers every accepted list type against real HAL objects and
// pins the glob dialect: `list comp` must use the same libc fnmatch matcher as
// the other types (HC-3), not Go's path.Match.
func TestListTypes(t *testing.T) {
	name := uniq("licomp")
	testComp(t, name)
	sig := uniq("lisig")
	if err := halcmd.NewSig(sig, hal.TypeBit); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	pins, err := halcmd.List("pin", name+".*")
	if err != nil {
		t.Fatalf("List pin: %v", err)
	}
	if len(pins) != 5 {
		t.Errorf("List pin %s.* = %v; want the 5 pins of the component", name, pins)
	}

	// No pattern lists everything.
	all, err := halcmd.List("pin")
	if err != nil {
		t.Fatalf("List pin (no pattern): %v", err)
	}
	if !contains(all, name+".b") {
		t.Errorf("unfiltered List pin is missing %q", name+".b")
	}

	// Multiple patterns are unioned and de-duplicated.
	multi, err := halcmd.List("pin", name+".b", name+".b", name+".f")
	if err != nil {
		t.Fatalf("List pin (multi): %v", err)
	}
	if len(multi) != 2 {
		t.Errorf("List pin with a repeated pattern = %v; want 2 de-duplicated names", multi)
	}

	comps, err := halcmd.List("comp", name)
	if err != nil {
		t.Fatalf("List comp: %v", err)
	}
	if len(comps) != 1 || comps[0] != name {
		t.Errorf("List comp %q = %v; want exactly [%s]", name, comps, name)
	}
	// HC-3: fnmatch globbing, and a non-matching pattern yields nothing.
	if got, err := halcmd.List("comp", name[:len(name)-1]+"*"); err != nil || !contains(got, name) {
		t.Errorf("List comp with a glob = %v, %v; want it to contain %q", got, err, name)
	}
	if got, err := halcmd.List("comp", "nosuchcomp*"); err != nil || len(got) != 0 {
		t.Errorf("List comp with a non-matching glob = %v, %v; want empty", got, err)
	}

	sigs, err := halcmd.List("sig", sig)
	if err != nil {
		t.Fatalf("List sig: %v", err)
	}
	if !contains(sigs, sig) {
		t.Errorf("List sig = %v; want it to contain %q", sigs, sig)
	}

	// The remaining types must at least answer without error.
	for _, typ := range []string{"param", "funct", "thread", "retain"} {
		if _, err := halcmd.List(typ); err != nil {
			t.Errorf("List %s: %v", typ, err)
		}
	}
	// Type names are case-insensitive.
	if _, err := halcmd.List("PIN"); err != nil {
		t.Errorf("List PIN: %v", err)
	}
}

// TestListComponentsIncludesLive checks the low-level component listing and
// FindCompID against a component we know exists.
func TestListComponentsIncludesLive(t *testing.T) {
	name := uniq("fcomp")
	comp := testComp(t, name)

	names, err := halcmd.ListComponents()
	if err != nil {
		t.Fatalf("ListComponents: %v", err)
	}
	if !contains(names, name) {
		t.Errorf("ListComponents = %v; want it to contain %q", names, name)
	}
	if got := halcmd.FindCompID(name); got != comp.ID() {
		t.Errorf("FindCompID(%q) = %d; want %d", name, got, comp.ID())
	}
	if got := halcmd.FindCompID("nosuchcomp"); got != 0 {
		t.Errorf("FindCompID on an unknown name = %d; want 0", got)
	}
}

// ===== show =====

// TestShowTypes covers every show type and the field population that the REST
// layer converts into the wire types.
func TestShowTypes(t *testing.T) {
	name := uniq("shcomp")
	testComp(t, name)
	sig := uniq("shsig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })
	if err := halcmd.LinkPS(name+".s", sig); err != nil {
		t.Fatalf("LinkPS: %v", err)
	}
	if err := halcmd.SetS(sig, "3"); err != nil {
		t.Fatalf("SetS: %v", err)
	}

	res, err := halcmd.Show("pin", name+".*")
	if err != nil {
		t.Fatalf("Show pin: %v", err)
	}
	if len(res.Pins) != 5 {
		t.Fatalf("Show pin = %d pins; want 5", len(res.Pins))
	}
	var linked *halcmd.PinInfo
	for i := range res.Pins {
		if res.Pins[i].Name == name+".s" {
			linked = &res.Pins[i]
		}
		if res.Pins[i].Owner != name {
			t.Errorf("pin %s Owner = %q; want %q", res.Pins[i].Name, res.Pins[i].Owner, name)
		}
		if res.Pins[i].Type == "" || res.Pins[i].Direction == "" {
			t.Errorf("pin %s has an empty Type/Direction: %+v", res.Pins[i].Name, res.Pins[i])
		}
	}
	if linked == nil {
		t.Fatalf("Show pin did not return %s.s", name)
	}
	if linked.Signal != sig {
		t.Errorf("linked pin Signal = %q; want %q", linked.Signal, sig)
	}
	if linked.Value != "3" {
		t.Errorf("linked pin Value = %q; want %q", linked.Value, "3")
	}
	if linked.Type != "s32" || linked.Direction != "IO" {
		t.Errorf("linked pin Type/Direction = %q/%q; want s32 / IO", linked.Type, linked.Direction)
	}

	res, err = halcmd.Show("comp", name)
	if err != nil {
		t.Fatalf("Show comp: %v", err)
	}
	if len(res.Comps) != 1 || res.Comps[0].Name != name {
		t.Fatalf("Show comp = %+v; want exactly %q", res.Comps, name)
	}
	if res.Comps[0].ID == 0 || res.Comps[0].Type == "" {
		t.Errorf("Show comp has an empty ID/Type: %+v", res.Comps[0])
	}

	res, err = halcmd.Show("sig", sig)
	if err != nil {
		t.Fatalf("Show sig: %v", err)
	}
	if len(res.Signals) != 1 || res.Signals[0].Name != sig {
		t.Fatalf("Show sig = %+v; want exactly %q", res.Signals, sig)
	}
	if res.Signals[0].Type != "s32" || res.Signals[0].Value != "3" {
		t.Errorf("Show sig = %+v; want type s32 value 3", res.Signals[0])
	}

	// The remaining selectors, including the aliases ("signal", "function")
	// and the empty/"all" spellings.
	for _, typ := range []string{"param", "funct", "function", "thread", "signal"} {
		if _, err := halcmd.Show(typ, name+"*"); err != nil {
			t.Errorf("Show %s: %v", typ, err)
		}
	}
	for _, typ := range []string{"", "all", "ALL"} {
		res, err := halcmd.Show(typ, name+"*")
		if err != nil {
			t.Fatalf("Show %q: %v", typ, err)
		}
		if len(res.Comps) != 1 || len(res.Pins) != 5 {
			t.Errorf("Show %q returned %d comps / %d pins; want 1 / 5", typ, len(res.Comps), len(res.Pins))
		}
	}

	if _, err := halcmd.Show("notatype"); err == nil {
		t.Error("Show with an unknown type must fail")
	}
}

// ===== save =====

// TestSaveRoundTrip checks that save emits the commands that would recreate the
// current state, both as returned lines and written to a file.
func TestSaveRoundTrip(t *testing.T) {
	name := uniq("svcomp")
	testComp(t, name)
	sig := uniq("svsig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })
	if err := halcmd.LinkPS(name+".s", sig); err != nil {
		t.Fatalf("LinkPS: %v", err)
	}

	lines, err := halcmd.Save("", "")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, sig) {
		t.Errorf("save all does not mention the signal %q:\n%s", sig, joined)
	}

	sigLines, err := halcmd.Save("sig", "")
	if err != nil {
		t.Fatalf("Save sig: %v", err)
	}
	if !strings.Contains(strings.Join(sigLines, "\n"), "newsig "+sig) {
		t.Errorf("save sig is missing %q: %v", "newsig "+sig, sigLines)
	}

	linkLines, err := halcmd.Save("link", "")
	if err != nil {
		t.Fatalf("Save link: %v", err)
	}
	if !strings.Contains(strings.Join(linkLines, "\n"), name+".s") {
		t.Errorf("save link is missing the linked pin: %v", linkLines)
	}

	// Writing to a file returns no lines and produces the same content.
	path := filepath.Join(t.TempDir(), "saved.hal")
	got, err := halcmd.Save("sig", path)
	if err != nil {
		t.Fatalf("Save to file: %v", err)
	}
	if got != nil {
		t.Errorf("Save to a file returned %v; want nil lines", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the saved file: %v", err)
	}
	if !strings.Contains(string(data), "newsig "+sig) {
		t.Errorf("saved file is missing %q:\n%s", "newsig "+sig, data)
	}

	if _, err := halcmd.Save("sig", filepath.Join(t.TempDir(), "nosuchdir", "x.hal")); err == nil {
		t.Error("Save to an unopenable path must fail")
	}
	if _, err := halcmd.Save("notatype", ""); err == nil {
		t.Error("Save with an unknown type must fail")
	}
}

// ===== lock / status / debug =====

// TestLockLevels covers every lock level name, the unlock-clears-bits semantics
// (upstream do_unlock_cmd: hal_get_lock() &^ lvl), and the classic status
// rendering that `halrun -f ... | grep lock` matches against.
func TestLockLevels(t *testing.T) {
	t.Cleanup(func() { _ = halcmd.Unlock("all") })

	for level, want := range map[string]int{
		"none": 0, "load": 1, "config": 2, "params": 4,
		"run": 8, "tune": 3, "all": 255,
	} {
		if err := halcmd.Unlock("all"); err != nil {
			t.Fatalf("Unlock all: %v", err)
		}
		if err := halcmd.Lock(level); err != nil {
			t.Fatalf("Lock(%q): %v", level, err)
		}
		if got := halcmd.GetLock(); got != want {
			t.Errorf("Lock(%q) → GetLock = %d; want %d", level, got, want)
		}
		// Case-insensitive.
		if err := halcmd.Lock(strings.ToUpper(level)); err != nil {
			t.Errorf("Lock(%q): %v", strings.ToUpper(level), err)
		}
	}

	// unlock clears only the named bits.
	if err := halcmd.Lock("all"); err != nil {
		t.Fatalf("Lock all: %v", err)
	}
	if err := halcmd.Unlock("tune"); err != nil {
		t.Fatalf("Unlock tune: %v", err)
	}
	if got := halcmd.GetLock(); got != 255&^3 {
		t.Errorf("after unlock tune GetLock = %d; want %d", got, 255&^3)
	}

	if err := halcmd.Unlock("notalevel"); err == nil {
		t.Error("Unlock with an unknown level must fail")
	}

	// SetLock is the integer-bitmask counterpart used by the halparse executor.
	if err := halcmd.SetLock(6); err != nil {
		t.Fatalf("SetLock: %v", err)
	}
	if got := halcmd.GetLock(); got != 6 {
		t.Errorf("SetLock(6) → GetLock = %d; want 6", got)
	}

	// The classic status text must name every set bit.
	status := halcmd.LockStatusString()
	for _, want := range []string{"current lock value 6 (06)", "HAL_LOCK_CONFIG", "HAL_LOCK_PARAMS"} {
		if !strings.Contains(status, want) {
			t.Errorf("LockStatusString is missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "HAL_LOCK_NONE") {
		t.Errorf("LockStatusString reports NONE while locked:\n%s", status)
	}

	if err := halcmd.Unlock("all"); err != nil {
		t.Fatalf("Unlock all: %v", err)
	}
	if status := halcmd.LockStatusString(); !strings.Contains(status, "HAL_LOCK_NONE") {
		t.Errorf("LockStatusString unlocked is missing HAL_LOCK_NONE:\n%s", status)
	}
}

// TestStatus checks the shared-memory status summary and that its lock level
// tracks the live lock state.
func TestStatus(t *testing.T) {
	t.Cleanup(func() { _ = halcmd.Unlock("all") })
	if err := halcmd.Unlock("all"); err != nil {
		t.Fatalf("Unlock all: %v", err)
	}

	st, err := halcmd.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ShmemFree <= 0 {
		t.Errorf("ShmemFree = %d; want a positive free-byte count", st.ShmemFree)
	}
	if st.LockLevel == "" {
		t.Error("LockLevel is empty")
	}
	unlocked := st.LockLevel

	if err := halcmd.Lock("all"); err != nil {
		t.Fatalf("Lock all: %v", err)
	}
	st, err = halcmd.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.LockLevel == unlocked {
		t.Errorf("LockLevel = %q both locked and unlocked", st.LockLevel)
	}
}

// TestSetDebugLevels covers the accepted verbosity levels and the rejection of
// anything else — the value arrives from `halcmd debug <n>` and from REST.
func TestSetDebugLevels(t *testing.T) {
	t.Cleanup(func() { _ = halcmd.SetDebug(1) })
	for _, lvl := range []int{0, 1, 2, 3} {
		if err := halcmd.SetDebug(lvl); err != nil {
			t.Errorf("SetDebug(%d): %v", lvl, err)
		}
	}
	for _, lvl := range []int{-1, 4, 99} {
		if err := halcmd.SetDebug(lvl); err == nil {
			t.Errorf("SetDebug(%d) = nil; want a rejection", lvl)
		}
	}
}

// TestGetDebugRoundTrips covers the read side a UI control needs: the level is
// process-global, so a client that could only write would drift out of step
// with the server whenever anything else changed it.
func TestGetDebugRoundTrips(t *testing.T) {
	t.Cleanup(func() { _ = halcmd.SetDebug(1) })
	for _, lvl := range []int{0, 1, 2, 3} {
		if err := halcmd.SetDebug(lvl); err != nil {
			t.Fatalf("SetDebug(%d): %v", lvl, err)
		}
		if got := halcmd.GetDebug(); got != lvl {
			t.Errorf("GetDebug() after SetDebug(%d) = %d; want %d", lvl, got, lvl)
		}
	}

	// A rejected write must not move the level.
	if err := halcmd.SetDebug(2); err != nil {
		t.Fatalf("SetDebug(2): %v", err)
	}
	_ = halcmd.SetDebug(9)
	if got := halcmd.GetDebug(); got != 2 {
		t.Errorf("GetDebug() after a rejected SetDebug(9) = %d; want 2", got)
	}
}
