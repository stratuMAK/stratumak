// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// fakeMotion is a scripted motion stack: it answers the motctl commands and
// motstat reads the control loop makes, and simulates just enough of motmod to
// drive the state machine — an enable that shows up in status, mode switches
// that take, and a home sequence that runs for a configurable number of cycles.
//
// It is deliberately simple-minded about ordering (a mutex around everything):
// the control loop calls it from one goroutine and the test from another, and
// what the tests assert is which commands arrived, not how fast.
type fakeMotion struct {
	mu sync.Mutex

	st     motstat.MotionStatus
	posFb  motstat.Pose
	posCmd motstat.Pose

	numJoints int

	// Scripting knobs.
	refuseEnable bool // Enable() is accepted but never shows up in status
	homingCycles int  // how many status reads report "homing" before homed
	statusErr    error

	// moveCycles is how many status reads a dispatched move stays in flight
	// before it reports drained; 0 completes it instantly, which is what most
	// cases want (they are about the sequence, not about the travel time).
	moveCycles int
	moving     int

	// homingGapFrom/homingGapLen script a HOME_SEQUENCE group handover: for
	// homingGapLen status reads, starting when the countdown reaches
	// homingGapFrom, no joint reports Homing although the sequence is still
	// running — the window motmod exposes between sequence groups.
	homingGapFrom int
	homingGapLen  int

	// Recorded calls.
	calls     []string
	jogs      []jogCall
	homeCmds  []int32
	moves     []moveCall
	termConds []termCall
}

type jogCall struct {
	axis int32
	vel  float64
	stop bool
}

// moveCall is one dispatched SetLine: where to and under which limits.
type moveCall struct {
	pos      motctl.Pose
	vel      float64
	acc      float64
	id       int32
	termCond int32
}

type termCall struct {
	cond      int32
	tolerance float64
}

func newFakeMotion(numJoints int) *fakeMotion {
	f := &fakeMotion{numJoints: numJoints}
	f.st.MotionState = motstat.MOTION_DISABLED
	// A machine standing still with an empty queue, which is what every job
	// starts from.
	f.st.Inpos = 1
	return f
}

func (f *fakeMotion) record(name string) {
	f.calls = append(f.calls, name)
}

// --- motstat ---

func (f *fakeMotion) GetStatus() (motstat.MotionStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return motstat.MotionStatus{}, f.statusErr
	}
	if f.moving > 0 {
		f.moving--
		if f.moving == 0 {
			f.st.Inpos, f.st.QueueDepth = 1, 0
		}
	}
	if f.homingCycles > 0 {
		f.homingCycles--
		inGap := f.homingGapFrom > 0 &&
			f.homingCycles <= f.homingGapFrom && f.homingCycles > f.homingGapFrom-f.homingGapLen
		switch {
		case f.homingCycles == 0:
			for j := 0; j < f.numJoints; j++ {
				f.st.Joints[j].Homing = 0
				f.st.Joints[j].Homed = 1
			}
		case inGap:
			for j := 0; j < f.numJoints; j++ {
				f.st.Joints[j].Homing = 0
			}
		default:
			for j := 0; j < f.numJoints; j++ {
				if f.st.Joints[j].Homed == 0 {
					f.st.Joints[j].Homing = 1
				}
			}
		}
	}
	return f.st, nil
}

func (f *fakeMotion) GetPosFb() (motstat.Pose, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posFb, nil
}

func (f *fakeMotion) GetPosCmd() (motstat.Pose, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posCmd, nil
}

// --- motctl: the commands the control loop issues ---

func (f *fakeMotion) Abort() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Abort")
	return nil
}

func (f *fakeMotion) Enable() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Enable")
	if !f.refuseEnable {
		f.st.Enabled = 1
		f.st.MotionState = motstat.MOTION_FREE
	}
	return nil
}

func (f *fakeMotion) Disable() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Disable")
	f.st.Enabled = 0
	f.st.MotionState = motstat.MOTION_DISABLED
	return nil
}

func (f *fakeMotion) setMode(name string, state int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(name)
	if f.st.Enabled != 0 {
		f.st.MotionState = state
	}
	return nil
}

func (f *fakeMotion) SetFree() error   { return f.setMode("SetFree", motstat.MOTION_FREE) }
func (f *fakeMotion) SetTeleop() error { return f.setMode("SetTeleop", motstat.MOTION_TELEOP) }
func (f *fakeMotion) SetCoord() error  { return f.setMode("SetCoord", motstat.MOTION_COORD) }

func (f *fakeMotion) JointHome(joint int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("JointHome")
	f.homeCmds = append(f.homeCmds, joint)
	if f.homingCycles == 0 {
		// Instant home, the sim config's behaviour (home switch always made).
		for j := 0; j < f.numJoints; j++ {
			f.st.Joints[j].Homed = 1
		}
		return nil
	}
	for j := 0; j < f.numJoints; j++ {
		f.st.Joints[j].Homing = 1
	}
	return nil
}

