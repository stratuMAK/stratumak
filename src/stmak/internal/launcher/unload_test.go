// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// loadFake installs a countingModule under the given instance name, the way a
// runtime REST load would leave it.
func loadFake(l *Launcher, name string) *countingModule {
	m := newCountingModule()
	l.goModules = append(l.goModules, &goModule{mod: m, name: name})
	return m
}

func TestUnloadModule_NotFound(t *testing.T) {
	l := testLauncher()
	if err := l.UnloadModule("nosuchmodule"); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("UnloadModule(unknown) = %v, want ENOENT", err)
	}
}

// TestUnloadGoModule_StopsDestroysAndRemoves walks the whole runtime-unload
// path for a Go module: stop, unregister its APIs (REST *and* watch — the HJ-1
// fix, since a WatchAPI closure captures pins that Destroy frees), destroy,
// remove from the slice. The neighbouring module must be untouched.
func TestUnloadGoModule_StopsDestroysAndRemoves(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	watch := apiserver.NewWatchRegistry()
	apiserver.SetDefaultWatchRegistry(watch)

	l := testLauncher()
	victim := loadFake(l, "victim")
	bystander := loadFake(l, "bystander")

	if err := reg.RegisterNoREST("victim_api", 1, "victim", nil); err != nil {
		t.Fatalf("RegisterNoREST: %v", err)
	}

	if err := l.UnloadModule("victim"); err != nil {
		t.Fatalf("UnloadModule(victim): %v", err)
	}

	if _, stops, destroys := victim.counts(); stops != 1 || destroys != 1 {
		t.Errorf("victim lifecycle = (stop %d, destroy %d), want (1, 1)", stops, destroys)
	}
	if _, stops, destroys := bystander.counts(); stops != 0 || destroys != 0 {
		t.Errorf("bystander lifecycle = (stop %d, destroy %d), want (0, 0)", stops, destroys)
	}
	if len(l.goModules) != 1 || l.goModules[0].name != "bystander" {
		t.Errorf("goModules after unload = %v, want just [bystander]", moduleNames(l))
	}
	if l.isModuleLoaded("victim") {
		t.Error("isModuleLoaded(victim) still true after unload")
	}
	if got := reg.GetAll("victim"); len(got) != 0 {
		t.Errorf("victim's API registration survived unload: %d entries", len(got))
	}

	// A second unload of the same name is ENOENT, not a double Stop.
	if err := l.UnloadModule("victim"); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("second UnloadModule(victim) = %v, want ENOENT", err)
	}
	if _, stops, _ := victim.counts(); stops != 1 {
		t.Errorf("victim stopped %d times across two unloads, want 1", stops)
	}
}

// TestUnloadModule_BusyWithLiveConsumer covers the dependency guard: a module
// whose APIs another LOADED module consumes cannot be unloaded (its Destroy
// would pull the callbacks out from under the consumer).
func TestUnloadModule_BusyWithLiveConsumer(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())

	l := testLauncher()
	provider := loadFake(l, "provider")
	loadFake(l, "consumer")
	reg.RecordConsumer("consumer", "some_api", "provider")

	err := l.UnloadModule("provider")
	if !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("UnloadModule(provider) = %v, want EBUSY", err)
	}
	if _, stops, destroys := provider.counts(); stops != 0 || destroys != 0 {
		t.Errorf("a refused unload still ran lifecycle calls (stop %d, destroy %d)", stops, destroys)
	}
	if !l.isModuleLoaded("provider") {
		t.Error("provider was removed despite the refusal")
	}
}

// TestUnloadModule_ConsumerRecordsThatDoNotBlock pins the two filters on the
// guard: a self-reference, and a consumer that is no longer loaded (its record
// outlives it), must not make the module permanently un-unloadable.
func TestUnloadModule_ConsumerRecordsThatDoNotBlock(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())

	l := testLauncher()
	loadFake(l, "provider")
	reg.RecordConsumer("provider", "own_api", "provider") // self
	reg.RecordConsumer("ghost", "some_api", "provider")   // never loaded

	if err := l.UnloadModule("provider"); err != nil {
		t.Fatalf("UnloadModule(provider) = %v, want success (self + stale consumer records)", err)
	}
}

func moduleNames(l *Launcher) []string {
	var out []string
	for _, gm := range l.goModules {
		out = append(out, gm.name)
	}
	return out
}

// --------------------------------------------------------------------------
// REST address / WebSocket origin resolution
// --------------------------------------------------------------------------

func iniWith(t *testing.T, content string) *inifile.IniFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.ini")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing INI: %v", err)
	}
	ini, err := inifile.Parse(path)
	if err != nil {
		t.Fatalf("parsing INI: %v", err)
	}
	return ini
}

