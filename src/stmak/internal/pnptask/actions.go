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
func (c *control) waitPickerOpen(pk int) error {
	c.m.pins.pickers[pk].close.Set(false)
	return c.waitUntil(errPlaceFailed, settleTimeout(c.m.pins.pickSettleTime.Get()),
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

// busyGate keeps the head out of a busy process station's area: it waits at the
// station's wait position if one is configured, or where it stands at movement
// height if not.
//
// busy is passed in rather than read here because §7.4 specifies *when* it is
// sampled — at job start for a pick-from-proc, after the pick leg for a
// place-to-proc. The release/released handshake remains the authoritative
// synchronisation; this only saves travel time and keeps the head clear.
//
// auto-enable going low aborts the wait (D15): the operator wants the machine to
// hand over to manual, and a job parked over a station forever is not a handover.
// The picker keeps holding its material for exactly that manual handling.
func (c *control) busyGate(j *job, pk int, s *procState, busy bool) error {
	if !busy {
		return nil
	}
	if err := c.retract(j.height); err != nil {
		return err
	}
	if s.cfg.HasWait {
		if err := c.travel(j, pk, s.cfg.Wait); err != nil {
			return err
		}
	}
	c.m.logger.Info("pnptask: waiting for a busy station",
		"station", s.cfg.ID, "at_wait_position", s.cfg.HasWait)
	for {
		if !c.in.procBusy[s.idx] {
			c.m.logger.Info("pnptask: station cleared", "station", s.cfg.ID)
			return nil
		}
		if !c.in.autoEnable {
			return faultf(errWaitAborted,
				"station %d: the wait was aborted by auto-enable going low", s.cfg.ID)
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
	if err := c.busyGate(j, pk, s, j.originBusy); err != nil {
		return err
	}
	// Whatever goes wrong from here on, the fixture must not stay commanded
	// open (D19: a station left open is a station whose own process runs
	// unclamped). Fire-and-forget — an errored action may be estopped, and
	// waiting for feedback belongs to the success path alone.
	defer func() {
		if err != nil {
			c.requestRelease(s, false)
		}
	}()
	return c.removeFromProc(j, pk, s, false)
}

// removeFromProc is the shared body of §7.4's "pick from proc": grip the part,
// then have the fixture unclamp before lifting. It serves both the job's own
// pick and §8's swap, which differ only in what happens to the release request
// afterwards — see the swap parameter — and in the busy gating, which is the
// caller's business (the swap's destination was already gated by placeToProc).
//
// The caller owns the error-path release withdrawal (D19), because on the swap
// path the request outlives this function.
func (c *control) removeFromProc(j *job, pk int, s *procState, swap bool) error {
	if err := c.approach(j, pk, s.cfg.Pos, s.cfg.ZPick+s.pins.zOffset.Get()); err != nil {
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
func (c *control) placeToTray(j *job, pk int) error {
	t := j.dest.tray
	slot, ok := t.freeSlot()
	if !ok {
		return faultf(errTrayFull, "station %d has no free slot", t.cfg.ID)
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
	if err := c.busyGate(j, pk, s, c.in.procBusy[s.idx]); err != nil {
		return err
	}
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
	if s.hasMaterial {
		if err := c.swapOut(j, pk, s); err != nil {
			return err
		}
	}
	c.requestRelease(s, true)
	if err := c.retract(j.height); err != nil {
		return err
	}
	if err := c.travel(j, pk, s.cfg.Pos); err != nil {
		return err
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

// swapOut empties an occupied destination station with the free picker so the
// placer can put the job's material in (§8).
//
// The removed part stays in that picker and is recorded as swap-removed, which
// is what makes the *next* job's origin mandatory: the station is about to run
// its process on the piece that replaced it, so the picker is the only place
// the removed part can be until a job carries it away (see validateJob).
//
// Which picker does the removing is asked here rather than assigned by the
// caller (D20): the placer is holding the job's material, so it is not free,
// and any other free one will do. Validation counted them at job start; this
// re-asks because that is where the answer is used.
func (c *control) swapOut(j *job, holder int, s *procState) error {
	pk, ok := c.m.world.freePicker()
	if !ok {
		return faultf(errNoFreePicker,
			"station %d is occupied and no picker is free to take the part out", s.cfg.ID)
	}
	c.m.logger.Info("pnptask: swapping the occupant out of a station",
		"station", s.cfg.ID, "picker", pk, "placer", holder)
	// The busy gating already happened in placeToProc — this is an approach to
	// the same station in the same job.
	return c.removeFromProc(j, pk, s, true)
}
