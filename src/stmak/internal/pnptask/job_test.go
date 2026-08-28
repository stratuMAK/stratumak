// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"bytes"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// machineSim drives the feedback a PLC and the gripper hardware provide: the
// picker opened/closed pins follow each close command, and every process
// station's released follows its release request.
//
// It runs on its own goroutine at the loop rate, which is how the real signals
// arrive too — the module reads them out of its input snapshot, so the two only
// meet through HAL pins.
type machineSim struct {
	f    *machineFixture
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}

	lastClose []bool
	gripMiss  []bool

	// lastRelease/releaseRises track each process station's release request.
	// Counting the rises is how §8's "no re-clamp in between" is observed: the
	// module waits for released to follow every change it makes, so the sim
	// cannot miss one — two rises within a job mean the fixture was closed and
	// opened again between the swap and the place.
	lastRelease  []bool
	releaseRises []int

	// missesLeft makes the next N close commands report "closed onto nothing",
	// which is how an empty tray slot presents itself (§5.2).
	missesLeft int
	// jammedOpen: the picker never reports opened again once it has closed.
	jammedOpen bool
	// jammedClosed: the picker never leaves opened, i.e. it never closes at all.
	jammedClosed bool
	// fixtureStuck: released never follows release.
	fixtureStuck bool
}

func newMachineSim(f *machineFixture) *machineSim {
	return newMachineSimOpts(f, nil)
}

// newMachineSimOpts is newMachineSim with a hook that runs before the first
// step(): for knobs that must be armed before the sim ever publishes, like a
// pre-armed miss judging a close command that was issued during startControl.
func newMachineSimOpts(f *machineFixture, prep func(*machineSim)) *machineSim {
	n := len(f.m.pins.pickers)
	s := &machineSim{
		f:            f,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		lastClose:    make([]bool, n),
		gripMiss:     make([]bool, n),
		lastRelease:  make([]bool, len(f.m.pins.procs)),
		releaseRises: make([]int, len(f.m.pins.procs)),
	}
	if prep != nil {
		prep(s)
	}
	s.step() // publish the resting state before the loop can sample it
	go s.run()
	f.t.Cleanup(s.shutdown)
	return s
}

func (s *machineSim) shutdown() {
	select {
	case <-s.stop:
		return // already stopped
	default:
	}
	close(s.stop)
	<-s.done
}

// pickSettleBudget is what every fixture gives the simulated gripper to answer
// a close or an open in. It is a duration rather than a cycle count on purpose:
// what it has to survive is the machine descheduling this process, which has
// nothing to do with how fast the control loop is configured to run.
const pickSettleBudget = 50 * time.Millisecond

// simStepInterval is how often the simulated hardware republishes. It runs
// several times per control cycle deliberately.
//
// What the settle times budget for is the hardware taking time to answer, and
// the control loop already enforces the ordering the cases care about: dwell
// costs at least one cycle, so a feedback check always reads a snapshot taken
// after the command it is checking. The sim's own tick rate adds nothing to
// that — it only decides how much of a settle budget is spent waiting for this
// goroutine to be scheduled at all. At one tick per cycle that was most of it:
// time.Ticker coalesces the ticks missed during a scheduler stall into one, so
// a stall as long as the settle time left the sim a single step to answer in.
// Stepping several times per cycle takes the sim out of that race without
// inflating any budget or costing a case any wall time.
//
// Read per sim rather than as a package var: fastLoop lowers pollInterval when
// a case starts, long after package initialisation would have captured it.
func simStepInterval() time.Duration {
	if d := pollInterval / 4; d > 0 {
		return d
	}
	return time.Microsecond
}

func (s *machineSim) run() {
	defer close(s.done)
	t := time.NewTicker(simStepInterval())
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.step()
		}
	}
}

func (s *machineSim) step() {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.f.m.pins
	for i := range p.pickers {
		pk := &p.pickers[i]
		closing := pk.close.Get()
		if closing != s.lastClose[i] {
			s.lastClose[i] = closing
			if closing {
				// Decide once per close command whether this position holds
				// anything: a gripper that finds nothing runs to fully closed.
				s.gripMiss[i] = s.missesLeft > 0
				if s.gripMiss[i] {
					s.missesLeft--
				}
			}
		}
		switch {
		case s.jammedClosed:
			pk.opened.Set(true)
			pk.closed.Set(false)
		case closing:
			pk.opened.Set(false)
			pk.closed.Set(s.gripMiss[i])
		default:
			pk.opened.Set(!s.jammedOpen)
			pk.closed.Set(false)
		}
	}
	for i := range p.procs {
		want := p.procs[i].release.Get()
		if want && !s.lastRelease[i] {
			s.releaseRises[i]++
		}
		s.lastRelease[i] = want
		if s.fixtureStuck {
			continue
		}
		p.procs[i].released.Set(want)
	}
}

// releaseRiseCount is how often a station's release request has been raised.
func (s *machineSim) releaseRiseCount(i int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseRises[i]
}

func (s *machineSim) resetReleaseRises() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.releaseRises {
		s.releaseRises[i] = 0
	}
}

func (s *machineSim) set(f func(*machineSim)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// jobFixture is a machine ready to run jobs: enabled, homed, parked inside the
// travel envelope, with the settle times turned down and the gripper/fixture
// feedback simulated.
type jobFixture struct {
	*machineFixture
	sim *machineSim
}

// newJobFixtureOpts loads a job-capable machine: auto mode, the settle times
// turned down, and the gripper/fixture feedback simulated. It does not home or
// park the machine — the cases about homing do that themselves.
func newJobFixtureOpts(t *testing.T, o fixtureOpts) *jobFixture {
	t.Helper()
	userPrep := o.prep
	o.prep = func(t *testing.T, m *pnptaskModule) {
		// The INI seeds tenths of a second; a case does not need to wait them out
		// to prove a sequence. Zero still costs one cycle, which is what makes the
		// feedback checks read a fresh sample (see dwell).
		m.pins.posSettleTime.Set(0)
		// The pick settle time is what the gripper feedback is given to arrive
		// in, and it is sized against SCHEDULER JITTER rather than against the
		// loop rate. dwell counts wall-clock time while the sim answers on its
		// own goroutine, so a stall longer than this budget leaves the deadline
		// already expired on the first cycle after it: the feedback check then
		// judges a sample the gripper never had a chance to appear in, and the
		// case fails with PICKER_CLOSE_FAILED against working code. A handful
		// of cycles (10 ms) sat inside the jitter of a loaded machine and was
		// the suite's dominant false failure.
		m.pins.pickSettleTime.Set(pickSettleBudget.Seconds())
		m.pins.releaseTime.Set(0)
		// Auto mode: jobs are accepted (§6.4). An unwired pin reads low, which is
		// manual mode.
		m.pins.autoEnable.Set(true)
		if userPrep != nil {
			userPrep(t, m)
		}
	}
	f := newMachineFixtureOpts(t, o)
	jf := &jobFixture{machineFixture: f}
	jf.sim = newMachineSim(f)
	return jf
}

// newJobFixture is a machine ready to run jobs: enabled, homed and parked inside
// the travel envelope.
func newJobFixture(t *testing.T, args ...string) *jobFixture {
	t.Helper()
	return newJobFixtureSeeded(t, nil, args...)
}

// newJobFixtureSeeded is newJobFixture with the world model seeded first — the
// state a job engine cannot be asked to produce (a station that already holds
// material, a picker loaded by a manual intervention). It has to happen before
// the loop starts: after that the model belongs to the control goroutine.
func newJobFixtureSeeded(t *testing.T, seed func(*world), args ...string) *jobFixture {
	t.Helper()
	jf := newJobFixtureOpts(t, fixtureOpts{
		args: args,
		prep: func(_ *testing.T, m *pnptaskModule) {
			if seed != nil {
				seed(m.world)
			}
		},
	})
	jf.homed()
	// Park the machine where a homing sequence would leave it *inside* the travel
	// envelope. The eroded outer limit is what a route's start point has to lie
	// in, so a machine parked at the very corner of its axis range has no valid
	// start — which is why the outer-limit drawing has to cover wherever the
	// machine can stand, with the clearance to spare.
	jf.mot.setPos(100, 100, 60)
	return jf
}

// tray is the fixture's single tray station (INI id 10), procN the process
// stations (ids 20 and, in the fuller config, none — see stationSections).
func (f *jobFixture) tray() *trayPins { return &f.m.pins.trays[0] }
func (f *jobFixture) proc() *procPins { return &f.m.pins.procs[0] }

// selectTray points the tray station at a TRAYDEF and waits for it to take.
func (f *jobFixture) selectTray(id uint32) {
	f.t.Helper()
	f.tray().trayID.Set(id)
	f.eventually("tray geometry selected", func() bool {
		w := f.m.world
		return f.bit("tray.10.empty") || f.bit("tray.10.full") || w == nil
	})
}

// fillTray marks every slot as holding material at the given process step.
func (f *jobFixture) fillTray(step uint32) {
	f.t.Helper()
	f.m.pins.processStep.Set(step)
	f.pulse(f.tray().setFull)
	f.eventually("tray full", func() bool { return f.bit("tray.10.full") })
}

// runJob commands one job and waits for the handshake to complete.
func (f *jobFixture) runJob(origin, dest, step uint32) {
	f.t.Helper()
	// Drive start-job low and give the loop time to see it: the handshake has to
	// be re-armed before a level counts as a new request (see jobRequest).
	f.m.pins.startJob.Set(false)
	time.Sleep(levelHold)
	f.m.pins.originID.Set(origin)
	f.m.pins.destID.Set(dest)
	f.m.pins.processStep.Set(step)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)
	f.eventually("the job handshake to complete", func() bool { return !f.bit("start-job") })
	// busy is cleared in the same cycle as start-job, but read it back so a case
	// cannot pass with the module still claiming to be running.
	f.eventually("busy to clear", func() bool { return !f.bit("busy") })
	f.m.pins.startJob.Set(false)
}

