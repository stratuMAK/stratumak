// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"
	"math"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
)

// The control loop's timing. They are vars, not consts, so tests can run the
// same loop at a pace that does not cost a wall-clock second per case.
var (
	// pollInterval is one control cycle: 10 ms (100 Hz), the rate milltask's
	// monitor runs at. Every input this module reacts to is a PLC signal or an
	// operator button, so the rate is not about tracking the inputs — it is
	// what bounds the module's reaction time to an estop.
	pollInterval = 10 * time.Millisecond

	// motionEnableTimeout bounds the wait for motion to report itself enabled
	// after Enable(). Two servo cycles suffice on a healthy machine, so a hit
	// deadline means the enable was refused or instantly revoked (a tripped
	// limit, say) rather than that the machine is slow.
	motionEnableTimeout = 1 * time.Second

	// motionModeTimeout bounds a free/teleop/coord mode switch. Motion refuses
	// a switch while it is not in position, so the command is re-sent every
	// cycle until it takes (milltask's guards.go does the same).
	motionModeTimeout = 500 * time.Millisecond

	// commFailureTicks is how many consecutive motion-status read failures
	// declare the motion controller gone. A single failure is an expected
	// split read; a full second of them means the RT side died or detached.
	commFailureTicks = 100

	// statusStaleTicks is how many cycles the last good status snapshot keeps
	// serving after failed reads. A single failed read is an expected split
	// read (see commFailureTicks) and must not glitch state derived from the
	// snapshot — the homed pin dropping for one cycle, a jog aborted and
	// re-issued. 10 cycles is 100 ms of staleness, conservative because
	// nothing in the machine state changes uncommanded faster than the estop
	// chain, which has its own pin.
	statusStaleTicks = 10

	// homingSettleTicks is how many consecutive cycles "no joint homing" must
	// hold before it counts as the end of the homing sequence. motmod's homing
	// FSM has a 1-2 servo-cycle window between HOME_SEQUENCE groups in which
	// no joint reports Homing; a single snapshot landing in that window is a
	// group handover, not a finished sequence.
	homingSettleTicks = 5
)

// motionControl is the motctl subset this module commands. It is an interface,
// not the generated client, so the control loop can be exercised against a
// scripted motion stack — none of the logic below is reachable in a unit test
// otherwise, because a real motmod needs the RT environment.
type motionControl interface {
	// The startup configuration push (§6.1) goes through the same client.
	motsetup.MotionConfig

	Abort() error
	Enable() error
	Disable() error
	SetFree() error
	SetTeleop() error
	SetCoord() error
	JointHome(joint int32) error
	JogCont(num int32, vel float64, isTeleop int32) error
	JogAbort(num int32, isTeleop int32) error

	// Job motion (§7.3). Every move is a straight line: there are no arcs to
	// command, the planner's corners are rounded by the TP's blending.
	SetTermCond(cond int32, tolerance float64) error
	SetLine(pos motctl.Pose, vel, iniMaxvel, acc float64,
		motionType, id int32, feedMmPerMin float64, indexerJnum int32) error
}

// motionStatus is the motstat subset this module reads.
type motionStatus interface {
	GetStatus() (motstat.MotionStatus, error)
	GetPosFb() (motstat.Pose, error)
	// GetPosCmd is where a job's route planning starts from (§7.3). It is read
	// once per job rather than per cycle: the commanded position runs ahead of
	// the machine while the TP queue drains, and everything a job commands is
	// tracked from this anchor (see seedCmdPos).
	GetPosCmd() (motstat.Pose, error)
}

// The generated clients are what Start actually plugs in; assert the shapes
// here so a GMI regeneration that changes a signature fails to build rather
// than at the type assertion in Start.
var (
	_ motionControl = (*motctl.MotctlClient)(nil)
	_ motionStatus  = (*motstat.MotstatClient)(nil)
)

// inputs is one cycle's reading of every HAL input the control loop reacts to.
// Sampling them all at once keeps a cycle's decisions consistent: an estop that
// arrives mid-cycle is acted on by the next cycle rather than by half of this
// one.
type inputs struct {
	estop      bool
	machineOn  bool
	autoEnable bool
	home       bool
	errorReset bool

	// The job handshake. processStep is read every cycle, not only at a job's
	// latch: the tray "empty" outputs are "no slot matching a pick" and the
	// set-full edge writes this value into every slot, both of which are about
	// the step the PLC is asking about right now.
	processStep    uint32
	originID       uint32
	destID         uint32
	deadzoneSelect uint32
	startJob       bool

	pickerOpen  []bool
	pickerClose []bool

	// Per picker, parallel to pins.pickers: the gripper feedback the pick and
	// place sequences read.
	pickerOpened []bool
	pickerClosed []bool

	// Per process station, parallel to pins.procs.
	procBusy     []bool
	procReleased []bool

	// Per tray station, parallel to pins.trays: the selected geometry and the
	// two state-reset requests (D8).
	trayID       []uint32
	traySetFull  []bool
	traySetEmpty []bool

	jogPos   []bool
	jogNeg   []bool
	jogSpeed float64
}

