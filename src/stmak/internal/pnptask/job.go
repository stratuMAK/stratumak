// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"
	"slices"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// job is one latched job command plus the state its action sequences share.
//
// Everything the PLC can change is captured at the start-job edge (§7.4): a job
// that re-read origin-id halfway through would be a job whose destination changed
// under it. What is deliberately *not* latched is the tuning params and the
// z-offset pins — those are corrections to the machine, and a correction made
// while a job runs should apply to it.
type job struct {
	// The latched request.
	step     int64
	originID uint32
	destID   uint32
	deadzone uint32

	// Resolved from the request.
	origin  *station
	dest    *station
	planner *pnproute.Planner
	height  float64

	// originBusy is a pick-from-proc origin's busy state as sampled at job start
	// (§7.4, R1). The dest's is sampled later, once the pick leg is done.
	originBusy bool

	// The §8 plan, decided in validation so a job is refused before it moves
	// rather than in the middle of a sequence.
	//
	// skipPick says a picker already holds material from the origin — the flow
	// of §8 steps 3 and 4, where the part was taken out by the previous job's
	// swap — so there is nothing to pick; runPick re-asks holderOf for which
	// picker that is, at the moment the answer is used (D20). Otherwise the
	// holder is decided by the pick itself, from whichever picker is free.
	//
	// swap says the destination process station is occupied and the occupant has
	// to come out first, with a second picker.
	skipPick bool
	swap     bool

	// pickedSlot is the tray slot the job's own pick emptied, −1 until a tray
	// pick succeeds. A tray-to-itself place excludes it (see placeToTray).
	pickedSlot int

	// planMax is the longest route plan this job has run, published on the
	// plan-time pin (D13). Kept on the job because that is the span it
	// describes — one job's worst case, not the module's since startup.
	planMax time.Duration
}

// jobRequest is the start-job handshake (§7.4, D12: one job at a time, no
// queueing). start-job is a level the PLC raises and the module clears, and it
// has to be seen low again before it counts as a new request:
//
//   - a level already high when the loop starts is not a request (D26) — a PLC
//     that latched it across a stmakd restart is not asking for that job again;
//   - a pin linked to a signal the PLC keeps driving high reads high again on the
//     cycle after the module cleared it, and that is still the same request.
//
// An external clear *during* a job is ignored simply by not being looked at
// (D16): the job runs inside the control loop and this is only reached when the
// module is idle.
func (c *control) jobRequest() bool {
	if !c.in.startJob {
		c.jobArmed = true
		return false
	}
	if !c.jobArmed {
		return false
	}
	c.jobArmed = false
	return true
}

// runJobHandshake is one whole job: run it, publish its outcome, and complete the
// handshake whichever way it went.
func (c *control) runJobHandshake() {
	if c.m.pins.errorFlag.Get() {
		// Faults latch first-error-wins (§6.5), so a job started with the latch
		// set could fail with nothing to show for it — its error id would be
		// swallowed by the one already there. Refuse instead, and complete the
		// handshake: start-job cleared with error still high says exactly what
		// happened, and error-reset is what the PLC owes before the next job.
		c.m.logger.Warn("pnptask: start-job refused, an error is still latched",
			"error_id", c.m.pins.errorID.Get())
		c.m.pins.startJob.Set(false)
		return
	}

	c.m.pins.busy.Set(true)
	err := c.runJob()

	// The diagnosis is published *before* the handshake completes: busy going low
	// with start-job cleared is what tells the PLC to look, and it must not find
	// the error pins still describing the previous job.
	switch {
	case err == nil:
		c.m.logger.Info("pnptask: job complete",
			"origin", c.m.pins.originID.Get(), "dest", c.m.pins.destID.Get())
	case errors.Is(err, errStopping):
		// The module is shutting down mid-job. Not a machine fault: the pins are
		// cleared so a restart does not inherit a job that is no longer running,
		// but nothing is latched (raise ignores it anyway).
		c.m.logger.Warn("pnptask: job abandoned, module stopping")
	default:
		c.raise(err)
	}

	c.m.pins.busy.Set(false)
	c.m.pins.startJob.Set(false)
}

