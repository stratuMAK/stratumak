// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// The four action sequences of §7.4. Each one is written as the design document
// spells it out, top to bottom, and every wait inside them ticks the control loop
// (§6.5) so an estop or a machine-off ends the action wherever it is.
//
// None of them takes "the picker" for granted: the picker index is a parameter,
// its offsets are applied inside travel, and its feedback is read out of this
// cycle's input snapshot at that index. That is what D20 asks for — with one
// picker the index never varies, with two the same code serves either role.

// gripResult is what a picker's feedback says after a close command has settled.
type gripResult int

const (
	// gripHolding: the picker closed onto something and stopped short of fully
	// closed — it holds material.
	gripHolding gripResult = iota
	// gripEmpty: the picker closed all the way, so there was nothing to grip
	// (§5.2: closed after a pick means gripped nothing).
	gripEmpty
)

// closeAndCheck closes a picker and reads what it found. A picker that still
// reports opened after the settle time never moved, which is a fault rather than
// an empty position.
func (c *control) closeAndCheck(pk int) (gripResult, error) {
	c.m.pins.pickers[pk].close.Set(true)
	if err := c.dwell(c.m.pins.pickSettleTime.Get()); err != nil {
		return gripEmpty, err
	}
	if c.in.pickerOpened[pk] {
		return gripEmpty, faultf(errPickerCloseFail,
			"picker %d: opened is still high after the pick settle time", pk)
	}
	if c.in.pickerClosed[pk] {
		return gripEmpty, nil
	}
	return gripHolding, nil
}

// openAndCheck opens a picker and confirms it. On the place side "opened stayed
// low" means the material is still in the picker, which is PLACE_FAILED — the job
// cannot lift away from a part it did not let go of.
func (c *control) openAndCheck(pk int) error {
	c.m.pins.pickers[pk].close.Set(false)
	if err := c.dwell(c.m.pins.pickSettleTime.Get()); err != nil {
		return err
	}
	if !c.in.pickerOpened[pk] {
		return faultf(errPlaceFailed, "picker %d: opened stayed low after the release", pk)
	}
	return nil
}

// waitPickerOpen waits for a picker commanded open to report it, bounded by the
// pick settle time (§7.4). It is the probing retry's exit: a picker that will not
// open cannot be sent down onto the next candidate slot.
//
// The failure is PICKER_OPEN_FAILED, not PLACE_FAILED: both callers are on the
// pick side (the probing retry, the empty-fixture recovery), where "opened
// stayed low" is a jammed gripper at the origin — a PLC keyed on the place-side
// id would send the operator to inspect the wrong station.
func (c *control) waitPickerOpen(pk int) error {
	c.m.pins.pickers[pk].close.Set(false)
	return c.waitUntil(errPickerOpenFail, settleTimeout(c.m.pins.pickSettleTime.Get()),
		fmt.Sprintf("picker %d to open", pk), func() bool { return c.in.pickerOpened[pk] })
}

// ---------------------------------------------------------------------------
// Fixture release handshake (D19)
// ---------------------------------------------------------------------------

// requestRelease drives a process station's release output. The wait is separate
// (waitReleased) because place-to-proc asks the fixture to open *before* it
// travels there, so the fixture opens while the head is on its way.
func (c *control) requestRelease(s *procState, want bool) {
	s.pins.release.Set(want)
}

// waitReleased waits for a fixture's released feedback to follow the request.
// Both directions are waited out (D19): at the end of an action release goes low
// and the fixture has to confirm it is holding again before the job is done —
// otherwise the next job would arrive at a station that is still open.
//
// RELEASE_TIMEOUT of 0 means "wait forever", as the INI documents; the default is
// 5 s, because a timeout that defaults to forever turns a stuck fixture into a
// hung job with nothing on the error pin.
func (c *control) waitReleased(s *procState, want bool) error {
	timeout := time.Duration(c.m.cfg.ReleaseTimeout * float64(time.Second))
	what := fmt.Sprintf("station %d released to go %s", s.cfg.ID, highLow(want))
	return c.waitUntil(errReleaseTimeout, timeout, what,
		func() bool { return c.in.procReleased[s.idx] == want })
}

