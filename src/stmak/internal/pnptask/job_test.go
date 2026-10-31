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
// It steps inside the control loop, at the start of every cycle before the
// inputs are read (pnptaskModule.tickHook), so a command written in one cycle
// is answered in the next one's sample. On a goroutine of its own it raced the
// loop: a stall of that goroutine longer than a settle budget left the module
// judging feedback the simulated gripper never had the chance to produce, and
// the case failed with PICKER_CLOSE_FAILED against working code.
type machineSim struct {
	m  *pnptaskModule
	mu sync.Mutex

	lastClose []bool
	gripMiss  []bool

	// lastRelease/releaseRises track each process station's release request.
	// Counting the rises is how §8's "no re-clamp in between" is observed: the
	// module waits for released to follow every change it makes, so the sim
	// cannot miss one — two rises within a job mean the fixture was closed and
	// opened again between the swap and the place.
	lastRelease  []bool
	releaseRises []int
	// releasedAt is when each station's released feedback last went high.
	releasedAt []time.Time
	// closedAt is when each picker's close command was last seen rising.
	closedAt []time.Time

	// missesLeft makes the next N close commands report "closed onto nothing",
	// which is how an empty tray slot presents itself (§5.2).
	missesLeft int
	// jammedOpen: the picker never reports opened again once it has closed.
	jammedOpen bool
	// jammedClosed: the picker never leaves opened, i.e. it never closes at all.
	jammedClosed bool
	// fixtureStuck: released never follows release.
	fixtureStuck bool

	// pending runs at the start of the next cycle, on the control goroutine
	// (see inLoop).
	pending []func()

	// onBusyFall runs in the first cycle that finds busy low after it was
	// high, before that cycle publishes anything: the pins as a PLC reading
	// them on the fall of busy sees them.
	onBusyFall func()
	lastBusy   bool
}

// newMachineSim builds the sim and hooks it into the control loop. It has to
// run before startControl: the hook is read by the control goroutine. prep
// arms knobs before the sim ever publishes, like a pre-armed miss judging a
// close command that startControl re-drives from a restored held record.
func newMachineSim(m *pnptaskModule, prep func(*machineSim)) *machineSim {
	n := len(m.pins.pickers)
	s := &machineSim{
		m:            m,
		lastClose:    make([]bool, n),
		gripMiss:     make([]bool, n),
		lastRelease:  make([]bool, len(m.pins.procs)),
		releaseRises: make([]int, len(m.pins.procs)),
		releasedAt:   make([]time.Time, len(m.pins.procs)),
		closedAt:     make([]time.Time, n),
	}
	if prep != nil {
		prep(s)
	}
	s.step() // publish the resting state before the loop can sample it
	m.tickHook = s.step
	return s
}

// pickSettleBudget is what every fixture gives the simulated gripper to answer
// a close or an open in. With the sim stepping inside the loop the answer is
// in the first sample after the command, whatever the scheduler does, so this
// only has to outlast a cycle; it stays generous because the settle time is
// also what some cases hold a window open with.
const pickSettleBudget = 50 * time.Millisecond