// control is the module's single control goroutine and all of its state.
// Nothing here is touched from another goroutine, so none of it is locked —
// Start creates it, Stop asks it to finish, and everything in between belongs
// to run().
type control struct {
	m *pnptaskModule

	ticker *time.Ticker
	stop   chan struct{}
	done   chan struct{}

	// This cycle's inputs and the previous cycle's, for edge detection.
	in   inputs
	prev inputs

	// rise holds rising edges that have not been acted on yet. They are
	// *latched*, not per-cycle: the long operations of this module — the enable
	// handshake, homing, every action of a job — run inside the loop and tick it
	// themselves (§6.5), so each of them advances the edge detector many times
	// before step() looks again. A per-cycle edge pressed during one of those
	// would be sampled by a nested tick and gone by the time anything could act
	// on it, which is a button press silently doing nothing.
	//
	// Each consumer clears the flag it acts on (see take), and auto mode discards
	// the manual-control edges rather than storing them up (§6.4).
	rise inputs

	// The motion side of this cycle: one status snapshot and the feedback
	// position behind the picker teach pins.
	status     motstat.MotionStatus
	statusOK   bool
	posFb      motstat.Pose
	commErrors int

	// Machine state as this module sees it.
	estopped bool // estop-on was high and the teardown has run
	enabled  bool // motion is enabled and machine-is-on is set
	enabling bool // enable() is waiting for motion to confirm; see abortCheck

	// Jog state per configured axis: which way each axis is currently being
	// jogged (0 = not jogging), so a held pin does not re-issue JogCont every
	// cycle and a released one aborts exactly once.
	jogDir []int

	// cmdPos is the position the module has commanded so far, tracked so a
	// route's segments can be dispatched back to back without waiting for status
	// (see seedCmdPos). motionDispatched says whether anything has been commanded
	// since the last drain; moveID is the serial the moves carry into status.
	cmdPos           motctl.Pose
	motionDispatched bool
	moveID           int32

	// jobArmed is the start-job handshake's state: see jobRequest.
	jobArmed bool

	// trayPending is the latched tray reset request per station (D8): the
	// value every slot is to get, snapshotted at press time (set-full carries
	// the process-step the operator saw). nil = nothing pending; a later press
	// overwrites an earlier one. See the latch gating in sample.
	trayPending []*int64

	// heldVerify counts down to the one-shot validation of restored held
	// records against the picker feedback; see verifyRestoredHeld.
	heldVerify int
}

func newControl(m *pnptaskModule) *control {
	pickers, axes, trays := len(m.pins.pickers), len(m.pins.jog), len(m.pins.trays)
	procs := len(m.pins.procs)
	mk := func() inputs {
		return inputs{
			pickerOpen:   make([]bool, pickers),
			pickerClose:  make([]bool, pickers),
			pickerOpened: make([]bool, pickers),
			pickerClosed: make([]bool, pickers),
			trayID:       make([]uint32, trays),
			traySetFull:  make([]bool, trays),
			traySetEmpty: make([]bool, trays),
			procBusy:     make([]bool, procs),
			procReleased: make([]bool, procs),
			jogPos:       make([]bool, axes),
			jogNeg:       make([]bool, axes),
		}
	}
	return &control{
		m:           m,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		in:          mk(),
		prev:        mk(),
		rise:        mk(),
		jogDir:      make([]int, len(m.pins.jog)),
		trayPending: make([]*int64, trays),
	}
}

// take consumes a latched edge: it reports whether the edge was pending and
// clears it, so one press produces exactly one action.
func take(edge *bool) bool {
	if !*edge {
		return false
	}
	*edge = false
	return true
}

func (c *control) start() {
	// Prime the edge detector before the loop goroutine exists: any pin
	// written after start returns is then a real change, while levels already
	// present now are stale boot state (see primeEdges).
	c.primeEdges()
	c.ticker = time.NewTicker(pollInterval)
	go c.run()
}

// shutdown asks the loop to finish and waits for it. Safe to call on a control
// that was never started only via the module's nil check — see Stop.
func (c *control) shutdown() {
	close(c.stop)
	<-c.done
}

func (c *control) run() {
	defer close(c.done)
	defer c.ticker.Stop()

	c.m.logger.Debug("pnptask control loop running", "interval", pollInterval)
	for c.tick() {
		c.step()
	}
	c.park()
}

// tick waits for the next cycle and performs the work every state owes: read
// the inputs, publish the outputs that mirror state, persist what changed. It
// reports false when the module is shutting down — which is why every wait in
// this file goes through it. A wait that polled its own timer would both outlive
// Stop and freeze the teach pins for the seconds it runs.
func (c *control) tick() bool {
	select {
	case <-c.stop:
		return false
	case <-c.ticker.C:
		c.sample()
		c.publish()
		// Persisting from here rather than from step() is what keeps the state
		// of a long action durable: the actions of a job tick through this
		// function, so a slot emptied halfway through a pick is written within
		// one cycle instead of at the end of the job.
		c.m.world.flush()
		return true
	}
}