// errorID is the latched error code, as the PLC reads it.
func (f *jobFixture) errorID() errorID { return errorID(f.get("error-id")) }

// requireOK fails unless the last job finished without latching anything.
func (f *jobFixture) requireOK(what string) {
	f.t.Helper()
	if f.bit("error") {
		f.t.Fatalf("%s: job failed with %s", what, f.errorID())
	}
}

// requireError fails unless the given error id is latched.
func (f *jobFixture) requireError(what string, want errorID) {
	f.t.Helper()
	if !f.bit("error") {
		f.t.Fatalf("%s: no error latched, want %s", what, want)
	}
	if got := f.errorID(); got != want {
		f.t.Fatalf("%s: error-id = %s, want %s", what, got, want)
	}
}

// clearError resets the latch so the next job in a case can run.
func (f *jobFixture) clearError() {
	f.t.Helper()
	f.pulse(f.m.pins.errorReset)
	f.eventually("error cleared", func() bool { return !f.bit("error") })
	f.m.pins.errorReset.Set(false)
}

// zAtXY returns the Z values commanded at the given XY, in dispatch order —
// enough to check "down to the pick height, back up to the movement height"
// without pinning the exact move count.
func zAtXY(moves []moveCall, x, y float64) []float64 {
	var zs []float64
	for _, m := range moves {
		if math.Abs(m.pos.X-x) < 1e-6 && math.Abs(m.pos.Y-y) < 1e-6 {
			zs = append(zs, m.pos.Z)
		}
	}
	return zs
}

// ---------------------------------------------------------------------------
// The happy paths
// ---------------------------------------------------------------------------

// TestJobTrayToProc is the full cycle: pick from the first tray slot, place into
// the process station, with the fixture handshake on the place side.
func TestJobTrayToProc(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)

	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc")

	if !f.bit("proc.20.has-material") {
		t.Error("the process station does not report material after a place")
	}
	if f.bit("picker.0.missing") {
		t.Error("missing is set after a successful pick")
	}
	// The picker let go and holds nothing.
	if f.bit("picker.0.close") {
		t.Error("the picker is still closed after the place")
	}

	// TRAYDEF 1 is a 10x4 grid over (120,400)..(210,430) in C+R-~ order: R- runs
	// the rows from the far end, so the first slot of a fresh fill is (120, 430).
	moves := f.mot.moveList()
	// [PNPTASK_ROUTE_0] overrides the movement height for 10 -> 20 to 15 mm.
	const height = 15.0
	slotZ := zAtXY(moves, 120, 430)
	if len(slotZ) < 2 {
		t.Fatalf("no approach at the first tray slot: moves %+v", moves)
	}
	if slotZ[len(slotZ)-2] != 2.5 || slotZ[len(slotZ)-1] != height {
		t.Errorf("tray approach Z sequence = %v, want ... 2.5 then %g", slotZ, height)
	}
	procZ := zAtXY(moves, 300, 200)
	if len(procZ) < 2 {
		t.Fatalf("no approach at the process station: moves %+v", moves)
	}
	if procZ[len(procZ)-2] != 5.0 || procZ[len(procZ)-1] != height {
		t.Errorf("proc approach Z sequence = %v, want ... 5 then %g", procZ, height)
	}

	// A second job takes the *next* slot: the first one is empty now. Station 20
	// is occupied, so this one goes to the other tray geometry's position — use
	// the tray as its own destination instead, which is legal and lands in the
	// slot the pick just freed.
	f.runJob(10, 10, 0)
	f.requireOK("tray -> same tray")
	if zs := zAtXY(f.mot.moveList(), 130, 430); len(zs) == 0 {
		t.Errorf("the second job did not visit the second slot (130, 430): moves %+v",
			f.mot.moveList())
	}
}

// TestJobProcToTray covers the other direction: the pick side's release
// handshake, and a place that stamps the job's process step into the slot.
func TestJobProcToTray(t *testing.T) {
	// The station holds a part; the tray is empty and takes it.
	f := newJobFixtureSeeded(t, func(w *world) { w.procs[0].setHasMaterial(true) })
	f.selectTray(1)
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJob(20, 10, 7)
	f.requireOK("proc -> tray")

	if f.bit("proc.20.has-material") {
		t.Error("the process station still reports material after the pick")
	}
	if f.bit("proc.20.release") {
		t.Error("release is still asserted at the end of the job (D19)")
	}
	if f.bit("tray.10.empty") {
		t.Error("the tray reports empty although a part was placed into it")
	}
	// The part is in the tray at step 7, so the tray has step-7 material
	// available and no step-0 material. avail follows the tray's OWN step pin,
	// not the job request pin — the whole point of it being a separate input.
	f.m.pins.trays[0].step.Set(0)
	f.eventually("nothing available at step 0", func() bool { return !f.bit("tray.10.avail") })
	f.m.pins.trays[0].step.Set(7)
	f.eventually("step-7 material available", func() bool { return f.bit("tray.10.avail") })
	// ... and the job request pin moving has no effect on it at all.
	f.m.pins.processStep.Set(0)
	f.consistently("avail ignores the job step", func() bool { return f.bit("tray.10.avail") })
}

// TestJobUsesTheHoldingPicker pins D20 on the single-picker engine: the place is
// performed by the picker the world says holds the job's material, with that
// picker's offsets — not by picker 0 by construction. Picker 1 is the only free
// one here, so a correct engine picks and places with it and applies its offsets.
func TestJobUsesTheHoldingPicker(t *testing.T) {
	// Picker 0 is loaded (a manual intervention, say), so the free picker is 1.
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 21, false, 0, true) }, "pickers=2")
	f.m.pins.pickers[1].xOffset.Set(40)
	f.m.pins.pickers[1].yOffset.Set(-15)
	f.selectTray(1)
	f.fillTray(0)

	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc with picker 1")

	if f.bit("picker.0.close") {
		t.Error("picker 0 was actuated although picker 1 was the free one")
	}
	// Targeting commands (target - offset), so picker 1 over the first slot
	// (120, 430) means a machine position of (80, 445).
	moves := f.mot.moveList()
	if zs := zAtXY(moves, 80, 445); len(zs) == 0 {
		t.Errorf("no approach at picker 1's offset position for the first slot: moves %+v", moves)
	}
	if zs := zAtXY(moves, 260, 215); len(zs) == 0 {
		t.Errorf("no approach at picker 1's offset position for the station: moves %+v", moves)
	}

	w := f.stopped()
	if w.held[1].present {
		t.Error("picker 1 still holds material after placing it")
	}
	if !w.held[0].present {
		t.Error("the job disturbed the other picker's held record")
	}
}

