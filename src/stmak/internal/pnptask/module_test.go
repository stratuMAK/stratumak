// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and a subsequent hal_init then fails — see pkg/hal's TestMain for the full
// rationale. Keeping one component alive lets each test create and destroy its
// own instance.
func TestMain(m *testing.M) {
	keep, err := hal.NewComponent("pnptask-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// compCounter gives every test its own HAL component name. Component names are
// process-global and only freed on Exit, so reusing one across tests would
// collide when a test fails before its cleanup runs.
var compCounter int

func testInstanceName(t *testing.T) string {
	t.Helper()
	compCounter++
	return fmt.Sprintf("pnpt%d", compCounter)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loadModule runs the real factory over an INI text and registers the teardown.
func loadModule(t *testing.T, text, instance string, args ...string) (*pnptaskModule, error) {
	t.Helper()
	ini, err := inifile.ParseString(text)
	if err != nil {
		t.Fatalf("parsing test INI: %v", err)
	}
	mod, err := factory(ini, testLogger(), instance, args)
	if mod != nil {
		t.Cleanup(mod.Destroy)
	}
	if err != nil {
		return nil, err
	}
	return mod.(*pnptaskModule), nil
}

func mustLoadModule(t *testing.T, text, instance string, args ...string) *pnptaskModule {
	t.Helper()
	m, err := loadModule(t, text, instance, args...)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return m
}

// hasPin reports whether a HAL pin, signal or param of that name exists.
func hasPin(name string) bool {
	_, ok := hal.LookupValue(name)
	return ok
}

func TestModuleRegistered(t *testing.T) {
	if !stmak.HasModule("pnptask") {
		t.Fatal(`stmak.HasModule("pnptask") = false; the module did not register itself`)
	}
}

// TestFactoryExportsPins walks the HAL interface of §5.2: the pins have to
// exist by the time the factory returns, because the "net" lines that wire them
// run right after the load line.
func TestFactoryExportsPins(t *testing.T) {
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+pnptaskSection+stationSections, name)

	if m.pickers != 1 || m.motInstance != defaultMotionInstance || m.persistInstance != "" {
		t.Errorf("load-arg defaults = pickers %d, motion %q, persist %q",
			m.pickers, m.motInstance, m.persistInstance)
	}

	want := []string{
		// Global.
		"estop-on", "machine-on", "machine-is-on", "auto-enable", "homed",
		"jog-x-pos", "jog-x-neg", "jog-y-pos", "jog-y-neg", "jog-z-pos", "jog-z-neg", "jog-speed",
		"process-step", "origin-id", "dest-id", "deadzone-select", "start-job", "busy",
		"error", "error-id", "error-reset",
		// Params.
		"pos-settle-time", "pick-settle-time", "release-time",
		// Picker 0.
		"picker.0.close", "picker.0.opened", "picker.0.closed", "picker.0.missing",
		"picker.0.manual-open", "picker.0.manual-close",
		"picker.0.pos-x", "picker.0.pos-y", "picker.0.x-offset", "picker.0.y-offset",
		// Stations, addressed by their INI id.
		"tray.10.tray-id", "tray.10.set-full", "tray.10.set-empty",
		"tray.10.z-offset", "tray.10.empty", "tray.10.full",
		"proc.20.z-offset", "proc.20.busy", "proc.20.has-material",
		"proc.20.release", "proc.20.released",
	}
	for _, p := range want {
		if full := name + "." + p; !hasPin(full) {
			t.Errorf("missing HAL pin/param %q", full)
		}
	}

	// A single-picker instance must not export the second picker: the pin
	// tree is how a config declares what hardware is there (D5).
	if hasPin(name + ".picker.1.close") {
		t.Error("picker.1 pins exported without pickers=2")
	}
	// Only the configured axes get jog pins.
	if hasPin(name + ".jog-a-pos") {
		t.Error("jog pins exported for an axis outside [TRAJ]COORDINATES")
	}

	// The RW params carry their INI values, which is the only thing that
	// seeds them (a param starts at the zero value of its type).
	for _, tc := range []struct {
		pin  string
		want float64
	}{
		{"pos-settle-time", 0.1},
		{"pick-settle-time", 0.2},
		{"release-time", 0.3},
		// The picker offsets have no INI keys by design (D3) — picker 0
		// defaults to 0/0 and is taught with halcmd setp.
		{"picker.0.x-offset", 0},
		{"picker.0.y-offset", 0},
	} {
		got, ok := hal.LookupValue(name + "." + tc.pin)
		if !ok {
			t.Errorf("param %q not found", tc.pin)
			continue
		}
		if got != tc.want {
			t.Errorf("param %q = %g, want %g", tc.pin, got, tc.want)
		}
	}
}

func TestFactoryTwoPickers(t *testing.T) {
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+pnptaskSection+stationSections, name,
		"pickers=2", "motion_instance=pnp.mot", "persist_instance=persist")

	if m.pickers != 2 || m.motInstance != "pnp.mot" || m.persistInstance != "persist" {
		t.Errorf("load args = pickers %d, motion %q, persist %q",
			m.pickers, m.motInstance, m.persistInstance)
	}
	for _, p := range []string{"picker.1.close", "picker.1.pos-x", "picker.1.x-offset"} {
		if full := name + "." + p; !hasPin(full) {
			t.Errorf("missing HAL pin/param %q", full)
		}
	}
}

// fakeCallbacks stands in for a provider's C callback table. Start only wraps
// the pointer in a typed client and never dereferences it, so a dummy non-nil
// value is enough — the same stand-in apiserver's own tests use.
var fakeCallbacks = unsafe.Pointer(&struct{}{})

// registerFakeMotion registers a motctl/motstat provider under the given
// instance name, the way motmod does when it loads.
func registerFakeMotion(t *testing.T, instance string) {
	t.Helper()
	prev := apiserver.DefaultRegistry()
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	t.Cleanup(func() { apiserver.SetDefaultRegistry(prev) })

	if err := reg.RegisterNoREST("motctl", motctlVersion, instance, fakeCallbacks); err != nil {
		t.Fatalf("registering fake motctl: %v", err)
	}
	if err := reg.RegisterNoREST("motstat", motstatVersion, instance, fakeCallbacks); err != nil {
		t.Fatalf("registering fake motstat: %v", err)
	}
}

// TestStopBeforeStart: the launcher stops every module it loaded, including
// ones whose Start never ran because a peer failed first.
func TestStopBeforeStart(t *testing.T) {
	setupPaths(t)
	m := mustLoadModule(t, trajSection+pnptaskSection+stationSections, testInstanceName(t),
		"motion_instance=pnp.mot")
	m.Stop()
	m.Stop() // and twice, for the launcher that is not sure either
}

// The successful half of Start — the configuration push and the control loop —
// is covered in machine_test.go against a scripted motion stack: past the
// callback-table lookup, Start is startControl, and a fake provider's callback
// table cannot be called through the C ABI.

// TestStartRequiresMotion covers the other half of resolving the motion stack
// in Start: a load line naming an instance that no motmod provides has to fail
// startup, not surface as a nil client at the first job.
func TestStartRequiresMotion(t *testing.T) {
	setupPaths(t)
	registerFakeMotion(t, "pnp.mot")
	m := mustLoadModule(t, trajSection+pnptaskSection+stationSections, testInstanceName(t),
		"motion_instance=typo.mot")
	err := m.Start()
	if err == nil {
		t.Fatal("Start succeeded with an unknown motion_instance")
	}
	if !strings.Contains(err.Error(), "motctl API lookup (typo.mot)") {
		t.Errorf("error = %v, want it to name the missing motctl provider", err)
	}
}

// TestStartRejectsVersionMismatch: a provider registered at another API version
// is refused rather than called through a mismatched ABI.
func TestStartRejectsVersionMismatch(t *testing.T) {
	setupPaths(t)
	prev := apiserver.DefaultRegistry()
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	t.Cleanup(func() { apiserver.SetDefaultRegistry(prev) })
	if err := reg.RegisterNoREST("motctl", motctlVersion+1, "pnp.mot", fakeCallbacks); err != nil {
		t.Fatalf("registering fake motctl: %v", err)
	}

	m := mustLoadModule(t, trajSection+pnptaskSection+stationSections, testInstanceName(t),
		"motion_instance=pnp.mot")
	if err := m.Start(); err == nil {
		t.Fatal("Start accepted a motctl provider of the wrong version")
	}
}

func TestFactoryErrors(t *testing.T) {
	setupPaths(t)
	good := trajSection + pnptaskSection + stationSections

	cases := []struct {
		name string
		ini  string
		args []string
		want string
	}{
		{name: "unknown argument", ini: good, args: []string{"picker=2"}, want: "unknown load argument"},
		{name: "argument without value", ini: good, args: []string{"pickers"}, want: "expected key=value"},
		{name: "three pickers", ini: good, args: []string{"pickers=3"}, want: "must be 1 or 2"},
		{name: "non-numeric pickers", ini: good, args: []string{"pickers=both"}, want: "must be 1 or 2"},
		{name: "empty motion instance", ini: good, args: []string{"motion_instance="}, want: "empty instance name"},
		{name: "bad config", ini: trajSection + pnptaskSection, want: "no stations configured"},
		{
			// The geometric validation runs at load too, so a station taught
			// outside the machine limits never reaches a job.
			name: "station outside the machine limits",
			ini: trajSection + pnptaskSection + `
[PNPTASK_PROC_0]
ID = 20
X = 700.0
Y = 200.0
Z_PICK = 5.0
`,
			want: "outside the outer limit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadModule(t, tc.ini, testInstanceName(t), tc.args...)
			if err == nil {
				t.Fatalf("factory succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestMotionInstanceFromINI covers the milltask-compatible fallback: the
// motion instance may come from [EMCMOT]MOTION_INSTANCE so a shared HAL file
// does not hardcode it, and the load arg still wins over the INI.
func TestMotionInstanceFromINI(t *testing.T) {
	setupPaths(t)
	text := trajSection + "[EMCMOT]\nMOTION_INSTANCE = ini.mot\n" + pnptaskSection + stationSections

	m := mustLoadModule(t, text, testInstanceName(t))
	if m.motInstance != "ini.mot" {
		t.Errorf("motion instance = %q, want the INI's %q", m.motInstance, "ini.mot")
	}

	m = mustLoadModule(t, text, testInstanceName(t), "motion_instance=arg.mot")
	if m.motInstance != "arg.mot" {
		t.Errorf("motion instance = %q, want the load arg's %q (arg wins over INI)", m.motInstance, "arg.mot")
	}
}

// TestFactoryWithoutINI covers the halrun case: no INI file at all. Every
// station and limit comes out of the INI, so this has to fail with a clear
// message rather than a nil dereference.
func TestFactoryWithoutINI(t *testing.T) {
	_, err := factory(nil, testLogger(), testInstanceName(t), nil)
	if err == nil || !strings.Contains(err.Error(), "requires an INI file") {
		t.Fatalf("factory(nil ini) = %v, want a 'requires an INI file' error", err)
	}
}

// TestFactoryFailureReleasesComponent makes sure a factory that fails *after*
// creating its HAL component releases it again: the launcher only tears down
// modules whose factory returned one, so an abandoned component would hold its
// name for the life of the process.
//
// The failure is provoked with an instance name long enough that the component
// still fits HAL's 127-character name limit but its longest pin no longer does.
func TestFactoryFailureReleasesComponent(t *testing.T) {
	setupPaths(t)
	name := strings.Repeat("p", 120)
	_, err := loadModule(t, trajSection+pnptaskSection+stationSections, name)
	if err == nil {
		t.Fatal("factory succeeded with an over-long instance name, want a pin creation error")
	}
	// The point of the test is the failure happening after NewComponent.
	if !strings.Contains(err.Error(), "creating pin") {
		t.Fatalf("error = %v, want a pin creation failure (the component was never created)", err)
	}
	// hal_init refuses a duplicate name, so this only succeeds if the failed
	// factory exited its component.
	comp, err2 := hal.NewComponent(name)
	if err2 != nil {
		t.Fatalf("the failed factory left its HAL component behind: %v", err2)
	}
	_ = comp.Exit()
}