// setRelease is request + wait, for the paths that have nothing to overlap.
func (c *control) setRelease(s *procState, want bool) error {
	c.requestRelease(s, want)
	return c.waitReleased(s, want)
}

func highLow(v bool) string {
	if v {
		return "high"
	}
	return "low"
}

// approach is §7.4's shared approach phrase — "retract; route XY; Z down;
// pos-settle" — written once so a change to the recipe (an extra abort
// re-check, a settle rule) cannot silently miss one of the sequences.
// placeToProc keeps its own interleaved shape: it opens the fixture before
// travelling and waits for it between travel and descent.
//
// z is evaluated by the caller at the call, which is where the z-offset pin
// belongs: the offset is a correction (a height sensor, a tray shim) applied
// to the approach that is about to happen, not latched with the job.
func (c *control) approach(j *job, pk int, target pnproute.Point, z float64) error {
	if err := c.retract(j.height); err != nil {
		return err
	}
	if err := c.travel(j, pk, target); err != nil {
		return err
	}
	if err := c.zStroke(z); err != nil {
		return err
	}
	return c.dwell(c.m.pins.posSettleTime.Get())
}

// ---------------------------------------------------------------------------
// Busy gating (D15)
// ---------------------------------------------------------------------------

// gatedTravel keeps the head out of a busy process station's area on the way
// in: it brings picker pk to the station and returns with the machine drained
// at movement height, having waited out the station's busy input on the way.
//
// busy is passed in rather than read here because §7.4 specifies *when* it is
// sampled — at job start for a pick-from-proc, after the pick leg for a
// place-to-proc. The release/released handshake remains the authoritative
// synchronisation; this only saves travel time and keeps the head clear.
//
// Which picker matters: the leg ends at the station point minus THAT picker's
// offset, so the caller has to name the picker that is actually going in. On a
// swap that is the picker taking the occupant out, not the one carrying the
// job's material — see placeToProc.
//
// There are three shapes of wait, and which one applies is per station:
//
//   - WAIT_DEADZONE (D29): two legs. The first runs to the derived wait point
//     and is left running while busy is polled; the second is queued into the
//     trajectory planner the instant it clears, so the two blend and the head
//     does not stop. If it never clears in time the queue simply runs dry at
//     the wait point, which is the stop this is trying to avoid and the thing
//     wait-stops counts.
//   - WAIT_X/WAIT_Y (D15): one leg to a fixed park spot, wait there, then the
//     leg in. Right where the gate cannot flip mid-approach — an M-code gate on
//     a cut that takes minutes — and committing to the park spot costs nothing.
//   - neither: wait where it stands, at movement height.
//
// A station that is not busy is driven to in one continuous leg whatever its
// wait configuration, which for a WAIT_DEADZONE station is already the outcome
// the two legs exist to produce — there is nothing to wait for, so there is
// nothing to split for. Splitting anyway would also break the two cases where
// the approach legitimately BEGINS inside the nominated zone: the second job at
// the same station, and the placer after a swap. What the scene check below
// preserves is the diagnosis, which is the part worth having.
func (c *control) gatedTravel(j *job, pk int, s *procState, busy bool) error {
	if err := c.retract(j.height); err != nil {
		return err
	}
	if s.cfg.HasWaitZone {
		if busy {
			return c.streamedApproach(j, pk, s)
		}
		if err := c.checkClearScene(s); err != nil {
			return err
		}
		return c.travel(j, pk, s.cfg.Pos)
	}
	if busy {
		if s.cfg.HasWait {
			if err := c.travel(j, pk, s.cfg.Wait); err != nil {
				return err
			}
		}
		if _, err := c.awaitClear(s, s.cfg.HasWait); err != nil {
			return err
		}
	}
	return c.travel(j, pk, s.cfg.Pos)
}

