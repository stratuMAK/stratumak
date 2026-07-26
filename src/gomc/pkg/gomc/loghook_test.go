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

// TestOnLogError_Unregister verifies the returned unregister function removes a
// hook so it stops receiving errors, and is idempotent. Without this a destroyed
// module's hook keeps firing on later errors (the shutdown final-flush hazard).
func TestOnLogError_Unregister(t *testing.T) {
	resetLogHooks()
	t.Cleanup(resetLogHooks)

	var got []string
	unregister := OnLogError(func(component, msg string) {
		got = append(got, component)
	})

	NotifyLogError("a.mot", "x")
	unregister()
	NotifyLogError("b.mot", "y") // must NOT reach the removed hook
	unregister()                 // idempotent: second call is a no-op

	if want := []string{"a.mot"}; !equalStrings(got, want) {
		t.Errorf("after unregister got %v, want %v", got, want)
	}
}

// TestOnLogError_UnregisterOneOfMany verifies removing one hook leaves the
// others registered and in order.
func TestOnLogError_UnregisterOneOfMany(t *testing.T) {
	resetLogHooks()
	t.Cleanup(resetLogHooks)

	var first, second []string
	unregFirst := OnLogError(func(_, msg string) { first = append(first, msg) })
	OnLogError(func(_, msg string) { second = append(second, msg) })

	NotifyLogError("c", "1")
	unregFirst()
	NotifyLogError("c", "2")

	if want := []string{"1"}; !equalStrings(first, want) {
		t.Errorf("first hook got %v, want %v", first, want)
	}
	if want := []string{"1", "2"}; !equalStrings(second, want) {
		t.Errorf("second hook got %v, want %v", second, want)
	}
}

// TestNotifyLogError_PanicIsolation verifies a panicking hook is contained: it
// does not propagate out of NotifyLogError (which runs on the recover-less drain
// goroutine) and does not stop the remaining hooks from running.
func TestNotifyLogError_PanicIsolation(t *testing.T) {
	resetLogHooks()
	t.Cleanup(resetLogHooks)

	var after []string
	OnLogError(func(_, _ string) { panic("boom") })
	OnLogError(func(component, _ string) { after = append(after, component) })

	// Must not panic out to the caller.
	NotifyLogError("d.mot", "z")

	if want := []string{"d.mot"}; !equalStrings(after, want) {
		t.Errorf("hook after the panicking one got %v, want %v (panic not isolated?)", after, want)
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
