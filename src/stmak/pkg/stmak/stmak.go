// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package stratuMAK provides the registration interface for Go modules compiled
// into the stmakd binary. External Go packages and in-tree modules use
// this package to register themselves at init() time so the launcher can
// instantiate them when a HAL "load" command references their name.
//
// This replaces the old plugin-based gomodule.Factory mechanism — modules are
// now compiled directly into the server binary instead of loaded as .so plugins.
package stmak

import (
	"log/slog"
	"sync"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// Module is the lifecycle interface for Go modules compiled into the server.
// Mirrors the cmod lifecycle: factory → Start → Stop → Destroy.
//
// Lifecycle contract (the launcher relies on all four points):
//
//   - The factory returns a fully constructed module: every resource Stop and
//     Destroy touch must already exist when the factory returns, because a
//     later module's load failure aborts startup and the launcher then tears
//     down everything already loaded.
//   - Start is called once, after HAL threads are running. Returning an error
//     aborts startup.
//   - Stop must be safe to call WITHOUT a preceding (or successful) Start:
//     the launcher stops every loaded module even when a peer's Start failed
//     first, so it does not track a per-module started flag (goModule has no
//     `started` field, unlike cModule). Implementations either make Stop a
//     no-op or guard the teardown on their own started/running state — see
//     halscope's saverStarted, stress_gc's startedOK, classicladder's modbus
//     running flags, and the monitor/sequencer/poslog nil-and-running guards
//     in milltask.
//   - Stop is called at most once, and always before Destroy. The shutdown
//     path (stopGoModules) and the runtime-unload path (unloadGoModule, which
//     removes the module from l.goModules) both run under the launcher's modMu
//     and the shuttingDown gate, so the two can never stop the same module —
//     a bare close(stopCh) in Stop is legal. Making Stop idempotent
//     (sync.Once) is still cheap insurance, as the ADS server does.
type Module interface {
	Start() error
	Stop()
	Destroy()
}

// Factory creates a new Module instance. Called by the launcher when a HAL
// "load" command references the registered module name.
type Factory func(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (Module, error)

var (
	mu        sync.RWMutex
	factories = make(map[string]Factory)
)

// RegisterModule registers a Go module factory by name. Called from init() of
// compiled-in packages. Panics on duplicate registration.
func RegisterModule(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		panic("stratuMAK: duplicate module registration: " + name)
	}
	factories[name] = factory
}

// GetFactory returns the factory for the named module, or nil if not registered.
func GetFactory(name string) Factory {
	mu.RLock()
	defer mu.RUnlock()
	return factories[name]
}

// HasModule returns true if a module with the given name is registered.
func HasModule(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[name]
	return ok
}
