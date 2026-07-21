// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package gomc

import (
	"regexp"
	"testing"
)

// NotifyLogError fans out to EVERY registered hook. In a multi-instance config
// each milltask instance registers its own hook, so without per-hook filtering
// one instance's error (e.g. "pnp.mot: joint N following error") reaches every
// other instance's operator message list. This test pins down that fan-out
// behaviour and that a component-matching filter isolates instances — the
// coat.task/pnp.task cross-talk regression.
func TestNotifyLogError_FanOutAndFilter(t *testing.T) {
	resetLogHooks()
	t.Cleanup(resetLogHooks)

	// Two instances, each forwarding only its own namespace, mirroring the hook
	// milltask registers (forward iff filter == nil || filter matches component).
	var coatGot, pnpGot []string
	register := func(filter *regexp.Regexp, sink *[]string) {
		OnLogError(func(component, msg string) {
			if filter != nil && !filter.MatchString(component) {
				return
			}
			*sink = append(*sink, component+": "+msg)
		})
	}
	register(regexp.MustCompile(`^coat\.`), &coatGot)
	register(regexp.MustCompile(`^pnp\.`), &pnpGot)

	// An error from pnp's motion module must reach only pnp.
	NotifyLogError("pnp.mot", "joint 2 following error")
	// A homing-module error (distinct component name) must still be routed by
	// the namespace filter, not just an exact motmod match.
	NotifyLogError("pnp.home.0", "drive reported homing error")
	// An error from coat must reach only coat.
	NotifyLogError("coat.mot", "joint 0 amplifier fault")

	wantPnp := []string{
		"pnp.mot: joint 2 following error",
		"pnp.home.0: drive reported homing error",
	}
	wantCoat := []string{"coat.mot: joint 0 amplifier fault"}

	if !equalStrings(pnpGot, wantPnp) {
		t.Errorf("pnp hook got %v, want %v", pnpGot, wantPnp)
	}
	if !equalStrings(coatGot, wantCoat) {
		t.Errorf("coat hook got %v, want %v", coatGot, wantCoat)
	}
}

// An unfiltered hook (filter == nil) must receive everything — the legacy
// single-instance behaviour that the default (no error_filter) preserves.
func TestNotifyLogError_UnfilteredForwardsAll(t *testing.T) {
	resetLogHooks()
	t.Cleanup(resetLogHooks)

	var got []string
	OnLogError(func(component, msg string) {
		got = append(got, component)
	})

	NotifyLogError("pnp.mot", "x")
	NotifyLogError("coat.io", "y")
	NotifyLogError("anything.else", "z")

	want := []string{"pnp.mot", "coat.io", "anything.else"}
	if !equalStrings(got, want) {
		t.Errorf("unfiltered hook got %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
