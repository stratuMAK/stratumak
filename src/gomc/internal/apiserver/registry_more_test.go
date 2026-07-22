// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Registry coverage for the paths the module launcher depends on: consumer
// tracking (which decides whether a module can safely be unloaded), the
// unload sweep, the C-registration/upgrade handshake, and the package-level
// defaults every generated init() reaches through.
package apiserver

import (
	"encoding/json"
	"sync"
	"syscall"
	"testing"
	"unsafe"
)

func TestRegisterNoRESTLeavesMetaUnattached(t *testing.T) {
	meta := &APIMeta{Name: "noREST", Version: 3, RESTExport: true}
	RegisterMeta(meta)

	r := NewRegistry()
	// The plain Register attaches whatever meta was registered for name+version.
	if err := r.Register("noREST", 3, "a", fakeCallbacks); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := r.GetByAPI("noREST", "a"); got == nil || got.Meta != meta {
		t.Fatalf("Register did not attach the registered meta: %+v", got)
	}

	// RegisterNoREST must NOT: a C module's callbacks pointer is a C struct, and
	// letting the Go dispatch wrappers loose on it would be a wild pointer call.
	if err := r.RegisterNoREST("noREST", 3, "b", fakeCallbacks); err != nil {
		t.Fatalf("RegisterNoREST: %v", err)
	}
	got := r.GetByAPI("noREST", "b")
	if got == nil {
		t.Fatal("RegisterNoREST did not register the instance")
	}
	if got.Meta != nil {
		t.Error("RegisterNoREST attached REST meta — the Go dispatchers would be called with a C pointer")
	}
	if r.GetByAPI("noREST", "missing") != nil {
		t.Error("GetByAPI returned a value for an unregistered instance")
	}
}

func TestUpgradeReplacesCallbacksAndMeta(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterNoREST("up", 1, "i", fakeCallbacks); err != nil {
		t.Fatalf("RegisterNoREST: %v", err)
	}

	newCB := unsafe.Pointer(&struct{ x int }{})
	meta := &APIMeta{Name: "up", Version: 1, RESTExport: true}
	if err := r.Upgrade("up", 1, "i", newCB, meta); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	api := r.GetByAPI("up", "i")
	if api.Callbacks != newCB || api.Meta != meta {
		t.Errorf("Upgrade did not take effect: %+v", api)
	}

	// Upgrading something that was never registered must not create it — a Go
	// module taking over REST serving from a C registration that is not there
	// is a wiring bug, not a silent new registration.
	if err := r.Upgrade("up", 1, "nope", newCB, meta); err != syscall.ENOENT {
		t.Errorf("Upgrade of an unregistered instance = %v, want ENOENT", err)
	}
}

func TestOnRegisterFiresForExistingAndFuture(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("early", 1, "e", fakeCallbacks); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	r.OnRegister(func(api *RegisteredAPI) {
		mu.Lock()
		seen = append(seen, api.APIName+":"+api.Instance)
		mu.Unlock()
	})

	// Already-registered APIs must be replayed, otherwise a listener installed
	// after module load silently misses them.
	mu.Lock()
	if len(seen) != 1 || seen[0] != "early:e" {
		mu.Unlock()
		t.Fatalf("listener not replayed for existing APIs: %v", seen)
	}
	mu.Unlock()

	if err := r.Register("late", 1, "l", fakeCallbacks); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[1] != "late:l" {
		t.Fatalf("listener not fired for a new API: %v", seen)
	}
}