// sample reads the HAL inputs and the motion status for this cycle and derives
// the rising edges against the previous one.
func (c *control) sample() {
	p := c.m.pins

	// Roll this cycle's reading into the previous one BEFORE overwriting it.
	// A plain `prev := c.in` would not do: the per-picker slices are shared by
	// the copy, so the new reading would overwrite the very values the edge
	// detection is about to compare against.
	c.prev.estop = c.in.estop
	c.prev.machineOn = c.in.machineOn
	c.prev.autoEnable = c.in.autoEnable
	c.prev.home = c.in.home
	c.prev.errorReset = c.in.errorReset
	copy(c.prev.pickerOpen, c.in.pickerOpen)
	copy(c.prev.pickerClose, c.in.pickerClose)
	copy(c.prev.traySetFull, c.in.traySetFull)
	copy(c.prev.traySetEmpty, c.in.traySetEmpty)

	c.in.estop = p.estopOn.Get()
	c.in.machineOn = p.machineOn.Get()
	c.in.autoEnable = p.autoEnable.Get()
	c.in.home = p.home.Get()
	c.in.errorReset = p.errorReset.Get()
	// start-job is read BEFORE the parameters it carries, and that order matters:
	// the PLC writes origin-id, dest-id, process-step and deadzone-select and
	// *then* raises start-job. This loop is not synchronised with whatever writes
	// those pins, so a sample can straddle the PLC's update — and reading the
	// request first means that a request we see high was raised before the
	// parameter reads that follow it, so the job is never latched with the
	// parameters of the previous one.
	c.in.startJob = p.startJob.Get()
	c.in.processStep = p.processStep.Get()
	c.in.originID = p.originID.Get()
	c.in.destID = p.destID.Get()
	c.in.deadzoneSelect = p.deadzoneSelect.Get()
	for i := range p.pickers {
		c.in.pickerOpen[i] = p.pickers[i].manualOpen.Get()
		c.in.pickerClose[i] = p.pickers[i].manualClose.Get()
		c.in.pickerOpened[i] = p.pickers[i].opened.Get()
		c.in.pickerClosed[i] = p.pickers[i].closed.Get()
	}
	for i := range p.trays {
		c.in.trayID[i] = p.trays[i].trayID.Get()
		c.in.traySetFull[i] = p.trays[i].setFull.Get()
		c.in.traySetEmpty[i] = p.trays[i].setEmpty.Get()
	}
	for i := range p.procs {
		c.in.procBusy[i] = p.procs[i].busy.Get()
		c.in.procReleased[i] = p.procs[i].released.Get()
	}
	for i := range p.jog {
		c.in.jogPos[i] = p.jog[i].pos.Get()
		c.in.jogNeg[i] = p.jog[i].neg.Get()
	}
	c.in.jogSpeed = p.jogSpeed.Get()

	// machine-on's first cycle deliberately compares against a zeroed previous
	// reading, so held high at startup it counts as a rising edge: a PLC that
	// holds it high across a stmakd restart is asking for the machine, and
	// waiting for an edge that already happened would leave it off with
	// nothing to show why. The one-shot inputs (home, error-reset, manual
	// open/close, the tray resets) go the other way — see primeEdges.
	//
	// The edges are OR-ed into the latches rather than assigned (see the rise
	// field), and each latch is GATED AT PRESS TIME: sample() runs on every
	// nested tick, so this cycle's inputs are exactly the context the press
	// happened in, and a press that was invalid then must not be remembered
	// into a context where it would fire unrequested. A machine-on edge during
	// estop must not enable the machine at estop clear (the post-estop cycle
	// rule of §6.2); a home press during estop must not home on reset; an
	// error-reset with no fault latched must not pre-clear a fault raised
	// later; and a manual picker press outside manual mode is ignored, not
	// queued (§6.4). What the latch is FOR — a valid press surviving a long
	// job's nested ticks — is untouched: those presses pass their gate.
	c.rise.machineOn = c.rise.machineOn || (c.in.machineOn && !c.prev.machineOn && !c.in.estop)
	c.rise.home = c.rise.home || (c.in.home && !c.prev.home && !c.in.estop)
	c.rise.errorReset = c.rise.errorReset ||
		(c.in.errorReset && !c.prev.errorReset && c.m.pins.errorFlag.Get())
	manualOK := !c.in.estop && !c.in.autoEnable
	for i := range p.pickers {
		c.rise.pickerOpen[i] = c.rise.pickerOpen[i] ||
			(manualOK && c.in.pickerOpen[i] && !c.prev.pickerOpen[i])
		c.rise.pickerClose[i] = c.rise.pickerClose[i] ||
			(manualOK && c.in.pickerClose[i] && !c.prev.pickerClose[i])
	}
	// The tray resets latch a pending VALUE, not a boolean: set-full means "all
	// slots become the process step the operator saw when pressing", so the
	// step is snapshotted at press time — consuming it later with a process
	// step the PLC has meanwhile staged for its next job would mark the whole
	// tray wrongly. Later presses overwrite earlier ones (last wins); only two
	// edges in the SAME sample are a genuinely contradictory pair, ignored like
	// the picker branch ignores its own.
	for i := range p.trays {
		full := c.in.traySetFull[i] && !c.prev.traySetFull[i]
		empty := c.in.traySetEmpty[i] && !c.prev.traySetEmpty[i]
		switch {
		case full && empty:
			c.m.logger.Debug("pnptask: tray set-full and set-empty in the same cycle, ignored",
				"station", c.m.world.trays[i].cfg.ID)
		case full:
			v := int64(c.in.processStep)
			c.trayPending[i] = &v
		case empty:
			v := slotEmpty
			c.trayPending[i] = &v
		}
	}

	if st, err := c.m.ms.GetStatus(); err != nil {
		c.commErrors++
	} else {
		c.commErrors = 0
		c.status = st
	}
	// A failed read keeps serving the last good snapshot for a bounded number
	// of cycles (statusStaleTicks) instead of invalidating it outright: a
	// single split read must not glitch the homed pin low or abort a running
	// jog. Beyond the tolerance the snapshot is stale and everything derived
	// from it turns conservative; commFailureTicks then declares motion gone.
	c.statusOK = c.commErrors <= statusStaleTicks
	if fb, err := c.m.ms.GetPosFb(); err == nil {
		c.posFb = fb
	}
}

