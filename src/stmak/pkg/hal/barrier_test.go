// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestPinAccessAfterExit verifies the component-liveness barrier (finding H1):
// once Exit() has released the component, its pins' HAL shared memory is freed,
// so Get() must return the zero value and TrySet() must return
// ErrComponentExited rather than dereferencing freed memory.
func TestPinAccessAfterExit(t *testing.T) {
	comp, err := hal.NewComponent("test-after-exit")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	pin, err := hal.NewPin[float64](comp, "v", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// Live access works.
	pin.Set(3.5)
	if got := pin.Get(); got != 3.5 {
		t.Fatalf("Get before Exit: got %v, want 3.5", got)
	}

	if err := comp.Exit(); err != nil {
		t.Fatalf("Exit: %v", err)
	}

	// After Exit the pin memory is gone: Get is a safe no-op returning zero,
	// TrySet reports the component has exited (and does not touch freed memory).
	if got := pin.Get(); got != 0 {
		t.Errorf("Get after Exit: got %v, want 0 (zero value, no dereference)", got)
	}
	if err := pin.TrySet(9.9); !errors.Is(err, hal.ErrComponentExited) {
		t.Errorf("TrySet after Exit: got %v, want ErrComponentExited", err)
	}
	// Set is fire-and-forget: it must not panic on a freed pin.
	pin.Set(1.0)
	if got := pin.Get(); got != 0 {
		t.Errorf("Get after Set-on-exited: got %v, want 0", got)
	}
}

// TestExitIdempotent verifies Exit() is idempotent (finding M3): a second call
// is a no-op returning nil, so it never runs hal_exit() twice on the same id
// (which would error, or tear down a recycled id's new component).
func TestExitIdempotent(t *testing.T) {
	comp, err := hal.NewComponent("test-exit-idem")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	if err := comp.Exit(); err != nil {
		t.Fatalf("first Exit: %v", err)
	}
	if err := comp.Exit(); err != nil {
		t.Errorf("second Exit: got %v, want nil (idempotent)", err)
	}
	if err := comp.Exit(); err != nil {
		t.Errorf("third Exit: got %v, want nil (idempotent)", err)
	}
}

// TestPinExitRace exercises the barrier under concurrency: many goroutines hammer
// Get/Set/TrySet on a pin while another goroutine calls Exit(). With the disjoint
// pre-fix mutexes this was a use-after-free of freed HAL shared memory; with the
// barrier every access either completes on live memory or is safely refused.
//
// Run with -race to catch the data race between pin access and Exit's hal_exit.
// The workers accept BOTH live results (pre-Exit) and refused results (post-Exit)
// as valid — the invariant under test is "no crash, no race, no freed-memory
// access", plus that access is definitively refused once Exit has returned.
func TestPinExitRace(t *testing.T) {
	comp, err := hal.NewComponent("test-exit-race")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	pin, err := hal.NewPin[int32](comp, "n", hal.IO)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			<-start
			for j := 0; j < 5000; j++ {
				// Any of these may run before or after Exit; all must be safe.
				_ = pin.Get()
				pin.Set(id)
				if err := pin.TrySet(id); err != nil && !errors.Is(err, hal.ErrComponentExited) {
					t.Errorf("TrySet: unexpected error %v", err)
					return
				}
			}
		}(int32(i))
	}

	close(start)
	// Exit concurrently with the workers so the barrier is exercised mid-flight.
	if err := comp.Exit(); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	wg.Wait()

	// Once Exit has returned, access is definitively refused.
	if got := pin.Get(); got != 0 {
		t.Errorf("Get after Exit returned: got %v, want 0", got)
	}
	if err := pin.TrySet(1); !errors.Is(err, hal.ErrComponentExited) {
		t.Errorf("TrySet after Exit returned: got %v, want ErrComponentExited", err)
	}
}
