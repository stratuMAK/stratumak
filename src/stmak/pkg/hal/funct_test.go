// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests for the realtime half of pkg/hal: exporting a C cyclic function and the
// RT-hardened allocation the function walks (docs/dev/GOMOD_RT_DESIGN.md).
//
// The function under test lives in internal/hallib/rtfuncttest — a real .c
// translation unit, because that is the convention the API documents and the
// only shape "make rt-effects-check" can verify.
package hal_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/internal/hallib/rtfuncttest"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// unitSquare is one polygon covering [0,1]x[0,1], enough to tell a
// bounding-box rejection from a real point-in-polygon hit.
func unitSquare() [][]rtfuncttest.Point {
	return [][]rtfuncttest.Point{{
		{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1},
	}}
}

// TestRTCallocWalk covers the memory model on its own, without HAL in the way:
// Go assembles a flat scene into one RTCalloc block, the C function walks it,
// and RTFree releases the lot. Failing here means the layout is wrong, which
// would otherwise surface as a servo-thread crash.
func TestRTCallocWalk(t *testing.T) {
	scene := rtfuncttest.Build(unitSquare())
	if !scene.Valid() {
		t.Fatal("Build: RTCalloc returned nil")
	}
	defer scene.Free()

	comp, err := hal.NewRTComponent("test-rt-walk")
	if err != nil {
		t.Fatalf("NewRTComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	pin, err := hal.NewPin[bool](comp, "inside", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	scene.SetOut(pin.RTDataPtr())

	cases := []struct {
		x, y float64
		want bool
		why  string
	}{
		{0.5, 0.5, true, "inside the square"},
		{5, 5, false, "rejected by the bounding box"},
		{0.5, 2, false, "inside the box in x, outside the polygon"},
	}
	for _, tc := range cases {
		scene.SetQuery(tc.x, tc.y)
		scene.Invoke(1_000_000)
		if got := pin.Get(); got != tc.want {
			t.Errorf("query (%g,%g) [%s]: pin = %v, want %v", tc.x, tc.y, tc.why, got, tc.want)
		}
	}

	if scene.Calls() != int64(len(cases)) {
		t.Errorf("Calls = %d, want %d", scene.Calls(), len(cases))
	}
	if scene.LastPeriod() != 1_000_000 {
		t.Errorf("LastPeriod = %d, want 1000000", scene.LastPeriod())
	}

	// A cleared output slot must not be dereferenced — the module clears it
	// nowhere, but a NULL here is the difference between a guard and a fault.
	scene.SetOut(nil)
	scene.Invoke(1_000_000)
}

// TestExportFunctGuards covers every refusal ExportFunct owns, because each one
// stands for a way HAL would otherwise fail late (or not at all): a userspace
// component gets a bare EINVAL out of hal_lib, a duplicate or post-Ready export
// leaks the funct struct in HAL's bump allocator first, and a nil address is
// only discovered by the servo thread calling through it.
func TestExportFunctGuards(t *testing.T) {
	scene := rtfuncttest.Build(unitSquare())
	if !scene.Valid() {
		t.Fatal("Build: RTCalloc returned nil")
	}
	defer scene.Free()

	t.Run("userspace component", func(t *testing.T) {
		comp, err := hal.NewComponent("test-rt-guard-user")
		if err != nil {
			t.Fatalf("NewComponent: %v", err)
		}
		defer func() { _ = comp.Exit() }()

		if comp.IsRealtime() {
			t.Error("IsRealtime = true for a NewComponent component")
		}
		err = comp.ExportFunct("f", rtfuncttest.Funct(), scene.Arg(), true, false)
		if !errors.Is(err, hal.ErrNotRealtime) {
			t.Errorf("ExportFunct on a userspace component: got %v, want ErrNotRealtime", err)
		}
	})

	t.Run("nil funct", func(t *testing.T) {
		comp, err := hal.NewRTComponent("test-rt-guard-nil")
		if err != nil {
			t.Fatalf("NewRTComponent: %v", err)
		}
		defer func() { _ = comp.Exit() }()

		err = comp.ExportFunct("f", nil, scene.Arg(), true, false)
		if !errors.Is(err, hal.ErrNilFunct) {
			t.Errorf("ExportFunct(nil): got %v, want ErrNilFunct", err)
		}
	})

	t.Run("duplicate and post-ready", func(t *testing.T) {
		comp, err := hal.NewRTComponent("test-rt-guard-dup")
		if err != nil {
			t.Fatalf("NewRTComponent: %v", err)
		}
		defer func() { _ = comp.Exit() }()

		if !comp.IsRealtime() {
			t.Error("IsRealtime = false for a NewRTComponent component")
		}
		if err := comp.ExportFunct("check", rtfuncttest.Funct(), scene.Arg(), true, false); err != nil {
			t.Fatalf("ExportFunct: %v", err)
		}
		// A function and a pin may share a name — they are different HAL
		// namespaces, so the two name sets must not be one.
		if _, err := hal.NewPin[bool](comp, "check", hal.Out); err != nil {
			t.Errorf("NewPin with the same name as a funct: %v", err)
		}
		err = comp.ExportFunct("check", rtfuncttest.Funct(), scene.Arg(), true, false)
		if !errors.Is(err, hal.ErrNameExists) {
			t.Errorf("duplicate ExportFunct: got %v, want ErrNameExists", err)
		}

		if err := comp.Ready(); err != nil {
			t.Fatalf("Ready: %v", err)
		}
		err = comp.ExportFunct("late", rtfuncttest.Funct(), scene.Arg(), true, false)
		if !errors.Is(err, hal.ErrAlreadyReady) {
			t.Errorf("ExportFunct after Ready: got %v, want ErrAlreadyReady", err)
		}
	})

	t.Run("exited component", func(t *testing.T) {
		comp, err := hal.NewRTComponent("test-rt-guard-exited")
		if err != nil {
			t.Fatalf("NewRTComponent: %v", err)
		}
		if err := comp.Exit(); err != nil {
			t.Fatalf("Exit: %v", err)
		}
		err = comp.ExportFunct("f", rtfuncttest.Funct(), scene.Arg(), true, false)
		if !errors.Is(err, hal.ErrComponentExited) {
			t.Errorf("ExportFunct after Exit: got %v, want ErrComponentExited", err)
		}
	})
}

// TestExportFunctOnThread is the end-to-end rehearsal of the whole path, and in
// particular of the teardown order a runtime unload uses: the launcher removes
// the component's functions from every thread, waits for a full cycle of every
// realtime thread, and only then lets Destroy release the memory the function
// was walking. Getting that order wrong is a use-after-free inside the servo
// thread, which is precisely the failure this exists to keep out of the tree.
func TestExportFunctOnThread(t *testing.T) {
	scene := rtfuncttest.Build(unitSquare())
	if !scene.Valid() {
		t.Fatal("Build: RTCalloc returned nil")
	}

	comp, err := hal.NewRTComponent("test-rt-thread")
	if err != nil {
		t.Fatalf("NewRTComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	pin, err := hal.NewPin[bool](comp, "inside", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.ExportFunct("check", rtfuncttest.Funct(), scene.Arg(), true, false); err != nil {
		t.Fatalf("ExportFunct: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	scene.SetOut(pin.RTDataPtr())
	scene.SetQuery(0.5, 0.5)

	const thread = "test-rt-thread-servo"
	if err := halcmd.CreateThreadCPU(thread, 1_000_000, 1, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	defer func() { _ = halcmd.ThreadDelete(thread) }()

	if err := halcmd.AddF("test-rt-thread.check", thread, -1); err != nil {
		t.Fatalf("AddF: %v", err)
	}
	if err := halcmd.StartThreads(); err != nil {
		t.Fatalf("StartThreads: %v", err)
	}
	defer func() { _ = halcmd.StopThreads() }()

	// Wait for the function to have actually run rather than assuming a sleep
	// is long enough: an under-loaded CI box and a busy one disagree wildly.
	deadline := time.Now().Add(2 * time.Second)
	for scene.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if scene.Calls() == 0 {
		t.Fatal("cyclic function never ran")
	}
	if !pin.Get() {
		t.Error("pin = false with the query point inside the polygon")
	}
	if p := scene.LastPeriod(); p != 1_000_000 {
		t.Errorf("LastPeriod = %d, want the thread period 1000000", p)
	}

	// The unload sequence, in the launcher's order.
	removed, err := halcmd.DelFunctsByComp(comp.ID())
	if err != nil {
		t.Fatalf("DelFunctsByComp: %v", err)
	}
	if removed != 1 {
		t.Errorf("DelFunctsByComp removed %d functions, want 1", removed)
	}
	if err := halcmd.WaitCycleAdvance(); err != nil {
		t.Fatalf("WaitCycleAdvance: %v", err)
	}

	// Past the barrier the function is off every thread. Prove it before
	// freeing: the counter must not move again, however long the threads run.
	// (Checked while the block is still allocated — reading it after the free
	// to detect a missed removal is itself the use-after-free.)
	settled := scene.Calls()
	time.Sleep(50 * time.Millisecond)
	if now := scene.Calls(); now != settled {
		t.Fatalf("cyclic function still running after DelFunctsByComp + WaitCycleAdvance: %d -> %d calls", settled, now)
	}

	// Only now may the block the function was walking be released.
	scene.Free()
}