// TestJobMoveLimits checks that every move a job dispatches keeps each
// participating axis inside the lower of its [AXIS_*] maximum and the INI
// ceiling — and that the ceiling is a PER-AXIS one, so a diagonal travel is
// commanded ABOVE MOVE_VEL rather than braking its axes down to it.
func TestJobMoveLimits(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// Where the fixture parked the machine: each move's displacement is the step
	// from the previous target, and the first one starts here.
	start, err := f.mot.GetPosCmd()
	if err != nil {
		t.Fatalf("reading the start position: %v", err)
	}
	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc")

	// The axis maxima of machineIniAxes, and the INI ceilings of pnptaskSection.
	axVel := [3]float64{800, 500, 300}
	axAcc := [3]float64{4000, 4000, 3000}
	const moveVel, moveAcc = 500.0, 2000.0
	const zVel, zAcc = 50.0, 500.0

	prev := toMotctlPose(start)
	var sawTravel, sawZ, sawDiagonal bool
	for _, m := range f.mot.moveList() {
		d := [3]float64{
			math.Abs(m.pos.X - prev.X),
			math.Abs(m.pos.Y - prev.Y),
			math.Abs(m.pos.Z - prev.Z),
		}
		prev = m.pos
		length := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
		if length == 0 {
			t.Errorf("move to %+v displaces nothing", m.pos)
			continue
		}

		velCeil, accCeil := moveVel, moveAcc
		what := "travel"
		switch m.termCond {
		case tpTermCondParabolic:
			sawTravel = true
		case tpTermCondStop:
			sawZ = true
			velCeil, accCeil, what = zVel, zAcc, "Z stroke"
		default:
			t.Errorf("move dispatched under termination condition %d", m.termCond)
			continue
		}

		// Per-axis: the path value scaled by the axis's share of the move.
		var atCeiling bool
		for i, letter := range []string{"X", "Y", "Z"} {
			share := d[i] / length
			v, a := m.vel*share, m.acc*share
			wantV, wantA := math.Min(axVel[i], velCeil), math.Min(axAcc[i], accCeil)
			if v > wantV+1e-6 || a > wantA+1e-6 {
				t.Errorf("%s to %+v runs %s at vel %g acc %g, want at most %g/%g",
					what, m.pos, letter, v, a, wantV, wantA)
			}
			if d[i] > 0 && (v > wantV-1e-6 || a > wantA-1e-6) {
				atCeiling = true
			}
		}
		// The blend has to be tight: some axis reaches its ceiling, or the move
		// is slower than it needs to be — which is the whole point here.
		if !atCeiling {
			t.Errorf("%s to %+v leaves every axis below its ceiling at vel %g acc %g",
				what, m.pos, m.vel, m.acc)
		}
		// A move that displaces X and Y at once must exceed the per-axis
		// ceiling on the path: both axes cap at 500, so any diagonal has a path
		// speed above it. Under the old path-feed reading this was the value
		// that got clamped, and X gave up speed at every corner.
		if m.termCond == tpTermCondParabolic && d[0] > 0 && d[1] > 0 {
			sawDiagonal = true
			if m.vel <= moveVel {
				t.Errorf("diagonal travel to %+v commanded at path vel %g, want more than MOVE_VEL = %g",
					m.pos, m.vel, moveVel)
			}
		}
	}
	if !sawTravel || !sawZ || !sawDiagonal {
		t.Errorf("job dispatched travel = %v, Z strokes = %v, diagonal travel = %v; want all three",
			sawTravel, sawZ, sawDiagonal)
	}
}

// TestMoveLimitsPerAxisCeiling is the corner from §7.3 in isolation: an X-only
// segment followed by a 45° XY one. The X axis must be commanded at the same
// speed in both — a pick-and-place move wants the shortest time, not the
// constant path feed a cutter needs.
func TestMoveLimitsPerAxisCeiling(t *testing.T) {
	c := &control{m: &pnptaskModule{limits: &motsetup.Result{
		MaxVelocity:     600,
		MaxAcceleration: 3000,
		AxisMaxVel:      [motsetup.MaxAxes]float64{800, 800, 300},
		AxisMaxAcc:      [motsetup.MaxAxes]float64{4000, 4000, 3000},
	}}}
	const vReq, aReq = 500.0, 2000.0
	at := func(x, y, z float64) motctl.Pose { return motctl.Pose{X: x, Y: y, Z: z} }

	cases := []struct {
		name             string
		from, to         motctl.Pose
		wantVel, wantAcc float64
	}{
		// X alone: path and axis are the same thing, so the ceiling applies as
		// it always did.
		{"x only", at(0, 0, 0), at(100, 0, 0), vReq, aReq},
		// 45°: X keeps vReq and Y comes up alongside it, so the path runs √2
		// faster. The old path-feed reading returned vReq here and braked X.
		{"45 degrees", at(0, 0, 0), at(100, 100, 0), vReq * math.Sqrt2, aReq * math.Sqrt2},
		// Shallow: Y is the short axis, so X still sets the time and the path is
		// its speed over the cosine.
		{"shallow", at(0, 0, 0), at(100, 50, 0), vReq * math.Sqrt(1.25), aReq * math.Sqrt(1.25)},
		// Z below the ceiling caps itself: [AXIS_Z] allows 300, not vReq.
		{"z limited", at(0, 0, 0), at(0, 0, 100), 300, 2000},
		// A ceiling of 0 is no ceiling at all: the axis maxima alone.
		{"no ceiling", at(0, 0, 0), at(100, 0, 0), 800, 4000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			velReq, accReq := vReq, aReq
			if tc.name == "no ceiling" {
				velReq, accReq = 0, 0
			}
			vel, acc, moved := c.moveLimits(tc.from, tc.to, velReq, accReq)
			if !moved {
				t.Fatal("moveLimits reports no displacement")
			}
			if math.Abs(vel-tc.wantVel) > 1e-9 || math.Abs(acc-tc.wantAcc) > 1e-9 {
				t.Errorf("vel/acc = %g/%g, want %g/%g", vel, acc, tc.wantVel, tc.wantAcc)
			}
		})
	}
}

// TestJobNaNZOffsetFaults: a non-finite move target (a z-offset pin wired to a
// broken sensor) is a fault, never a silently skipped stroke — the silent skip
// ran the pick at travel height and marked good slots empty one after another.
func TestJobNaNZOffsetFaults(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.tray().zOffset.Set(math.NaN())

	f.runJob(10, 20, 0)
	f.requireError("a NaN pick height", errMotionError)
	if f.bit("tray.10.empty") {
		t.Error("the faulted job emptied the tray model")
	}
}