func (f *fakeMotion) JogCont(num int32, vel float64, _ int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("JogCont")
	f.jogs = append(f.jogs, jogCall{axis: num, vel: vel})
	return nil
}

func (f *fakeMotion) SetTermCond(cond int32, tolerance float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SetTermCond")
	f.termConds = append(f.termConds, termCall{cond: cond, tolerance: tolerance})
	return nil
}

// SetLine takes the machine to the commanded pose. The position is applied at
// once — what these cases are about is which moves a job makes, in which order and
// under which limits, not how the machine gets there; moveCycles is for the one
// case that needs a move still in flight.
func (f *fakeMotion) SetLine(pos motctl.Pose, vel, _, acc float64, _, id int32, _ float64, _ int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SetLine")
	term := int32(-1)
	if n := len(f.termConds); n > 0 {
		term = f.termConds[n-1].cond
	}
	f.moves = append(f.moves, moveCall{pos: pos, vel: vel, acc: acc, id: id, termCond: term})
	f.posCmd = motstat.Pose{X: pos.X, Y: pos.Y, Z: pos.Z}
	f.posFb = f.posCmd
	if f.moveCycles > 0 {
		f.moving = f.moveCycles
		f.st.Inpos, f.st.QueueDepth = 0, 1
	} else {
		f.st.Inpos, f.st.QueueDepth = 1, 0
	}
	return nil
}

func (f *fakeMotion) JogAbort(num int32, _ int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("JogAbort")
	f.jogs = append(f.jogs, jogCall{axis: num, stop: true})
	return nil
}

// --- motctl: the configuration push (motsetup.MotionConfig), all no-ops ---

func (f *fakeMotion) SetVel(float64) error                  { return nil }
func (f *fakeMotion) SetVelLimit(float64) error             { return nil }
func (f *fakeMotion) SetAcc(float64) error                  { return nil }
func (f *fakeMotion) SetMaxFeedOverride(float64) error      { return nil }
func (f *fakeMotion) SetWorldHome(motctl.Pose) error        { return nil }
func (f *fakeMotion) SetProbeErrInhibit(int32, int32) error { return nil }
func (f *fakeMotion) SetupArcBlends(int32, int32, int32, int32, float64, float64) error {
	return nil
}
func (f *fakeMotion) JointActivate(int32) error                            { return nil }
func (f *fakeMotion) SetJointPositionLimits(int32, float64, float64) error { return nil }
func (f *fakeMotion) SetJointBacklash(int32, float64) error                { return nil }
func (f *fakeMotion) SetJointMaxFerror(int32, float64) error               { return nil }
func (f *fakeMotion) SetJointMinFerror(int32, float64) error               { return nil }
func (f *fakeMotion) SetJointVelLimit(int32, float64) error                { return nil }
func (f *fakeMotion) SetJointAccLimit(int32, float64) error                { return nil }
func (f *fakeMotion) SetJointJerkLimit(int32, float64) error               { return nil }
func (f *fakeMotion) SetJointHomingParams(int32, float64, float64, float64, float64, float64, int32, int32, int32) error {
	return nil
}
func (f *fakeMotion) SetJointComp(int32, float64, float64, float64) error { return nil }
func (f *fakeMotion) SetAxisPositionLimits(int32, float64, float64) error { return nil }
func (f *fakeMotion) SetAxisVelLimit(int32, float64, float64) error       { return nil }
func (f *fakeMotion) SetAxisAccLimit(int32, float64, float64) error       { return nil }
func (f *fakeMotion) SetAxisLockingJoint(int32, int32) error              { return nil }
func (f *fakeMotion) SetSpindleParams(int32, float64, float64, float64, float64, float64, int32, float64) error {
	return nil
}

// --- test helpers ---

// callList is the recorded call names; taking a copy under the lock is what
// keeps a failing assertion from racing the loop that is still running.
func (f *fakeMotion) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMotion) setStatusErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusErr = err
}

// setInFlight scripts a move in progress: status reports not-in-position for
// the given number of reads, then drained.
func (f *fakeMotion) setInFlight(cycles int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moving = cycles
	f.st.Inpos, f.st.QueueDepth = 0, 1
}

// setHomingCycles scripts the homing duration while the loop may already be
// reading — the counter lives under the same mutex GetStatus takes.
func (f *fakeMotion) setHomingCycles(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homingCycles = n
}

func (f *fakeMotion) setHomingGap(from, length int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homingGapFrom, f.homingGapLen = from, length
}

func (f *fakeMotion) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeMotion) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *fakeMotion) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.jogs = nil
	f.moves = nil
	f.termConds = nil
}