// streamedApproach is the two-leg approach of D29.
//
// Leg 1 is planned against the scene of the moment, like every other leg —
// the derived wait point only says where to stop, not how to get there, and
// planning the drive to it normally is what guarantees it stays out of every
// zone the current drawing has, not just the nominated one.
//
// Leg 2 is dispatched without draining in between. Three outcomes follow from
// the trajectory planner with no arithmetic here: queued before the braking
// ramp begins, the two legs blend and nothing slows down; queued during the
// ramp, the planner re-accelerates and the head dips rather than stops; never
// queued, the queue runs dry exactly at the wait point.
func (c *control) streamedApproach(j *job, pk int, s *procState) error {
	wp, err := c.waitPoint(j, pk, s)
	if err != nil {
		return err
	}
	if err := c.dispatchLeadingLeg(j, pk, wp); err != nil {
		return err
	}
	stopped, err := c.awaitClear(s, true)
	if err != nil {
		return err
	}
	if err := c.checkClearScene(s); err != nil {
		return err
	}
	if stopped {
		// The queue ran dry before the station cleared: the head really stopped
		// at the wait point. Counted rather than assumed — see pins.go.
		c.m.pins.waitStops.Set(c.m.pins.waitStops.Get() + 1)
		c.m.logger.Info("pnptask: the approach stopped at the wait position",
			"station", s.cfg.ID, "picker", pk)
	}
	if err := c.dispatchTravel(j, pk, s.cfg.Pos); err != nil {
		return err
	}
	return c.waitMotionDone()
}

// checkClearScene refuses to drive into a WAIT_DEADZONE station that reads
// clear while deadzone-select still names the drawing it is enclosed in.
//
// The station lives inside a zone of one drawing and is reachable in another,
// and WAIT_CLEAR_DEADZONE says which. A station reporting done in any other
// scene is a PLC sequencing bug — it said it was finished before the enclosure
// it sits in was released — and it gets its own id rather than surfacing as
// whatever planning against the wrong drawing happens to do, which for the
// blocked drawing is a PLANNING_FAILED that sends the operator looking for an
// obstructed route.
func (c *control) checkClearScene(s *procState) error {
	if c.in.deadzoneSelect == s.cfg.WaitClearDeadzone {
		return nil
	}
	return faultf(errWaitSceneMismatch,
		"station %d is clear with deadzone-select %d, but it is only reachable in dead-zone file %d (WAIT_CLEAR_DEADZONE)",
		s.cfg.ID, c.in.deadzoneSelect, s.cfg.WaitClearDeadzone)
}

// awaitClear holds until a process station's busy input goes low, and reports
// whether the machine came to a standstill before it did.
//
// auto-enable going low aborts the wait (D15): the operator wants the machine to
// hand over to manual, and a job parked over a station forever is not a handover.
// The picker keeps holding its material for exactly that manual handling. On the
// streamed approach the abort can now land while the first leg is still running;
// the job ends either way and the job-abort path stops motion.
//
// stopped is what the streamed approach measures itself by: the wait normally
// begins with a leg still running, and whether the queue ran dry before the
// station cleared is the difference between a continuous drive in and a dead
// stop at the wait point. The other two shapes of wait always begin at a
// standstill and ignore it.
//
// The drain is only believed once the status has had time to catch up with what
// was dispatched before the wait. Motion status is published by the servo
// thread, so the first cycles here still describe the machine standing still —
// the same stale-inpos race waitMotionDone skips past — and reading them would
// report a stop at the wait point on every single approach. Only cycles whose
// status read succeeded count, so a burst of comm errors lengthens the settle
// rather than shortening it.
func (c *control) awaitClear(s *procState, atWait bool) (stopped bool, err error) {
	c.m.logger.Info("pnptask: waiting for a busy station",
		"station", s.cfg.ID, "at_wait_position", atWait)
	settled := 0
	for {
		if settled >= dispatchSettleTicks && c.motionDrained() {
			stopped = true
		}
		if !c.in.procBusy[s.idx] {
			c.m.logger.Info("pnptask: station cleared", "station", s.cfg.ID)
			return stopped, nil
		}
		if !c.in.autoEnable {
			return stopped, faultf(errWaitAborted,
				"station %d: the wait was aborted by auto-enable going low", s.cfg.ID)
		}
		if !c.tick() {
			return stopped, errStopping
		}
		if err := c.abortCheck(); err != nil {
			return stopped, err
		}
		if c.commErrors == 0 {
			settled++
		}
	}
}