// TestWaitMotionDoneIgnoresStaleSnapshotAfterDispatch: the dispatch settle
// counts only cycles whose status read succeeded. Failed reads re-serve the
// last snapshot — five of them spanning a dispatch would otherwise hand the
// drain check the PRE-dispatch picture (standing still, queue empty) and let
// the full-stop barrier pass while the machine is still moving.
func TestWaitMotionDoneIgnoresStaleSnapshotAfterDispatch(t *testing.T) {
	fastLoop(t)
	setupPaths(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, testInstanceName(t))
	mot := newFakeMotion(m.cfg.NumJoints)
	m.mc, m.ms = mot, mot

	c := newControl(m)
	c.ticker = time.NewTicker(pollInterval)
	defer c.ticker.Stop()
	c.sample() // one good pre-dispatch snapshot: standing still, queue empty

	// The dispatch happens; every following read fails, so the loop keeps
	// serving the stale pre-dispatch snapshot within statusStaleTicks.
	c.motionDispatched = true
	mot.setInFlight(5)
	mot.setStatusErr(fmt.Errorf("split read"))

	done := make(chan error, 1)
	go func() { done <- c.waitMotionDone() }()
	select {
	case err := <-done:
		t.Fatalf("waitMotionDone returned (%v) on a stale pre-dispatch snapshot", err)
	case <-time.After(30 * pollInterval):
	}

	// Reads recover: the settle counts up, the move drains, the wait ends.
	mot.setStatusErr(nil)
	if err := <-done; err != nil {
		t.Fatalf("waitMotionDone after recovery: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Probing and the tray running out
// ---------------------------------------------------------------------------

// TestJobTrayProbingToEmpty covers D9 and MAX_UNPOPULATED: slots that turn out to
// be empty are corrected in the model and skipped, and after MAX_UNPOPULATED of
// them the tray is declared empty with TRAY_EMPTY and the picker's missing flag.
func TestJobTrayProbingToEmpty(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)

	// The first two candidates are empty, the third holds a part. MAX_UNPOPULATED
	// is 3 for TRAYDEF 1, so the job still succeeds.
	f.sim.set(func(s *machineSim) { s.missesLeft = 2 })
	f.runJob(10, 20, 0)
	f.requireOK("pick after two empty slots")
	if f.bit("picker.0.missing") {
		t.Error("missing is set after a pick that eventually succeeded")
	}
	// It probed slots 0 and 1 and picked from slot 2 — the tray positions are
	// 10 mm apart along the columns.
	moves := f.mot.moveList()
	for _, x := range []float64{120, 130, 140} {
		if zs := zAtXY(moves, x, 430); len(zs) == 0 {
			t.Errorf("no approach at (%g, 430): the probing did not walk the slots", x)
		}
	}

	// Put the part back where it came from, so the station is free for the next
	// job — and the tray has slots to offer again.
	f.runJob(20, 10, 0)
	f.requireOK("returning the part to the tray")

	// Now three in a row: the tray gives up.
	f.sim.set(func(s *machineSim) { s.missesLeft = 3 })
	f.runJob(10, 20, 0)
	f.requireError("three empty slots in a row", errTrayEmpty)
	if !f.bit("picker.0.missing") {
		t.Error("missing is not set after the tray was declared empty")
	}
	if f.bit("tray.10.avail") {
		t.Error("the tray still offers material after probing declared it empty")
	}

	// A declared-empty tray refuses the next job outright, without moving.
	f.clearError()
	if f.bit("picker.0.missing") {
		t.Error("error-reset did not clear the missing flag")
	}
	f.runJob(10, 20, 0)
	f.requireError("job against a tray declared empty", errTrayEmpty)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}

	// set-full is how the operator says the tray was refilled.
	f.clearError()
	f.sim.set(func(s *machineSim) { s.missesLeft = 0 })
	f.fillTray(0)
	f.runJob(10, 20, 0)
	f.requireOK("job after a refill")
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// TestJobValidationRefusals walks §7.4's VALIDATE: every one of these is refused
// before the machine moves.
func TestJobValidationRefusals(t *testing.T) {
	cases := []struct {
		name string
		// seed runs before the control loop exists, setup after it (see
		// newJobFixtureSeeded).
		seed   func(*world)
		setup  func(*jobFixture)
		origin uint32
		dest   uint32
		want   errorID
	}{
		{
			name:   "unknown origin",
			origin: 99, dest: 20, want: errInvalidOrigin,
		},
		{
			name:   "unknown destination",
			origin: 10, dest: 99, want: errInvalidDest,
		},
		{
			name: "dead-zone selector out of range",
			setup: func(f *jobFixture) {
				f.selectTray(1)
				f.fillTray(0)
				f.m.pins.deadzoneSelect.Set(7)
			},
			origin: 10, dest: 20, want: errInvalidDeadzoneSelect,
		},
		{
			name:   "tray-id names no TRAYDEF",
			setup:  func(f *jobFixture) { f.m.pins.trays[0].trayID.Set(77) },
			origin: 10, dest: 20, want: errInvalidTrayID,
		},
		{
			name:   "tray has nothing at this step",
			setup:  func(f *jobFixture) { f.selectTray(1); f.fillTray(0) },
			origin: 10, dest: 20, want: errTrayEmpty,
		},
		{
			name: "destination tray is full",
			seed: func(w *world) { w.procs[0].setHasMaterial(true) },
			setup: func(f *jobFixture) {
				f.selectTray(1)
				f.fillTray(0)
			},
			origin: 20, dest: 10, want: errTrayFull,
		},
		{
			name:   "process station holds nothing",
			setup:  func(f *jobFixture) { f.selectTray(1) },
			origin: 20, dest: 10, want: errProcNoMaterial,
		},
		{
			name: "process station is already occupied",
			seed: func(w *world) { w.procs[0].setHasMaterial(true) },
			setup: func(f *jobFixture) {
				f.selectTray(1)
				f.fillTray(0)
			},
			origin: 10, dest: 20, want: errProcHasMaterial,
		},
		{
			name: "no free picker",
			seed: func(w *world) { w.setHeld(0, 21, false, 0, true) },
			setup: func(f *jobFixture) {
				f.selectTray(1)
				f.fillTray(0)
			},
			origin: 10, dest: 20, want: errNoFreePicker,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJobFixtureSeeded(t, tc.seed)
			if tc.setup != nil {
				tc.setup(f)
			}
			// The "nothing at this step" case fills at step 0 and asks for step 1.
			step := uint32(0)
			if tc.want == errTrayEmpty {
				step = 1
			}
			f.runJob(tc.origin, tc.dest, step)
			f.requireError(tc.name, tc.want)
			if n := len(f.mot.moveList()); n != 0 {
				t.Errorf("a refused job dispatched %d moves, want none", n)
			}
		})
	}
}

// TestJobRefusedInManualMode: §6.4 — auto-enable low rejects the request with
// AUTO_DISABLED and completes the handshake.
func TestJobRefusedInManualMode(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.setBit(f.m.pins.autoEnable, false)

	f.runJob(10, 20, 0)
	f.requireError("start-job in manual mode", errAutoDisabled)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the rejected job dispatched %d moves, want none", n)
	}

	// Back in auto mode the same request runs.
	f.clearError()
	f.setBit(f.m.pins.autoEnable, true)
	f.runJob(10, 20, 0)
	f.requireOK("the same job in auto mode")
}

// TestJobRefusedWhileErrorLatched: faults latch first-error-wins, so a job
// started with the latch set could fail invisibly. It is refused instead, and the
// original error stays on the pin.
func TestJobRefusedWhileErrorLatched(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)

	// Provoke a first error: an empty tray.
	f.runJob(10, 20, 0)
	f.requireError("empty tray", errTrayEmpty)

	f.fillTray(0)
	f.runJob(10, 20, 0)
	f.requireError("job while an error is latched", errTrayEmpty)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}

	f.clearError()
	f.runJob(10, 20, 0)
	f.requireOK("job after the reset")
}

// TestJobNotHomed: AUTOHOME off and unhomed joints is a refusal, not a silent
// home (§6.3).
func TestJobNotHomed(t *testing.T) {
	jf := newJobFixtureOpts(t, fixtureOpts{
		tweak: func(cfg *Config) { cfg.AutoHome = false },
		prep:  func(_ *testing.T, m *pnptaskModule) { m.pins.trays[0].trayID.Set(1) },
	})
	jf.machineOn() // enabled but deliberately not homed
	jf.fillTray(0)

	jf.runJob(10, 20, 0)
	jf.requireError("job on an unhomed machine with AUTOHOME off", errNotHomed)
	if jf.bit("homed") {
		t.Error("the refused job homed the machine anyway")
	}
}

// TestJobAutohomesOnFirstJob: with AUTOHOME on, the first job homes the machine
// itself (§6.3) — and does so *after* validation, so a job with a bad station id
// does not move anything first.
func TestJobAutohomesOnFirstJob(t *testing.T) {
	jf := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) { m.pins.trays[0].trayID.Set(1) },
	})
	jf.machineOn()
	jf.fillTray(0)

	// A job that cannot run must not home the machine on its way to refusing.
	jf.runJob(99, 20, 0)
	jf.requireError("unknown origin", errInvalidOrigin)
	if jf.bit("homed") {
		t.Error("a job refused in validation homed the machine")
	}

	jf.clearError()
	jf.mot.setPos(100, 100, 60)
	jf.runJob(10, 20, 0)
	jf.requireOK("first job with AUTOHOME on")
	if !jf.bit("homed") {
		t.Error("the first job did not home the machine")
	}
}

// ---------------------------------------------------------------------------
// Busy gating (D15)
// ---------------------------------------------------------------------------

// TestJobBusyGateWaitsAtTheWaitPosition: a job whose destination station is busy
// travels to the station's wait position, holds there with the material, and
// finishes once busy clears.
func TestJobBusyGateWaitsAtTheWaitPosition(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.setBit(f.proc().busy, true)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)

	// [PNPTASK_PROC_0] waits at (250, 150).
	f.eventually("arrival at the wait position", func() bool {
		return len(zAtXY(f.mot.moveList(), 250, 150)) > 0
	})
	// It holds there, with the material gripped, and does not approach the
	// station.
	f.consistently("holding at the wait position", func() bool {
		return f.bit("busy") && f.bit("picker.0.close") &&
			len(zAtXY(f.mot.moveList(), 300, 200)) == 0
	})

	f.setBit(f.proc().busy, false)
	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("busy-gated job")
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds no material after the gated place")
	}
}

// TestJobBusyWaitAbortedByAutoEnable: D15 — dropping auto-enable ends the wait
// with WAIT_ABORTED, and the picker keeps holding its material for the manual
// handling the operator just asked for.
func TestJobBusyWaitAbortedByAutoEnable(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.setBit(f.proc().busy, true)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.startJob.Set(true)
	f.eventually("arrival at the wait position", func() bool {
		return len(zAtXY(f.mot.moveList(), 250, 150)) > 0
	})

	f.setBit(f.m.pins.autoEnable, false)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("auto-enable dropped during the wait", errWaitAborted)

	if !f.bit("picker.0.close") {
		t.Error("the picker released its material when the wait was aborted")
	}
	if f.bit("proc.20.has-material") {
		t.Error("the station reports material although the place never happened")
	}
	w := f.stopped()
	if !w.held[0].present || w.held[0].station != 10 {
		t.Errorf("held record after the abort = %+v, want the material from station 10", w.held[0])
	}
}

