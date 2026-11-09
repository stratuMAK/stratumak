// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

import (
	"errors"
	"math"
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

	m.pins.machineIsOn.Set(true)
	m.pins.trays[0].count.Set(7)
	m.pins.pickers[0].close.Set(true)
	m.rt.invoke()
	if get("machine-is-on") != 0 || get("tray.10.count") != 0 || get("picker.0.close") != 0 {
		t.Fatal("outputs reached the pins without a commit")
	}

	m.rt.commit()
	m.rt.invoke()
	if get("machine-is-on") != 1 || get("tray.10.count") != 7 || get("picker.0.close") != 1 {
		t.Errorf("after the commit: machine-is-on=%v count=%v close=%v; want all of them",
			get("machine-is-on"), get("tray.10.count"), get("picker.0.close"))
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

// TestCycleJobHandshake: a start-job edge is accepted in the run that samples
// it -- busy up, the parameters latched from that same sample -- and the job
// ends in the run that applies Go's end, with the outcome next to it.
func TestCycleJobHandshake(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke() // primes

	m.pins.originID.Set(10)
	m.pins.destID.Set(20)
	m.pins.processStep.Set(3)
	m.pins.stayNear.Set(true)
	m.pins.startJob.Set(true)
	m.rt.invoke()
	m.pins.originID.Set(99) // after the edge: not the job's
	m.rt.invoke()
	m.rt.read()
	j := &m.rt.in.job
	if get("busy") != 1 || j.seq != 1 {
		t.Fatalf("after the edge: busy=%v seq=%d, want 1 1", get("busy"), j.seq)
	}
	if j.origin_id != 10 || j.dest_id != 20 || j.process_step != 3 || j.stay_near != 1 {
		t.Errorf("latched origin %d dest %d step %d stay %d; want 10 20 3 1",
			j.origin_id, j.dest_id, j.process_step, j.stay_near)
	}

	// A second edge while busy is no request.
	m.pins.startJob.Set(false)
	m.rt.invoke()
	m.pins.startJob.Set(true)
	m.rt.invoke()
	m.rt.read()
	if m.rt.in.job.seq != 1 {
		t.Errorf("an edge while busy was accepted (seq %d)", m.rt.in.job.seq)
	}

	// The end and a fault in one commit: busy falls in the run the error rises.
	if !m.rt.raise(uint32(errTrayEmpty), 10, 1) {
		t.Fatal("raise refused with nothing latched")
	}
	m.rt.endJob(1)
	m.rt.commit()
	m.rt.invoke()
	if get("busy") != 0 || get("error") != 1 || get("error-id") != float64(errTrayEmpty) ||
		get("error-station") != 10 || get("picker.0.missing") != 1 {
		t.Errorf("after the end: busy=%v error=%v id=%v station=%v missing=%v; want 0 1 %d 10 1",
			get("busy"), get("error"), get("error-id"), get("error-station"),
			get("picker.0.missing"), errTrayEmpty)
	}

	// An edge while latched is no request either.
	m.pins.startJob.Set(false)
	m.rt.invoke()
	m.pins.startJob.Set(true)
	m.rt.invoke()
	m.rt.read()
	if m.rt.in.job.seq != 1 || get("busy") != 0 {
		t.Errorf("an edge while latched was accepted (seq %d busy %v)", m.rt.in.job.seq, get("busy"))
	}
}

// TestCycleStartJobHeldAtStartIsNoRequest: D26 -- a level already high at the
// function's first run only primes.
func TestCycleStartJobHeldAtStartIsNoRequest(t *testing.T) {
	m, get := rtModule(t)
	m.pins.startJob.Set(true)
	m.rt.invoke()
	m.rt.invoke()
	if get("busy") != 0 {
		t.Error("a start-job high at the first run was accepted")
	}
	m.pins.startJob.Set(false)
	m.rt.invoke()
	m.pins.startJob.Set(true)
	m.rt.invoke()
	if get("busy") != 1 {
		t.Error("a start-job raised after a low was not accepted")
	}
}

// TestCycleErrorLatch: the first fault wins, a reset edge clears the whole
// latch in one run and is counted only then, and a raise in the frame a reset
// is applied with latches after it.
func TestCycleErrorLatch(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()

	m.rt.raise(uint32(errReleaseTimeout), 20, 0)
	m.rt.commit()
	m.rt.invoke()
	m.rt.read()
	if m.rt.raise(uint32(errMotionError), 0, 0) {
		t.Error("a second raise was sent with one latched")
	}
	if get("error") != 1 || get("error-id") != float64(errReleaseTimeout) {
		t.Fatalf("latched: error=%v id=%v", get("error"), get("error-id"))
	}

	// A reset with nothing else: cleared, and counted.
	before := uint32(m.rt.in.edges.error_reset)
	m.pins.errorReset.Set(true)
	m.rt.invoke()
	m.rt.read()
	if get("error") != 0 || get("error-id") != 0 || get("error-station") != 0 {
		t.Errorf("after the reset: error=%v id=%v station=%v; want all 0",
			get("error"), get("error-id"), get("error-station"))
	}
	if uint32(m.rt.in.edges.error_reset) != before+1 {
		t.Error("the reset that cleared the latch was not counted")
	}

	// A reset with nothing latched: not counted.
	m.pins.errorReset.Set(false)
	m.rt.invoke()
	m.pins.errorReset.Set(true)
	m.rt.invoke()
	m.rt.read()
	if uint32(m.rt.in.edges.error_reset) != before+1 {
		t.Error("a reset with nothing latched was counted")
	}

	// A reset and a raise in the same run: the raise is newer, it stays.
	m.pins.errorReset.Set(false)
	m.rt.invoke()
	m.rt.raise(uint32(errTrayEmpty), 10, 0)
	m.rt.commit()
	m.pins.errorReset.Set(true)
	m.rt.invoke()
	if get("error") != 1 || get("error-id") != float64(errTrayEmpty) {
		t.Errorf("a raise in the reset's run: error=%v id=%v, want it latched", get("error"), get("error-id"))
	}
}

// TestCycleParkHandshake: park is the job's handshake on the park edge, and
// not under estop.
func TestCycleParkHandshake(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()

	m.pins.estopOn.Set(true)
	m.pins.park.Set(true)
	m.rt.invoke()
	if get("busy") != 0 {
		t.Fatal("a park under estop was accepted")
	}
	m.pins.estopOn.Set(false)
	m.pins.park.Set(false)
	m.rt.invoke()
	m.pins.park.Set(true)
	m.rt.invoke()
	m.rt.read()
	if get("busy") != 1 || m.rt.in.park_seq != 1 || m.rt.in.busy_park != 1 {
		t.Fatalf("park: busy=%v seq=%d busy_park=%d", get("busy"), m.rt.in.park_seq, m.rt.in.busy_park)
	}
	// home is not counted while busy.
	home := uint32(m.rt.in.edges.home)
	m.pins.home.Set(true)
	m.rt.invoke()
	m.rt.read()
	if uint32(m.rt.in.edges.home) != home {
		t.Error("a home press while busy was counted")
	}
	m.pins.parked.Set(true)
	m.rt.endPark(1)
	m.rt.commit()
	m.rt.invoke()
	if get("busy") != 0 || get("parked") != 1 {
		t.Errorf("after the end: busy=%v parked=%v, want 0 1 in one run", get("busy"), get("parked"))
	}
}

// TestCycleSync: sync is the cycle of the frame the control loop last read,
// out with its next commit; cycle counts the runs.
func TestCycleSync(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()
	m.rt.invoke()
	m.rt.read()
	m.rt.invoke()
	m.rt.commit()
	m.rt.invoke()
	if get("cycle") != 4 || get("sync") != 2 {
		t.Errorf("cycle=%v sync=%v, want 4 2", get("cycle"), get("sync"))
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
// an output still reading the value from before the write (the scenegate
// runtest caught a descent under a scene-busy that had already been dropped,
// when scene-busy was still the loop's).
func TestOutputsBeforeMotion(t *testing.T) {
	m, get := rtModule(t)
	mot := newFakeMotion(m.cfg.NumJoints)
	cm := committedMotion{motionControl: mot, rt: m.rt}

	seen := -1.0
	mot.onMove = func(moveCall) {
		seen = get("proc.20.release") // the pin as the command is sent
	}

	m.pins.procs[0].release.Set(true)
	if err := cm.SetLine(motctl.Pose{X: 1}, 10, 10, 100, 0, 1, 0, -1); err != nil {
		t.Fatalf("SetLine: %v", err)
	}
	if seen != 1 {
		t.Errorf("release = %v when the move was taken, want 1", seen)
	}
}

// TestCycleSceneLock: the lock is granted in the run the request meets
// scene-hold low, scene-busy with it; hold in the run of the request wins;
// granted, it stays whatever hold does until the request drops.
func TestCycleSceneLock(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()

	m.rt.requestScene(true)
	m.rt.commit()
	m.rt.invoke()
	m.rt.read()
	if get("scene-busy") != 1 || !m.rt.sceneGranted() {
		t.Fatalf("request, hold low: scene-busy=%v granted=%v, want both", get("scene-busy"), m.rt.sceneGranted())
	}
	m.pins.sceneHold.Set(true)
	m.rt.invoke()
	if get("scene-busy") != 1 {
		t.Error("hold raised after the grant took the lock back")
	}
	m.rt.requestScene(false)
	m.rt.commit()
	m.rt.invoke()
	if get("scene-busy") != 0 {
		t.Fatal("the request dropped, scene-busy stayed")
	}

	// Hold up when the request comes: no grant until it drops.
	m.rt.requestScene(true)
	m.rt.commit()
	m.rt.invoke()
	m.rt.invoke()
	if get("scene-busy") != 0 {
		t.Error("granted against scene-hold")
	}
	m.pins.sceneHold.Set(false)
	m.rt.invoke()
	if get("scene-busy") != 1 {
		t.Error("hold dropped, the waiting request not granted in that run")
	}
}

// TestCycleLiftWatch: a watch fires in the run motion runs a segment beyond
// its id, and holds its stations' release low from that run on, whatever the
// loop's frame still says -- until the loop ends the watch.
func TestCycleLiftWatch(t *testing.T) {
	m, get := rtModule(t)
	mot := dztest.NewFakeMot()
	defer mot.Free()
	m.deadzone.setMot(mot.Ptr())
	defer m.deadzone.setMot(nil)

	m.pins.procs[0].release.Set(true)
	m.rt.armWatch(math.MaxInt32, 1)
	m.rt.commit()
	mot.SetSnapshot(false, 3, 10, 0, 0, 0)
	m.rt.invoke()
	m.rt.read()
	if get("proc.20.release") != 1 || m.rt.watchFired() {
		t.Fatal("a watch with no segment yet fired")
	}

	m.rt.setWatchID(12)
	m.rt.commit()
	mot.SetSnapshot(false, 3, 12, 0, 0, 0) // the watched segment itself: not past it
	m.rt.invoke()
	m.rt.read()
	if get("proc.20.release") != 1 || m.rt.watchFired() {
		t.Fatal("fired on the watched segment, not past it")
	}
	mot.SetSnapshot(false, 3, 13, 0, 0, 0)
	m.rt.invoke()
	m.rt.read()
	if get("proc.20.release") != 0 || !m.rt.watchFired() {
		t.Fatalf("past the segment: release=%v fired=%v, want 0 and fired", get("proc.20.release"), m.rt.watchFired())
	}

	// The loop's hook drops release itself and ends the watch; a new watch
	// does not inherit the old one's fire.
	m.pins.procs[0].release.Set(false)
	m.rt.disarmWatch()
	m.pins.procs[0].release.Set(true)
	m.rt.armWatch(20, 1)
	m.rt.commit()
	m.rt.invoke()
	m.rt.read()
	if get("proc.20.release") != 1 || m.rt.watchFired() {
		t.Errorf("a new watch: release=%v fired=%v, want 1 and not fired", get("proc.20.release"), m.rt.watchFired())
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

// TestEdgesGatedAtThePress: machine-on and home are not counted under estop
// (nor a park accepted), the manual picker presses neither under estop nor in auto -- judged
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
	m.rt.read()
	if e.machine_on != 0 || e.home != 0 || m.rt.in.park_seq != 0 || e.picker[0].open != 0 {
		t.Errorf("counted under estop: machine-on %d home %d park %d open %d",
			e.machine_on, e.home, m.rt.in.park_seq, e.picker[0].open)
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

// TestJobAcceptedBeforeTheLoopRuns: a start-job edge between the cycle
// function's first run and the loop's start is accepted (busy up). The loop
// takes it as a request, not as history, or busy would never fall.
func TestJobAcceptedBeforeTheLoopRuns(t *testing.T) {
	m, _ := rtModule(t)
	m.rt.invoke()
	m.pins.startJob.Set(true)
	m.rt.invoke()

	c := newControl(m)
	c.primeEdges()
	if seq, ok := c.jobRequest(); !ok || seq != 1 {
		t.Errorf("jobRequest = %d %v, want the accepted job 1", seq, ok)
	}
	if _, ok := c.jobRequest(); ok {
		t.Error("the job was taken twice")
	}

	// Without one in flight, everything accepted is history.
	m2, _ := rtModule(t)
	m2.rt.invoke()
	c2 := newControl(m2)
	c2.primeEdges()
	if _, ok := c2.jobRequest(); ok {
		t.Error("a request with nothing accepted")
	}
}

// teachModule is a cycle function with a motion snapshot it can be fed, and a
// control loop that is only used to publish (pickers=2).
func teachModule(t *testing.T) (*pnptaskModule, *control, *dztest.FakeMot, func(string) float64) {
	t.Helper()
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, name, "pickers=2")
	m.rt.runRT = m.rt.invoke
	mot := dztest.NewFakeMot()
	t.Cleanup(mot.Free)
	m.deadzone.setMot(mot.Ptr())
	t.Cleanup(func() { m.deadzone.setMot(nil) })
	get := func(pin string) float64 {
		t.Helper()
		v, ok := hal.LookupValue(name + "." + pin)
		if !ok {
			t.Fatalf("pin %s not found", pin)
		}
		return v
	}
	return m, newControl(m), mot, get
}

// The teach pins report where each picker is: feedback plus that picker's
// offset (D21), computed by the cycle function every servo cycle from its
// motion snapshot. Targeting commands (x - offset), so this is its inverse. On
// a metric machine the -mu (machine unit, D26) pins carry the same values.
func TestPickerTeachPositions(t *testing.T) {
	m, c, mot, get := teachModule(t)
	// The offsets are taught with halcmd setp; here, directly. The loop hands
	// them to the function when it publishes.
	m.pins.pickers[1].xOffset.Set(40)
	m.pins.pickers[1].yOffset.Set(-15)
	c.publish()
	m.rt.commit()
	mot.SetSnapshot(false, 0, 0, 100, 200, 0)
	m.rt.invoke()
	if x0, x1 := get("picker.0.pos-x"), get("picker.1.pos-x"); x0 != 100 || x1 != 140 {
		t.Errorf("pos-x = %g/%g, want 100/140", x0, x1)
	}
	if got := get("picker.1.pos-y"); got != 185 {
		t.Errorf("picker.1.pos-y = %g, want 185 (200 + -15)", got)
	}
	if x, y := get("picker.1.pos-x-mu"), get("picker.1.pos-y-mu"); x != 140 || y != 185 {
		t.Errorf("metric -mu teach pins = %g/%g, want 140/185 (equal to the mm pins)", x, y)
	}

	// Every cycle, with no commit of the loop's in between.
	mot.SetSnapshot(false, 0, 0, 110, 200, 0)
	m.rt.invoke()
	if x := get("picker.0.pos-x"); x != 110 {
		t.Errorf("pos-x = %g after the head moved, want 110 without the loop", x)
	}
}

// On an inch machine the -mu teach pins carry machine units — the value the
// INI wants — while the plain pins keep the internal mm (D23/D26). Pasting the
// mm pins into the INI was the 25.4x teach hole of the phase-3 review.
func TestPickerTeachPositionsInchMachine(t *testing.T) {
	m, c, mot, get := teachModule(t)
	m.cfg.LinearUnits = 1.0 / 25.4
	c.publish()
	m.rt.commit()
	mot.SetSnapshot(false, 0, 0, 254, 508, 0) // mm feedback: 10 by 20 inches
	m.rt.invoke()
	if x := get("picker.0.pos-x"); x != 254 {
		t.Errorf("pos-x = %g, want 254 mm", x)
	}
	if x, y := get("picker.0.pos-x-mu"), get("picker.0.pos-y-mu"); math.Abs(x-10) > 1e-9 || math.Abs(y-20) > 1e-9 {
		t.Errorf("inch -mu teach pins = %g/%g, want 10/20 (machine units)", x, y)
	}
}

// TestTaskStalledMatchesTheCycleFunction: the id the cycle function latches
// is the loop's errTaskStalled.
func TestTaskStalledMatchesTheCycleFunction(t *testing.T) {
	if errorID(rtTaskStalled) != errTaskStalled {
		t.Errorf("PNP_RT_TASK_STALLED = %d, errTaskStalled = %d", rtTaskStalled, errTaskStalled)
	}
}

// TestCycleChangeCounter (§6.1): the doorbell rings for what the loop acts on --
// an input, the motion snapshot's segment -- not for position alone, and once
// when servo time reaches the loop's wake time.
func TestCycleChangeCounter(t *testing.T) {
	m, _ := rtModule(t)
	mot := dztest.NewFakeMot()
	defer mot.Free()
	m.deadzone.setMot(mot.Ptr())
	defer m.deadzone.setMot(nil)
	bell := m.rt.bell
	ring := func(what string, want bool) {
		t.Helper()
		before := bell.Change()
		m.rt.invoke()
		if got := bell.Change() != before; got != want {
			t.Errorf("%s: rang = %v, want %v", what, got, want)
		}
	}

	mot.SetSnapshot(false, 0, 5, 10, 10, 0)
	m.rt.invoke()
	m.rt.invoke()
	ring("nothing changed", false)
	m.pins.waitAbort.Set(true)
	ring("an input changed", true)
	ring("... and stays", false)
	mot.SetSnapshot(false, 0, 5, 20, 30, 0)
	ring("only the position moved", false)
	mot.SetSnapshot(false, 0, 6, 20, 30, 0)
	ring("the next segment runs", true)

	m.rt.read()
	m.rt.setWake(time.Duration(m.rt.in.servo_ns) + 3*time.Millisecond)
	m.rt.commit()
	ring("before the wake time", false)
	ring("before the wake time", false)
	ring("the wake time", true)
	ring("past it: once only", false)
}

// TestCycleStallCheck (§7.2): a ring the loop leaves untaken for task-timeout
// latches TASK_STALLED; taken in time, or with the check off, nothing.
func TestCycleStallCheck(t *testing.T) {
	m, get := rtModule(t)
	m.rt.invoke()
	m.rt.read()
	m.rt.setTaskTimeout(0.005)
	m.rt.commit() // takes everything so far

	m.pins.waitAbort.Set(true) // a ring
	for i := 0; i < 4; i++ {
		m.rt.invoke()
	}
	if get("error") != 0 {
		t.Fatal("stalled before task-timeout")
	}
	m.rt.read()
	m.rt.commit() // taken in time
	for i := 0; i < 10; i++ {
		m.rt.invoke()
	}
	if get("error") != 0 {
		t.Fatal("stalled with the ring taken")
	}

	m.pins.waitAbort.Set(false) // another ring, never taken
	for i := 0; i < 6; i++ {
		m.rt.invoke()
	}
	if get("error") != 1 || get("error-id") != float64(errTaskStalled) {
		t.Errorf("untaken past task-timeout: error=%v id=%v, want TASK_STALLED", get("error"), get("error-id"))
	}
}

// TestTaskStalledEndsTheWait (§7.2): a TASK_STALLED latched by the cycle
// function ends the job in flight at its next abort check -- the PLC has given
// up on it -- while a fault the loop raised itself does not.
func TestTaskStalledEndsTheWait(t *testing.T) {
	m, _ := rtModule(t)
	c := newControl(m)
	latchIn(&m.rt.in, errTrayEmpty)
	if err := c.abortCheck(); err != nil {
		t.Fatalf("a fault of the loop's own ended the wait: %v", err)
	}
	latchIn(&m.rt.in, errTaskStalled)
	var f *pnpFault
	if err := c.abortCheck(); !errors.As(err, &f) || f.id != errTaskStalled {
		t.Errorf("abortCheck with TASK_STALLED latched = %v, want TASK_STALLED", err)
	}
}