func (f *fakeMotion) lastJog() (jogCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jogs) == 0 {
		return jogCall{}, false
	}
	return f.jogs[len(f.jogs)-1], true
}

func (f *fakeMotion) setMotionState(state int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.MotionState = state
}

func (f *fakeMotion) setPosFb(x, y float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posFb.X, f.posFb.Y = x, y
}

// setPos parks the machine at a position, feedback and command alike — where a
// homing sequence would leave it.
func (f *fakeMotion) setPos(x, y, z float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posCmd = motstat.Pose{X: x, Y: y, Z: z}
	f.posFb = f.posCmd
}

// moveList is the dispatched moves, copied under the lock.
func (f *fakeMotion) moveList() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]moveCall(nil), f.moves...)
}

func (f *fakeMotion) setMoveCycles(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveCycles = n
}

// machineFixture is a loaded module with a scripted motion stack and a running
// control loop, plus the pin plumbing the tests poke.
type machineFixture struct {
	t    *testing.T
	m    *pnptaskModule
	mot  *fakeMotion
	name string
}

// The control loop is time-driven; the tests run it far faster than the machine
// would so a case costs milliseconds rather than seconds.
func fastLoop(t *testing.T) {
	t.Helper()
	oldPoll, oldEnable, oldMode := pollInterval, motionEnableTimeout, motionModeTimeout
	pollInterval = time.Millisecond
	motionEnableTimeout = 100 * time.Millisecond
	motionModeTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		pollInterval, motionEnableTimeout, motionModeTimeout = oldPoll, oldEnable, oldMode
	})
}

// jogIniSection gives the axes a max velocity, so the clamp has something to
// clamp to. Joint sections keep the config push happy on the way through.
const machineIniAxes = `
[AXIS_X]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[AXIS_Y]
MAX_VELOCITY = 500
MAX_ACCELERATION = 4000
[AXIS_Z]
MAX_VELOCITY = 300
MAX_ACCELERATION = 3000
[JOINT_0]
MAX_VELOCITY = 800
[JOINT_1]
MAX_VELOCITY = 500
[JOINT_2]
MAX_VELOCITY = 300
`

func newMachineFixture(t *testing.T, args ...string) *machineFixture {
	return newMachineFixtureOpts(t, fixtureOpts{args: args})
}

// newMachineFixtureWith is newMachineFixture with a configuration tweak.
func newMachineFixtureWith(t *testing.T, tweak func(*Config), args ...string) *machineFixture {
	return newMachineFixtureOpts(t, fixtureOpts{tweak: tweak, args: args})
}

// fixtureOpts are the hooks a case needs before the control loop exists: after
// it starts, the config and the world model belong to the control goroutine.
type fixtureOpts struct {
	// tweak adjusts the parsed configuration.
	tweak func(*Config)
	// prep runs last, with the pins exported and the motion stack plugged in but
	// the loop not yet started — where a test seeds pins or attaches
	// persistence.
	prep func(*testing.T, *pnptaskModule)
	args []string
	// ini is appended to the fixture INI — for the cases that need a station
	// the shared config does not have (§8's chain needs two process stations).
	ini string
}

// newMachineFixtureOpts loads a module, hands it a scripted motion stack and
// starts the control loop.
func newMachineFixtureOpts(t *testing.T, o fixtureOpts) *machineFixture {
	t.Helper()
	fastLoop(t)
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections+o.ini, name, o.args...)
	if o.tweak != nil {
		o.tweak(m.cfg)
	}

	mot := newFakeMotion(m.cfg.NumJoints)
	m.mc, m.ms = mot, mot
	if o.prep != nil {
		o.prep(t, m)
	}
	if err := m.startControl(); err != nil {
		t.Fatalf("startControl: %v", err)
	}
	t.Cleanup(m.Stop)
	return &machineFixture{t: t, m: m, mot: mot, name: name}
}

// setBit and setFloat write an input pin the way halcmd setp would: the pins
// are unconnected here, so the write lands in the pin's own value cell. Reads
// go the other way, through the HAL name table, so the tests also pin down the
// exported pin names.
func (f *machineFixture) setBit(pin *hal.Pin[bool], v bool) { pin.Set(v) }

func (f *machineFixture) setFloat(pin *hal.Pin[float64], v float64) { pin.Set(v) }

// pulse drives an edge-triggered input low, gives the loop time to see the low,
// and drives it high again — the edge detection compares consecutive cycles, so
// a low/high pair inside one cycle is no edge at all.
func (f *machineFixture) pulse(pin *hal.Pin[bool]) {
	pin.Set(false)
	time.Sleep(10 * pollInterval)
	pin.Set(true)
}

