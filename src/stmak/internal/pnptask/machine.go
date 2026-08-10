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
}

// motionStatus is the motstat subset this module reads.
type motionStatus interface {
	GetStatus() (motstat.MotionStatus, error)
	GetPosFb() (motstat.Pose, error)
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

	pickerOpen  []bool
	pickerClose []bool

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

	// This cycle's inputs, the previous cycle's (for edge detection) and the
	// rising edges between them.
	in   inputs
	prev inputs
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

	// Jog state per configured axis: which way each axis is currently being
	// jogged (0 = not jogging), so a held pin does not re-issue JogCont every
	// cycle and a released one aborts exactly once.
	jogDir []int
}

func newControl(m *pnptaskModule) *control {
	pickers, axes := len(m.pins.pickers), len(m.pins.jog)
	mk := func() inputs {
		return inputs{
			pickerOpen:  make([]bool, pickers),
			pickerClose: make([]bool, pickers),
			jogPos:      make([]bool, axes),
			jogNeg:      make([]bool, axes),
		}
	}
	return &control{
		m:      m,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		in:     mk(),
		prev:   mk(),
		rise:   mk(),
		jogDir: make([]int, len(m.pins.jog)),
	}
}

func (c *control) start() {
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
// the inputs, publish the outputs that mirror state. It reports false when the
// module is shutting down — which is why every wait in this file goes through
// it. A wait that polled its own timer would both outlive Stop and freeze the
// teach pins for the seconds it runs.
func (c *control) tick() bool {
	select {
	case <-c.stop:
		return false
	case <-c.ticker.C:
		c.sample()
		c.publish()
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

	c.in.estop = p.estopOn.Get()
	c.in.machineOn = p.machineOn.Get()
	c.in.autoEnable = p.autoEnable.Get()
	c.in.home = p.home.Get()
	c.in.errorReset = p.errorReset.Get()
	for i := range p.pickers {
		c.in.pickerOpen[i] = p.pickers[i].manualOpen.Get()
		c.in.pickerClose[i] = p.pickers[i].manualClose.Get()
	}
	for i := range p.jog {
		c.in.jogPos[i] = p.jog[i].pos.Get()
		c.in.jogNeg[i] = p.jog[i].neg.Get()
	}
	c.in.jogSpeed = p.jogSpeed.Get()

	// The first cycle compares against a zeroed previous reading, so an input
	// already high at startup counts as a rising edge. That is deliberate for
	// machine-on: a PLC that holds it high across a stmakd restart is asking
	// for the machine, and waiting for an edge that already happened would
	// leave it off with nothing to show why.
	c.rise.machineOn = c.in.machineOn && !c.prev.machineOn
	c.rise.home = c.in.home && !c.prev.home
	c.rise.errorReset = c.in.errorReset && !c.prev.errorReset
	for i := range p.pickers {
		c.rise.pickerOpen[i] = c.in.pickerOpen[i] && !c.prev.pickerOpen[i]
		c.rise.pickerClose[i] = c.in.pickerClose[i] && !c.prev.pickerClose[i]
	}

	if st, err := c.m.ms.GetStatus(); err != nil {
		c.commErrors++
		c.statusOK = false
	} else {
		c.commErrors = 0
		c.statusOK = true
		c.status = st
	}
	if fb, err := c.m.ms.GetPosFb(); err == nil {
		c.posFb = fb
	}
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
	if c.rise.errorReset {
		c.clearError()
	}

	// The homing request is honored in both modes (D25): in auto mode a PLC
	// that wants the machine homed before its first job should not have to
	// drop auto-enable to ask for it, and in manual mode it is the only way to
	// get an AUTOHOME = 0 machine homed enough to jog.
	if c.rise.home {
		if err := c.runHoming(); err != nil && !errors.Is(err, errStopping) {
			c.raise(err)
		}
	}

	if c.in.autoEnable {
		// Auto mode: the manual controls are inert, and a jog left running by
		// the mode switch must not stay running (§6.4).
		c.stopJogs()
		return
	}
	c.manualPickers()
	c.jog()
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

	switch {
	case c.rise.machineOn && !c.enabled:
		return c.enable()
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
	c.clearJogState()
	_ = c.m.mc.Abort()
	_ = c.m.mc.Disable()
	c.m.pins.machineIsOn.Set(false)
	// D14: estop is the one event that drops held material. Everywhere else
	// the close outputs keep their state so a part stays gripped.
	for i := range c.m.pins.pickers {
		c.m.pins.pickers[i].close.Set(false)
	}
	c.m.logger.Warn("pnptask: estop — motion aborted and disabled, pickers released")
}

// enable brings the machine up: Enable(), then wait for a published status that
// actually reports it. Committing machine-is-on before that would hand the PLC
// a machine that is not yet enabled, and hide an enable motion refused outright.
func (c *control) enable() error {
	if err := c.m.mc.Enable(); err != nil {
		return faultf(errMotionError, "enabling motion: %v", err)
	}
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

// publish writes the outputs that simply mirror state. It runs from tick, so it
// keeps running through a homing wait or (later) a job action — a teach pin
// that froze while the machine moved would be worse than useless.
func (c *control) publish() {
	p := c.m.pins
	p.machineIsOn.Set(c.enabled && !c.estopped)
	p.homed.Set(c.allHomed())

	// Position teach (D21): where each picker actually is, in machine
	// coordinates. Targeting commands (x - offset), so the picker sits at
	// (feedback + offset).
	for i := range p.pickers {
		pk := &p.pickers[i]
		pk.posX.Set(c.posFb.X + pk.xOffset.Get())
		pk.posY.Set(c.posFb.Y + pk.yOffset.Get())
	}
}

// ---------------------------------------------------------------------------
// Homing (§6.3)
// ---------------------------------------------------------------------------

// allHomed reports whether every configured joint is homed. A stale status
// (the read failed this cycle) is never reported as homed: this gates motion.
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
	// see now instead of a HOME_TIMEOUT one HOME_TIMEOUT later.
	started := false
	timeout := time.Duration(c.m.cfg.HomeTimeout * float64(time.Second))
	err := c.waitUntil(errHomingFailed, timeout, "homing to complete", func() bool {
		switch {
		case c.allHomed():
			return true
		case c.anyHoming():
			started = true
			return false
		default:
			return started
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
		open, close := c.rise.pickerOpen[i], c.rise.pickerClose[i]
		switch {
		case open && close:
			// Both edges in one cycle: a contradictory command, ignored.
			c.m.logger.Debug("pnptask: picker manual-open and manual-close in the same cycle, ignored", "picker", i)
		case open:
			c.m.pins.pickers[i].close.Set(false)
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
	if c.enabled && !c.in.machineOn {
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
	c.m.logger.Debug("pnptask control loop stopped")
}
