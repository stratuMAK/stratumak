// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halcmd

import "testing"

// setPool installs a deterministic pool state for a test, bypassing topology
// detection. `avail` is treated as both the isolated set and the free list
// (sorted descending, as InitCPUPool leaves it).
func setPool(t *testing.T, avail []int, posixRT bool) {
	t.Helper()
	pool.mu.Lock()
	pool.isolated = append([]int(nil), avail...)
	pool.available = append([]int(nil), avail...)
	pool.lastAssigned = -1
	pool.logger = nil
	pool.posixRT = posixRT
	pool.mu.Unlock()
}

func mustAcquire(t *testing.T, name string, cpu int) int {
	t.Helper()
	got, err := acquireCPU(name, cpu)
	if err != nil {
		t.Fatalf("acquireCPU(%q, %d) unexpected error: %v", name, cpu, err)
	}
	return got
}

// The motivating case: one isolated core, base+servo. The base takes the core
// and the servo co-locates onto it instead of floating (returning -1).
func TestAcquireCPU_CoLocateSingleCore(t *testing.T) {
	setPool(t, []int{3}, true)

	if got := mustAcquire(t, "base-thread", -1); got != 3 {
		t.Fatalf("base: got cpu=%d, want 3", got)
	}
	if got := mustAcquire(t, "servo-thread", -1); got != 3 {
		t.Fatalf("servo: got cpu=%d, want 3 (co-located), not floated", got)
	}
}

// With several isolated cores, free cores are handed out fastest-first
// (descending), then overflow co-locates onto the last one assigned — leaving
// the fastest thread alone on the highest core.
func TestAcquireCPU_MultiCoreThenCoLocate(t *testing.T) {
	setPool(t, []int{3, 2}, true)

	if got := mustAcquire(t, "t1", -1); got != 3 {
		t.Fatalf("t1: got %d, want 3", got)
	}
	if got := mustAcquire(t, "t2", -1); got != 2 {
		t.Fatalf("t2: got %d, want 2", got)
	}
	if got := mustAcquire(t, "t3", -1); got != 2 {
		t.Fatalf("t3: got %d, want 2 (co-located on last assigned)", got)
	}
}

// No isolated cores at all: fall back to no affinity (-1), not a bogus core.
func TestAcquireCPU_NoIsolatedCores(t *testing.T) {
	setPool(t, nil, true)

	if got := mustAcquire(t, "servo-thread", -1); got != -1 {
		t.Fatalf("got cpu=%d, want -1 (no affinity)", got)
	}
}

// Explicit cpu=N: a free isolated core is taken; a second explicit request for
// the same core co-locates; a non-isolated core is rejected.
func TestAcquireCPU_Explicit(t *testing.T) {
	setPool(t, []int{3, 2}, true)

	if got := mustAcquire(t, "t1", 3); got != 3 {
		t.Fatalf("explicit cpu=3: got %d, want 3", got)
	}
	// 3 is now taken; requesting it again co-locates rather than erroring.
	if got := mustAcquire(t, "t2", 3); got != 3 {
		t.Fatalf("explicit cpu=3 again: got %d, want 3 (co-located)", got)
	}
	// A CPU that is not isolated is an error.
	if _, err := acquireCPU("t3", 0); err == nil {
		t.Fatalf("explicit cpu=0 (not isolated): want error, got nil")
	}
}

// An explicit request updates lastAssigned, so a later auto (-1) request that
// finds the pool exhausted co-locates onto the explicitly-chosen core rather
// than the one the free list would have handed out.
func TestAcquireCPU_ExplicitThenAutoCoLocate(t *testing.T) {
	setPool(t, []int{3, 2}, true)

	// Explicitly take the lower core first; free list still holds 3.
	if got := mustAcquire(t, "t1", 2); got != 2 {
		t.Fatalf("explicit cpu=2: got %d, want 2", got)
	}
	// Auto pops the remaining free core (3), which becomes lastAssigned.
	if got := mustAcquire(t, "t2", -1); got != 3 {
		t.Fatalf("auto: got %d, want 3", got)
	}
	// Pool now exhausted: auto co-locates onto the last one assigned (3).
	if got := mustAcquire(t, "t3", -1); got != 3 {
		t.Fatalf("auto overflow: got %d, want 3 (co-located on last assigned)", got)
	}
}