func TestConsumerTrackingAndUnregister(t *testing.T) {
	r := NewRegistry()
	for _, inst := range []string{"provider", "other"} {
		if err := r.Register("svc", 1, inst, fakeCallbacks); err != nil {
			t.Fatalf("Register(%q): %v", inst, err)
		}
	}

	// GetAPIFor records the lookup; GetAPIUntracked deliberately does not.
	if _, err := r.GetAPIFor("modA", "svc", "provider", 1); err != nil {
		t.Fatalf("GetAPIFor: %v", err)
	}
	if _, err := r.GetAPIUntracked("svc", "provider", 1); err != nil {
		t.Fatalf("GetAPIUntracked: %v", err)
	}
	if _, err := r.GetAPIFor("modB", "svc", "provider", 1); err != nil {
		t.Fatalf("GetAPIFor: %v", err)
	}
	// A repeated lookup must not duplicate the record.
	if _, err := r.GetAPIFor("modA", "svc", "provider", 1); err != nil {
		t.Fatalf("GetAPIFor: %v", err)
	}
	if _, err := r.GetAPIFor("modA", "svc", "other", 1); err != nil {
		t.Fatalf("GetAPIFor: %v", err)
	}

	if got := len(r.Consumers()); got != 3 {
		t.Errorf("Consumers() = %d records, want 3 (no duplicates)", got)
	}

	deps := r.ConsumersOfProvider("provider")
	if len(deps) != 2 {
		t.Errorf("ConsumersOfProvider = %v, want modA and modB", deps)
	}
	if got := r.ConsumersOfProvider("nobody"); got != nil {
		t.Errorf("ConsumersOfProvider for an unused provider = %v, want nil", got)
	}

	// A failed lookup must not be recorded — that would make an unrelated
	// module look like a dependency and block the provider's unload.
	if _, err := r.GetAPIFor("modC", "svc", "provider", 99); err != syscall.EINVAL {
		t.Errorf("version mismatch = %v, want EINVAL", err)
	}
	if _, err := r.GetAPIFor("modC", "svc", "gone", 1); err != syscall.ENOENT {
		t.Errorf("missing instance = %v, want ENOENT", err)
	}
	for _, c := range r.Consumers() {
		if c.ConsumerInstance == "modC" {
			t.Error("a failed lookup was recorded as a consumer")
		}
	}

	// Unloading a provider drops its registrations and the records naming it as
	// a consumer, but keeps records where it is the provider's dependent.
	if n := r.UnregisterByInstance("provider"); n != 1 {
		t.Errorf("UnregisterByInstance removed %d APIs, want 1", n)
	}
	if r.GetByAPI("svc", "provider") != nil {
		t.Error("the instance is still registered after UnregisterByInstance")
	}
	if n := r.UnregisterByInstance("modA"); n != 0 {
		t.Errorf("UnregisterByInstance for a pure consumer removed %d APIs, want 0", n)
	}
	for _, c := range r.Consumers() {
		if c.ConsumerInstance == "modA" {
			t.Error("modA's consumer records survived its unload")
		}
	}
	if n := r.UnregisterByInstance("never-registered"); n != 0 {
		t.Errorf("UnregisterByInstance for an unknown instance = %d, want 0", n)
	}
}