// primeEdges seeds the edge detector with the current pin state, so a one-shot
// input already high when the loop starts does NOT fire on the first cycle.
// home, error-reset, the manual picker pins and the tray set-full/set-empty
// pins are requests for an action; a PLC that latches them across a stmakd
// restart is not re-issuing them, and acting on the stale level would home the
// machine, close a picker or wipe a tray's slot state at boot with nobody asking
// (D25/D26/§5.2 specify rising-edge semantics). machine-on is deliberately NOT
// primed — it is the standing request for the machine, and its boot edge is
// wanted (see sample).
//
// tray-id needs no priming: it is a level, and updateStations compares it against
// the geometry the model has actually selected — which world.start has already
// taken from this same pin, so the boot value is by construction not a change.
func (c *control) primeEdges() {
	p := c.m.pins
	c.in.home = p.home.Get()
	c.in.errorReset = p.errorReset.Get()
	for i := range p.pickers {
		c.in.pickerOpen[i] = p.pickers[i].manualOpen.Get()
		c.in.pickerClose[i] = p.pickers[i].manualClose.Get()
	}
	for i := range p.trays {
		c.in.traySetFull[i] = p.trays[i].setFull.Get()
		c.in.traySetEmpty[i] = p.trays[i].setEmpty.Get()
	}
	// start-job is not edge-detected but armed (see jobRequest), and it starts
	// disarmed: a level held high across a restart is not a request for that job.
	c.jobArmed = false
}

// step is one cycle's decisions. Order matters: the machine state is settled
// before anything is published, and manual controls only ever run once the
// machine half of the cycle is done.
func (c *control) step() {
	if c.commErrors >= commFailureTicks {
		c.motionLost()
		return
	}
	if err := c.updateMachine(); err != nil {
		c.raise(err)
	}
	if take(&c.rise.errorReset) {
		c.clearError()
	}

	// The tray state resets are the operator's way to resync the model with the
	// physical world (§6.4), so they are honored in both modes — a tray refilled
	// by hand is exactly the situation manual mode exists for.
	c.updateStations()

	// The homing request is honored in both modes (D25): in auto mode a PLC
	// that wants the machine homed before its first job should not have to
	// drop auto-enable to ask for it, and in manual mode it is the only way to
	// get an AUTOHOME = 0 machine homed enough to jog.
	if take(&c.rise.home) {
		if err := c.runHoming(); err != nil && !errors.Is(err, errStopping) {
			c.raise(err)
		}
	}

	if c.in.autoEnable {
		// Auto mode: the manual controls are inert, and a jog left running by
		// the mode switch must not stay running (§6.4). Presses made IN auto
		// mode were never latched (the gate in sample); the discard here kills
		// the other direction — a press made in manual mode whose latch was
		// still pending when the PLC switched to auto. Either way, a press
		// outside manual mode's reach is ignored, not queued.
		c.stopJogs()
		for i := range c.rise.pickerOpen {
			c.rise.pickerOpen[i], c.rise.pickerClose[i] = false, false
		}
		if c.jobRequest() {
			// The job runs to completion inside this call, ticking this same
			// loop: that is what makes an estop end it wherever it is (§6.5).
			c.runJobHandshake()
		}
		return
	}
	c.manualPickers()
	c.jog()
	if c.jobRequest() {
		// §6.4: in manual mode a job request is refused, latched like any other
		// error, and the handshake is completed so the PLC is not left waiting —
		// the error first, so the cleared start-job it reacts to is already
		// accompanied by the reason.
		c.raise(faultf(errAutoDisabled, "start-job while auto-enable is low"))
		c.m.pins.startJob.Set(false)
	}
}