// press is pulse followed by the release — a whole button press. The high half
// is held for a handful of cycles: a level the loop never samples is no edge,
// so a caller that has no observable effect to wait for cannot simply drive the
// pin low again.
func (f *machineFixture) press(pin *hal.Pin[bool]) {
	f.pulse(pin)
	time.Sleep(10 * pollInterval)
	pin.Set(false)
}

func (f *machineFixture) get(pin string) float64 {
	f.t.Helper()
	v, ok := hal.LookupValue(f.name + "." + pin)
	if !ok {
		f.t.Fatalf("pin %s not found", pin)
	}
	return v
}

func (f *machineFixture) bit(pin string) bool { return f.get(pin) != 0 }

// eventually polls cond at the loop rate until it holds or the deadline passes.
// The control loop runs on its own goroutine, so every assertion about what it
// did is a race unless it is given time to run.
func (f *machineFixture) eventually(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollInterval)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

// consistently gives the loop a handful of cycles and then checks cond — for
// the assertions that something did NOT happen.
func (f *machineFixture) consistently(what string, cond func() bool) {
	f.t.Helper()
	for i := 0; i < 20; i++ {
		time.Sleep(pollInterval)
		if !cond() {
			f.t.Fatalf("%s: stopped holding after %d cycles", what, i)
		}
	}
}

// stopped joins the control goroutine and hands back the world model. Reading
// the model while the loop runs would be a data race — it belongs to that
// goroutine — so a test that has to inspect it stops the module first. Stop
// tolerates being called again by the fixture's cleanup.
func (f *machineFixture) stopped() *world {
	f.t.Helper()
	f.m.Stop()
	return f.m.world
}

// machineOn brings the fixture to the state most cases start from: enabled and
// homed.
func (f *machineFixture) machineOn() {
	f.t.Helper()
	f.setBit(f.m.pins.machineOn, true)
	f.eventually("machine-is-on", func() bool { return f.bit("machine-is-on") })
}

func (f *machineFixture) homed() {
	f.t.Helper()
	f.machineOn()
	f.setBit(f.m.pins.home, true)
	f.eventually("homed", func() bool { return f.bit("homed") })
	f.setBit(f.m.pins.home, false)
}

// ---------------------------------------------------------------------------

// machine-on high enables motion, and machine-is-on only goes high once motion
// itself reports enabled.
func TestMachineEnable(t *testing.T) {
	f := newMachineFixture(t)
	f.consistently("machine off until asked", func() bool { return !f.bit("machine-is-on") })

	f.machineOn()
	if !f.mot.called("Enable") {
		t.Error("Enable was not commanded")
	}

	// And machine-on going low takes it back down, aborting motion first.
	f.setBit(f.m.pins.machineOn, false)
	f.eventually("machine off", func() bool { return !f.bit("machine-is-on") })
	if !f.mot.called("Disable") {
		t.Error("Disable was not commanded")
	}
}

// An enable motion never confirms must not leave machine-is-on set: the PLC
// interlocks on that pin, and a machine that reports itself on while motion is
// disabled is one that swallows the next job.
func TestMachineEnableRefused(t *testing.T) {
	f := newMachineFixture(t)
	f.mot.refuseEnable = true

	f.setBit(f.m.pins.machineOn, true)
	f.eventually("MOTION_ERROR", func() bool { return f.get("error-id") == float64(errMotionError) })
	if f.bit("machine-is-on") {
		t.Error("machine-is-on set although motion never reported enabled")
	}
}

// Estop aborts and disables motion and releases the pickers (D14) — the one
// event that drops held material.
func TestEstopAbortsAndReleasesPickers(t *testing.T) {
	f := newMachineFixture(t, "pickers=2")
	f.machineOn()

	// Something is held.
	f.setBit(f.m.pins.autoEnable, false)
	f.setBit(f.m.pins.pickers[0].manualClose, true)
	f.eventually("picker closed", func() bool { return f.bit("picker.0.close") })

	f.mot.resetCalls()
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("machine-is-on low", func() bool { return !f.bit("machine-is-on") })
	if !f.mot.called("Abort") || !f.mot.called("Disable") {
		t.Errorf("estop did not abort and disable motion: %v", f.mot.callList())
	}
	if f.bit("picker.0.close") {
		t.Error("estop did not release the picker")
	}
	// Manual picker control is inhibited while estop is active.
	f.pulse(f.m.pins.pickers[0].manualClose)
	f.consistently("picker stays released in estop", func() bool { return !f.bit("picker.0.close") })
}

// Clearing estop must NOT bring the machine back by itself: machine-on is a
// request, and a level still high from before the estop is not one.
func TestEstopClearRequiresMachineOnCycle(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("off", func() bool { return !f.bit("machine-is-on") })

	f.setBit(f.m.pins.estopOn, false)
	f.consistently("stays off while machine-on was never cycled", func() bool {
		return !f.bit("machine-is-on")
	})

	f.pulse(f.m.pins.machineOn)
	f.eventually("back on after cycling machine-on", func() bool { return f.bit("machine-is-on") })
}