func TestAllAndGetAll(t *testing.T) {
	r := NewRegistry()
	if got := r.All(); len(got) != 0 {
		t.Errorf("All() on an empty registry = %v, want empty (not nil-panicking)", got)
	}
	for _, api := range []string{"a", "b"} {
		if err := r.Register(api, 1, "shared", fakeCallbacks); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if got := r.All(); len(got) != 2 {
		t.Errorf("All() = %d, want 2", len(got))
	}
	// Two APIs under one instance name: GetAll must return both, Get only one.
	if got := r.GetAll("shared"); len(got) != 2 {
		t.Errorf("GetAll = %d, want 2", len(got))
	}
	if r.Get("shared") == nil {
		t.Error("Get returned nil for a registered instance")
	}
	if got := r.GetAll("nope"); got != nil {
		t.Errorf("GetAll for an unknown instance = %v, want nil", got)
	}
}

func TestPackageLevelDefaults(t *testing.T) {
	origReg, origWatch, origServer := defaultRegistry, defaultWatchRegistry, defaultServer
	origPending := registryReadyCallbacks
	t.Cleanup(func() {
		defaultRegistry, defaultWatchRegistry, defaultServer = origReg, origWatch, origServer
		registryReadyCallbacks = origPending
	})

	defaultRegistry, registryReadyCallbacks = nil, nil

	// A callback registered before the launcher publishes the registry must be
	// queued and fired later — generated publish-drain hooks rely on this.
	var early *Registry
	OnDefaultRegistryReady(func(r *Registry) { early = r })
	if early != nil {
		t.Fatal("the callback fired before a registry was published")
	}

	reg := NewRegistry()
	SetDefaultRegistry(reg)
	if early != reg {
		t.Error("the queued callback did not fire on SetDefaultRegistry")
	}
	if DefaultRegistry() != reg {
		t.Error("DefaultRegistry did not return the published registry")
	}

	// Registering after the registry exists fires immediately.
	var late *Registry
	OnDefaultRegistryReady(func(r *Registry) { late = r })
	if late != reg {
		t.Error("a late callback did not fire immediately")
	}
	// ...and must not be queued a second time.
	if len(registryReadyCallbacks) != 0 {
		t.Errorf("registryReadyCallbacks = %d, want 0", len(registryReadyCallbacks))
	}

	wreg := NewWatchRegistry()
	SetDefaultWatchRegistry(wreg)
	if DefaultWatchRegistry() != wreg {
		t.Error("DefaultWatchRegistry did not return the published watch registry")
	}

	srv := NewServer(reg, "127.0.0.1:0")
	SetDefaultServer(srv)
	if DefaultServer() != srv {
		t.Error("DefaultServer did not return the published server")
	}
}

func TestMetaRegistry(t *testing.T) {
	meta := &APIMeta{Name: "metatest", Version: 7}
	RegisterMeta(meta)
	if GetMeta("metatest", 7) != meta {
		t.Error("GetMeta did not return the registered meta")
	}
	// The version is part of the key: a v7 meta must not answer a v8 lookup.
	if GetMeta("metatest", 8) != nil {
		t.Error("GetMeta matched across versions")
	}
	if GetMeta("nosuchapi", 1) != nil {
		t.Error("GetMeta returned a value for an unregistered API")
	}
}

func TestWatchAndStreamFactoryRegistries(t *testing.T) {
	wf := func(instance string, cb unsafe.Pointer) *WatchAPI {
		return &WatchAPI{APIName: "wf", Instance: instance}
	}
	RegisterWatchFactory("wf", wf)
	got := GetWatchFactory("wf")
	if got == nil {
		t.Fatal("GetWatchFactory returned nil for a registered factory")
	}
	if api := got("i", nil); api.Instance != "i" {
		t.Errorf("the factory produced %+v", api)
	}
	if GetWatchFactory("nosuch") != nil {
		t.Error("GetWatchFactory returned a value for an unregistered API")
	}

	RegisterStreamFactory("sf", func(instance string, cb unsafe.Pointer) StreamServer { return nil })
	if GetStreamFactory("sf") == nil {
		t.Fatal("GetStreamFactory returned nil for a registered factory")
	}
	if GetStreamFactory("nosuch") != nil {
		t.Error("GetStreamFactory returned a value for an unregistered API")
	}
}

// --- Push watches ---

func TestPushWatchStoresConvertedJSON(t *testing.T) {
	var gotSize int
	pw := NewPushWatch(func(data unsafe.Pointer, size int) (json.RawMessage, error) {
		gotSize = size
		return json.RawMessage(`{"v":1}`), nil
	})

	// Nothing pushed yet: the watch func returns nil, which pushLoop skips.
	if d, err := pw.WatchFunc(); err != nil || d != nil {
		t.Fatalf("WatchFunc before any push = %s, %v; want nil, nil", d, err)
	}

	payload := struct{ x int }{}
	if err := pw.Push(unsafe.Pointer(&payload), 4); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotSize != 4 {
		t.Errorf("converter saw size %d, want 4", gotSize)
	}
	d, err := pw.WatchFunc()
	if err != nil || string(d) != `{"v":1}` {
		t.Errorf("WatchFunc = %s, %v", d, err)
	}
}

func TestPushWatchConverterErrorLeavesLastGood(t *testing.T) {
	fail := false
	pw := NewPushWatch(func(unsafe.Pointer, int) (json.RawMessage, error) {
		if fail {
			return nil, syscall.EINVAL
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err := pw.Push(nil, 0); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// A failing conversion must be reported and must not overwrite the stored
	// snapshot with garbage — subscribers keep seeing the last good value.
	fail = true
	if err := pw.Push(nil, 0); err != syscall.EINVAL {
		t.Errorf("Push with a failing converter = %v, want EINVAL", err)
	}
	if d, _ := pw.WatchFunc(); string(d) != `{"ok":true}` {
		t.Errorf("stored data = %s, want the last good snapshot", d)
	}
}

func TestGetOrCreatePushWatch(t *testing.T) {
	// An API with no registered converter cannot get a PushWatch — returning a
	// half-built one would nil-panic on the first push from C.
	if pw := GetOrCreatePushWatch("noconv", "i", "f"); pw != nil {
		t.Error("GetOrCreatePushWatch created a watch without a converter")
	}
	if GetPushWatch("noconv", "i", "f") != nil {
		t.Error("a watch was cached for an API with no converter")
	}

	conv := func(unsafe.Pointer, int) (json.RawMessage, error) { return json.RawMessage(`1`), nil }
	RegisterPushConverter("convtest", conv)
	if GetPushConverter("convtest") == nil {
		t.Fatal("GetPushConverter returned nil for a registered converter")
	}
	if GetPushConverter("nosuch") != nil {
		t.Error("GetPushConverter returned a value for an unregistered API")
	}

	// The triple identifies the watch: same triple → same object (so the C
	// push side and the WS read side share one buffer), different triple → new.
	pw := GetOrCreatePushWatch("convtest", "i", "f")
	if pw == nil {
		t.Fatal("GetOrCreatePushWatch returned nil despite a registered converter")
	}
	if again := GetOrCreatePushWatch("convtest", "i", "f"); again != pw {
		t.Error("GetOrCreatePushWatch returned a different watch for the same triple")
	}
	if GetPushWatch("convtest", "i", "f") != pw {
		t.Error("GetPushWatch did not return the cached watch")
	}
	if other := GetOrCreatePushWatch("convtest", "i", "g"); other == pw {
		t.Error("two different funcs share one PushWatch")
	}
}