// ---------------------------------------------------------------------------
// pick from tray (§7.4)
// ---------------------------------------------------------------------------

// pickFromTray searches the tray for material at the job's process step and picks
// the first candidate that turns out to actually be there.
//
// The tracked slot state decides *which* slots to try; the picker feedback only
// validates and corrects it (D9). A candidate that grips nothing is marked empty
// and the search continues after it, and MAX_UNPOPULATED successive misses
// declare the tray empty — the point of the counter being that a tray really is
// finished at some point, and driving over all forty slots to find that out is
// forty wasted approaches.
func (c *control) pickFromTray(j *job, pk int) error {
	t := j.origin.tray
	picker := &c.m.pins.pickers[pk]
	from := 0
	for {
		slot, next, ok := t.nextPick(j.step, from)
		if !ok {
			picker.missing.Set(true)
			return faultf(errTrayEmpty, "station %d: no slot holding step-%d material left",
				t.cfg.ID, j.step)
		}
		from = next

		if err := c.approach(j, pk, t.slotPos(slot), t.cfg.ZPick+t.pins.zOffset.Get()); err != nil {
			return err
		}
		grip, err := c.closeAndCheck(pk)
		if err != nil {
			return err
		}
		if grip == gripHolding {
			picker.missing.Set(false)
			t.markPicked(slot)
			j.pickedSlot = slot
			c.m.world.setHeld(pk, j.origin.id, false, j.step, true)
			c.m.logger.Debug("pnptask: picked from tray",
				"station", t.cfg.ID, "slot", slot, "picker", pk)
			return c.zStroke(j.height)
		}

		// Nothing there. Correct the model, open up, lift, and go on.
		t.markEmpty(slot)
		c.m.logger.Info("pnptask: tray slot was empty",
			"station", t.cfg.ID, "slot", slot, "successive_misses", t.misses)
		if err := c.waitPickerOpen(pk); err != nil {
			return err
		}
		if err := c.zStroke(j.height); err != nil {
			return err
		}
		if t.probedEmpty {
			picker.missing.Set(true)
			return faultf(errTrayEmpty,
				"station %d: %d successive picks found nothing (MAX_UNPOPULATED = %d)",
				t.cfg.ID, t.misses, t.geom.def.MaxUnpopulated)
		}
	}
}

// ---------------------------------------------------------------------------
// pick from proc (§7.4)
// ---------------------------------------------------------------------------

// pickFromProc takes the material out of a process station as the job's pick:
// busy-gated (§7.4, R1), and with the fixture clamped again on the way out.
func (c *control) pickFromProc(j *job, pk int) (err error) {
	s := j.origin.proc
	// Whatever goes wrong from here on, the fixture must not stay commanded
	// open (D19: a station left open is a station whose own process runs
	// unclamped). Fire-and-forget — an errored action may be estopped, and
	// waiting for feedback belongs to the success path alone.
	defer func() {
		if err != nil {
			c.requestRelease(s, false)
		}
	}()
	return c.removeFromProc(j, pk, s, false, j.originBusy)
}