// runJob is the job state machine of §7.4:
// LATCH -> VALIDATE -> [HOME] -> PLAN/EXECUTE -> FINISH.
//
// Homing comes after validation, not before: a job with an unknown station id is
// refused without first moving the machine, which is what an operator watching
// a mis-configured PLC expects to see. Planning is not a phase of its own — each
// leg plans its own route just before it runs (§7.4), because the wait leg of a
// busy-gated job is not known until the station has been sampled.
func (c *control) runJob() error {
	j := c.latchJob()
	c.m.logger.Info("pnptask: job start",
		"origin", j.originID, "dest", j.destID, "process_step", j.step,
		"deadzone_select", j.deadzone)

	// A held record restored from persistence must be verified against the
	// gripper feedback before anything trusts it. The countdown normally runs
	// in step(), but a start-job arriving inside the settle window reaches this
	// point first — and validation below would read the unverified record,
	// skipPick on a part that fell out during the downtime, and place a phantom.
	if err := c.settleRestoredHeld(); err != nil {
		return err
	}
	if err := c.validateJob(j); err != nil {
		return err
	}
	// The movement height is only known once the stations resolve, so it is
	// logged here rather than with the request.
	c.m.logger.Debug("pnptask: job accepted",
		"origin", j.originID, "dest", j.destID, "move_height", j.height,
		"origin_is_tray", j.origin.isTray(), "dest_is_tray", j.dest.isTray())
	if err := c.ensureHomed(); err != nil {
		return err
	}
	if err := c.seedCmdPos(); err != nil {
		return err
	}
	holder, err := c.runPick(j)
	if err != nil {
		return err
	}
	return c.runPlace(j, holder)
}

// latchJob captures the request pins. The values come from this cycle's input
// snapshot, like every other decision in the loop (§6.5) — and that snapshot read
// start-job before the parameters it carries, so they belong to this request and
// not to the previous one (see sample).
func (c *control) latchJob() *job {
	// plan-time describes one job (see pins.go), so the previous job's worst
	// case goes with the previous job.
	c.m.pins.planTime.Set(0)
	return &job{
		step:       int64(c.in.processStep),
		originID:   c.in.originID,
		destID:     c.in.destID,
		deadzone:   c.in.deadzoneSelect,
		pickedSlot: -1,
	}
}

// validateJob is §7.4's VALIDATE: the ids exist, the dead-zone selector is in
// range, and the stations are in a state that can serve the job. Every failure
// here happens before the machine moves.
func (c *control) validateJob(j *job) error {
	// The machine has to be there at all. Both of these are abort conditions
	// during the job as well (abortCheck), but a job started against a machine
	// that is off would otherwise fail at its first mode switch, reported as
	// MOTION_ERROR instead of as the plain truth.
	if c.estopped {
		return faultf(errEstop, "cannot start a job: estop is active")
	}
	if !c.enabled {
		return faultf(errMachineOff, "cannot start a job: machine is off")
	}

	j.origin = c.m.world.station(j.originID)
	if j.origin == nil {
		return faultf(errInvalidOrigin, "origin-id %d: no such station", j.originID)
	}
	j.dest = c.m.world.station(j.destID)
	if j.dest == nil {
		return faultf(errInvalidDest, "dest-id %d: no such station", j.destID)
	}

	planner, err := c.m.planners.at(j.deadzone)
	if err != nil {
		return err
	}
	j.planner = planner
	j.height = c.moveHeight(j.originID, j.destID)

	// §8.1: a retained record is a manual intervention in progress — the part
	// is out of the picker in an operator's hands, and the next manual close
	// is what decides where it went. No job runs against a world in that
	// limbo: the picker is not free, the material is not placeable, and the
	// swap obligation may be attached to it.
	if picker, station, ok := c.m.world.retainedPicker(); ok {
		return faultf(errNoFreePicker,
			"picker %d is mid manual handling (material from station %d was let go and not re-judged); close the picker — on the part to restore it, empty to clear it — before the next job",
			picker, station)
	}

	// §8's sequence constraint. Material a swap took out of a process station is
	// homeless: the station is running its process on the piece that replaced
	// it, and the picker holding it is the only place it can be. The next job
	// therefore has to be one that carries it away. Normally a single swap
	// record exists; a place that failed after its swap-out leaves two (both
	// parts really are in pickers, each with its obligation), and a job from
	// either station is the way back.
	if stations := c.m.world.swapStations(); len(stations) > 0 && !slices.Contains(stations, j.originID) {
		return faultf(errAltPickerSeq,
			"pickers hold material removed from station(s) %v; the next job must originate at one of them, not at %d",
			stations, j.originID)
	}

	// §8's pick phase: a picker already holding the origin's material makes the
	// physical pick unnecessary, and that picker is the one that will place.
	// The material has to BE what the job asks for, though: the record matches
	// by station, and where its process step is known (any pick the PLC
	// commanded; a swap's removed occupant is unknown) a mismatching request
	// is refused — matching by station alone would deliver a step-0 part as
	// step-3 material with no error and a corrupted tray model behind it.
	if pk, ok := c.m.world.holderOf(j.originID); ok {
		j.skipPick = true
		if h := c.m.world.held[pk]; h.stepKnown && h.step != j.step {
			return faultf(errInvalidOrigin,
				"picker %d holds step-%d material from station %d, the job asks for step %d",
				pk, h.step, j.originID, j.step)
		}
	}

	if err := c.checkOrigin(j); err != nil {
		return err
	}
	if err := c.checkDest(j); err != nil {
		return err
	}
	return c.checkPickers(j)
}

