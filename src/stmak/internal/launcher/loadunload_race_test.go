// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// l3FakeModule is a no-op stmak.Module for the load/unload race test.
type l3FakeModule struct{}

func (l3FakeModule) Start() error { return nil }
func (l3FakeModule) Stop()        {}
func (l3FakeModule) Destroy()     {}

const l3RaceModName = "l3racetestmod"

func init() {
	// Registered once per test process; the name resolves to no .so, so
	// loadModuleNamed takes the Go-module path (no cgo/HAL needed).
	stmak.RegisterModule(l3RaceModName, func(*inifile.IniFile, *slog.Logger, string, []string) (stmak.Module, error) {
		return l3FakeModule{}, nil
	})
}

// TestLoadRace is the core L-3 regression: runtime module load is a supported
// production path (halcmd REST), and every REST request is its own goroutine,
// so loadModuleNamed runs concurrently with itself. Its `l.goModules = append(...)`
// plus the `goModules[len-1]` read below race the slice header when unsynchronized.
// modMu serializes the whole load. Removing the modMu Lock/Unlock from
// loadModuleNamed makes this fail under -race (verified by mutation).
func TestLoadRace(t *testing.T) {
	l := &Launcher{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	const loaders = 8
	const perLoader = 40

	var wg sync.WaitGroup
	for i := 0; i < loaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perLoader; j++ {
				if err := l.loadModuleNamed(l3RaceModName, "", nil); err != nil {
					t.Errorf("loadModuleNamed: unexpected error: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := len(l.goModules); got != loaders*perLoader {
		t.Errorf("loaded %d modules, want %d (lost appends indicate a race)", got, loaders*perLoader)
	}
}

// TestShutdownGate verifies the shuttingDown handshake: once doCleanup sets the
// gate (under modMu, which also waits for any in-flight load), further
// load/unload fail fast with ESHUTDOWN so the shutdown iterators run with no
// concurrent mutator.
func TestShutdownGate(t *testing.T) {
	l := &Launcher{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	if err := l.loadModuleNamed(l3RaceModName, "", nil); err != nil {
		t.Fatalf("pre-shutdown load failed: %v", err)
	}

	l.modMu.Lock()
	l.shuttingDown = true
	l.modMu.Unlock()

	if err := l.loadModuleNamed(l3RaceModName, "", nil); !errors.Is(err, syscall.ESHUTDOWN) {
		t.Errorf("post-shutdown load: got %v, want ESHUTDOWN", err)
	}
	if err := l.UnloadModule("anything"); !errors.Is(err, syscall.ESHUTDOWN) {
		t.Errorf("post-shutdown unload: got %v, want ESHUTDOWN", err)
	}

	// The iterators still run cleanly against the loaded module.
	l.stopGoModules()
	l.destroyGoModules()
}