// removeFromProc is the shared body of §7.4's "pick from proc": grip the part,
// then have the fixture unclamp before lifting. It serves both the job's own
// pick and §8's swap, which differ only in what happens to the release request
// afterwards — see the swap parameter.
//
// The approach is gated here rather than by the caller: the gate ends at the
// station with THIS picker's offset applied (D29's second leg drives all the
// way in), so it cannot be separated from the travel it belongs to.
//
// The caller owns the error-path release withdrawal (D19), because on the swap
// path the request outlives this function.
func (c *control) removeFromProc(j *job, pk int, s *procState, swap, busy bool) error {
	if err := c.gatedTravel(j, pk, s, busy); err != nil {
		return err
	}
	if err := c.zStroke(s.cfg.ZPick + s.pins.zOffset.Get()); err != nil {
		return err
	}
	if err := c.dwell(c.m.pins.posSettleTime.Get()); err != nil {
		return err
	}
	grip, err := c.closeAndCheck(pk)
	if err != nil {
		return err
	}
	if grip == gripEmpty {
		// has-material said there was a part and there is not. Correcting the
		// flag is the point: the next job must not be sent for it again.
		s.setHasMaterial(false)
		if err := c.waitPickerOpen(pk); err != nil {
			return err
		}
		return faultf(errProcNoMaterial,
			"station %d: the fixture is empty although has-material was set", s.cfg.ID)
	}

	if err := c.setRelease(s, true); err != nil {
		// The picker is closed around a part the fixture never confirmed
		// releasing: the part belongs to the fixture, so the picker lets go
		// (best effort — no feedback wait on an error path) and has-material
		// stays true. Leaving the picker clamped while the world called it
		// free would send the next job's retract tearing the part out of a
		// closed fixture.
		c.m.pins.pickers[pk].close.Set(false)
		return err
	}
	// The material is in the picker and out of the fixture's hands: both records
	// move together, before the lift, so an abort during the retract leaves a
	// world that says where the part is. has-material drops even on the swap
	// path, where the place is about to set it again: between the two the
	// station really is empty, and an estop landing in that window has to leave
	// a model that says so.
	s.setHasMaterial(false)
	// The job's own pick takes the material the PLC asked for, so its step is
	// the latched one; a swap removes whatever occupied the station, whose
	// step the model never tracked — unknown, and exempt from the skipPick
	// step check.
	c.m.world.setHeld(pk, s.cfg.ID, swap, j.step, !swap)
	if err := c.zStroke(j.height); err != nil {
		return err
	}
	if swap {
		// The fixture stays commanded open (§8: "no re-clamp in between"). The
		// placer is on its way to the nest the removed part has just left, and
		// a clamp cycling shut on an empty nest and open again is both wasted
		// time and one more chance for the fixture to fail.
		return nil
	}
	return c.setRelease(s, false)
}

// ---------------------------------------------------------------------------
// place to tray (§7.4)
// ---------------------------------------------------------------------------

// placeToTray puts the held material into the first free slot.
//
// A tray-to-itself job must not use the slot its own pick just emptied: putting
// the part back where it came from completes "successfully" having moved
// nothing, and a PLC compacting a tray with such jobs would loop on that no-op
// forever. The exclusion cannot starve the place — the pick only empties a slot
// that held material, so the free slot checkDest saw is necessarily a different
// one; the TRAY_FULL below is a defensive branch, not a reachable outcome of a
// validated job.
func (c *control) placeToTray(j *job, pk int) error {
	t := j.dest.tray
	exclude := -1
	if j.originID == j.destID {
		exclude = j.pickedSlot
	}
	slot, ok := t.freeSlot(exclude)
	if !ok {
		return faultf(errTrayFull, "station %d has no free slot besides the one this job emptied", t.cfg.ID)
	}
	if err := c.approach(j, pk, t.slotPos(slot), t.cfg.ZPick+t.pins.zOffset.Get()); err != nil {
		return err
	}
	if err := c.openAndCheck(pk); err != nil {
		return err
	}
	// The records move the moment "opened" confirms — that is the physical
	// commit point: an open picker cannot take the part back, so an abort
	// during the settle dwell below must find a world that already says where
	// the part is. Committing after the dwell lost the part from the model
	// exactly when an estop hit mid-dwell, and the next job stacked a second
	// part onto the invisible one.
	t.markPlaced(slot, j.step)
	c.m.world.clearHeld(pk)
	c.m.logger.Debug("pnptask: placed into tray",
		"station", t.cfg.ID, "slot", slot, "picker", pk, "process_step", j.step)
	// The dwell is before the retract: the part has to have settled into the
	// slot, not still be leaning on the picker.
	if err := c.dwell(c.m.pins.releaseTime.Get()); err != nil {
		return err
	}
	return c.zStroke(j.height)
}

// ---------------------------------------------------------------------------
// place to proc (§7.4)
// ---------------------------------------------------------------------------

