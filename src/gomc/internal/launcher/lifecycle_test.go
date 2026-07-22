// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
)

// countingModule records its lifecycle calls and reproduces the failure mode a
// second Stop() has in a real module: it closes a channel, so stopping twice
// panics with "close of closed channel" (mqttbridge's bridge.stop and
// milltask's mcode worker shutdown are exactly this shape).
type countingModule struct {
	mu       sync.Mutex
	starts   int
	stops    int
	destroys int
	startErr error // when set, Start reports this failure
	stopCh   chan struct{}
}

var _ gomc.Module = (*countingModule)(nil)

func newCountingModule() *countingModule {
	return &countingModule{stopCh: make(chan struct{})}
}

func (m *countingModule) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	return m.startErr
}

func (m *countingModule) Stop() {
	m.mu.Lock()
	m.stops++
	m.mu.Unlock()
	close(m.stopCh)
}

func (m *countingModule) Destroy() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.destroys++
}

func (m *countingModule) counts() (starts, stops, destroys int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts, m.stops, m.destroys
}

func testLauncher() *Launcher {
	return &Launcher{
		logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		shutdownCh: make(chan struct{}),
	}
}

// TestStopGoModules_WithoutStart is the L-4 contract test: the launcher stops
// every loaded Go module even when none of them was started (startGoModules
// returning an error mid-loop leaves the rest loaded-but-not-started, and
// goModule deliberately carries no `started` flag). gomc.Module.Stop is
// therefore specified to be safe without a preceding Start — this pins the
// launcher half of that contract.
func TestStopGoModules_WithoutStart(t *testing.T) {
	l := testLauncher()
	mods := []*countingModule{newCountingModule(), newCountingModule()}
	for i, m := range mods {
		l.goModules = append(l.goModules, &goModule{mod: m, name: string(rune('a' + i))})
	}

	l.stopGoModules()
	l.destroyGoModules()

	for i, m := range mods {
		starts, stops, destroys := m.counts()
		if starts != 0 {
			t.Errorf("module %d: started %d times, want 0", i, starts)
		}
		if stops != 1 {
			t.Errorf("module %d: stopped %d times, want exactly 1", i, stops)
		}
		if destroys != 1 {
			t.Errorf("module %d: destroyed %d times, want exactly 1", i, destroys)
		}
	}
}

// TestDoCleanup_HALUninitialized_StopsModulesOnce covers the shutdown path taken
// when startup failed before hal.NewComponent (halComp == nil). That branch used
// to re-run stopCModules/stopGoModules after the unconditional stops higher up
// in doCleanup, i.e. a second Stop() on every loaded module — which the
// lifecycle contract forbids and which panics for any module that closes its own
// stop channel. Mutation check: restoring the duplicate stopGoModules() call in
// cleanup.go makes this test panic.
func TestDoCleanup_HALUninitialized_StopsModulesOnce(t *testing.T) {
	l := testLauncher()
	m := newCountingModule()
	l.goModules = append(l.goModules, &goModule{mod: m, name: "gomod"})

	l.doCleanup() // halComp == nil, ini == nil, apiServer == nil, retain == nil

	starts, stops, destroys := m.counts()
	if starts != 0 || stops != 1 || destroys != 1 {
		t.Errorf("lifecycle calls = (start %d, stop %d, destroy %d), want (0, 1, 1)",
			starts, stops, destroys)
	}
}

// TestWatchSignals_ExitsOnShutdown is the L-6 regression: the signal watcher
// used to block forever on <-sigCh, so it outlived the shutdown it was watching
// for and left signal.Notify delivering into a channel nobody reads — a leaked
// goroutine plus a leaked runtime signal registration per Launcher (visible as
// soon as two are built in one process, e.g. in tests). It now also selects on
// shutdownCh and calls signal.Stop on the way out.
func TestWatchSignals_ExitsOnShutdown(t *testing.T) {
	l := testLauncher()
	done := l.watchSignals()

	select {
	case <-done:
		t.Fatal("watcher exited before shutdown was signalled")
	case <-time.After(20 * time.Millisecond):
	}

	l.shutdown()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher still running 2s after shutdown — signal registration leaked")
	}
}

// TestDoCleanup_ReleasesSignalWatcher checks the other half of L-6: cleanup
// reached without the shutdown channel ever being closed (a startup error
// return, or halrun finishing its one-shot file) must still release the watcher.
func TestDoCleanup_ReleasesSignalWatcher(t *testing.T) {
	l := testLauncher()
	done := l.watchSignals()

	l.doCleanup()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher still running after doCleanup")
	}
}