// updateMachine is the estop/enable sequencing of §6.2.
func (c *control) updateMachine() error {
	if c.in.estop {
		c.enterEstop()
		return nil
	}
	if c.estopped {
		// Leaving estop does NOT re-enable the machine: machine-on has to be
		// cycled. A level that is still high from before the estop is not a
		// request to start moving again — the operator has to ask.
		c.estopped = false
		c.m.logger.Info("pnptask: estop cleared, machine-on must be cycled to enable")
	}

	switch requested := take(&c.rise.machineOn); {
	case requested && !c.enabled:
		err := c.enable()
		var f *pnpFault
		if errors.As(err, &f) && f.id == errMachineOff {
			// machine-on was withdrawn while the enable was in flight: the
			// request is gone, so this is a cancellation, not a fault — the
			// machine simply stays off (see abortCheck's enabling clause).
			c.m.logger.Info("pnptask: machine-on withdrawn during enable, staying off")
			return nil
		}
		return err
	case !c.in.machineOn && c.enabled:
		c.disable("machine-on dropped")
	}
	return nil
}

// enterEstop is the estop teardown. It is idempotent: both the per-cycle check
// and every wait's abort check call it, and only the first one does the work.
func (c *control) enterEstop() {
	if c.estopped {
		return
	}
	c.estopped = true
	c.enabled = false
	// Latches from before the estop die with it: a machine-on, home or manual
	// picker press that was valid when made must not fire into the post-estop
	// world — leaving estop requires fresh requests (§6.2). The latch gating in
	// sample() stops NEW presses during the estop; this stops the ones already
	// pending when it hit. error-reset deliberately survives: clearing a fault
	// is valid in estop, and its gate (a fault was latched) still held.
	c.rise.machineOn = false
	c.rise.home = false
	for i := range c.rise.pickerOpen {
		c.rise.pickerOpen[i], c.rise.pickerClose[i] = false, false
	}
	c.clearJogState()
	_ = c.m.mc.Abort()
	_ = c.m.mc.Disable()
	c.m.pins.machineIsOn.Set(false)
	// D14: estop is the one event that drops held material. Everywhere else
	// the close outputs keep their state so a part stays gripped.
	for i := range c.m.pins.pickers {
		c.m.pins.pickers[i].close.Set(false)
	}
	// The close outputs just went low, so whatever was gripped is on the table:
	// the held-material records (D20) describe a world that no longer exists and
	// would otherwise send the next job to place a part nobody is holding.
	c.m.world.clearAllHeld()
	c.m.logger.Warn("pnptask: estop — motion aborted and disabled, pickers released")
}

// enable brings the machine up: Enable(), then wait for a published status that
// actually reports it. Committing machine-is-on before that would hand the PLC
// a machine that is not yet enabled, and hide an enable motion refused outright.
func (c *control) enable() error {
	if err := c.m.mc.Enable(); err != nil {
		return faultf(errMotionError, "enabling motion: %v", err)
	}
	// enabling arms abortCheck's machine-on clause for the wait below: without
	// it, a machine-on withdrawn during this wait could not cancel the enable
	// (c.enabled is still false), and machine-is-on would pulse true against
	// the withdrawn request.
	c.enabling = true
	defer func() { c.enabling = false }()
	if err := c.waitUntil(errMotionError, motionEnableTimeout, "motion to report enabled",
		func() bool { return c.statusOK && c.status.Enabled != 0 }); err != nil {
		// Stay off and report rather than flapping on and off.
		_ = c.m.mc.Disable()
		c.m.pins.machineIsOn.Set(false)
		return err
	}
	c.enabled = true
	c.m.pins.machineIsOn.Set(true)
	c.m.logger.Info("pnptask: machine on")
	return nil
}

// disable stops the machine on request (machine-on dropped) or on shutdown.
// Motion is aborted before the enable is dropped so the machine decelerates
// under control instead of being cut loose mid-move. The picker outputs are
// deliberately untouched (D14).
func (c *control) disable(reason string) {
	c.stopJogs()
	_ = c.m.mc.Abort()
	_ = c.m.mc.Disable()
	c.enabled = false
	c.m.pins.machineIsOn.Set(false)
	c.m.logger.Info("pnptask: machine off", "reason", reason)
}

// motionLost handles a motion controller that stopped answering. There is
// nothing left to command — Disable would go to the same dead interface — so
// this only publishes the fault and drops machine-is-on, which is what the PLC
// interlocks on.
func (c *control) motionLost() {
	if c.enabled {
		c.enabled = false
		c.clearJogState()
		c.m.pins.machineIsOn.Set(false)
	}
	c.raise(faultf(errMotionError, "motion controller not responding (%d consecutive status reads failed)", c.commErrors))
}