// ---------------------------------------------------------------------------
// The dead-zone-derived wait position (D29)
// ---------------------------------------------------------------------------

// waitZoneSections adds a process station that sits inside a dead zone of one
// drawing and is clear in another — the camera-in-a-sphere shape the derived
// wait position exists for. Station 21 is at (500, 100), the centre of the
// 100x100 zone zones.dxf draws at (450,50)..(550,150); zones_open.dxf has no
// zone at all.
const waitZoneSections = `
[PNPTASK_PROC_1]
ID = 21
X = 500.0
Y = 100.0
Z_PICK = 5.0
WAIT_DEADZONE = 1
WAIT_CLEAR_DEADZONE = 0
`

// newWaitZoneFixture is a job fixture whose station 21 holds material and whose
// scene is the blocked one, ready for a pick-from-proc that has to wait its way
// in. The machine parks at (100, 100), so the route in runs straight along
// y = 100: the zone's offset edge (CLEARANCE = 10) is at x = 440, and pulling
// back by BLEND_TOLERANCE = 2 puts the wait point at (438, 100).
func newWaitZoneFixture(t *testing.T) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{
		ini:   waitZoneSections,
		files: map[string]string{zonesA: fixtureOpen, zonesB: fixtureClear},
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.procs[1].setHasMaterial(true)
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	// Blocked scene, station working: the state a job arrives into.
	f.m.pins.deadzoneSelect.Set(1)
	f.setBit(f.m.pins.procs[1].busy, true)
	return f
}

// The derived wait point is not a taught number, so what the cases assert is
// the rule that produced it: on the y = 100 run in, it has to sit outside the
// zone's drawn left edge (x = 450) by CLEARANCE plus BLEND_TOLERANCE, and no
// further back than that — stopping as late as it safely can is the whole
// point. The millimetre of slack absorbs the offset construction, which
// circumscribes the corner arcs and so pushes the flat faces a hair past the
// clearance.
const (
	waitZoneY    = 100.0
	waitZoneMaxX = 450.0 - 10.0 - 2.0 // zone edge − CLEARANCE − BLEND_TOLERANCE
	waitZoneMinX = waitZoneMaxX - 1.0
	procCamX     = 500.0 // station 21 itself
	procCamY     = 100.0
)

// atWaitZone reports whether any commanded move stops in that band.
func atWaitZone(moves []moveCall) bool {
	for _, m := range moves {
		if math.Abs(m.pos.Y-waitZoneY) < 1e-6 && m.pos.X >= waitZoneMinX && m.pos.X <= waitZoneMaxX {
			return true
		}
	}
	return false
}

// startCamPick commands the job that picks station 21's material into the tray.
func startCamPick(f *jobFixture) {
	f.m.pins.originID.Set(21)
	f.m.pins.destID.Set(10)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)
}

// TestJobWaitZoneStopsWhenTheStationStaysBusy is the outcome the wait position
// exists for: the station never clears while the approach runs, so the queue
// runs dry at the derived wait point and the head stands there — never inside
// the zone — until it does.
func TestJobWaitZoneStopsWhenTheStationStaysBusy(t *testing.T) {
	f := newWaitZoneFixture(t)
	startCamPick(f)

	f.eventually("arrival at the derived wait position", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	f.consistently("holding at the derived wait position", func() bool {
		return f.bit("busy") && len(zAtXY(f.mot.moveList(), procCamX, procCamY)) == 0
	})

	// Clearing the station means releasing the scene too: the last leg is
	// planned in the drawing WAIT_CLEAR_DEADZONE names.
	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)
	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("wait-zone gated job")

	if len(zAtXY(f.mot.moveList(), procCamX, procCamY)) == 0 {
		t.Error("the job never reached the station after it cleared")
	}
	// The head really stopped: that is what the counter is for.
	if got := f.get("wait-stops"); got != 1 {
		t.Errorf("wait-stops = %v, want 1", got)
	}
}

// TestJobWaitZoneDoesNotStopWhenTheStationClearsInTime is the point of the
// whole design: with the first leg still in flight when busy drops, the second
// is queued behind it and the head never comes to a stop. The counter says so.
func TestJobWaitZoneDoesNotStopWhenTheStationClearsInTime(t *testing.T) {
	f := newWaitZoneFixture(t)
	// Keep every dispatched move in flight long enough that the station can
	// clear while the first leg is still running — the marginal case on the
	// machine, made deterministic here.
	f.mot.setMoveCycles(40)
	startCamPick(f)

	f.eventually("the first leg to be dispatched", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)

	f.eventually("the leg into the station", func() bool {
		return len(zAtXY(f.mot.moveList(), procCamX, procCamY)) > 0
	})
	// Queued while the approach was still moving, so nothing stopped.
	if got := f.get("wait-stops"); got != 0 {
		t.Errorf("wait-stops = %v, want 0 — the approach should not have stopped", got)
	}
	f.mot.setMoveCycles(0)
	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("streamed wait-zone job")
}

// TestJobWaitZoneDrivesStraightInWhenTheStationIsClear: a station that is not
// busy has nothing to wait for, so it is approached in one continuous leg —
// which is the outcome the two legs exist to produce anyway. No stop, no split,
// nothing counted.
func TestJobWaitZoneDrivesStraightInWhenTheStationIsClear(t *testing.T) {
	f := newWaitZoneFixture(t)
	// The enclosure is open and the station is done before the job is commanded.
	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)
	startCamPick(f)

	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("a wait-zone job whose station was never busy")

	moves := f.mot.moveList()
	if atWaitZone(moves) {
		t.Error("the approach stopped short of the zone although the station was clear")
	}
	if len(zAtXY(moves, procCamX, procCamY)) == 0 {
		t.Error("the job never reached the station")
	}
	if got := f.get("wait-stops"); got != 0 {
		t.Errorf("wait-stops = %v, want 0", got)
	}
}

// TestJobWaitZoneRefusesAWrongSceneWhenClear: the scene check does not depend on
// having waited. A station that reports done while deadzone-select still names
// the drawing it is enclosed in is the same PLC sequencing bug whether or not
// the job had to stop for it, and saying so beats the PLANNING_FAILED that
// planning against the blocked drawing would otherwise produce.
func TestJobWaitZoneRefusesAWrongSceneWhenClear(t *testing.T) {
	f := newWaitZoneFixture(t)
	// Never busy, but the selector still says the enclosure is shut.
	f.setBit(f.m.pins.procs[1].busy, false)
	startCamPick(f)

	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("a clear station in the blocked scene", errWaitSceneMismatch)

	if len(zAtXY(f.mot.moveList(), procCamX, procCamY)) > 0 {
		t.Error("the head drove into the station although the scene was still blocked")
	}
}

// TestJobWaitZoneStartingAtTheStation: the second job at the same station. The
// one before it left the head standing at the station, which is inside the zone
// the wait point would be derived from — there is no boundary crossing to be
// had from in there. Since the station is not busy the approach never asks for
// one, and the job runs.
func TestJobWaitZoneStartingAtTheStation(t *testing.T) {
	f := newWaitZoneFixture(t)
	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)
	// Where the previous job's place left it: at the station, at travel height.
	f.mot.setPos(procCamX, procCamY, 60)
	startCamPick(f)

	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("a job that starts with the head already at the station")
}

// TestJobWaitZoneLeavesAnUnstartedTailForLegTwo: the first leg of a streamed
// approach ends with the braking distance as a segment of its own, so the leg
// queued behind it joins one the machine has not started driving (D29).
//
// Geometry only — what the tail is FOR is a velocity profile through a segment
// boundary, which needs a servo-rate instrument and belongs on the machine.
// What can be pinned here is that the split is the right size, that it lies on
// the leg so the driven path is unchanged, and that the param turns it off.
func TestJobWaitZoneLeavesAnUnstartedTailForLegTwo(t *testing.T) {
	f := newWaitZoneFixture(t)
	startCamPick(f) // the station never clears, so this is all leg 1
	f.eventually("the approach to reach the wait position", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	f.consistently("still waiting at the wait position", func() bool { return f.bit("busy") })

	legs := travelMoves(f.mot.moveList())
	if len(legs) < 2 {
		t.Fatalf("leg 1 was dispatched as %d segment(s); it has to end with a tail of its own", len(legs))
	}
	last, prev := legs[len(legs)-1], legs[len(legs)-2]
	tail := math.Hypot(last.pos.X-prev.pos.X, last.pos.Y-prev.pos.Y)

	want := f.m.pins.blendTailMargin.Get() * last.vel * last.vel / (2 * last.acc)
	if math.Abs(tail-want) > 0.5 {
		t.Errorf("tail is %.3f mm, want the braking distance %.3f mm (vel %.1f, acc %.1f)",
			tail, want, last.vel, last.acc)
	}
	if len(legs) >= 3 {
		a, b, c := legs[len(legs)-3], prev, last
		cross := (b.pos.X-a.pos.X)*(c.pos.Y-a.pos.Y) - (b.pos.Y-a.pos.Y)*(c.pos.X-a.pos.X)
		if math.Abs(cross) > 1e-3 {
			t.Errorf("the split moved the path off the leg: cross product %g", cross)
		}
	}

	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)
	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("the job with a split leg")
}

