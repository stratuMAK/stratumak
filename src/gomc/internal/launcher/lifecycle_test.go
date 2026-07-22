// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"log/slog"
	"os"
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
	return nil
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
	l := testLauncher()
	l.createAPIServer()

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
