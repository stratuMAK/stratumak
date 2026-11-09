// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pnptask/dztest"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// rtModule loads a module without starting its control loop: the cycle
// function, its frames and the pins, driven by hand.
func rtModule(t *testing.T) (*pnptaskModule, func(string) float64) {
	t.Helper()
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, name)
	m.rt.runRT = m.rt.invoke // no servo thread here: waits run the function themselves
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
// pin before motmod takes the command -- already when the command is sent, not
// only in the servo cycle that takes it. Motion commands go out at once and the
// outputs at a commit, so without committedMotion the head could move with
// scene-busy still reading the value from before the write (the scenegate
// runtest caught a descent under a scene-busy that had already been dropped).
func TestOutputsBeforeMotion(t *testing.T) {
	m, get := rtModule(t)
	mot := newFakeMotion(m.cfg.NumJoints)
	cm := committedMotion{motionControl: mot, rt: m.rt}

	seen := -1.0
	mot.onMove = func(moveCall) {
		seen = get("scene-busy") // the pin as the command is sent
	}

	m.pins.sceneBusy.Set(true)
	if err := cm.SetLine(motctl.Pose{X: 1}, 10, 10, 100, 0, 1, 0, -1); err != nil {
		t.Fatalf("SetLine: %v", err)
	}
	if seen != 1 {
		t.Errorf("scene-busy = %v when the move was taken, want 1", seen)
	}
}

// TestRTStatusFromFrame: the control loop's motion status is motmod's state as
// the cycle function read it, in the frame with the pins.
func TestRTStatusFromFrame(t *testing.T) {
	m, _ := rtModule(t)
	st := &rtStatus{rt: m.rt, numJoints: m.cfg.NumJoints}

	m.rt.invoke()
	m.rt.read()
	if _, err := st.GetStatus(); !errors.Is(err, errNoSnapshot) {
		t.Fatalf("without mot: got %v, want errNoSnapshot", err)
	}

	mot := dztest.NewFakeMot()
	defer mot.Free()
	m.deadzone.setMot(mot.Ptr())
	defer m.deadzone.setMot(nil)
	mot.SetSnapshot(false, 3, 42, 120, 80, 0b101)

	m.rt.invoke()
	m.rt.read()
	s, err := st.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if s.Enabled != 1 || s.Inpos != 0 || s.QueueDepth != 3 || s.Id != 42 {
		t.Errorf("status: enabled %d inpos %d depth %d id %d; want 1 0 3 42", s.Enabled, s.Inpos, s.QueueDepth, s.Id)
	}
	if s.Joints[0].Homed != 1 || s.Joints[1].Homed != 0 || s.Joints[2].Homed != 1 {
		t.Errorf("homed: %d %d %d; want 1 0 1", s.Joints[0].Homed, s.Joints[1].Homed, s.Joints[2].Homed)
	}
	if fb, err := st.GetPosFb(); err != nil || fb.X != 120 || fb.Y != 80 {
		t.Errorf("GetPosFb = %+v, %v; want (120,80)", fb, err)
	}
}

// TestRTStatusStaleAfterDispatch: a frame from before a motion command is
// refused, so the first good read after a dispatch describes the machine
// after it -- what dispatchSettleTicks used to guess at.
func TestRTStatusStaleAfterDispatch(t *testing.T) {
	m, _ := rtModule(t)
	st := &rtStatus{rt: m.rt, numJoints: m.cfg.NumJoints}
	mot := dztest.NewFakeMot()
	defer mot.Free()
	m.deadzone.setMot(mot.Ptr())
	defer m.deadzone.setMot(nil)
	mot.SetSnapshot(true, 0, 1, 0, 0, 0b111)

	m.rt.invoke()
	m.rt.read() // the frame of the cycle before the command

	cm := committedMotion{motionControl: newFakeMotion(m.cfg.NumJoints), rt: m.rt}
	if err := cm.SetLine(motctl.Pose{X: 1}, 10, 10, 100, 0, 2, 0, -1); err != nil {
		t.Fatalf("SetLine: %v", err)
	}
	if _, err := st.GetStatus(); !errors.Is(err, errStaleStatus) {
		t.Errorf("frame from before the command: got %v, want errStaleStatus", err)
	}

	mot.SetSnapshot(false, 1, 2, 0, 0, 0b111)
	m.rt.invoke()
	m.rt.read()
	s, err := st.GetStatus()
	if err != nil {
		t.Fatalf("frame after the command: %v", err)
	}
	if s.Inpos != 0 || s.QueueDepth != 1 {
		t.Errorf("after the command: inpos %d depth %d; want 0 1", s.Inpos, s.QueueDepth)
	}
}

// edges is the edge block of the latest input frame.
func edges(m *pnptaskModule) *edgeBlock {
	m.rt.read()
	return &m.rt.in.edges
}