// A machine-on edge made DURING estop is not a request that survives it: the
// post-estop rule is a fresh cycle (§6.2), and a latched pre-reset edge powering
// the servos the instant the estop chain closes is a restart-interlock hole.
func TestMachineOnEdgeDuringEstopNotLatched(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("off in estop", func() bool { return !f.bit("machine-is-on") })

	// The PLC cycles machine-on while estop is still active.
	f.pulse(f.m.pins.machineOn)
	time.Sleep(10 * pollInterval)

	f.setBit(f.m.pins.estopOn, false)
	f.consistently("stays off after estop clears", func() bool { return !f.bit("machine-is-on") })

	// A cycle made after the estop cleared is the real request.
	f.pulse(f.m.pins.machineOn)
	f.eventually("enabled by a post-estop cycle", func() bool { return f.bit("machine-is-on") })
}

// A manual picker press during estop must not actuate when estop clears: the
// pickers were just force-opened on purpose (D14), and a stale re-grip closes a
// gripper over a workspace someone may be clearing.
func TestManualPickerPressDuringEstopNotLatched(t *testing.T) {
	f := newMachineFixture(t)
	f.setBit(f.m.pins.autoEnable, false)
	f.setBit(f.m.pins.estopOn, true)
	time.Sleep(10 * pollInterval)

	f.pulse(f.m.pins.pickers[0].manualClose)
	time.Sleep(10 * pollInterval)

	f.setBit(f.m.pins.estopOn, false)
	f.consistently("press during estop stays dead after clear", func() bool {
		return !f.bit("picker.0.close")
	})

	f.pulse(f.m.pins.pickers[0].manualClose)
	f.eventually("fresh press closes", func() bool { return f.bit("picker.0.close") })
}

// An error-reset pulsed while NO fault is latched is a no-op, not a stored
// credit: it must not clear a fault raised later (the PLC would see a failed
// job with clean error pins and call it success).
func TestErrorResetWithoutFaultNotLatched(t *testing.T) {
	f := newMachineFixture(t)
	f.pulse(f.m.pins.errorReset)
	time.Sleep(10 * pollInterval)
	f.setBit(f.m.pins.errorReset, false)

	// Now a fault: homing with the machine off.
	f.setBit(f.m.pins.home, true)
	f.eventually("MACHINE_OFF latched", func() bool { return f.get("error-id") == float64(errMachineOff) })
	f.consistently("the pre-fault reset pulse does not clear it", func() bool {
		return f.bit("error")
	})

	// A reset made with the fault present clears it.
	f.pulse(f.m.pins.errorReset)
	f.eventually("cleared by a real reset", func() bool { return !f.bit("error") })
}

// machine-on going low keeps whatever the pickers hold (D14): only estop drops
// material.
func TestMachineOffKeepsPickerState(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()
	f.setBit(f.m.pins.autoEnable, false)
	f.setBit(f.m.pins.pickers[0].manualClose, true)
	f.eventually("picker closed", func() bool { return f.bit("picker.0.close") })

	f.setBit(f.m.pins.machineOn, false)
	f.eventually("machine off", func() bool { return !f.bit("machine-is-on") })
	f.consistently("picker keeps holding", func() bool { return f.bit("picker.0.close") })

	// And manual picker control still works with the machine off.
	f.setBit(f.m.pins.pickers[0].manualOpen, true)
	f.eventually("picker opened with the machine off", func() bool { return !f.bit("picker.0.close") })
}

// The home pin runs the §6.3 sequence: free mode, home everything, coord mode.
func TestHomeSequence(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()
	// Homing needs free mode; put motion in coord first so the switch is not a
	// no-op (a fresh enable already leaves motmod in free).
	f.mot.setMotionState(motstat.MOTION_COORD)
	f.mot.resetCalls()

	f.setBit(f.m.pins.home, true)
	f.eventually("homed", func() bool { return f.bit("homed") })
	// The homed pin is published one cycle before the mode switch back to
	// coord is commanded, so wait for the call rather than racing that gap.
	f.eventually("SetCoord after homing", func() bool { return f.mot.called("SetCoord") })

	if !f.mot.called("SetFree") || !f.mot.called("JointHome") {
		t.Errorf("home sequence calls = %v, want SetFree and JointHome", f.mot.callList())
	}
	if len(f.mot.homeCmds) != 1 || f.mot.homeCmds[0] != -1 {
		t.Errorf("home commands = %v, want a single home-all (-1)", f.mot.homeCmds)
	}
	// Level, not edge: holding the pin high must not re-home every cycle.
	f.consistently("no repeated homing", func() bool { return f.mot.callCount("JointHome") == 1 })
}