// TestJobWaitZoneTailMarginZeroRestoresOneSegment: blend-tail-margin = 0 is the
// way back to the previous behaviour on a machine where the split turns out not
// to help, so it has to actually restore it.
func TestJobWaitZoneTailMarginZeroRestoresOneSegment(t *testing.T) {
	f := newWaitZoneFixture(t)
	f.m.pins.blendTailMargin.Set(0)
	startCamPick(f)
	f.eventually("the approach to reach the wait position", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	f.consistently("still waiting at the wait position", func() bool { return f.bit("busy") })

	legs := travelMoves(f.mot.moveList())
	if len(legs) < 2 {
		return // a single-segment leg cannot carry a tail either way
	}
	last, prev := legs[len(legs)-1], legs[len(legs)-2]
	tail := math.Hypot(last.pos.X-prev.pos.X, last.pos.Y-prev.pos.Y)
	// What a margin of 2 would have reserved. The final segment of an
	// unsplit leg is a whole leg of the route and is far longer than that.
	brake := 2.0 * last.vel * last.vel / (2 * last.acc)
	if tail < 2*brake {
		t.Errorf("final segment is %.3f mm, close to the %.3f mm a split would reserve — the margin of 0 did not disable it",
			tail, brake)
	}
}

// travelMoves is the moves commanded at the fixture's movement height — the XY
// travel, without the Z strokes on either side of it.
func travelMoves(moves []moveCall) []moveCall {
	var out []moveCall
	for _, m := range moves {
		if math.Abs(m.pos.Z-30.0) < 1e-6 { // MOVE_HEIGHT in the fixture INI
			out = append(out, m)
		}
	}
	return out
}

// TestJobWaitZoneRefusesAWrongScene: the wait point was derived from a route
// planned in WAIT_CLEAR_DEADZONE, so the leg that finishes the approach has to
// be planned in it too. A station that reports done before its scene is
// released is a PLC sequencing bug and gets said so, rather than surfacing as
// whatever planning against the wrong drawing happens to do.
func TestJobWaitZoneRefusesAWrongScene(t *testing.T) {
	f := newWaitZoneFixture(t)
	startCamPick(f)

	f.eventually("arrival at the derived wait position", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	// busy drops, the selector still says the sphere is shut.
	f.setBit(f.m.pins.procs[1].busy, false)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("the station cleared in the wrong scene", errWaitSceneMismatch)

	if len(zAtXY(f.mot.moveList(), procCamX, procCamY)) > 0 {
		t.Error("the head drove into the station although the scene was still blocked")
	}
}

// TestJobWaitZoneAbortedByAutoEnable: D15's handover, now landing while the
// first leg may still be running. The job ends with WAIT_ABORTED and the head
// never enters the zone.
func TestJobWaitZoneAbortedByAutoEnable(t *testing.T) {
	f := newWaitZoneFixture(t)
	f.mot.setMoveCycles(40)
	startCamPick(f)

	f.eventually("the first leg to be dispatched", func() bool {
		return atWaitZone(f.mot.moveList())
	})
	f.setBit(f.m.pins.autoEnable, false)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("auto-enable dropped during the streamed wait", errWaitAborted)

	if len(zAtXY(f.mot.moveList(), procCamX, procCamY)) > 0 {
		t.Error("the head drove into the station after the wait was aborted")
	}
}

// ---------------------------------------------------------------------------
// Faults during a job
// ---------------------------------------------------------------------------

// TestJobEstopMidMove: an estop ends the job wherever it is, aborts motion,
// releases the pickers (D14) and clears the handshake.
func TestJobEstopMidMove(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// Keep every move in flight so the estop lands during one.
	f.mot.setMoveCycles(1000)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.startJob.Set(true)
	f.eventually("the job to start moving", func() bool { return f.bit("busy") && len(f.mot.moveList()) > 0 })

	f.setBit(f.m.pins.estopOn, true)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("estop during a job", errEstop)

	if f.bit("busy") {
		t.Error("busy is still set after the estop ended the job")
	}
	if f.bit("machine-is-on") {
		t.Error("machine-is-on is still set after an estop")
	}
	if f.bit("picker.0.close") {
		t.Error("the picker was not released on estop (D14)")
	}
	if !f.mot.called("Abort") {
		t.Error("motion was not aborted on estop")
	}
	w := f.stopped()
	if w.held[0].present {
		t.Error("a held record survived the estop that released the picker")
	}
}

// TestJobMachineOffMidMove: machine-on dropping aborts the job with MACHINE_OFF —
// but the picker keeps holding what it has (D14).
func TestJobMachineOffMidMove(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setMoveCycles(1000)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.startJob.Set(true)
	f.eventually("the job to start moving", func() bool { return f.bit("busy") && len(f.mot.moveList()) > 0 })

	f.setBit(f.m.pins.machineOn, false)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("machine-on dropped during a job", errMachineOff)
	if f.bit("machine-is-on") {
		t.Error("machine-is-on is still set after machine-on dropped")
	}
}

// TestJobPickerFaults covers the two gripper faults of §7.4: a picker that never
// closes, and one that never opens again on the place.
func TestJobPickerFaults(t *testing.T) {
	t.Run("close failed", func(t *testing.T) {
		f := newJobFixture(t)
		f.selectTray(1)
		f.fillTray(0)
		f.sim.set(func(s *machineSim) { s.jammedClosed = true })

		f.runJob(10, 20, 0)
		f.requireError("a picker that never closes", errPickerCloseFail)
	})

	t.Run("place failed", func(t *testing.T) {
		f := newJobFixture(t)
		f.selectTray(1)
		f.fillTray(0)

		// The pick succeeds; the picker then refuses to let go.
		f.eventually("resting picker reports opened", func() bool {
			return f.m.pins.pickers[0].opened.Get()
		})
		f.sim.set(func(s *machineSim) { s.jammedOpen = true })

		f.runJob(10, 20, 0)
		f.requireError("a picker that never opens", errPlaceFailed)
		if f.bit("proc.20.has-material") {
			t.Error("the station reports material although the place failed")
		}
	})
}

// TestJobReleaseTimeout: a fixture whose released feedback never follows is a
// RELEASE_TIMEOUT, not a hung job.
func TestJobReleaseTimeout(t *testing.T) {
	jf := newJobFixtureOpts(t, fixtureOpts{
		tweak: func(cfg *Config) { cfg.ReleaseTimeout = 0.05 },
		prep:  func(_ *testing.T, m *pnptaskModule) { m.pins.trays[0].trayID.Set(1) },
	})
	jf.homed()
	jf.mot.setPos(100, 100, 60)
	jf.fillTray(0)
	jf.sim.set(func(s *machineSim) { s.fixtureStuck = true })

	jf.runJob(10, 20, 0)
	jf.requireError("a fixture that never reports released", errReleaseTimeout)
	// The failed action withdrew its release request: no error path may leave
	// the fixture commanded open (D19).
	jf.consistently("release withdrawn after the fault", func() bool {
		return !jf.bit("proc.20.release")
	})
}

// TestPickFromProcReleaseFailureKeepsWorldTruthful: a release wait that fails
// AFTER the grip confirmed means the fixture never let go — the part belongs to
// the fixture. The picker must open again (not leave clamped around a part the
// world calls free), has-material stays set, and no held record is written.
func TestPickFromProcReleaseFailureKeepsWorldTruthful(t *testing.T) {
	jf := newJobFixtureOpts(t, fixtureOpts{
		tweak: func(cfg *Config) { cfg.ReleaseTimeout = 0.05 },
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.world.procs[0].setHasMaterial(true)
		},
	})
	jf.homed()
	jf.mot.setPos(100, 100, 60)
	jf.sim.set(func(s *machineSim) { s.fixtureStuck = true })

	jf.runJob(20, 10, 0) // pick from the proc station, place into the tray
	jf.requireError("fixture held the part through the release wait", errReleaseTimeout)

	jf.consistently("release withdrawn and picker open", func() bool {
		return !jf.bit("proc.20.release") && !jf.bit("picker.0.close")
	})
	if !jf.bit("proc.20.has-material") {
		t.Error("has-material dropped although the fixture never released the part")
	}
	w := jf.stopped()
	if w.held[0].present {
		t.Error("a held record was written for a part the fixture kept")
	}
}

// TestEstopDuringPlaceDwellKeepsRecords: the records commit the moment "opened"
// confirms — the part physically left the picker — so an estop during the
// release-time dwell must not lose it from the model (a lost record is a second
// part stacked onto the invisible one by the next job).
func TestEstopDuringPlaceDwellKeepsRecords(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// A wide dwell so the estop lands inside it.
	f.m.pins.releaseTime.Set(100 * pollInterval.Seconds())

	f.m.pins.startJob.Set(false)
	time.Sleep(levelHold)
	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.m.pins.startJob.Set(true)

	// The record rises at the "opened" confirmation, while the dwell still runs.
	f.eventually("has-material committed before the dwell ends", func() bool {
		return f.bit("proc.20.has-material")
	})
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("job ended by the estop", func() bool { return !f.bit("busy") })
	f.requireError("estop mid-dwell", errEstop)

	f.consistently("the placed part stays in the model", func() bool {
		return f.bit("proc.20.has-material")
	})
	w := f.stopped()
	if w.held[0].present {
		t.Error("the held record survived a place whose part left the picker")
	}
}

// TestJobPlanningFailed: a machine parked outside the eroded travel envelope has
// no valid route start, and that is PLANNING_FAILED rather than a move commanded
// into the unknown.
func TestJobPlanningFailed(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// The fixture drawing's outer limit is 0..600 x 0..500 and CLEARANCE is 10,
	// so the corner is outside the eroded boundary.
	f.mot.setPos(0, 0, 60)

	f.runJob(10, 20, 0)
	f.requireError("a route from outside the envelope", errPlanningFailed)
}

// ---------------------------------------------------------------------------
// The handshake itself
// ---------------------------------------------------------------------------

// TestStartJobHeldAtStartupIsNotARequest: D26 — a level a PLC latched across a
// stmakd restart is not a request for that job.
func TestStartJobHeldAtStartupIsNotARequest(t *testing.T) {
	jf := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.pins.startJob.Set(true)
			m.pins.originID.Set(10)
			m.pins.destID.Set(20)
			m.pins.trays[0].trayID.Set(1)
		},
	})
	jf.homed()

	jf.consistently("no job from a held start-job", func() bool {
		return !jf.bit("busy") && !jf.bit("error") && len(jf.mot.moveList()) == 0
	})
	// Released and raised again it is a request — and this one runs into the
	// empty tray, which proves it was actually started.
	jf.pulse(jf.m.pins.startJob)
	jf.eventually("the job to be answered", func() bool { return jf.bit("error") })
	jf.requireError("a real start-job edge", errTrayEmpty)
}