// ---------------------------------------------------------------------------
// Station state (§7.1)
// ---------------------------------------------------------------------------

// updateStations applies the tray station inputs of this cycle: the tray-id
// selector and the two slot-state reset edges (D8).
func (c *control) updateStations() {
	for i, t := range c.m.world.trays {
		// The selector is compared against what the *model* has selected, not
		// against the previous sample: a nested tick would have moved the previous
		// sample on (see the rise field), and the model's id is the honest answer
		// to "is this still the tray we are tracking".
		if c.in.trayID[i] != t.trayID {
			c.m.world.applyTrayID(t, c.in.trayID[i])
		}
		v := c.trayPending[i]
		if v == nil {
			continue
		}
		c.trayPending[i] = nil
		if t.geom == nil {
			// A phantom success would be worse than a refusal: with no
			// geometry there are no slots to set, and persisting the "result"
			// would clobber a record that may still be the only surviving
			// state (set tray-id before the resets).
			c.m.logger.Warn("pnptask: tray reset ignored, station has no geometry",
				"station", t.cfg.ID)
			continue
		}
		t.setAll(*v)
		if *v == slotEmpty {
			c.m.logger.Info("pnptask: tray declared empty",
				"station", t.cfg.ID, "slots", len(t.slots))
		} else {
			c.m.logger.Info("pnptask: tray declared full",
				"station", t.cfg.ID, "process_step", *v, "slots", len(t.slots))
		}
	}
}


// publish writes the outputs that simply mirror state. It runs from tick, so it
// keeps running through a homing wait or a job action — a teach pin that froze
// while the machine moved would be worse than useless.
func (c *control) publish() {
	p := c.m.pins
	p.machineIsOn.Set(c.enabled && !c.estopped)
	p.homed.Set(c.allHomed())
	c.m.world.publish(int64(c.in.processStep))

	// Position teach (D21): where each picker actually is. Targeting commands
	// (x - offset), so the picker sits at (feedback + offset). The plain pins
	// carry mm like every float pin (D23); the -mu pair carries machine units
	// (D26) — the value the INI wants, since its lengths are parsed as machine
	// units. LinearUnits is machine units per mm, so mu = mm * LinearUnits.
	for i := range p.pickers {
		pk := &p.pickers[i]
		x := c.posFb.X + pk.xOffset.Get()
		y := c.posFb.Y + pk.yOffset.Get()
		pk.posX.Set(x)
		pk.posY.Set(y)
		pk.posXMu.Set(x * c.m.cfg.LinearUnits)
		pk.posYMu.Set(y * c.m.cfg.LinearUnits)
	}
}

// ---------------------------------------------------------------------------
// Homing (§6.3)
// ---------------------------------------------------------------------------

// allHomed reports whether every configured joint is homed. The snapshot may
// be up to statusStaleTicks old (a split read must not glitch this — the homed
// pin and the jog gate hang off it); a snapshot staler than that is never
// reported as homed, because this gates motion.
func (c *control) allHomed() bool {
	if !c.statusOK {
		return false
	}
	for j := 0; j < c.m.cfg.NumJoints; j++ {
		if c.status.Joints[j].Homed == 0 {
			return false
		}
	}
	return true
}

func (c *control) anyHoming() bool {
	if !c.statusOK {
		return false
	}
	for j := 0; j < c.m.cfg.NumJoints; j++ {
		if c.status.Joints[j].Homing != 0 {
			return true
		}
	}
	return false
}

// ensureHomed is the job engine's entry point (§6.3): home the machine if it
// needs it and the configuration allows it. AUTOHOME = 0 with unhomed joints is
// NOT_HOMED — a refusal, not a silent home.
func (c *control) ensureHomed() error {
	if c.allHomed() {
		return nil
	}
	if !c.m.cfg.AutoHome {
		return faultf(errNotHomed, "joints are not homed and AUTOHOME is off")
	}
	return c.runHoming()
}