// checkPickers is §8's free-picker requirement, counted rather than asked as a
// yes/no: a physical pick needs one free picker and a swap needs another, and a
// job that finds that out at the swap would discover it with a part in the air.
// Which picker takes which role is decided at the moment it is needed (D20) —
// any free one will do.
//
// A well-formed job sequence can never hit this; what it guards against is
// manual intervention that left a picker loaded.
func (c *control) checkPickers(j *job) error {
	need := 0
	if !j.skipPick {
		need++
	}
	if j.swap {
		need++
	}
	if free := c.m.world.freeCount(); free < need {
		return faultf(errNoFreePicker,
			"job %d -> %d needs %d free picker(s), %d of %d are free",
			j.originID, j.destID, need, free, len(c.m.world.held))
	}
	return nil
}

// checkOrigin is the pick-side precondition: something to pick has to be there.
// A job whose pick is skipped has no origin preconditions at all — it never
// goes near the station, and the material it carries is already in a picker.
func (c *control) checkOrigin(j *job) error {
	if j.skipPick {
		return nil
	}
	if j.origin.isTray() {
		t := j.origin.tray
		if t.geom == nil {
			return faultf(errInvalidTrayID, "origin station %d: tray-id %d matches no TRAYDEF",
				t.cfg.ID, t.trayID)
		}
		if t.emptyFor(j.step) {
			return faultf(errTrayEmpty, "origin station %d: no slot holds step-%d material",
				t.cfg.ID, j.step)
		}
		return nil
	}
	s := j.origin.proc
	if !s.hasMaterial {
		return faultf(errProcNoMaterial, "origin station %d holds no material", s.cfg.ID)
	}
	// Sampled here, at job start, which is where §7.4 (R1) puts the gating for a
	// pick-from-proc: normally the PLC only commands the job once the station is
	// done, and the gating exists for the case where it does not.
	j.originBusy = c.in.procBusy[s.idx]
	return nil
}