// TestJobRequestArming pins the start-job handshake rule directly: a level has to
// be seen low before it counts as a request. The interesting case cannot be built
// out of pins — it is a start-job wired to a signal a PLC keeps driving high, so
// the module's own clear is overwritten before the loop ever samples a low, and
// the level would read as a fresh job on the very next cycle.
func TestJobRequestArming(t *testing.T) {
	var logs bytes.Buffer
	c := &control{m: &pnptaskModule{
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}}

	// High from the first cycle: not a request (D26, and what primeEdges leaves
	// jobArmed at).
	c.in.startJob = true
	if c.jobRequest() {
		t.Error("a start-job level present from the start counted as a request")
	}
	if c.jobRequest() {
		t.Error("the same held level counted as a request on a later cycle")
	}

	// Seen low, then raised: one request, once.
	c.in.startJob = false
	if c.jobRequest() {
		t.Error("a low start-job counted as a request")
	}
	c.in.startJob = true
	if !c.jobRequest() {
		t.Error("a start-job raised after a low was not taken as a request")
	}
	if c.jobRequest() {
		t.Error("one request was taken twice")
	}
}

// TestHeldStartJobIsReported: the unarmed level is a deadlock -- the PLC waits
// for the acknowledgement accepting would have produced, this waits for a low
// only the PLC can produce -- and it used to be entirely silent. On the machine
// that presented as a job that simply never started, with the reason on no pin.
func TestHeldStartJobIsReported(t *testing.T) {
	var logs bytes.Buffer
	c := &control{m: &pnptaskModule{
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}}

	c.in.startJob = true
	for i := 0; i < jobUnarmedWarnTicks-1; i++ {
		if c.jobRequest() {
			t.Fatal("an unarmed level counted as a request")
		}
	}
	if logs.Len() != 0 {
		t.Errorf("warned before %d ticks: %s", jobUnarmedWarnTicks, logs.String())
	}

	c.jobRequest()
	if !strings.Contains(logs.String(), "start-job") {
		t.Errorf("no warning after %d ticks of a held level: %q",
			jobUnarmedWarnTicks, logs.String())
	}

	// Once per episode, not once per cycle.
	n := logs.Len()
	for i := 0; i < 3*jobUnarmedWarnTicks; i++ {
		c.jobRequest()
	}
	if logs.Len() != n {
		t.Errorf("warned more than once: %s", logs.String()[n:])
	}

	// Breaking the tie re-arms, and a later held level is reported again.
	c.in.startJob = false
	c.jobRequest()
	c.in.startJob = true
	if !c.jobRequest() {
		t.Fatal("a level raised after a low was not taken as a request")
	}
	n = logs.Len()
	for i := 0; i < jobUnarmedWarnTicks; i++ {
		c.jobRequest()
	}
	if logs.Len() == n {
		t.Error("a second held level went unreported")
	}
}

// TestTrayResetDuringAJobIsNotLost: the reset edges are latched, so a request
// that arrives while a job runs is applied when the loop gets back to its own
// decisions — and never in the middle of the slot search.
func TestTrayResetDuringAJobIsNotLost(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setMoveCycles(20)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.startJob.Set(true)
	f.eventually("the job to start moving", func() bool { return f.bit("busy") })

	// Ask for the tray to be emptied while the job is running.
	f.tray().setEmpty.Set(false)
	f.tray().setEmpty.Set(true)
	f.eventually("the job to finish", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("job during which set-empty was pressed")

	f.eventually("the request to be honoured after the job", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
}

// TestJobPublishesPlanTime: plan-time carries the slowest route plan of the job
// that just ran (§5.2), which is what makes D13's budget assertable from
// outside — and it belongs to one job, so the next job's latch resets it.
func TestJobPublishesPlanTime(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)

	if got := f.get("plan-time"); got != 0 {
		t.Errorf("plan-time = %v before any job, want 0", got)
	}

	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc")

	plan := f.get("plan-time")
	if plan <= 0 {
		t.Errorf("plan-time = %v after a job that planned several legs, want > 0", plan)
	}
	if plan >= 0.1 {
		t.Errorf("plan-time = %v s, over D13's 100 ms budget on a fixture scene", plan)
	}

	// A job refused before it plans anything reports no planning, rather than
	// leaving the previous job's number standing for the PLC to read.
	f.runJob(99, 20, 0)
	f.requireError("unknown origin", errInvalidOrigin)
	if got := f.get("plan-time"); got != 0 {
		t.Errorf("plan-time = %v after a job that never planned, want 0", got)
	}
}

// TestTrayToItselfMovesThePart: a tray-to-itself job must not put the part back
// into the slot its own pick just emptied — that is a successful-looking
// physical no-op a compacting PLC would loop on forever. The place lands in the
// next free slot instead.
func TestTrayToItselfMovesThePart(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) {
		w.setHeld(0, 20, false, 0, true)
	})
	f.selectTray(1)

	// The seeded held material goes into the empty tray first: the first slot
	// of the DIR_MODE order.
	f.runJob(20, 10, 0)
	f.requireOK("placing the held material into the tray")

	// The only material now sits in the first slot of the order and every slot
	// after it is free — the shape where an unexcluded freeSlot returns the
	// very slot the pick empties.
	f.runJob(10, 10, 0)
	f.requireOK("the tray-to-itself job")

	w := f.stopped()
	tr := w.trays[0]
	first, second := tr.geom.order[0], tr.geom.order[1]
	if tr.slots[first] != slotEmpty {
		t.Errorf("slot %d still holds %d — the part went back where it came from", first, tr.slots[first])
	}
	if tr.slots[second] != 0 {
		t.Errorf("slot %d = %d, want the moved part (step 0)", second, tr.slots[second])
	}
}