// runHoming runs the whole homing sequence: drop to free mode, ask motmod to
// home everything (it honors HOME_SEQUENCE itself), wait it out, and leave
// motion in coord mode — the resting mode job motion is commanded from.
func (c *control) runHoming() error {
	if c.estopped {
		return faultf(errEstop, "cannot home: estop is active")
	}
	if !c.enabled {
		return faultf(errMachineOff, "cannot home: machine is off")
	}
	c.stopJogs()
	if err := c.setMotionMode(motstat.MOTION_FREE); err != nil {
		return err
	}
	c.m.logger.Info("pnptask: homing all joints")
	if err := c.m.mc.JointHome(-1); err != nil {
		return faultf(errHomingFailed, "commanding home: %v", err)
	}

	// Two ways to be done: everything homed, or the sequence stopped without
	// getting there (a joint faulted, a home switch never made). Watching for
	// the second is what turns a failed home into an error the operator can
	// see now instead of a HOME_TIMEOUT one HOME_TIMEOUT later. "Stopped" must
	// hold for homingSettleTicks consecutive cycles: between HOME_SEQUENCE
	// groups motmod briefly reports no joint homing, and a single snapshot in
	// that handover window is not the end of the sequence.
	started := false
	idle := 0
	timeout := time.Duration(c.m.cfg.HomeTimeout * float64(time.Second))
	err := c.waitUntil(errHomingFailed, timeout, "homing to complete", func() bool {
		switch {
		case !c.statusOK:
			return false // a stale snapshot proves nothing either way
		case c.allHomed():
			return true
		case c.anyHoming():
			started = true
			idle = 0
			return false
		default:
			if !started {
				return false
			}
			idle++
			return idle >= homingSettleTicks
		}
	})
	if err != nil {
		return err
	}
	if !c.allHomed() {
		return faultf(errHomingFailed, "homing sequence ended with joints still unhomed")
	}
	c.m.logger.Info("pnptask: homing complete")
	return c.setMotionMode(motstat.MOTION_COORD)
}

// setMotionMode switches motion between free, teleop and coord and waits for
// the switch to show up in status. The command is re-sent every cycle because
// motion rejects a mode switch while it is not in position; a single send would
// simply be dropped (milltask's guards.go learned the same thing).
func (c *control) setMotionMode(want int32) error {
	deadline := time.Now().Add(motionModeTimeout)
	for {
		if c.statusOK && c.status.MotionState == want {
			return nil
		}
		var err error
		switch want {
		case motstat.MOTION_FREE:
			err = c.m.mc.SetFree()
		case motstat.MOTION_TELEOP:
			err = c.m.mc.SetTeleop()
		case motstat.MOTION_COORD:
			err = c.m.mc.SetCoord()
		default:
			return faultf(errMotionError, "unknown motion mode %d", want)
		}
		if err != nil {
			return faultf(errMotionError, "switching motion to mode %d: %v", want, err)
		}
		if !c.tick() {
			return errStopping
		}
		if err := c.abortCheck(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return faultf(errMotionError, "motion did not switch to mode %d within %v", want, motionModeTimeout)
		}
	}
}

// ---------------------------------------------------------------------------
// Manual mode (§6.4)
// ---------------------------------------------------------------------------

// jog drives the per-axis jog pins. Continuous jog is a latched command in
// motion, so this issues exactly one JogCont when a pin goes high and exactly
// one JogAbort when the reason to jog goes away.
func (c *control) jog() {
	if !c.canJog() {
		c.stopJogs()
		return
	}
	for i := range c.m.pins.jog {
		j := &c.m.pins.jog[i]
		pos, neg := c.in.jogPos[i], c.in.jogNeg[i]
		dir := 0
		switch {
		case pos && !neg:
			dir = 1
		case neg && !pos:
			dir = -1
		}
		// Both pins high is a contradictory request, and it is exactly what a
		// miswired panel produces: treat it as "no jog" rather than picking a
		// direction on the operator's behalf.
		if dir == c.jogDir[i] {
			continue
		}
		if c.jogDir[i] != 0 {
			c.stopJog(i)
		}
		if dir == 0 {
			continue
		}
		vel := c.jogVelocity(j.axis)
		if vel <= 0 {
			// jog-speed unwired or zero: nothing to command. Silent, because
			// this repeats every cycle the button is held.
			continue
		}
		// Axis (teleop) jog needs motion in teleop mode; homing left it in
		// coord, and a fresh enable leaves it in free.
		if err := c.setMotionMode(motstat.MOTION_TELEOP); err != nil {
			if !errors.Is(err, errStopping) {
				c.raise(err)
			}
			return
		}
		if err := c.m.mc.JogCont(j.axis.Index, float64(dir)*vel, 1); err != nil {
			c.raise(faultf(errMotionError, "jogging axis %c: %v", j.axis.Letter, err))
			return
		}
		c.jogDir[i] = dir
	}
}

// canJog is §6.4's gate: manual mode, machine on, no estop, and homed (D18 — an
// unhomed machine has no soft limits to stop the jog at).
func (c *control) canJog() bool {
	return !c.in.autoEnable && c.enabled && !c.estopped && c.allHomed()
}

// jogVelocity is the jog-speed pin clamped to the axis maximum. The pin is a
// request from a potentiometer or a PLC word and is not to be trusted with the
// machine's limits.
func (c *control) jogVelocity(ax Axis) float64 {
	v := math.Abs(c.in.jogSpeed)
	if math.IsNaN(v) {
		return 0
	}
	if max := c.m.limits.AxisMaxVel[ax.Index]; max > 0 && v > max {
		return max
	}
	return v
}

func (c *control) stopJog(i int) {
	c.jogDir[i] = 0
	_ = c.m.mc.JogAbort(c.m.pins.jog[i].axis.Index, 1)
}