// checkDest is the place-side precondition: somewhere to put it.
//
// The check is a snapshot taken before the pick, which matters for the one job
// shape where the two interact: a tray-to-itself job on a completely full tray is
// refused with TRAY_FULL even though its own pick would free a slot.
func (c *control) checkDest(j *job) error {
	if j.dest.isTray() {
		t := j.dest.tray
		if t.geom == nil {
			return faultf(errInvalidTrayID, "destination station %d: tray-id %d matches no TRAYDEF",
				t.cfg.ID, t.trayID)
		}
		if t.full() {
			return faultf(errTrayFull, "destination station %d has no free slot", t.cfg.ID)
		}
		return nil
	}
	s := j.dest.proc
	if !s.hasMaterial {
		return nil
	}
	// A job from a process station to itself takes the occupant out on the way
	// in, so by the time the place happens the station is free — there is
	// nothing to swap out. This is DELIBERATELY allowed (phase-7 review): it
	// is the re-seat operation — pick the part out, put it back down — the one
	// way a PLC can re-clamp a part without a second station. A PLC bug
	// looping it cycles one part in place, which is wasteful but loses
	// nothing; the skipPick variant below is refused because it additionally
	// exchanges two parts and re-runs processes on finished material.
	if !j.skipPick && j.originID == j.destID {
		return nil
	}
	// The skipPick variant of the same pair is a different animal: the job's
	// material already came OUT of this station (a previous swap), and the
	// station is occupied by the piece that replaced it. Running it would be a
	// self-exchange — pull the occupant, put the old part back, re-arm the
	// sequence constraint — which no flow in §8 describes; a repeating PLC bug
	// would ping-pong the same two parts and re-run the process on an
	// already-processed piece forever. Refused like every other mis-sequence.
	if j.skipPick && j.originID == j.destID {
		return faultf(errProcHasMaterial,
			"station %d is occupied and the job's material was removed from it; putting it back is only valid once the station is free",
			s.cfg.ID)
	}
	// Single-picker semantics (§7.4): with one picker an occupied process
	// station has to be emptied by a job of its own first, because the only
	// picker there is already carries this job's material.
	if len(c.m.world.held) < 2 {
		return faultf(errProcHasMaterial, "destination station %d already holds material", s.cfg.ID)
	}
	// Two pickers: this is §8's swap. The free picker takes the occupant out and
	// keeps it, the placer puts the job's material in, and the *next* job has to
	// be the one that carries the removed part away (see the sequence constraint
	// in validateJob).
	j.swap = true
	return nil
}

// moveHeight is the travel height for this station pair: the global MOVE_HEIGHT
// unless a [PNPTASK_ROUTE_x] override names the pair (§7.3).
func (c *control) moveHeight(origin, dest uint32) float64 {
	for _, r := range c.m.cfg.Routes {
		if r.Origin == origin && r.Dest == dest {
			return r.MoveHeight
		}
	}
	return c.m.cfg.MoveHeight
}

// runPick performs the job's pick and returns the picker that came away
// holding the material — the one that will place (D20). Roles are not fixed:
// with one picker the answer never varies, but the question is asked where the
// design asks it.
//
// §8's pick phase: when a picker already holds material from the origin — the
// part a previous job's swap took out of that station — there is nothing to
// pick, and that picker is the placer.
func (c *control) runPick(j *job) (int, error) {
	if j.skipPick {
		// Re-asked here rather than cached in the job (D20's "where the
		// answer is used"): a cached picker number would read as a plausible
		// picker 0 on any path that forgot to check skipPick first.
		pk, ok := c.m.world.holderOf(j.originID)
		if !ok {
			// Validation saw a holder and it is gone — only an intervention
			// between validate and here could do that; reported, not assumed.
			return 0, faultf(errNoFreePicker,
				"no picker holds the material of station %d anymore", j.originID)
		}
		c.m.logger.Info("pnptask: pick skipped, a picker already holds the origin's material",
			"station", j.originID, "picker", pk)
		return pk, nil
	}
	// Validation already established that one is free; re-asking is how the
	// picker is chosen rather than assumed (D20, "picker 0 preferred when both
	// are free").
	pk, ok := c.m.world.freePicker()
	if !ok {
		return 0, faultf(errNoFreePicker, "no picker is free to pick at station %d", j.originID)
	}
	var err error
	if j.origin.isTray() {
		err = c.pickFromTray(j, pk)
	} else {
		err = c.pickFromProc(j, pk)
	}
	if err != nil {
		return 0, err
	}
	return pk, nil
}

// runPlace performs the job's place, with the picker holding the job's material
// (D20) and that picker's own offsets.
func (c *control) runPlace(j *job, holder int) error {
	if j.dest.isTray() {
		return c.placeToTray(j, holder)
	}
	return c.placeToProc(j, holder)
}