// TestJobGatedOnRestoredHeldVerification: a start-job arriving inside the
// restored-held settle window must wait for the grip verification instead of
// trusting the unverified record — a part lost in the downtime would otherwise
// be "placed" as a phantom, marking a station occupied by nothing.
func TestJobGatedOnRestoredHeldVerification(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			// A held record as a restore would leave it: present, flagged for
			// verification, the close output re-driven.
			m.world.setHeld(0, 20, false, 0, true)
			m.world.restoredHeld[0] = true
			m.pins.pickers[0].close.Set(true)
			// A long settle keeps the verification window open while the test
			// commands its job into it.
			m.pins.pickSettleTime.Set(20 * pickSettleBudget.Seconds())
			m.pins.posSettleTime.Set(0)
			m.pins.releaseTime.Set(0)
			m.pins.autoEnable.Set(true)
		},
	})
	// The pre-armed miss judges the re-driven close as "gripped nothing": the
	// part is gone.
	sim := newMachineSimOpts(f, func(s *machineSim) { s.missesLeft = 1 })
	jf := &jobFixture{machineFixture: f, sim: sim}
	jf.homed()
	jf.mot.setPos(100, 100, 60)

	// Straight into the settle window: put the "held" material back into its
	// own free station — the job an unverified record would run as a phantom
	// place, setting has-material on an empty fixture.
	jf.runJob(20, 20, 0)
	jf.requireError("a job on a phantom record", errProcNoMaterial)
	if jf.bit("proc.20.has-material") {
		t.Error("the phantom part was placed — has-material is set")
	}
}

// TestJobPlansEachLegAgainstTheCurrentScene is the sequence a machine whose
// fixture sits inside a movable enclosure runs: the station is unreachable
// while the enclosure is closed, so the job waits it out at the wait position,
// the enclosure then opens, the PLC selects the drawing that describes the open
// machine and clears busy, and the leg into the station is planned against
// *that* scene. Latching deadzone-select at job start would plan this last leg
// around an obstacle that is no longer there and fail with PLANNING_FAILED.
func TestJobPlansEachLegAgainstTheCurrentScene(t *testing.T) {
	// Drawing 0 covers proc station 20 with a dead zone, drawing 1 is clear:
	// the station is only reachable while drawing 1 is selected.
	f := newJobFixtureOpts(t, fixtureOpts{
		files: map[string]string{zonesA: fixtureBlock, zonesB: fixtureClear},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)

	// Enclosure closed, station busy.
	f.m.pins.deadzoneSelect.Set(0)
	f.proc().busy.Set(true)

	f.m.pins.startJob.Set(false)
	time.Sleep(levelHold)
	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)

	// The job picks and parks at the wait position (250, 150) — planned in the
	// closed scene, where the station itself could not have been reached.
	f.eventually("the head to reach the wait position", func() bool {
		for _, m := range f.mot.moveList() {
			if math.Abs(m.pos.X-250) < 1e-6 && math.Abs(m.pos.Y-150) < 1e-6 {
				return true
			}
		}
		return false
	})
	if f.bit("start-job") == false {
		t.Fatal("the job finished before the station was freed")
	}

	// The enclosure opens: the other drawing describes the machine now, and the
	// station is free.
	f.m.pins.deadzoneSelect.Set(1)
	f.proc().busy.Set(false)

	f.eventually("the job handshake to complete", func() bool { return !f.bit("start-job") })
	f.eventually("busy to clear", func() bool { return !f.bit("busy") })
	f.m.pins.startJob.Set(false)
	f.requireOK("a job whose destination was freed by a scene change")

	if !f.bit("proc.20.has-material") {
		t.Error("the part never reached the station")
	}
	// And the last leg really did drive into the once-blocked station.
	reached := false
	for _, m := range f.mot.moveList() {
		if math.Abs(m.pos.X-300) < 1e-6 && math.Abs(m.pos.Y-200) < 1e-6 {
			reached = true
		}
	}
	if !reached {
		t.Errorf("no move to the station at (300, 200); moves = %v", f.mot.moveList())
	}
}

// segmentCrossesRect reports whether the segment a-b passes through the
// axis-aligned rectangle (Liang-Barsky clip; a touch on the boundary does not
// count, which leaves the planner's CLEARANCE margin out of the question).
func segmentCrossesRect(a, b pnproute.Point, xmin, ymin, xmax, ymax float64) bool {
	t0, t1 := 0.0, 1.0
	dx, dy := b.X-a.X, b.Y-a.Y
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return false
			}
			if r > t0 {
				t0 = r
			}
			return true
		}
		if r < t0 {
			return false
		}
		if r < t1 {
			t1 = r
		}
		return true
	}
	return clip(-dx, a.X-xmin) && clip(dx, xmax-a.X) &&
		clip(-dy, a.Y-ymin) && clip(dy, ymax-a.Y) && t0 < t1
}

// TestTravelPlansInMachineFrame (D28): dead zones are machine-position
// geometry — taught by driving the machine — so with a picker offset the
// planner must route the MACHINE around them. Planning in the pick-point
// frame and shifting the waypoints afterwards translates the whole polyline
// by the offset; the fixture drawing puts a zone in the machine-frame
// corridor between the tray and the press while staying clear of the
// pick-point corridor, so a route planned in the wrong frame is commanded
// straight through it.
func TestTravelPlansInMachineFrame(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		files: map[string]string{zonesA: "zones_between.dxf", zonesB: fixtureClear},
	})
	f.m.pins.pickers[0].xOffset.Set(40)
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("a job routed with a picker offset")

	prev := pnproute.Point{X: 100, Y: 100}
	for _, m := range f.mot.moveList() {
		cur := pnproute.Point{X: m.pos.X, Y: m.pos.Y}
		if segmentCrossesRect(prev, cur, 100, 250, 135, 350) {
			t.Errorf("commanded segment (%.1f, %.1f) -> (%.1f, %.1f) crosses the dead zone",
				prev.X, prev.Y, cur.X, cur.Y)
		}
		prev = cur
	}
}

// TestHomePressDuringJobIsIgnored (§12.3, resolved): the home latch is gated
// on idle at press time, like the manual-picker edges are gated on manual
// mode. A press while a job runs is ignored rather than deferred — deferred,
// it fired as a surprise homing sequence the moment the job completed, which
// turned a PLC glitch on the home line into a homing run. A press while idle
// still homes.
func TestHomePressDuringJobIsIgnored(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)

	// Park the job at the busy station's wait point: a mid-job window to
	// press in, with the machine standing still.
	f.proc().busy.Set(true)
	f.m.pins.startJob.Set(false)
	time.Sleep(levelHold)
	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.m.pins.startJob.Set(true)
	f.eventually("the job to be busy-waiting", func() bool { return f.bit("busy") })

	f.mot.resetCalls()
	f.press(f.m.pins.home)
	f.proc().busy.Set(false)
	f.eventually("the job handshake to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("the job around the ignored press")
	f.consistently("no homing run after the job", func() bool {
		return f.mot.callCount("JointHome") == 0
	})

	// The same press while idle homes (runHoming commands home-all even on a
	// homed machine — D25's re-home).
	f.press(f.m.pins.home)
	f.eventually("an idle press to home", func() bool { return f.mot.callCount("JointHome") == 1 })
}

// TestJobRefusedWhenPickerCannotReach: the per-picker reachability validation
// (checkReach). What is commanded is endpoint − picker offset, so a picker
// offset can carry a taught station outside the axis limits for that picker
// only — refused at job accept with TARGET_UNREACHABLE, before any motion,
// instead of an intermittent mid-job MOTION_ERROR by whichever picker
// happened to be free.
func TestJobRefusedWhenPickerCannotReach(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		ini: `
[AXIS_X]
MIN_LIMIT = 0
MAX_LIMIT = 600
[AXIS_Y]
MIN_LIMIT = 0
MAX_LIMIT = 500
[AXIS_Z]
MIN_LIMIT = 0
MAX_LIMIT = 80
`,
	})
	// The tray's first column sits at X = 120; picker 1's offset puts the
	// machine at 120 − 130 = −10 for those slots, past the X minimum. Which
	// picker would actually serve the pick is beside the point (D20 decides
	// that only when the leg runs): the job is refused for the machine state.
	f.m.pins.pickers[1].xOffset.Set(130)
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireError("a station outside picker 1's envelope", errUnreachable)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the machine moved %d times before the refusal", n)
	}

	// Pulled back inside every picker's envelope, the same job runs.
	f.clearError()
	f.m.pins.pickers[1].xOffset.Set(20)
	f.runJob(10, 20, 0)
	f.requireOK("the same job with a covered offset")
}