// placeToProc puts the held material into a process station.
//
// The fixture is asked to open *before* the travel and only waited for on
// arrival: the head must not come down onto a clamp that is still closed, and
// there is no reason to stand still while the fixture opens.
func (c *control) placeToProc(j *job, pk int) (err error) {
	s := j.dest.proc
	// Sampled now, with the pick leg complete, which is where §7.4 puts the
	// gating for a place-to-proc. The gate covers the swap below as well: that
	// is an approach to this same station, and keeping the head out of a station
	// that is still working is exactly what the gating is for.
	busy := c.in.procBusy[s.idx]
	// See removeFromProc: no error path may leave the fixture commanded open —
	// including the swap's, whose release request outlives the removal.
	defer func() {
		if err != nil {
			c.requestRelease(s, false)
		}
	}()
	// §8's swap: the station is occupied, so its occupant comes out with the
	// free picker before this job's material goes in. The release the removal
	// raised is deliberately left standing — it is what the place below would
	// ask for anyway, so its waitReleased finds the fixture already open.
	//
	// Which picker does the removing is settled HERE rather than inside swapOut
	// (where D20 used to re-ask it) because the gate needs it: a gated approach
	// ends at the station with the approaching picker's offset applied, and the
	// remover is the one that goes in first. Asking afterwards would have driven
	// the placer into the station and then moved over by the offset difference.
	if s.hasMaterial {
		rm, ok := c.m.world.freePicker()
		if !ok {
			return faultf(errNoFreePicker,
				"station %d is occupied and no picker is free to take the part out", s.cfg.ID)
		}
		if err := c.swapOut(j, pk, rm, s, busy); err != nil {
			return err
		}
		c.requestRelease(s, true)
		// Not gated, and deliberately so: the removal's approach already served
		// the gate — same station, same job, and the station cannot go busy
		// again while this job holds its fixture open — and it left the head AT
		// the station. All the placer has to do is shift from the remover's
		// offset to its own. Sending it back through the gate would ask for a
		// wait point derived from a route that starts inside the very zone the
		// wait point is supposed to keep it out of.
		if err := c.retract(j.height); err != nil {
			return err
		}
		if err := c.travel(j, pk, s.cfg.Pos); err != nil {
			return err
		}
	} else {
		c.requestRelease(s, true)
		if err := c.gatedTravel(j, pk, s, busy); err != nil {
			return err
		}
	}
	if err := c.waitReleased(s, true); err != nil {
		return err
	}
	if err := c.zStroke(s.cfg.ZPick + s.pins.zOffset.Get()); err != nil {
		return err
	}
	if err := c.dwell(c.m.pins.posSettleTime.Get()); err != nil {
		return err
	}
	if err := c.openAndCheck(pk); err != nil {
		return err
	}
	// Records at the "opened" confirmation, before the dwell — the physical
	// commit point; see placeToTray.
	s.setHasMaterial(true)
	c.m.world.clearHeld(pk)
	c.m.logger.Debug("pnptask: placed into station", "station", s.cfg.ID, "picker", pk)
	if err := c.dwell(c.m.pins.releaseTime.Get()); err != nil {
		return err
	}
	if err := c.zStroke(j.height); err != nil {
		return err
	}
	// Clamp again and confirm it (D19) — the station now holds the part.
	return c.setRelease(s, false)
}

// ---------------------------------------------------------------------------
// The swap (§8)
// ---------------------------------------------------------------------------

// swapOut empties an occupied destination station with picker pk so the placer
// can put the job's material in (§8).
//
// The removed part stays in that picker and is recorded as swap-removed, which
// is what makes the *next* job's origin mandatory: the station is about to run
// its process on the piece that replaced it, so the picker is the only place
// the removed part can be until a job carries it away (see validateJob).
//
// pk comes from the caller because the caller needs it too — it is the picker
// the gated approach below is planned for. Validation counted the free pickers
// at job start; placeToProc is where the answer is picked and used.
func (c *control) swapOut(j *job, holder, pk int, s *procState, busy bool) error {
	c.m.logger.Info("pnptask: swapping the occupant out of a station",
		"station", s.cfg.ID, "picker", pk, "placer", holder)
	// This removal's approach carries the busy gating for the whole job's
	// business at this station: it is the first thing that goes in.
	return c.removeFromProc(j, pk, s, true, busy)
}
