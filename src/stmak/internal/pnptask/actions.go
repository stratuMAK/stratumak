// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
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

// openForPick makes sure a picker the engine counts free really is open before
// it is sent down onto material (pre-merge review 2026-09-08). The record says
// the picker holds nothing; it says nothing about the close output, and a
// close left standing — a manual close that gripped nothing leaves one on
// purpose, so the picker stays as the operator put it — turns closeAndCheck
// into a lie: jaws that are already shut read
// "closed" whatever lies under the head, which is an empty verdict on a
// populated slot and, at MAX_UNPOPULATED's default of 1, a whole tray declared
// empty. Only a standing close is acted on: the output is dropped and the
// picker waited for, and one that will not open is PICKER_OPEN_FAILED before
// any descent. A picker whose close output is already low is left to
// closeAndCheck's own judgement, as before.
func (c *control) openForPick(pk int) error {
	if !c.m.pins.pickers[pk].close.Get() {
		return nil
	}
	c.m.logger.Warn("pnptask: a picker counted free was still commanded closed, opening it before the head goes down",
		"picker", pk)
	// Down in a tray (D38) the jaws would open next to the parts: up first.
	if err := c.liftFromLow(); err != nil {
		return err
	}
	return c.waitPickerOpen(pk)
}

// openFreePickers is openForPick for every picker the engine counts free,
// before the job's first descent. The other picker goes down with the head
// on every stroke, not only on its own pick, so a standing close on it -- a
// manual close that gripped nothing leaves one on purpose (§8) -- would put
// closed jaws down over a populated slot, meeting the part instead of
// straddling it. A present record is a grip and is left alone; a retained
// one never gets here (validateJob refuses the job, §8.1).
func (c *control) openFreePickers() error {
	for i := range c.m.pins.pickers {
		if c.m.world.held[i].present {
			continue
		}
		if err := c.openForPick(i); err != nil {
			return err
		}
	}
	return nil
}