// A HOME_SEQUENCE group handover — a short window in which no joint reports
// Homing although the sequence is still running — must not be mistaken for a
// finished sequence: the "stopped homing" observation has to hold for
// homingSettleTicks before it counts (a spurious HOMING_FAILED here faulted a
// perfectly healthy multi-group homing).
func TestHomeSurvivesSequenceGroupHandover(t *testing.T) {
	f := newMachineFixture(t)
	f.mot.setHomingCycles(60)
	f.mot.setHomingGap(30, homingSettleTicks-2) // gap shorter than the settle window
	f.machineOn()

	f.setBit(f.m.pins.home, true)
	f.eventually("homed through the group handover", func() bool { return f.bit("homed") })
	if f.bit("error") {
		t.Errorf("error latched during a homing group handover (error-id %v)", f.get("error-id"))
	}
}

// A home that never completes ends as HOMING_FAILED rather than as a machine
// that quietly waits forever, and error-reset clears the latch.
func TestHomeTimeoutAndErrorReset(t *testing.T) {
	f := newMachineFixtureWith(t, func(cfg *Config) { cfg.HomeTimeout = 0.05 })
	// Homing that runs "forever": more cycles than the timeout allows.
	f.mot.setHomingCycles(1 << 30)
	f.machineOn()

	f.setBit(f.m.pins.home, true)
	f.eventually("HOMING_FAILED", func() bool { return f.get("error-id") == float64(errHomingFailed) })
	if !f.bit("error") {
		t.Error("error flag not set alongside error-id")
	}

	f.setBit(f.m.pins.errorReset, true)
	f.eventually("error cleared", func() bool { return !f.bit("error") && f.get("error-id") == 0 })
}

// Homing with the machine off is MACHINE_OFF, not a silent no-op.
func TestHomeRequiresMachineOn(t *testing.T) {
	f := newMachineFixture(t)
	f.setBit(f.m.pins.home, true)
	f.eventually("MACHINE_OFF", func() bool { return f.get("error-id") == float64(errMachineOff) })
}

// Withdrawing machine-on while the enable is still waiting for motion to
// confirm must cancel the enable: no machine-is-on pulse against the withdrawn
// request, and no latched fault — the PLC changed its mind, nothing failed.
func TestEnableCancelledByMachineOnDrop(t *testing.T) {
	f := newMachineFixture(t)
	// Motion never confirms, so the enable keeps waiting until the request is
	// withdrawn. The wait must outlive any scheduling hiccup (race-detector
	// runs slow the goroutines), or the timeout fault would race the
	// cancellation this test is about; fastLoop's cleanup restores it.
	motionEnableTimeout = 10 * time.Second
	f.mot.refuseEnable = true

	f.setBit(f.m.pins.machineOn, true)
	f.eventually("Enable commanded", func() bool { return f.mot.called("Enable") })
	f.setBit(f.m.pins.machineOn, false)

	f.eventually("Disable commanded", func() bool { return f.mot.called("Disable") })
	f.consistently("stays off with no fault", func() bool {
		return !f.bit("machine-is-on") && !f.bit("error")
	})
}

// One-shot inputs held high when the loop starts are stale levels, not fresh
// requests: home, error-reset and the manual picker pins must not fire on the
// first cycle. machine-on is the deliberate exception — held high across a
// restart it re-enables the machine (D26).
func TestHeldInputsAtStartupAreNotEdges(t *testing.T) {
	fastLoop(t)
	setupPaths(t)
	name := testInstanceName(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, name)
	mot := newFakeMotion(m.cfg.NumJoints)
	m.mc, m.ms = mot, mot

	// The PLC's latched state from before the restart: everything high.
	m.pins.machineOn.Set(true)
	m.pins.home.Set(true)
	m.pins.autoEnable.Set(false)
	m.pins.pickers[0].manualClose.Set(true)

	if err := m.startControl(); err != nil {
		t.Fatalf("startControl: %v", err)
	}
	t.Cleanup(m.Stop)
	f := &machineFixture{t: t, m: m, mot: mot, name: name}

	// machine-on's boot edge is wanted...
	f.eventually("machine on from the held level", func() bool { return f.bit("machine-is-on") })
	// ...but the one-shot requests are not.
	f.consistently("no homing and no picker close from stale levels", func() bool {
		return f.mot.callCount("JointHome") == 0 && !f.bit("picker.0.close")
	})

	// A real edge after startup still works.
	f.pulse(f.m.pins.home)
	f.eventually("homed on a fresh edge", func() bool { return f.bit("homed") })
}