func TestResolveRESTAddr_Precedence(t *testing.T) {
	l := testLauncher()

	if got := l.resolveRESTAddr(); got != defaultRESTAddr {
		t.Errorf("no INI, no env: got %q, want the loopback default %q", got, defaultRESTAddr)
	}

	l.ini = iniWith(t, "[GMC]\nREST_ADDR = 127.0.0.1:6001\n")
	if got, want := l.resolveRESTAddr(), "127.0.0.1:6001"; got != want {
		t.Errorf("INI only: got %q, want %q", got, want)
	}

	// The env override exists so the test harness can run several servers on
	// distinct ports without editing per-config INIs — it must win over the INI.
	t.Setenv("GMC_REST_ADDR", "127.0.0.1:6002")
	if got, want := l.resolveRESTAddr(), "127.0.0.1:6002"; got != want {
		t.Errorf("env over INI: got %q, want %q", got, want)
	}
}

// TestResolveWSOriginPatterns covers the N1 security default: an empty
// allow-list means same-origin only, which is what blocks cross-site WebSocket
// hijacking of a machine-controlling endpoint. Anything else is opt-in.
func TestResolveWSOriginPatterns(t *testing.T) {
	l := testLauncher()

	if got := l.resolveWSOriginPatterns(); got != nil {
		t.Errorf("no INI, no env: got %v, want nil (same-origin only)", got)
	}

	l.ini = iniWith(t, "[GMC]\nREST_ORIGINS = hmi.local, *.shop.example ,\n")
	want := []string{"hmi.local", "*.shop.example"}
	if got := l.resolveWSOriginPatterns(); !reflect.DeepEqual(got, want) {
		t.Errorf("INI list: got %v, want %v (trimmed, empty entries dropped)", got, want)
	}

	t.Setenv("GMC_REST_ORIGINS", "*")
	if got := l.resolveWSOriginPatterns(); !reflect.DeepEqual(got, []string{"*"}) {
		t.Errorf("env override: got %v, want [*]", got)
	}
}

// --------------------------------------------------------------------------
// Startup helpers
// --------------------------------------------------------------------------

func TestInitHalibPath(t *testing.T) {
	l := testLauncher()
	l.opts.HalLibDirs = []string{"/first", "/second"}
	l.initHalibPath()

	// -H directories are prepended in reverse order of the loop, so the LAST
	// -H wins the search; "." always stays last (the config dir fallback).
	want := "/second:/first:."
	if l.halibPath != want {
		t.Errorf("halibPath = %q, want %q", l.halibPath, want)
	}
}

func TestSetConfigEnv(t *testing.T) {
	t.Setenv("INI_FILE_NAME", "")
	t.Setenv("CONFIG_DIR", "")

	l := testLauncher()
	l.opts.IniFile = "/opt/machine/cfg/mill.ini"
	if err := l.setConfigEnv(); err != nil {
		t.Fatalf("setConfigEnv: %v", err)
	}
	if got := os.Getenv("INI_FILE_NAME"); got != l.opts.IniFile {
		t.Errorf("INI_FILE_NAME = %q, want %q", got, l.opts.IniFile)
	}
	if got, want := os.Getenv("CONFIG_DIR"), "/opt/machine/cfg"; got != want {
		t.Errorf("CONFIG_DIR = %q, want %q", got, want)
	}
}

// --------------------------------------------------------------------------
// halrun command-line handling
// --------------------------------------------------------------------------

func TestParseHalArgs(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"plain", "loadrt and2 count=3", []string{"loadrt", "and2", "count=3"}},
		{"tabs and runs of spaces", "net  a\tb   c", []string{"net", "a", "b", "c"}},
		{"double quotes keep spaces", `setp x.name "a b"`, []string{"setp", "x.name", "a b"}},
		{"single quotes keep spaces", `setp x.name 'a b'`, []string{"setp", "x.name", "a b"}},
		{"the other quote is literal inside", `setp x "it's"`, []string{"setp", "x", "it's"}},
		{"comment ends the line", "loadrt and2 # why not", []string{"loadrt", "and2"}},
		{"comment attached to a token", "loadrt and2#c", []string{"loadrt", "and2"}},
		{"hash inside quotes is data", `setp x "a#b"`, []string{"setp", "x", "a#b"}},
		{"empty", "   ", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseHalArgs(tc.line); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseHalArgs(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestHalrunDispatch_RejectsUserspaceVerbs pins the intentional removal: stmak
// has no userspace HAL components, so loadusr/waitusr must fail with an error
// that says what to do instead, rather than being silently ignored.
func TestHalrunDispatch_RejectsUserspaceVerbs(t *testing.T) {
	l := testLauncher()
	for _, verb := range []string{"loadusr", "waitusr"} {
		err := l.halrunDispatch(verb + " halsampler -c 1")
		if err == nil {
			t.Fatalf("halrunDispatch(%s) returned no error", verb)
		}
		if got := err.Error(); !strings.Contains(got, "not supported") || !strings.Contains(got, "load/loadrt") {
			t.Errorf("halrunDispatch(%s) error = %q, want it to name the replacement", verb, got)
		}
	}
	// An empty line is a no-op, not an error.
	if err := l.halrunDispatch("   "); err != nil {
		t.Errorf("halrunDispatch(blank) = %v, want nil", err)
	}
}