func (s *machineSim) step() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fn := range s.pending {
		fn()
	}
	s.pending = nil
	p := s.m.pins
	busy := p.busy.Get()
	if !busy && s.lastBusy && s.onBusyFall != nil {
		s.onBusyFall()
	}
	s.lastBusy = busy
	for i := range p.pickers {
		pk := &p.pickers[i]
		closing := pk.close.Get()
		if closing != s.lastClose[i] {
			s.lastClose[i] = closing
			if closing {
				s.closedAt[i] = time.Now()
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
		if want && !p.procs[i].released.Get() {
			s.releasedAt[i] = time.Now()
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

// releasedSince is when station i's released feedback last went high.
func (s *machineSim) releasedSince(i int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releasedAt[i]
}

// closedSince is when picker pk's close command was last seen rising; the
// zero time if it never has.
func (s *machineSim) closedSince(pk int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closedAt[pk]
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

// inLoop runs fn on the control goroutine, at the start of the next cycle
// before the inputs are sampled, and returns once it has run. The world model
// belongs to that goroutine (see machineFixture.stopped): a test that has to
// change it while the loop runs does it here, not from its own goroutine.
func (s *machineSim) inLoop(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	s.mu.Lock()
	s.pending = append(s.pending, func() { fn(); close(done) })
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("inLoop: the control loop never ran the change")
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// jobFixture is a machine ready to run jobs: enabled, homed, parked inside the
// travel envelope, with the settle times turned down and the gripper/fixture
// feedback simulated.
type jobFixture struct {
	*machineFixture
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
		// in. The sim answers in the first sample after a command (it steps
		// inside the loop, see machineSim), so any settle time of a cycle or
		// more is met whatever the scheduler does.
		m.pins.pickSettleTime.Set(pickSettleBudget.Seconds())
		m.pins.releaseTime.Set(0)
		// Auto mode: jobs are accepted (§6.4). An unwired pin reads low, which is
		// manual mode.
		m.pins.autoEnable.Set(true)
		if userPrep != nil {
			userPrep(t, m)
		}
	}
	o.simulate = true
	f := newMachineFixtureOpts(t, o)
	return &jobFixture{machineFixture: f}
}

// logCapture is a logger sink the control goroutine and the test can share:
// for the cases whose subject is the sentence a fault is reported with, which
// the error-id pin cannot carry.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
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
	f.sampled()
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

// TestJobOutcomePublishedBeforeBusyFalls: busy low is the PLC's cue to read
// what the job left behind, so the pins must already describe it then -- not
// one cycle later, when tick publishes again.
func TestJobOutcomePublishedBeforeBusyFalls(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)

	type snap struct{ seen, procHas, holds, avail bool }
	var mu sync.Mutex
	var got snap
	f.sim.inLoop(t, func() {
		f.sim.onBusyFall = func() {
			mu.Lock()
			defer mu.Unlock()
			got = snap{
				seen:    true,
				procHas: f.m.pins.procs[0].hasMaterial.Get(),
				holds:   f.m.pins.pickers[0].holds.Get(),
				avail:   f.m.pins.trays[0].avail.Get(),
			}
		}
	})

	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc")
	f.sampled()

	mu.Lock()
	defer mu.Unlock()
	if !got.seen {
		t.Fatal("the fall of busy was never observed")
	}
	if !got.procHas {
		t.Error("busy fell with proc.20.has-material still low: the place was not published yet")
	}
	if got.holds {
		t.Error("busy fell with picker.0.holds still high: the release was not published yet")
	}
	if !got.avail {
		t.Error("busy fell with tray.10.avail low although 39 raw slots are left")
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

// TestJobPlaceOffset: Z_PLACE_OFFSET lifts the place stroke and nothing else
// (D31). The pick at either kind of station keeps Z_PICK + z-offset, the place
// stops the offset above it, on top of the live z-offset pin.
func TestJobPlaceOffset(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		// The world copies its station configs at load, so the offsets go
		// there rather than through tweak.
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.trays[0].cfg.ZPlaceOffset = 1.5
			m.world.procs[0].cfg.ZPlaceOffset = 2.0
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.proc().zOffset.Set(0.25)

	// tray -> proc: pick at the tray's own height, place 2 mm above the
	// station's (5.0 + 0.25).
	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc")
	moves := f.mot.moveList()
	if zs := zAtXY(moves, 120, 430); len(zs) < 2 || zs[len(zs)-2] != 2.5 {
		t.Errorf("tray pick Z sequence = %v, want ... 2.5 then the move height", zs)
	}
	if zs := zAtXY(moves, 300, 200); len(zs) < 2 || zs[len(zs)-2] != 7.25 {
		t.Errorf("proc place Z sequence = %v, want ... 7.25 then the move height", zs)
	}

	// proc -> tray: pick at the station's height, place 1.5 mm above the
	// tray's -- into the slot the first job emptied.
	f.runJob(20, 10, 1)
	f.requireOK("proc -> tray")
	moves = f.mot.moveList()
	if zs := zAtXY(moves, 300, 200); len(zs) < 2 || zs[len(zs)-2] != 5.25 {
		t.Errorf("proc pick Z sequence = %v, want ... 5.25 then the move height", zs)
	}
	if zs := zAtXY(moves, 120, 430); len(zs) < 2 || zs[len(zs)-2] != 4.0 {
		t.Errorf("tray place Z sequence = %v, want ... 4 then the move height", zs)
	}
}

// TestJobStartsJustOutsideALimit: homing to the drive origin can leave the
// head a few hundredths outside an axis limit that sits at home. The route's
// first waypoint is where the head stands; checked as a target, it refused
// every job from there with MOTION_ERROR (on the machine, 2026-10-28). The
// move away from it is a move back inside and runs.
func TestJobStartsJustOutsideALimit(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{})
	// The limits are pushed at start; the machine parks at Y 100, 0.05 below
	// this minimum.  Set before the first job reads them.
	f.m.limits.AxisMinPos[1] = 100.05
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)

	f.runJob(10, 20, 0)
	f.requireOK("a job starting 0.05 mm outside the Y limit")
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds nothing after the place")
	}
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
	// The travel move just before the current one: the last segment of a leg
	// has to end in an exact stop, or the planner runs it at half MOVE_ACC.
	var lastTravel *moveCall
	moves := f.mot.moveList()
	for k := range moves {
		m := &moves[k]
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
		// A Z stroke moves Z alone and stops exactly. A Z-only move under the
		// blend is the travel height being corrected at the start of a leg.
		isZ := d[0] == 0 && d[1] == 0 && m.termCond == tpTermCondStop
		switch {
		case isZ:
			sawZ = true
			velCeil, accCeil, what = zVel, zAcc, "Z stroke"
			if lastTravel != nil && lastTravel.termCond != tpTermCondStop {
				t.Errorf("the leg's last segment to %+v ends parabolic (%d), want an exact stop: the planner would halve its acceleration",
					lastTravel.pos, lastTravel.termCond)
			}
			lastTravel = nil
		case m.termCond == tpTermCondParabolic || m.termCond == tpTermCondStop:
			sawTravel = true
			lastTravel = m
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
		if !isZ && d[0] > 0 && d[1] > 0 {
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

// TestPlaceToProcRaisesReleaseOnlyWhenClear: §7.4's order for a place into a
// busy station is busy gating, THEN release. A fixture whose process is still
// running must not be told to unclamp — but the request has to go out the
// instant busy drops, ahead of the drive in, so the fixture is open on arrival.
func TestPlaceToProcRaisesReleaseOnlyWhenClear(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.setBit(f.proc().busy, true)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)

	f.eventually("arrival at the wait position", func() bool {
		return len(zAtXY(f.mot.moveList(), 250, 150)) > 0
	})
	f.consistently("release withheld while the station is busy", func() bool {
		return f.bit("busy") && !f.bit("proc.20.release")
	})

	f.setBit(f.proc().busy, false)
	f.eventually("release raised once the station cleared", func() bool { return f.bit("proc.20.release") })
	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("busy-gated place")

	if f.bit("proc.20.release") {
		t.Error("release is still asserted at the end of the job (D19)")
	}
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds no material after the gated place")
	}
	if n := f.sim.releaseRiseCount(0); n != 1 {
		t.Errorf("the fixture was asked to open %d times, want once", n)
	}
}

// TestPickOpensAStandingClose: a picker the engine counts free is opened before
// it is sent down onto material, whatever left its close output standing. On
// already-shut jaws the grip check reads "closed" — an empty verdict on a
// populated slot — so without this the job would empty slot 0 in the model and
// pick from slot 1.
func TestPickOpensAStandingClose(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) { m.pins.pickers[0].close.Set(true) },
		// The standing close is over nothing: the gripper reports fully
		// closed for it, and grips material on the next close command.
		sim: func(s *machineSim) { s.missesLeft = 1 },
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)

	f.runJob(10, 20, 0)
	f.requireOK("a pick with the picker's close output left standing")
	if got := f.get("tray.10.count"); got != 39 {
		t.Errorf("tray count after one pick = %v, want 39 — a populated slot was judged empty", got)
	}
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds no material after the job")
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
// The scene gate (D45)
// ---------------------------------------------------------------------------

// The gate fixture's two drawings: drawing 0 (zones_open.dxf) has no zone,
// drawing 1 (zones.dxf) has the 100x100 zone at (450,50)..(550,150) -- so that
// zone is the movable region, a sphere that is open in scene 0 and shut in
// scene 1. Station 21 sits inside it, station 22 beyond it, at the same y as
// the machine's start (100, 100): a leg to either runs straight through the
// zone in scene 0.
const sceneSections = `
[PNPTASK_PROC_1]
ID = 21
X = 500.0
Y = 100.0
Z_PICK = 5.0

[PNPTASK_PROC_2]
ID = 22
X = 580.0
Y = 100.0
Z_PICK = 5.0
`

// newSceneFixture is a job fixture on the gate drawings, scene 0 (open),
// stations 21 and 22 holding material, the machine at (100, 100).
func newSceneFixture(t *testing.T) *jobFixture {
	t.Helper()
	return newSceneFixtureWith(t, nil)
}

// newSceneFixtureWith is newSceneFixture with a configuration tweak.
func newSceneFixtureWith(t *testing.T, tweak func(*Config)) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{
		tweak: tweak,
		ini:   sceneSections,
		files: map[string]string{zonesA: fixtureOpen, zonesB: fixtureClear},
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.procs[1].setHasMaterial(true)
			m.world.procs[2].setHasMaterial(true)
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.m.pins.deadzoneSelect.Set(0)
	return f
}

// startJobTo commands a job without waiting for it -- runJob's first half,
// for the cases that act while the job runs.
func startJobTo(f *jobFixture, origin, dest uint32) {
	f.m.pins.startJob.Set(false)
	f.sampled()
	f.m.pins.originID.Set(origin)
	f.m.pins.destID.Set(dest)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)
}

// jobDone reports whether the job startJobTo commanded has ended.
func (f *jobFixture) jobDone() bool { return !f.bit("start-job") && !f.bit("busy") }

// awaitJob is runJob's second half: the handshake completed, busy down.
func (f *jobFixture) awaitJob() {
	f.t.Helper()
	f.eventually("the job to end", f.jobDone)
	f.m.pins.startJob.Set(false)
}

// inZone reports whether the commanded path -- from the start (100, 100)
// through every move -- touches the movable zone, crossing it included.
func inZone(moves []moveCall) bool {
	zone := pnproute.Polygon{{X: 450, Y: 50}, {X: 550, Y: 50}, {X: 550, Y: 150}, {X: 450, Y: 150}}
	path := []pnproute.Point{{X: 100, Y: 100}}
	for _, m := range moves {
		path = append(path, pnproute.Point{X: m.pos.X, Y: m.pos.Y})
	}
	return pnproute.PathTouches(path, zone)
}

// TestSceneGateHoldsALegThatTouchesTheRegion: with scene-hold up, a leg into
// the region waits standing -- nothing commanded towards it, scene-busy low --
// and goes once the hold drops, with scene-busy up while it runs and down when
// the job is over.
func TestSceneGateHoldsALegThatTouchesTheRegion(t *testing.T) {
	testSceneGateHolds(t, nil)
}

// TestSceneGateHoldsASmoothedLeg: the same with PATH_SHAPER = spline, the
// gate judging the commanded geometry (D45, D47).
func TestSceneGateHoldsASmoothedLeg(t *testing.T) {
	testSceneGateHolds(t, func(c *Config) { c.SmoothPaths = true })
}

func testSceneGateHolds(t *testing.T, tweak func(*Config)) {
	f := newSceneFixtureWith(t, tweak)
	f.mot.setMoveCycles(20)
	f.setBit(f.m.pins.sceneHold, true)

	startJobTo(f, 21, 10)
	f.eventually("the job to start", func() bool { return f.bit("busy") })
	f.consistently("the leg into the region held", func() bool {
		return f.bit("busy") && !f.bit("scene-busy") && !inZone(f.mot.moveList())
	})

	f.setBit(f.m.pins.sceneHold, false)
	f.eventually("scene-busy while the leg runs", func() bool { return f.bit("scene-busy") })
	f.awaitJob()
	f.requireOK("the job once the hold dropped")
	if f.bit("scene-busy") {
		t.Error("scene-busy still up after the job")
	}
}

// TestSceneGateLeavesOtherLegsAlone: a job whose legs keep away from the region
// runs with scene-hold up the whole time, and never raises scene-busy.
func TestSceneGateLeavesOtherLegsAlone(t *testing.T) {
	f := newSceneFixture(t)
	f.fillTray(0)
	f.setBit(f.m.pins.sceneHold, true)

	startJobTo(f, 10, 20)
	for !f.jobDone() {
		if f.bit("scene-busy") {
			t.Fatal("scene-busy raised by a job that never comes near the region")
		}
		time.Sleep(pollInterval)
	}
	f.requireOK("a job clear of the region, scene-hold up")
}

// TestSceneGateWaitAbort: a wait at the gate ends like a station wait (D33):
// WAIT_ABORTED, nothing driven into the region, scene-busy low.
func TestSceneGateWaitAbort(t *testing.T) {
	f := newSceneFixture(t)
	f.setBit(f.m.pins.sceneHold, true)

	startJobTo(f, 21, 10)
	f.eventually("the job to start", func() bool { return f.bit("busy") })
	f.consistently("the leg held", func() bool { return f.bit("busy") && !f.bit("scene-busy") })
	f.setBit(f.m.pins.waitAbort, true)
	f.awaitJob()
	f.setBit(f.m.pins.waitAbort, false)
	f.requireError("a gate wait given up", errWaitAborted)
	if inZone(f.mot.moveList()) || f.bit("scene-busy") {
		t.Errorf("after the abort: moves into the region %v, scene-busy %v",
			inZone(f.mot.moveList()), f.bit("scene-busy"))
	}
}

// TestSceneGateReplansAfterTheWait: the scene changes while a leg waits at the
// gate. The leg is planned again once the lock is taken -- in scene 1 the zone
// is drawn, so the route runs around it instead of through it -- and a route
// that no longer touches the region does not keep scene-busy up.
func TestSceneGateReplansAfterTheWait(t *testing.T) {
	f := newSceneFixture(t)
	f.mot.setMoveCycles(20)
	f.setBit(f.m.pins.sceneHold, true)

	startJobTo(f, 22, 10)
	f.eventually("the job to start", func() bool { return f.bit("busy") })
	f.consistently("the leg through the region held", func() bool {
		return f.bit("busy") && !inZone(f.mot.moveList())
	})

	f.m.pins.deadzoneSelect.Set(1)
	f.setBit(f.m.pins.sceneHold, false)
	f.awaitJob()
	f.requireOK("the job after the scene changed")
	if inZone(f.mot.moveList()) {
		t.Error("the leg was not planned again: it went through the zone scene 1 draws")
	}
}

// TestJobBusyWaitAbortedByWaitAbort: wait-abort ends a wait at a taught wait
// position the way auto-enable going low does (D33) -- WAIT_ABORTED, the
// picker keeps its material -- but the machine stays in auto mode, so the
// next job is taken at once.
func TestJobBusyWaitAbortedByWaitAbort(t *testing.T) {
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

	f.setBit(f.m.pins.waitAbort, true)
	f.eventually("the job to end", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireError("wait-abort during the wait", errWaitAborted)
	f.setBit(f.m.pins.waitAbort, false)

	if !f.bit("picker.0.close") {
		t.Error("the picker released its material when the wait was aborted")
	}
	if f.bit("proc.20.has-material") {
		t.Error("the station reports material although the place never happened")
	}

	// Still in auto mode: after the reset the material goes on, its pick
	// skipped, to the station that has cleared meanwhile.
	f.clearError()
	f.setBit(f.proc().busy, false)
	f.runJob(10, 20, 0)
	f.requireOK("the job after the abort")
	if !f.bit("proc.20.has-material") {
		t.Error("the held material was not placed by the next job")
	}
}

// TestJobWaitAbortIgnoredWithoutAWait: wait-abort only ends a wait. A job
// that never has to wait runs to its end with the pin high.
func TestJobWaitAbortIgnoredWithoutAWait(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	f.setBit(f.m.pins.waitAbort, true)

	f.runJob(10, 20, 0)
	f.requireOK("a job with wait-abort high and nothing to wait for")
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds nothing after the place")
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
	f.mot.hold()

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
	f.mot.hold()

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

// TestRemoveFromProcReleasesBeforeGrip pins D36's pick: the fixture lets go
// first and the picker closes after RELEASE_SETTLE, on a free part. A picker
// closing on a part the fixture still clamps forces any position mismatch
// through the portal.
func TestRemoveFromProcReleasesBeforeGrip(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.world.procs[0].setHasMaterial(true)
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	// A wide dwell, so a close cannot slip through it unnoticed.
	f.proc().releaseSettle.Set(100 * pollInterval.Seconds())

	// Started by hand rather than with runJob, which waits the job out: what
	// is being watched here happens in the middle of it.
	f.m.pins.startJob.Set(false)
	f.sampled()
	f.m.pins.originID.Set(20) // the proc station
	f.m.pins.destID.Set(10)   // the tray
	f.m.pins.processStep.Set(0)
	f.m.pins.startJob.Set(true)

	// And it is a dwell, not a deadlock: the grip follows, and the job runs on
	// to the tray.
	f.eventually("the picker closes after the settle", func() bool { return f.bit("picker.0.close") })
	f.eventually("the job finishes", func() bool { return !f.bit("busy") })
	f.requireOK("a removal that let go first")
	if f.bit("proc.20.has-material") || f.bit("proc.20.release") {
		t.Errorf("after the removal has-material=%v release=%v, want both low",
			f.bit("proc.20.has-material"), f.bit("proc.20.release"))
	}

	// The dwell is wall-clock time, so it is measured in wall-clock time: from
	// the released feedback the module saw to the close it commanded. A stall
	// anywhere can only lengthen that span; without the dwell it is a cycle
	// or two, far short of the settle, and a close before the release comes
	// out negative.
	settle := time.Duration(100 * float64(pollInterval))
	if waited := f.sim.closedSince(0).Sub(f.sim.releasedSince(0)); waited < settle {
		t.Errorf("the picker closed %v after the fixture released, want at least RELEASE_SETTLE %v",
			waited, settle)
	}
}

// TestPlaceToProcClampsBeforeLift pins D36's place: the fixture clamps the
// part before the picker lifts away from it -- a part left loose in an open
// fixture tips over as the picker leaves.
func TestPlaceToProcClampsBeforeLift(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// A wide release-time dwell, so the order is visible.
	f.m.pins.releaseTime.Set(30 * pollInterval.Seconds())

	f.m.pins.startJob.Set(false)
	f.sampled()
	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.m.pins.startJob.Set(true)

	f.eventually("the part is placed", func() bool { return f.bit("proc.20.has-material") })
	// The fixture is slow to report itself clamped: the lift has to wait for it.
	f.sim.set(func(s *machineSim) { s.fixtureStuck = true })
	moves := len(f.mot.moveList())
	f.eventually("the fixture is told to clamp", func() bool { return !f.bit("proc.20.release") })
	f.consistently("no lift until it has", func() bool { return len(f.mot.moveList()) == moves })
	f.sim.set(func(s *machineSim) { s.fixtureStuck = false })
	f.eventually("clamped: then the lift", func() bool { return len(f.mot.moveList()) > moves })
	f.eventually("the job finishes", func() bool { return !f.bit("busy") })
	f.requireOK("a place that clamped first")
}

// TestEstopDuringPlaceDwellKeepsRecords: the records commit the moment "opened"
// confirms — the part physically left the picker — so an estop during the
// release-time dwell must not lose it from the model (a lost record is a second
// part stacked onto the invisible one by the next job).
func TestEstopDuringPlaceDwellKeepsRecords(t *testing.T) {
	f := newJobFixture(t)
	f.selectTray(1)
	f.fillTray(0)
	// A dwell no test goroutine can sleep through, so the estop lands inside
	// it however late it is sent; the estop ends the dwell (abortCheck).
	f.m.pins.releaseTime.Set(3600)

	f.m.pins.startJob.Set(false)
	f.sampled()
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
	// The job stands in its first move until the request has been seen.
	f.mot.hold()

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.startJob.Set(true)
	f.eventually("the job to start moving", func() bool {
		return f.bit("busy") && len(f.mot.moveList()) > 0
	})

	// Ask for the tray to be emptied while the job is running.
	f.tray().setEmpty.Set(false)
	f.tray().setEmpty.Set(true)
	f.sampled()
	f.mot.release()
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
	// Not judged against D13's budget here: this is one wall-clock run, which a
	// preemption on a shared runner stretches. TestPlanWithinD13Budget is that
	// check.

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
		// The pre-armed miss judges the re-driven close as "gripped nothing":
		// the part is gone.
		sim: func(s *machineSim) { s.missesLeft = 1 },
	})
	jf := &jobFixture{machineFixture: f}
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
	f.sampled()
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

// TestTravelSmoothPath (D47-D50): with PATH_SHAPER = spline a leg around a
// zone is queued as a tangent chain of arcs and lines at the movement
// height, clear of the zone, at the arc limits canon would give (min of the
// axes) with the requested speed as ini_maxvel (D49), and ends in an exact
// stop on a line (D48's closing line).
func TestTravelSmoothPath(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		files: map[string]string{zonesA: "zones_between.dxf", zonesB: fixtureClear},
		tweak: func(c *Config) { c.SmoothPaths = true },
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("a job on smoothed paths")

	moves := f.mot.moveList()
	arcs := 0
	prev := pnproute.Point{X: 100, Y: 100}
	prevZ := 60.0
	for i, m := range moves {
		cur := pnproute.Point{X: m.pos.X, Y: m.pos.Y}
		if !m.arc {
			if segmentCrossesRect(prev, cur, 100, 250, 135, 350) {
				t.Errorf("line (%.1f, %.1f) -> (%.1f, %.1f) crosses the dead zone", prev.X, prev.Y, cur.X, cur.Y)
			}
			prev, prevZ = cur, m.pos.Z
			continue
		}
		arcs++
		if m.pos.Z != prevZ || m.center.Z != m.pos.Z || m.normal != (motctl.Cartesian{Z: 1}) {
			t.Fatalf("arc %d is not a constant-Z XY arc: %+v from Z %.3f", i, m, prevZ)
		}
		if m.turn != 0 && m.turn != -1 {
			t.Fatalf("arc %d has turn %d", i, m.turn)
		}
		c := pnproute.Point{X: m.center.X, Y: m.center.Y}
		r := math.Hypot(prev.X-c.X, prev.Y-c.Y)
		if math.Abs(math.Hypot(cur.X-c.X, cur.Y-c.Y)-r) > 1e-6*r {
			t.Fatalf("arc %d does not end on its circle", i)
		}
		if m.acc != 2000 || !(m.vel > 0) || m.vel > 500 || m.iniMaxvel > m.vel+1e-9 ||
			m.iniMaxvel < math.Min(m.vel, math.Sqrt(math.Sqrt(0.75)*2000*r))-1e-9 {
			t.Errorf("arc %d: vel %g, ini_maxvel %g, acc %g at R %.1f", i, m.vel, m.iniMaxvel, m.acc, r)
		}
		if m.termCond != tpTermCondParabolic {
			t.Errorf("arc %d queued with term cond %d", i, m.termCond)
		}
		// The arc itself stays out of the zone, sampled along its sweep.
		a0 := math.Atan2(prev.Y-c.Y, prev.X-c.X)
		sweep := math.Atan2(cur.Y-c.Y, cur.X-c.X) - a0
		if m.turn == 0 && sweep < 0 {
			sweep += 2 * math.Pi
		} else if m.turn == -1 && sweep > 0 {
			sweep -= 2 * math.Pi
		}
		q := prev
		for k := 1; k <= 16; k++ {
			ang := a0 + sweep*float64(k)/16
			p := pnproute.Point{X: c.X + r*math.Cos(ang), Y: c.Y + r*math.Sin(ang)}
			if segmentCrossesRect(q, p, 100, 250, 135, 350) {
				t.Errorf("arc %d enters the dead zone", i)
			}
			q = p
		}
		prev = cur
	}
	if arcs == 0 {
		t.Fatal("no arcs: the leg around the zone was not smoothed")
	}
	// Every leg ends on a line under an exact stop, never on an arc.
	for i, m := range moves {
		if m.arc && m.termCond == tpTermCondStop {
			t.Errorf("move %d: an arc queued as an exact stop", i)
		}
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
	f.sampled()
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
