// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package launcher — gomodules.go handles loading, lifecycle management of
// Go modules compiled into the stmakd binary.
//
// Go modules register themselves at init() time via stmak.RegisterModule().
// When a HAL "load" command references a module name that is not found as a
// cmod .so, the launcher looks it up in the stratuMAK registry and calls the
// registered factory.
package launcher

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// goModule wraps a stmak.Module instance for lifecycle management.
type goModule struct {
	mod  stmak.Module
	name string
}

// loadGoModule looks up a compiled-in Go module by registryName in the stratuMAK
// registry, calls its factory with instanceName, and appends the module to
// l.goModules. instanceName is the alias from the HAL file (or the module
// name if no alias was given).
//
// The factory is expected to create and fully initialize the module (including
// HAL component/pin creation) before returning.
func (l *Launcher) loadGoModule(registryName string, instanceName string, args []string) error {
	factory := stmak.GetFactory(registryName)
	if factory == nil {
		return fmt.Errorf("no Go module %q in registry", registryName)
	}

	l.logger.Info("loading Go module", "registry", registryName, "instance", instanceName)

	mod, err := factory(l.ini, l.logger, instanceName, args)
	if err != nil {
		return fmt.Errorf("load Go module %q (instance %q): %w", registryName, instanceName, err)
	}

	l.goModules = append(l.goModules, &goModule{
		mod:  mod,
		name: instanceName,
	})
	l.logger.Info("Go module loaded and initialized", "registry", registryName, "instance", instanceName)

	return nil
}

// startGoModules calls Start() on all loaded Go modules.
// Called after HAL threads are started, matching the protocol start sequence.
func (l *Launcher) startGoModules() error {
	for _, gm := range l.goModules {
		if err := gm.mod.Start(); err != nil {
			return fmt.Errorf("starting Go module %q: %w", gm.name, err)
		}
	}
	return nil
}

// stopGoModules calls Stop() on all loaded Go modules in reverse order.
// Called during cleanup before Destroy.
//
// There is deliberately no started-guard here (goModule has no `started` field,
// unlike cModule): when startGoModules fails mid-loop the later modules are
// loaded-but-not-started and must still be stopped, so stmak.Module.Stop is
// specified to be safe without a preceding Start (see the lifecycle contract on
// stmak.Module — every in-tree implementation was audited against it).
func (l *Launcher) stopGoModules() {
	l.modMu.Lock()
	snapshot := l.goModules
	l.modMu.Unlock()
	for i := len(snapshot) - 1; i >= 0; i-- {
		snapshot[i].mod.Stop()
	}
}

// destroyGoModules calls Destroy() on all loaded Go modules in reverse order.
// Called after all modules have been stopped to release resources.
func (l *Launcher) destroyGoModules() {
	// Snapshot and clear under modMu (see destroyCModules) so a straggler REST
	// unload cannot double-destroy.
	l.modMu.Lock()
	snapshot := l.goModules
	l.goModules = nil
	l.modMu.Unlock()
	for i := len(snapshot) - 1; i >= 0; i-- {
		snapshot[i].mod.Destroy()
	}
}