// A transient status-read failure — an expected split read — must not glitch
// state derived from the snapshot: the homed pin stays high and a running jog
// is not aborted and re-issued.
func TestStatusGlitchDoesNotDisturbHomedOrJog(t *testing.T) {
	f := newMachineFixture(t)
	f.homed()
	f.setBit(f.m.pins.autoEnable, false)
	f.setFloat(f.m.pins.jogSpeed, 100)
	f.setBit(f.m.pins.jog[0].pos, true)
	f.eventually("jog started", func() bool { return f.mot.callCount("JogCont") == 1 })

	// A glitch well inside the staleness tolerance.
	f.mot.setStatusErr(fmt.Errorf("split read"))
	time.Sleep(time.Duration(statusStaleTicks/2) * pollInterval)
	f.mot.setStatusErr(nil)

	f.consistently("jog undisturbed and homed held", func() bool {
		return f.mot.callCount("JogAbort") == 0 && f.bit("homed")
	})
}

// Jog: manual mode only, clamped to the axis maximum, one JogCont per press and
// one JogAbort per release.
func TestJog(t *testing.T) {
	f := newMachineFixture(t)
	f.homed()
	f.setBit(f.m.pins.autoEnable, false)
	f.setFloat(f.m.pins.jogSpeed, 200)
	f.mot.resetCalls()

	f.setBit(f.m.pins.jog[0].pos, true)
	f.eventually("jog started", func() bool { return f.mot.callCount("JogCont") == 1 })
	j, _ := f.mot.lastJog()
	if j.axis != 0 || j.vel != 200 {
		t.Errorf("jog = axis %d at %g, want axis 0 at 200", j.axis, j.vel)
	}
	f.consistently("one jog command per press", func() bool { return f.mot.callCount("JogCont") == 1 })

	f.setBit(f.m.pins.jog[0].pos, false)
	f.eventually("jog stopped", func() bool { return f.mot.callCount("JogAbort") >= 1 })

	// Negative direction, and the clamp: axis Y tops out at 500.
	f.setFloat(f.m.pins.jogSpeed, 900)
	f.setBit(f.m.pins.jog[1].neg, true)
	f.eventually("y jog", func() bool {
		j, ok := f.mot.lastJog()
		return ok && !j.stop && j.axis == 1
	})
	j, _ = f.mot.lastJog()
	if j.vel != -500 {
		t.Errorf("jog velocity = %g, want -500 (clamped to [AXIS_Y]MAX_VELOCITY, negative)", j.vel)
	}
}

// The jog pins are ignored in auto mode and while unhomed (D18), and a jog
// running when auto mode returns is stopped.
func TestJogGates(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()
	f.setBit(f.m.pins.autoEnable, false)
	f.setFloat(f.m.pins.jogSpeed, 100)

	f.setBit(f.m.pins.jog[0].pos, true)
	f.consistently("no jog while unhomed", func() bool { return f.mot.callCount("JogCont") == 0 })
	f.setBit(f.m.pins.jog[0].pos, false)

	f.homed()
	f.setBit(f.m.pins.autoEnable, true)
	// Give the mode switch a cycle before the jog pin goes high: writing both
	// between two reads of one sample would have the loop act on a manual-mode
	// jog request it is about to be told to ignore, which is this test's own
	// race, not the module's.
	time.Sleep(10 * pollInterval)
	f.setBit(f.m.pins.jog[0].pos, true)
	f.consistently("no jog in auto mode", func() bool { return f.mot.callCount("JogCont") == 0 })

	// Back to manual: it jogs, and switching to auto stops it.
	f.setBit(f.m.pins.autoEnable, false)
	f.eventually("jog started", func() bool { return f.mot.callCount("JogCont") == 1 })
	f.setBit(f.m.pins.autoEnable, true)
	f.eventually("jog aborted by leaving manual mode", func() bool { return f.mot.callCount("JogAbort") >= 1 })
}

// Both jog pins high at once is a contradictory request — a miswired panel —
// and must not pick a direction.
func TestJogBothDirections(t *testing.T) {
	f := newMachineFixture(t)
	f.homed()
	f.setBit(f.m.pins.autoEnable, false)
	f.setFloat(f.m.pins.jogSpeed, 100)

	f.setBit(f.m.pins.jog[0].pos, true)
	f.setBit(f.m.pins.jog[0].neg, true)
	f.consistently("no jog with both pins high", func() bool { return f.mot.callCount("JogCont") == 0 })
}

// Manual picker control is edge-triggered and confined to manual mode.
func TestManualPickers(t *testing.T) {
	f := newMachineFixture(t, "pickers=2")
	f.setBit(f.m.pins.autoEnable, true)
	f.setBit(f.m.pins.pickers[1].manualClose, true)
	f.consistently("ignored in auto mode", func() bool { return !f.bit("picker.1.close") })

	f.setBit(f.m.pins.autoEnable, false)
	// The pin is still high — but the edge happened in auto mode, so nothing
	// moves until it is pressed again.
	f.pulse(f.m.pins.pickers[1].manualClose)
	f.eventually("closed", func() bool { return f.bit("picker.1.close") })

	f.setBit(f.m.pins.pickers[1].manualOpen, true)
	f.eventually("opened", func() bool { return !f.bit("picker.1.close") })
}

