// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package launcher — unload.go implements per-module unload (halcmd unload).
package launcher

import (
	"fmt"
	"syscall"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	halcmd "github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
)

// UnloadModule unloads a single module by instance name.  The module is
// removed from the function lists, stopped, its APIs unregistered, destroyed,
// and (for cmods) dlclosed if no other instances share the same .so.
//
// Returns EBUSY if another loaded module depends on this module's APIs.
// Returns ENOENT if no module with the given name is found.
func (l *Launcher) UnloadModule(name string) error {
	// Serialize against concurrent REST load/unload and shutdown. isModuleLoaded,
	// unloadCModule, and unloadGoModule below run with modMu held (they are
	// caller-holds-modMu helpers — they must NOT re-lock it).
	l.modMu.Lock()
	defer l.modMu.Unlock()
	if l.shuttingDown {
		return fmt.Errorf("cannot unload %q: shutting down: %w", name, syscall.ESHUTDOWN)
	}

	// Check API dependency guard.
	reg := apiserver.DefaultRegistry()
	if reg != nil {
		consumers := reg.ConsumersOfProvider(name)
		// Filter out the module itself and modules that are no longer loaded.
		var active []string
		for _, c := range consumers {
			if c == name {
				continue
			}
			if l.isModuleLoaded(c) {
				active = append(active, c)
			}
		}
		if len(active) > 0 {
			return fmt.Errorf("cannot unload %q: APIs still consumed by %v: %w",
				name, active, syscall.EBUSY)
		}
	}

	// Try cmod first, then gomod.
	if err := l.unloadCModule(name); err == nil {
		return nil
	}
	if err := l.unloadGoModule(name); err == nil {
		return nil
	}

	return fmt.Errorf("module %q not found: %w", name, syscall.ENOENT)
}

// unregisterModuleAPIs removes every API registration for the given instance
// from BOTH the REST registry and the watch registry. It must run before the
// module is destroyed: a WatchAPI's Factory/Watch closures capture the module's
// HAL pins, so a registration left behind after Destroy frees those pins lets a
// later WS subscribe resolve it and read freed/recycled memory (and leaks the
// entry). The REST Registry was already unregistered here historically; the
// watch registry had no unregister at all, so its entries survived unload.
func unregisterModuleAPIs(name string) {
	if reg := apiserver.DefaultRegistry(); reg != nil {
		reg.UnregisterByInstance(name)
	}
	if wr := apiserver.DefaultWatchRegistry(); wr != nil {
		wr.UnregisterByInstance(name)
	}
}

// halCompID resolves a HAL component id by name, or 0 when HAL was never
// initialised.
//
// The guard is not cosmetic: halcmd.FindCompID goes straight into
// halpr_find_comp_by_name, which dereferences hal_data — NULL until the first
// hal_init — so calling it without HAL is a SIGSEGV, not an error return. The
// unload hooks are wired into halrest at the very top of Run(), before HAL
// comes up, and no HAL also means there are no RT functions to remove.
func (l *Launcher) halCompID(name string) int {
	if l.halComp == nil {
		return 0
	}
	return halcmd.FindCompID(name)
}

// isModuleLoaded returns true if a module with the given instance name is
// currently loaded (either as cmod or gomod).
// Caller must hold modMu (called only from UnloadModule).
func (l *Launcher) isModuleLoaded(name string) bool {
	for _, cm := range l.cModules {
		if cm.name == name {
			return true
		}
	}
	for _, gm := range l.goModules {
		if gm.name == name {
			return true
		}
	}
	return false
}

// unloadCModule unloads a single C plugin module.
// Caller must hold modMu (called only from UnloadModule).
func (l *Launcher) unloadCModule(name string) error {
	idx := -1
	for i, cm := range l.cModules {
		if cm.name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return syscall.ENOENT
	}

	cm := l.cModules[idx]

	// Step 1: Remove RT functions from threads. Skipped when HAL was never
	// initialised — see unloadGoModule for why the guard is needed at all.
	compID := l.halCompID(name)
	if compID > 0 {
		removed, _ := halcmd.DelFunctsByComp(compID)
		if removed > 0 {
			// Step 2: Wait for cycle barrier.
			baseline := halcmd.GetMaxCycleCount()
			if err := halcmd.WaitCycleAdvance(baseline); err != nil {
				l.logger.Warn("unload: cycle advance timeout", "module", name, "error", err)
			}
		}
	}

	// Step 3: Stop the module.
	if cm.started {
		cmodStop(cm)
	}

	// Step 4: Remove consumer records (this module as consumer).
	// Step 5: Unregister APIs (this module as provider) — both the REST registry
	// and the watch registry, BEFORE Destroy frees this module's HAL pins (a
	// registered WatchAPI captures those pins; see unregisterModuleAPIs).
	unregisterModuleAPIs(name)

	// Step 6: Destroy the module.
	cmodDestroy(cm)

	// Step 7: Clean up env.
	cmodDestroyEnv(cm)
	cm.hCtx.Delete()

	// Step 8: dlclose only if no other instance shares this handle.
	if cm.handle != nil {
		shared := false
		for i, other := range l.cModules {
			if i != idx && other.handle == cm.handle {
				shared = true
				break
			}
		}
		if !shared {
			cmodDlclose(cm)
		}
	}

	// Step 9: Remove from slice.
	l.cModules = append(l.cModules[:idx], l.cModules[idx+1:]...)

	l.logger.Info("unloaded C module", "name", name)
	return nil
}

// unloadGoModule unloads a single Go module.
// Caller must hold modMu (called only from UnloadModule).
func (l *Launcher) unloadGoModule(name string) error {
	idx := -1
	for i, gm := range l.goModules {
		if gm.name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return syscall.ENOENT
	}

	gm := l.goModules[idx]

	// Step 1: Remove RT functions from threads.
	compID := l.halCompID(name)
	if compID > 0 {
		removed, _ := halcmd.DelFunctsByComp(compID)
		if removed > 0 {
			// Step 2: Wait for cycle barrier.
			baseline := halcmd.GetMaxCycleCount()
			if err := halcmd.WaitCycleAdvance(baseline); err != nil {
				l.logger.Warn("unload: cycle advance timeout", "module", name, "error", err)
			}
		}
	}

	// Step 3: Stop the module.
	gm.mod.Stop()

	// Step 4+5: Remove consumer records and unregister APIs (REST + watch),
	// BEFORE Destroy frees this module's HAL pins.
	unregisterModuleAPIs(name)

	// Step 6: Destroy the module.
	gm.mod.Destroy()

	// Step 7: Remove from slice.
	l.goModules = append(l.goModules[:idx], l.goModules[idx+1:]...)

	l.logger.Info("unloaded Go module", "name", name)
	return nil
}
