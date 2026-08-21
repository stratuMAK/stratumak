// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package stmak

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
		OnLogError(func(component, msg string, severity int) {
			if filter != nil && !filter.MatchString(component) {
				return
			}
			*sink = append(*sink, component+": "+msg)
		})
	}
	register(regexp.MustCompile(`^coat\.`), &coatGot)
	register(regexp.MustCompile(`^pnp\.`), &pnpGot)

	// An error from pnp's motion module must reach only pnp.
	NotifyOperatorMessage("pnp.mot", "joint 2 following error", 3)
	// A homing-module error (distinct component name) must still be routed by
	// the namespace filter, not just an exact motmod match.
	NotifyOperatorMessage("pnp.home.0", "drive reported homing error", 3)
	// An error from coat must reach only coat.
	NotifyOperatorMessage("coat.mot", "joint 0 amplifier fault", 3)

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
	OnLogError(func(component, msg string, severity int) {
		got = append(got, component)
	})

	NotifyOperatorMessage("pnp.mot", "x", 3)
	NotifyOperatorMessage("coat.io", "y", 3)
	NotifyOperatorMessage("anything.else", "z", 3)

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
	unregister := OnLogError(func(component, msg string, severity int) {
		got = append(got, component)
	})

	NotifyOperatorMessage("a.mot", "x", 3)
	unregister()
	NotifyOperatorMessage("b.mot", "y", 3) // must NOT reach the removed hook
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
	unregFirst := OnLogError(func(_, msg string, _ int) { first = append(first, msg) })
	OnLogError(func(_, msg string, _ int) { second = append(second, msg) })

	NotifyOperatorMessage("c", "1", 3)
	unregFirst()
	NotifyOperatorMessage("c", "2", 3)

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
	OnLogError(func(_, _ string, _ int) { panic("boom") })
	OnLogError(func(component, _ string, _ int) { after = append(after, component) })

	// Must not panic out to the caller.
	NotifyOperatorMessage("d.mot", "z", 3)

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

// The flag is orthogonal to severity: what reaches the operator is decided by
// the mark, and how it is shown is decided by the level. A notice must arrive
// as a notice — promoting it to an error to make it visible is exactly the
// bind the flag exists to remove.
func TestOperatorMessageCarriesSeverity(t *testing.T) {
	type got struct {
		msg string
		sev int
	}
	var seen []got
	unregister := OnLogError(func(_, msg string, sev int) {
		seen = append(seen, got{msg, sev})
	})
	defer unregister()

	NotifyOperatorMessage("coat.pnp", "keine Rohteile mehr", 1) // INFO
	NotifyOperatorMessage("coat.pnp", "Portal nicht bereit!", 3) // ERROR

	if len(seen) != 2 {
		t.Fatalf("hook saw %d messages, want 2", len(seen))
	}
	if seen[0].sev != 1 {
		t.Errorf("notice arrived with severity %d, want 1 (INFO): a batch that "+
			"finished must not look like a fault", seen[0].sev)
	}
	if seen[1].sev != 3 {
		t.Errorf("fault arrived with severity %d, want 3 (ERROR)", seen[1].sev)
	}
}