// liftFromLow takes the head up from where a stay-near place left it low on
// a tray (D38) before a gripper is actuated down there: to the tray's
// NEAR_HEIGHT, which the next travel then keeps (travelZ), or to MOVE_HEIGHT
// if the tray has none. Nothing to do when the head is not low.
func (c *control) liftFromLow() error {
	if c.lowAt == 0 {
		return nil
	}
	st := c.m.world.station(c.lowAt)
	if st == nil || !c.isLowAt(st) {
		c.lowAt = 0
		return nil
	}
	c.lowAt = 0
	near, ok := st.near()
	if !ok {
		near = c.m.cfg.MoveHeight
	}
	if err := c.zStroke(near); err != nil {
		return err
	}
	if ok {
		c.nearAt = st.id
	}
	return nil
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
func (c *control) approach(j *job, pk int, st *station, target pnproute.Point, z float64) error {
	// D38: down at this tray from the last job's place, and the target is
	// within LOW_MOVE_MAX: shift there at this height, no lift.
	if shifted, err := c.lowShift(pk, st, target); err != nil {
		return err
	} else if shifted {
		if err := c.zStroke(z); err != nil {
			return err
		}
		return c.dwell(c.m.pins.posSettleTime.Get())
	}
	j.travel = c.travelZ(j, st)
	if err := c.retract(j.travel); err != nil {
		return err
	}
	if err := c.travel(j, pk, target, down(z, st.zCornerRadius())); err != nil {
		return err
	}
	return c.dwell(c.m.pins.posSettleTime.Get())
}

// ---------------------------------------------------------------------------
// Busy gating (D15)
// ---------------------------------------------------------------------------

// gatedTravel keeps the head out of a busy process station's area on the way
// in: it brings picker pk to the station and returns with the machine drained
// there, having waited out the station's busy input on the way. The leg in
// ends in d (at the movement height for noDescent).
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
// There are two shapes of wait, and which one applies is per station:
//
//   - WAIT_X/WAIT_Y (D15): one leg to a fixed park spot, wait there, then the
//     leg in — where travel can be overlapped with the station's process.
//   - neither: wait where it stands, at movement height, then the leg in.
//
// Either way the head waits standing, between legs. (D29's derived wait point,
// a leg left running while the station was polled, is gone with D46; a scene
// that changes while the head waits is the scene gate's business, D45.)
//
// onClear, if given, runs the moment the station is known to be clear — at
// once for a station that is not busy, otherwise the instant busy drops and
// before the leg into the station is dispatched. It is how place-to-proc
// raises the fixture's release in §7.4's order (busy gating, THEN release):
// the request has to overlap with the drive in, so that waitReleased on
// arrival finds the fixture already open, but it must not reach a fixture
// whose process is still running, which is what raising it before the gate
// did (pre-merge review 2026-09-08).
func (c *control) gatedTravel(j *job, pk int, s *procState, busy bool, onClear func(), d descent) error {
	j.travel = c.travelZ(j, c.m.world.station(s.cfg.ID))
	if err := c.retract(j.travel); err != nil {
		return err
	}
	if busy {
		if s.cfg.HasWait {
			if err := c.travel(j, pk, s.cfg.Wait, noDescent); err != nil {
				return err
			}
		}
		if err := c.awaitClear(s, s.cfg.HasWait); err != nil {
			return err
		}
	}
	if onClear != nil {
		onClear()
	}
	return c.travel(j, pk, s.cfg.Pos, d)
}

// awaitClear holds, standing, until a process station's busy input goes low.
//
// auto-enable going low aborts the wait (D15): the operator wants the machine to
// hand over to manual, and a job parked over a station forever is not a handover.
// wait-abort does the same in auto mode (D33). The picker keeps holding its
// material for exactly that manual handling.
func (c *control) awaitClear(s *procState, atWait bool) error {
	c.m.logger.Info("pnptask: waiting for a busy station",
		"station", s.cfg.ID, "at_wait_position", atWait)
	return c.waitStanding(func() bool { return !c.in.procBusy[s.idx] },
		fmt.Sprintf("station %d", s.cfg.ID), func() {
			c.m.logger.Info("pnptask: station cleared", "station", s.cfg.ID)
		})
}

// waitStanding is a wait between legs, standing: it returns once done reports
// true, or fails with WAIT_ABORTED when auto-enable drops (D15) or wait-abort
// is raised (D33) -- the station gate's wait and the scene gate's (D45) end
// the same way. what names the wait in the error; cleared, if given, runs once
// done is seen.
func (c *control) waitStanding(done func() bool, what string, cleared func()) error {
	for {
		if done() {
			if cleared != nil {
				cleared()
			}
			return nil
		}
		if !c.in.autoEnable {
			return faultf(errWaitAborted, "%s: the wait was aborted by auto-enable going low", what)
		}
		// D33: the same end, asked for without leaving auto mode -- so the
		// PLC can send the machine somewhere else (park) right after.
		if c.in.waitAbort {
			return faultf(errWaitAborted, "%s: the wait was aborted by wait-abort", what)
		}
		// The head waits up, not down at the station it came from: a lift
		// deferred for the next travel (D52) runs now.
		if err := c.flushLift(); err != nil {
			return err
		}
		if !c.tick() {
			return errStopping
		}
		if err := c.abortCheck(); err != nil {
			return err
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
	if err := c.openForPick(pk); err != nil {
		return err
	}
	from := 0
	for {
		slot, next, ok := t.nextPick(j.step, from)
		if !ok {
			picker.missing.Set(true)
			return faultf(errTrayEmpty, "station %d: no slot holding step-%d material left",
				t.cfg.ID, j.step)
		}
		from = next

		if err := c.approach(j, pk, j.origin, t.slotPos(slot), t.cfg.ZPick+t.pins.zOffset.Get()); err != nil {
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
			// A tray-to-itself job places on this same tray next (D37).
			return c.up(j, j.origin, j.destID == j.originID)
		}

		// Nothing there. Correct the model, open up, lift, and go on.
		t.markEmpty(slot)
		c.m.logger.Info("pnptask: tray slot was empty",
			"station", t.cfg.ID, "slot", slot, "successive_misses", t.misses)
		if err := c.waitPickerOpen(pk); err != nil {
			return err
		}
		// The next candidate is on this same tray (D37).
		if err := c.up(j, j.origin, !t.probedEmpty); err != nil {
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
	if err := c.openForPick(pk); err != nil {
		return err
	}
	st := c.m.world.station(s.cfg.ID)
	if err := c.gatedTravel(j, pk, s, busy, nil, down(s.cfg.ZPick+s.pins.zOffset.Get(), st.zCornerRadius())); err != nil {
		return err
	}
	if err := c.dwell(c.m.pins.posSettleTime.Get()); err != nil {
		return err
	}
	// D36: the fixture lets go first, the picker closes after. A picker
	// closing on a part the fixture still clamps forces any position mismatch
	// through the portal; closing on a free part, it centres it instead.
	// RELEASE_SETTLE covers the jaws still moving after the opened contact.
	//
	// The fixture is open from here on with nothing holding the part: any
	// failure below leaves the caller to withdraw the release (D19), which
	// clamps the part again where it stands.
	if err := c.setRelease(s, true); err != nil {
		return err
	}
	if err := c.dwell(s.pins.releaseSettle.Get()); err != nil {
		return err
	}
	grip, err := c.closeAndCheck(pk)
	if err != nil {
		return err
	}
	if grip == gripEmpty && s.hasMaterial {
		// D43: the model says a part is there, and the closed sensor says the
		// jaws met. The model wins: the picker stays closed and the part is
		// taken as gripped. A closed sensor that fires on a gripped part is
		// the failure this guards against -- opening the picker on that
		// verdict dropped the part back into the fixture, and the swap's place
		// went down onto it. Trusting the model when the fixture really is
		// empty costs a part that is not there travelling on, for an
		// inspection further down to reject; the other way round cost a crash.
		c.m.logger.Warn("pnptask: picker reports fully closed on a station the model says is occupied, kept as gripped -- check the closed sensor",
			"station", s.cfg.ID, "picker", pk)
		grip = gripHolding
	}
	if grip == gripEmpty {
		// Only reachable on a probing swap: every other removal is validated
		// on has-material, and D43 takes an empty grip over a set one as held.
		if err := c.waitPickerOpen(pk); err != nil {
			return err
		}
		// D32: a probing swap asked whether anything is there, and nothing is
		// an answer. The place goes on, the fixture already open; no record,
		// so no swap obligation.
		if swap && s.cfg.ProbeSwap {
			c.m.logger.Info("pnptask: swap found the station empty", "station", s.cfg.ID, "picker", pk)
			return errSwapEmpty
		}
		return faultf(errProcNoMaterial,
			"station %d: the fixture is empty although has-material was set", s.cfg.ID)
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
	// On a swap the placer goes in next, at this same station (D37).
	if err := c.up(j, c.m.world.station(s.cfg.ID), swap); err != nil {
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
	if err := c.approach(j, pk, j.dest, t.slotPos(slot), t.cfg.ZPick+t.pins.zOffset.Get()+t.cfg.ZPlaceOffset); err != nil {
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
	// D38: the next job picks on this tray; with LOW_MOVE_MAX it may start
	// right here, so the head is not lifted at all.
	if j.stayNear && t.cfg.LowMoveMax > 0 {
		c.lowAt = j.destID
		c.lowPos = c.cmdPos
		return nil
	}
	return c.up(j, j.dest, j.stayNear)
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
	if s.hasMaterial || j.probe {
		rm, ok := c.m.world.freePicker()
		if !ok {
			return faultf(errNoFreePicker,
				"station %d is occupied and no picker is free to take the part out", s.cfg.ID)
		}
		// errSwapEmpty (D32): nothing came out, and the fixture was never
		// asked to let go of anything -- the request below is its first.
		if err := c.swapOut(j, pk, rm, s, busy); err != nil && !errors.Is(err, errSwapEmpty) {
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
		j.travel = c.travelZ(j, j.dest)
		if err := c.retract(j.travel); err != nil {
			return err
		}
		if err := c.travel(j, pk, s.cfg.Pos, noDescent); err != nil {
			return err
		}
	} else {
		// The release is raised by the gate, once the station is clear (§7.4:
		// busy gating, then release): a fixture whose process is still running
		// must not be told to unclamp, and every error path below withdraws
		// the request for exactly that reason.
		// The descent waits for the fixture's released below, so it is a
		// stroke of its own, not part of the leg (D52).
		if err := c.gatedTravel(j, pk, s, busy, func() { c.requestRelease(s, true) }, noDescent); err != nil {
			return err
		}
	}
	if err := c.waitReleased(s, true); err != nil {
		return err
	}
	if err := c.zStroke(s.cfg.ZPick + s.pins.zOffset.Get() + s.cfg.ZPlaceOffset); err != nil {
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
	// D36: the fixture clamps before the picker lifts away: a part left
	// standing loose in an open fixture tips over as the picker leaves it.
	// Confirmed (D19), then up.
	if err := c.setRelease(s, false); err != nil {
		return err
	}
	return c.up(j, j.dest, j.stayNear)
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

// ---------------------------------------------------------------------------
// Near height (D37)
// ---------------------------------------------------------------------------

// travelZ is the height a travel to station st runs at: its NEAR_HEIGHT if
// the head was lifted to exactly that at this station and is still there,
// the job's movement height otherwise. Anything else in between -- another
// station, a park, a jog -- has taken the head up or forgotten it (nearAt).
func (c *control) travelZ(j *job, st *station) float64 {
	near, ok := st.near()
	if ok && c.isLowAt(st) {
		return near // down at it: up to its near height, not further
	}
	if ok && c.nearAt == st.id && math.Abs(c.effectiveZ()-near) < moveFuzz {
		return near
	}
	return j.height
}

// up lifts out of a stroke at station st: to its NEAR_HEIGHT when the next
// move stays at st and it has one, to the movement height otherwise. With a
// Z corner radius at st (D52) the lift is only noted (pendingLift): the next
// travel queues it with the leg, joined by a corner arc, and anything that
// needs the machine standing before that runs it on its own (flushLift).
func (c *control) up(j *job, st *station, stay bool) error {
	to := j.height
	near, ok := st.near()
	if stay && ok {
		to = near
		c.nearAt = st.id
	}
	if r := st.zCornerRadius(); r > 0 && c.cmdPos.Z < to-moveFuzz {
		c.lift = pendingLift{on: true, to: to, r: r}
		return nil
	}
	return c.zStroke(to)
}

// ---------------------------------------------------------------------------
// Low move (D38)
// ---------------------------------------------------------------------------

// isLowAt: the head is still where a stay-near place on tray st left it.
func (c *control) isLowAt(st *station) bool {
	return c.lowAt != 0 && c.lowAt == st.id && poseNear(c.cmdPos, c.lowPos)
}

func poseNear(a, b motctl.Pose) bool {
	return math.Abs(a.X-b.X) < moveFuzz && math.Abs(a.Y-b.Y) < moveFuzz && math.Abs(a.Z-b.Z) < moveFuzz
}

// lowShift moves picker pk onto target at the current height if the head is
// down at tray st (isLowAt) and the target is within its LOW_MOVE_MAX:
// both grippers are down in the tray, open -- one around the part it has
// just set down, the other over the next one -- so the shift has to stay
// inside the jaws' clearance, which is what LOW_MOVE_MAX says. Reports
// whether it did; false leaves the head as it is for the normal approach.
func (c *control) lowShift(pk int, st *station, target pnproute.Point) (bool, error) {
	if !st.isTray() || st.tray.cfg.LowMoveMax <= 0 || !c.isLowAt(st) {
		return false, nil
	}
	off := c.pickerOffset(pk)
	to := c.cmdPos
	to.X, to.Y = target.X-off.X, target.Y-off.Y
	if math.Hypot(to.X-c.cmdPos.X, to.Y-c.cmdPos.Y) > st.tray.cfg.LowMoveMax {
		// too far: the normal approach, up to the tray's near height first
		// (travelZ still sees the head down at it)
		return false, nil
	}
	c.lowAt = 0
	c.m.logger.Debug("pnptask: low move on the tray", "station", st.id, "picker", pk,
		"dx", to.X-c.cmdPos.X, "dy", to.Y-c.cmdPos.Y)
	if err := c.setMotionMode(motstat.MOTION_COORD); err != nil {
		return false, err
	}
	if err := c.waitMotionDone(); err != nil {
		return false, err
	}
	if err := c.m.mc.SetTermCond(tpTermCondStop, 0); err != nil {
		return false, faultf(errMotionError, "setting the exact-stop termination condition: %v", err)
	}
	if err := c.line(to, c.m.cfg.ZVel, c.m.cfg.ZAcc); err != nil {
		return false, err
	}
	return true, c.waitMotionDone()
}
