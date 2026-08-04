// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcstat"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

// TestWatchDeltaFlagsMatchIDL cross-checks the two places the @watch_delta
// contract is declared: the IDL (src/gmi/idl/emcstat.gmi, compiled into the
// generated bridge's Delta flags, which is what every generated client's merge
// behaviour is derived from) and the manual registration in registerWatches
// (watches.go), which is what the server actually serves. Nothing else ties
// them together, and disagreement in the dangerous direction — server sends
// deltas, clients don't merge — silently recreates the frame-two breakage
// where a partial StatFull replaced the whole cached object.
func TestWatchDeltaFlagsMatchIDL(t *testing.T) {
	const instance = "delta-contract-test"
	m := &milltaskModule{}

	// Manual registration path (the one production runs), into a fresh
	// registry so the test does not disturb — or get disturbed by — whatever
	// the default registry holds.
	prev := apiserver.DefaultWatchRegistry()
	defer apiserver.SetDefaultWatchRegistry(prev)
	apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())
	m.registerWatches(instance)
	manualAPI := apiserver.DefaultWatchRegistry().Get("emcstat", instance)
	if manualAPI == nil {
		t.Fatal("registerWatches did not register the emcstat watch API")
	}

	// Generated (IDL-derived) registration path.
	genReg := apiserver.NewWatchRegistry()
	emcstat.RegisterEmcstatWatch(genReg, instance, m, nil)
	genAPI := genReg.Get("emcstat", instance)
	if genAPI == nil {
		t.Fatal("generated bridge did not register the emcstat watch API")
	}

	manual := make(map[string]bool, len(manualAPI.Watches))
	for _, w := range manualAPI.Watches {
		manual[w.Name] = w.Delta
	}
	gen := make(map[string]bool, len(genAPI.Watches))
	for _, w := range genAPI.Watches {
		gen[w.Name] = w.Delta
	}

	// Every IDL-declared watch must be served, with the same Delta flag.
	// A mismatch either way breaks a wire contract: IDL delta + manual full
	// wastes bandwidth but merges fine; manual delta + IDL full hands clients
	// partial structs they will not merge.
	for name, idlDelta := range gen {
		manualDelta, ok := manual[name]
		if !ok {
			t.Errorf("watch %q is declared in the IDL but missing from the manual registration in watches.go", name)
			continue
		}
		if manualDelta != idlDelta {
			t.Errorf("watch %q Delta flag disagrees: watches.go=%v, IDL/generated bridge=%v", name, manualDelta, idlDelta)
		}
	}

	// A manually registered delta watch the IDL does not know about is the
	// dangerous direction on its own: the server diffs its frames, but no
	// generated client was told to merge them.
	for name, manualDelta := range manual {
		if _, ok := gen[name]; ok {
			continue // compared above
		}
		if manualDelta {
			t.Errorf("watch %q is registered Delta:true in watches.go but is not a @watch_delta in the IDL — generated clients will replace instead of merge", name)
		}
	}
}