// TestStopAPIServer_IdempotentAndConcurrent is the L-5 regression: l.apiServer
// was written and nil'd with no synchronisation, safe only because of the
// current start-before-stop call order. stopAPIServer now takes the server out
// of the field under apiMu, so a second (or concurrent) caller shuts nothing
// down twice and no reader can observe a torn field.
func TestStopAPIServer_IdempotentAndConcurrent(t *testing.T) {
	apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	// An ephemeral port: createAPIServer binds for real, and a unit test must
	// not fail because this machine happens to be running LinuxCNC on the
	// configured one (nor collide with a second test run).
	t.Setenv("GMC_REST_ADDR", "127.0.0.1:0")
	l := testLauncher()
	if err := l.createAPIServer(); err != nil {
		t.Fatalf("createAPIServer: %v", err)
	}

	if l.apiServerRef() == nil {
		t.Fatal("createAPIServer did not install a server")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.apiServerRef()
			l.stopAPIServer()
		}()
	}
	wg.Wait()

	if got := l.apiServerRef(); got != nil {
		t.Errorf("apiServer = %v after stop, want nil", got)
	}
	l.stopAPIServer() // still a no-op
}

// TestStartGoModules_PartialFailure is the fault path the whole Stop-without-
// Start contract exists for: a module's Start() fails, so startGoModules bails
// mid-loop and the modules after it are loaded-but-never-started. The launcher
// must still tear all of them down — exactly once each — rather than leaking the
// ones it did start or double-stopping the ones it did not.
func TestStartGoModules_PartialFailure(t *testing.T) {
	l := testLauncher()
	first, boom, never := newCountingModule(), newCountingModule(), newCountingModule()
	boom.startErr = errors.New("no device")
	for i, m := range []*countingModule{first, boom, never} {
		l.goModules = append(l.goModules, &goModule{mod: m, name: string(rune('a' + i))})
	}

	err := l.startGoModules()
	if err == nil {
		t.Fatal("startGoModules returned nil despite a failing module")
	}
	if !strings.Contains(err.Error(), "no device") || !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("error = %q, want it to name the failing module and its cause", err)
	}
	if starts, _, _ := never.counts(); starts != 0 {
		t.Errorf("module after the failure was started %d times, want 0", starts)
	}

	l.doCleanup()

	for name, m := range map[string]*countingModule{"started": first, "failed": boom, "never-started": never} {
		if _, stops, destroys := m.counts(); stops != 1 || destroys != 1 {
			t.Errorf("%s module: (stop %d, destroy %d), want (1, 1)", name, stops, destroys)
		}
	}
}

// TestFail_FirstErrorWinsAndTriggersShutdown covers the fatal-runtime-error path
// (the REST server dying): the error must reach Run's return value so the
// process exits non-zero instead of lingering as a headless zombie, the FIRST
// error must win, and the ordered shutdown must be triggered exactly once.
func TestFail_FirstErrorWinsAndTriggersShutdown(t *testing.T) {
	l := testLauncher()
	firstErr := errors.New("REST API server: bind: address already in use")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 0 {
				l.fail(firstErr)
				return
			}
			<-l.shutdownCh // let the first one land before piling on
			l.fail(errors.New("later error"))
		}(i)
	}
	wg.Wait()

	select {
	case <-l.shutdownCh:
	default:
		t.Fatal("fail() did not trigger shutdown")
	}
	l.fatalMu.Lock()
	got := l.fatalErr
	l.fatalMu.Unlock()
	if !errors.Is(got, firstErr) {
		t.Errorf("fatalErr = %v, want the first error %v", got, firstErr)
	}
}

// TestCreateAPIServer_BindFailureIsReported pins where a taken REST address is
// discovered. The bind used to happen inside the serve goroutine at the very
// end of startup, so a port conflict surfaced only after realtime was running,
// motmod was loaded, HAL threads were started and the interpreter was up — a
// fully started machine, torn straight back down. createAPIServer binds, so
// Run() can refuse before touching realtime.
func TestCreateAPIServer_BindFailureIsReported(t *testing.T) {
	apiserver.SetDefaultRegistry(apiserver.NewRegistry())

	// Hold an address, then point a launcher at it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	t.Setenv("GMC_REST_ADDR", blocker.Addr().String())

	l := testLauncher()
	err = l.createAPIServer()
	if err == nil {
		l.stopAPIServer()
		t.Fatal("createAPIServer on a taken address succeeded, want a bind error")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("error = %v, want it to name the bind failure", err)
	}
	if l.apiListenerRef() != nil {
		t.Error("a failed bind left a listener installed")
	}
}

// TestCreateAPIServer_ListenerClosedOnStop pins that a bind which never got as
// far as being served is still released. http.Server.Shutdown only knows about
// listeners it is serving, so without an explicit close the socket would
// outlive a launcher that failed a later startup step — and the next start
// would hit "address already in use" caused by its own predecessor.
func TestCreateAPIServer_ListenerClosedOnStop(t *testing.T) {
	apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	t.Setenv("GMC_REST_ADDR", "127.0.0.1:0")

	l := testLauncher()
	if err := l.createAPIServer(); err != nil {
		t.Fatalf("createAPIServer: %v", err)
	}
	ln := l.apiListenerRef()
	if ln == nil {
		t.Fatal("createAPIServer installed no listener")
	}
	addr := ln.Addr().String()

	l.stopAPIServer() // never served

	if l.apiListenerRef() != nil {
		t.Error("stopAPIServer left the listener installed")
	}
	// The address must be free again — the real proof, rather than trusting
	// that Close was called.
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address still held after stop: %v", err)
	}
	_ = again.Close()
}