// stopJogs aborts every running jog. Called whenever a condition that permits
// jogging is lost — the pins are level-triggered, so nothing else would stop an
// axis that is jogging when the machine leaves manual mode.
func (c *control) stopJogs() {
	for i := range c.jogDir {
		if c.jogDir[i] != 0 {
			c.stopJog(i)
		}
	}
}

// clearJogState forgets the jog state without commanding motion — for the paths
// where motion has already been aborted wholesale (estop, comm loss).
func (c *control) clearJogState() {
	for i := range c.jogDir {
		c.jogDir[i] = 0
	}
}

// manualPickers is the manual open/close control. It works with the machine off
// (picker actuation is typically powered independently, D14) but not in estop,
// where the pickers have just been released on purpose.
func (c *control) manualPickers() {
	if c.estopped {
		return
	}
	for i := range c.m.pins.pickers {
		open, close := take(&c.rise.pickerOpen[i]), take(&c.rise.pickerClose[i])
		switch {
		case open && close:
			// Both edges in one cycle: a contradictory command, ignored.
			c.m.logger.Debug("pnptask: picker manual-open and manual-close in the same cycle, ignored", "picker", i)
		case open:
			c.m.pins.pickers[i].close.Set(false)
			// Whatever the picker held has been let go, so its held-material
			// record goes with it (D20). §8's refinement — retaining the station
			// id so a following manual close can restore the record if material
			// was gripped again — is part of the alternating-picker phase; here
			// an opened picker simply holds nothing.
			c.m.world.clearHeld(i)
		case close:
			c.m.pins.pickers[i].close.Set(true)
		}
	}
}

// ---------------------------------------------------------------------------
// Waiting, aborting, error reporting
// ---------------------------------------------------------------------------

// waitUntil ticks the control loop until cond reports true, the timeout expires
// or an abort condition fires. Every long operation waits through here, so none
// of them can survive an estop, a machine-off or a module shutdown. A
// non-positive timeout waits indefinitely (only an abort ends it).
func (c *control) waitUntil(id errorID, timeout time.Duration, what string, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if timeout > 0 && time.Now().After(deadline) {
			return faultf(id, "timed out after %v waiting for %s", timeout, what)
		}
		if !c.tick() {
			return errStopping
		}
		if err := c.abortCheck(); err != nil {
			return err
		}
	}
}

// abortCheck is the guard every wait runs each cycle. Estop and machine-off are
// the only abort sources (D16), and both tear the machine down here rather than
// leaving it to the next step(): a wait can last seconds, and an estop may not
// wait for one.
func (c *control) abortCheck() error {
	if c.in.estop {
		c.enterEstop()
		return faultf(errEstop, "estop")
	}
	if (c.enabled || c.enabling) && !c.in.machineOn {
		c.disable("machine-on dropped")
		return faultf(errMachineOff, "machine-on dropped")
	}
	if c.commErrors >= commFailureTicks {
		return faultf(errMotionError, "motion controller stopped responding")
	}
	return nil
}

// raise latches a fault on the error pins. The first fault wins: a follow-up
// caused by the first one (an estop's abort failing a wait, say) must not
// overwrite the cause the operator needs to see. error-reset clears the latch.
func (c *control) raise(err error) {
	if err == nil || errors.Is(err, errStopping) {
		return
	}
	fault := &pnpFault{id: errMotionError, msg: err.Error()}
	var f *pnpFault
	if errors.As(err, &f) {
		fault = f
	}
	if c.m.pins.errorFlag.Get() {
		c.m.logger.Debug("pnptask: further fault while one is latched",
			"error_id", uint32(fault.id), "error", fault.id.String(), "detail", fault.msg)
		return
	}
	c.m.pins.errorID.Set(uint32(fault.id))
	c.m.pins.errorFlag.Set(true)
	c.m.logger.Error("pnptask: fault",
		"error_id", uint32(fault.id), "error", fault.id.String(), "detail", fault.msg)
}

// clearError is the error-reset edge (D11). It also clears the per-picker
// missing flags, which are the same kind of latched "something went wrong"
// state and would otherwise need a second, non-existent reset.
func (c *control) clearError() {
	c.m.pins.errorFlag.Set(false)
	c.m.pins.errorID.Set(uint32(errNone))
	for i := range c.m.pins.pickers {
		c.m.pins.pickers[i].missing.Set(false)
	}
	c.m.logger.Debug("pnptask: error latch cleared")
}

// park is the loop's last act. A jog left running would keep the machine moving
// with nobody left to stop it, so motion is aborted and disabled — but the
// picker outputs are left alone (D14): whatever is held stays held.
func (c *control) park() {
	c.stopJogs()
	_ = c.m.mc.Abort()
	_ = c.m.mc.Disable()
	c.m.pins.machineIsOn.Set(false)
	c.m.pins.busy.Set(false)
	c.m.logger.Debug("pnptask control loop stopped")
}