// The teach pins report where each picker is: feedback plus that picker's
// offset (D21). Targeting commands (x - offset), so this is its inverse. On a
// metric machine the -mu (machine unit, D26) pins carry the same values.
func TestPickerTeachPositions(t *testing.T) {
	f := newMachineFixture(t, "pickers=2")
	// The offsets are taught with halcmd setp; here, directly.
	f.m.pins.pickers[1].xOffset.Set(40)
	f.m.pins.pickers[1].yOffset.Set(-15)
	f.mot.setPosFb(100, 200)

	f.eventually("teach positions", func() bool {
		return f.get("picker.0.pos-x") == 100 && f.get("picker.1.pos-x") == 140
	})
	if got := f.get("picker.1.pos-y"); got != 185 {
		t.Errorf("picker.1.pos-y = %g, want 185 (200 + -15)", got)
	}
	if x, y := f.get("picker.1.pos-x-mu"), f.get("picker.1.pos-y-mu"); x != 140 || y != 185 {
		t.Errorf("metric -mu teach pins = %g/%g, want 140/185 (equal to the mm pins)", x, y)
	}
}

// On an inch machine the -mu teach pins carry machine units — the value the
// INI wants — while the plain pins keep the internal mm (D23/D26). Pasting the
// mm pins into the INI was the 25.4x teach hole of the phase-3 review.
func TestPickerTeachPositionsInchMachine(t *testing.T) {
	f := newMachineFixtureWith(t, func(cfg *Config) { cfg.LinearUnits = 1.0 / 25.4 })
	f.mot.setPosFb(254, 508) // mm feedback: 10 by 20 inches

	f.eventually("teach positions", func() bool { return f.get("picker.0.pos-x") == 254 })
	if x, y := f.get("picker.0.pos-x-mu"), f.get("picker.0.pos-y-mu"); x != 10 || y != 20 {
		t.Errorf("inch -mu teach pins = %g/%g, want 10/20 (machine units)", x, y)
	}
}

// A motion controller that stops answering has to surface as MOTION_ERROR and
// drop machine-is-on, not as a machine that looks fine and never moves.
func TestMotionCommLoss(t *testing.T) {
	f := newMachineFixture(t)
	f.machineOn()

	f.mot.setStatusErr(fmt.Errorf("motion gone"))

	f.eventually("MOTION_ERROR", func() bool { return f.get("error-id") == float64(errMotionError) })
	if f.bit("machine-is-on") {
		t.Error("machine-is-on still set after losing the motion controller")
	}
}

// ensureHomed is what the job engine will call at the start of a job: it homes
// when AUTOHOME allows it and refuses with NOT_HOMED when it does not. It runs
// here without the loop goroutine — the job engine will call it from inside
// that goroutine, so a direct call is the same sequence, minus the race a test
// would otherwise introduce by poking a running loop's state.
func TestEnsureHomed(t *testing.T) {
	fastLoop(t)
	setupPaths(t)
	m := mustLoadModule(t, trajSection+machineIniAxes+pnptaskSection+stationSections, testInstanceName(t))
	mot := newFakeMotion(m.cfg.NumJoints)
	m.mc, m.ms = mot, mot

	// machine-on high: every wait inside the sequence aborts on a machine that
	// is off, and this fixture drives the pins directly rather than through the
	// enable sequence.
	m.pins.machineOn.Set(true)

	c := newControl(m)
	c.ticker = time.NewTicker(pollInterval)
	defer c.ticker.Stop()
	c.sample()

	// AUTOHOME off: a refusal the job engine turns into an error-id, not a
	// machine that homes itself because it felt like it.
	m.cfg.AutoHome = false
	err := c.ensureHomed()
	var fault *pnpFault
	if !errors.As(err, &fault) || fault.id != errNotHomed {
		t.Fatalf("ensureHomed with AUTOHOME off = %v, want NOT_HOMED", err)
	}

	// AUTOHOME on, machine enabled: it homes.
	m.cfg.AutoHome = true
	c.enabled = true
	mot.st.Enabled = 1
	if err := c.ensureHomed(); err != nil {
		t.Fatalf("ensureHomed: %v", err)
	}
	if !mot.called("JointHome") {
		t.Errorf("ensureHomed did not home: %v", mot.callList())
	}

	// Already homed: nothing is commanded a second time.
	mot.resetCalls()
	if err := c.ensureHomed(); err != nil {
		t.Fatalf("ensureHomed (already homed): %v", err)
	}
	if len(mot.callList()) != 0 {
		t.Errorf("ensureHomed re-homed an already homed machine: %v", mot.callList())
	}
}