// TestEdgesOneServoCycle: a pulse that lasts one run of the cycle function is
// counted once, and an input already high at the function's first run is no
// edge.
func TestEdgesOneServoCycle(t *testing.T) {
	m, _ := rtModule(t)

	m.pins.home.Set(true) // high before the first run: primes, no edge
	m.rt.invoke()
	if n := edges(m).home; n != 0 {
		t.Fatalf("home counted %d at the first run, want 0 (primed)", n)
	}
	m.pins.home.Set(false)
	m.rt.invoke()

	m.pins.home.Set(true)
	m.rt.invoke()
	m.pins.home.Set(false)
	m.rt.invoke()
	m.rt.invoke()
	if n := edges(m).home; n != 1 {
		t.Errorf("one-cycle home pulse counted %d, want 1", n)
	}
	if n := edges(m).error_reset; n != 0 {
		t.Errorf("error-reset counted %d without a pulse", n)
	}
}

// TestEdgesGatedAtThePress: machine-on, home and park are not counted under
// estop, the manual picker presses neither under estop nor in auto -- judged
// in the servo cycle of the press, so clearing estop afterwards brings nothing
// back.
func TestEdgesGatedAtThePress(t *testing.T) {
	m, _ := rtModule(t)
	m.rt.invoke()

	pulse := func(p *hal.Pin[bool]) {
		p.Set(true)
		m.rt.invoke()
		p.Set(false)
		m.rt.invoke()
	}

	m.pins.estopOn.Set(true)
	pulse(m.pins.machineOn)
	pulse(m.pins.home)
	pulse(m.pins.park)
	pulse(m.pins.pickers[0].manualOpen)
	m.pins.estopOn.Set(false)
	m.rt.invoke()
	e := edges(m)
	if e.machine_on != 0 || e.home != 0 || e.park != 0 || e.picker[0].open != 0 {
		t.Errorf("counted under estop: machine-on %d home %d park %d open %d",
			e.machine_on, e.home, e.park, e.picker[0].open)
	}

	m.pins.autoEnable.Set(true)
	pulse(m.pins.pickers[0].manualClose)
	if n := edges(m).picker[0].close; n != 0 {
		t.Errorf("manual close counted %d in auto, want 0", n)
	}
	m.pins.autoEnable.Set(false)
	m.rt.invoke()
	pulse(m.pins.pickers[0].manualClose)
	pulse(m.pins.home)
	e = edges(m)
	if e.picker[0].close != 1 || e.home != 1 {
		t.Errorf("manual, no estop: close %d home %d, want 1 1", e.picker[0].close, e.home)
	}
}

// TestEdgesStationResets: set-full carries the tray's step of the press,
// set-empty -1, the latest wins, and both in one servo cycle is a conflict
// that changes nothing.
func TestEdgesStationResets(t *testing.T) {
	m, _ := rtModule(t)
	tr := &m.pins.trays[0]
	m.rt.invoke()

	tr.step.Set(5)
	tr.setFull.Set(true)
	m.rt.invoke()
	tr.setFull.Set(false)
	tr.step.Set(7) // moves after the press: not what the reset carries
	m.rt.invoke()
	r := edges(m).tray[0]
	if r.count != 1 || r.value != 5 {
		t.Errorf("after set-full: count %d value %d, want 1 5", r.count, r.value)
	}

	tr.setEmpty.Set(true)
	m.rt.invoke()
	tr.setEmpty.Set(false)
	m.rt.invoke()
	r = edges(m).tray[0]
	if r.count != 2 || r.value != -1 {
		t.Errorf("after set-empty: count %d value %d, want 2 -1", r.count, r.value)
	}

	tr.setFull.Set(true)
	tr.setEmpty.Set(true)
	m.rt.invoke()
	r = edges(m).tray[0]
	if r.count != 2 || r.conflicts != 1 || r.value != -1 {
		t.Errorf("both in one cycle: count %d conflicts %d value %d, want 2 1 -1", r.count, r.conflicts, r.value)
	}

	ps := &m.pins.procs[0]
	ps.setHasMaterial.Set(true)
	m.rt.invoke()
	if p := edges(m).proc[0]; p.count != 1 || p.value != 1 {
		t.Errorf("proc set-has-material: count %d value %d, want 1 1", p.count, p.value)
	}
}

// TestHomePulseBetweenTwoSamples: a home pulse that exists for one servo
// cycle between two samples of the control loop still homes the machine. The
// loop used to detect edges on its own 10 ms samples, and lost such a pulse
// without a trace (PNPTASK_DESIGN.md §5.2.1).
func TestHomePulseBetweenTwoSamples(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{simulate: true})
	f.machineOn()
	f.mot.resetCalls()

	f.sim.inLoop(t, func() {
		f.m.pins.home.Set(true)
		f.m.rt.invoke() // the one servo cycle the pulse lasts
		f.m.pins.home.Set(false)
	})
	f.eventually("homing from a one-cycle pulse", func() bool { return f.mot.called("JointHome") })
}
