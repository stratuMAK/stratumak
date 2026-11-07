// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

import (
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// rtModule loads a module without starting its control loop: the cycle
// function, its frames and the pins, driven by hand.
func rtModule(t *testing.T) (*pnptaskModule, func(string) float64) {
	t.Helper()
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, name)
	get := func(pin string) float64 {
		t.Helper()
		v, ok := hal.LookupValue(name + "." + pin)
		if !ok {
			t.Fatalf("pin %s not found", pin)
		}
		return v
	}
	return m, get
}

// TestCycleOutputsOnlyOnCommit: what the control loop writes reaches the pins
// when it commits, all of it in the same run of the cycle function, and never
// half of it.
func TestCycleOutputsOnlyOnCommit(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()

	m.pins.busy.Set(true)
	m.pins.errorID.Set(uint32(errTrayEmpty))
	m.pins.errorFlag.Set(true)
	m.pins.pickers[0].close.Set(true)
	m.rt.invoke()
	if get("busy") != 0 || get("error") != 0 || get("error-id") != 0 || get("picker.0.close") != 0 {
		t.Fatal("outputs reached the pins without a commit")
	}

	m.rt.commit()
	m.rt.invoke()
	if get("busy") != 1 || get("error") != 1 || get("error-id") != float64(errTrayEmpty) || get("picker.0.close") != 1 {
		t.Errorf("after the commit: busy=%v error=%v error-id=%v close=%v; want all of them",
			get("busy"), get("error"), get("error-id"), get("picker.0.close"))
	}
}

// TestCycleInputsInOneFrame: the inputs Go sees come from one run of the cycle
// function, and a pin written after that run is not in the frame until the
// next one.
func TestCycleInputsInOneFrame(t *testing.T) {
	m, _ := rtModule(t)

	m.pins.originID.Set(10)
	m.pins.destID.Set(20)
	m.pins.startJob.Set(true)
	m.rt.invoke()
	m.pins.originID.Set(99) // after the run: not in this frame

	m.rt.read()
	f := &m.rt.in
	if f.origin_id != 10 || f.dest_id != 20 || f.start_job != 1 {
		t.Errorf("frame: origin %d dest %d start %d; want 10 20 1", f.origin_id, f.dest_id, f.start_job)
	}
	if f.cycle != 1 {
		t.Errorf("frame cycle = %d, want 1", f.cycle)
	}

	m.rt.invoke()
	m.rt.read()
	if f.origin_id != 99 || f.cycle != 2 {
		t.Errorf("next frame: origin %d cycle %d; want 99 2", f.origin_id, f.cycle)
	}
}

// TestCycleStartJobClearWithBusy: the start-job clear and busy falling come
// from one commit, so they reach the pins in the same run. Without a new clear
// the function leaves start-job alone: the requester's raise survives.
func TestCycleStartJobClearWithBusy(t *testing.T) {
	m, get := rtModule(t)

	// A job running: the requester raised start-job, busy is up.
	m.pins.startJob.Set(true)
	m.pins.busy.Set(true)
	m.rt.commit()
	m.rt.invoke()
	m.rt.invoke()
	if get("start-job") != 1 || get("busy") != 1 {
		t.Fatalf("running: start-job=%v busy=%v, want both high", get("start-job"), get("busy"))
	}

	// The job ends: one commit.
	m.pins.busy.Set(false)
	m.rt.clearStartJob()
	m.rt.commit()
	m.rt.invoke()
	if get("start-job") != 0 || get("busy") != 0 {
		t.Errorf("after the end: start-job=%v busy=%v, want both low in the same run",
			get("start-job"), get("busy"))
	}

	// The next request: the function must not clear it again on its own.
	m.pins.startJob.Set(true)
	m.rt.commit()
	m.rt.invoke()
	m.rt.invoke()
	if get("start-job") != 1 {
		t.Error("the cycle function cleared a new start-job without being asked")
	}
}

// TestStartControlNeedsTheCycleFunction: with the function on no thread the
// module can see no input, so startControl refuses and names the addf line.
func TestStartControlNeedsTheCycleFunction(t *testing.T) {
	fastLoop(t)
	old := firstFrameTimeout
	firstFrameTimeout = 50 * time.Millisecond
	t.Cleanup(func() { firstFrameTimeout = old })

	m, _ := rtModule(t)
	mot := newFakeMotion(m.cfg.NumJoints)
	m.mc, m.ms = mot, mot

	err := m.startControl()
	if err == nil {
		m.Stop()
		t.Fatal("startControl succeeded with the cycle function never run")
	}
	if !strings.Contains(err.Error(), "addf "+m.name+".cycle servo-thread") {
		t.Errorf("error = %v, want it to name the addf line", err)
	}
}

// TestOutputsBeforeMotion: an output written before a motion command is on its
// pin by the time motmod takes the command. Motion commands go out at once and
// the outputs at a commit, so without committedMotion the head could move with
// scene-busy still reading the value from before the write (the scenegate
// runtest caught a descent under a scene-busy that had already been dropped).
func TestOutputsBeforeMotion(t *testing.T) {
	m, get := rtModule(t)
	mot := newFakeMotion(m.cfg.NumJoints)
	cm := committedMotion{motionControl: mot, rt: m.rt}

	seen := -1.0
	mot.onMove = func(moveCall) {
		// motmod takes the command in the next servo cycle, in which the
		// cycle function runs too.
		m.rt.invoke()
		seen = get("scene-busy")
	}

	m.pins.sceneBusy.Set(true)
	if err := cm.SetLine(motctl.Pose{X: 1}, 10, 10, 100, 0, 1, 0, -1); err != nil {
		t.Fatalf("SetLine: %v", err)
	}
	if seen != 1 {
		t.Errorf("scene-busy = %v when the move was taken, want 1", seen)
	}
}
