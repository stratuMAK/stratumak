// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"time"
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

		if err := c.retract(j.height); err != nil {
			return err
		}
		if err := c.travel(j, pk, t.slotPos(slot)); err != nil {
			return err
		}
		// The pick height is read here, not latched with the job: the z-offset pin
		// is a correction (a height sensor, a tray shim), and it belongs to the
		// approach that is about to happen.
		if err := c.zStroke(t.cfg.ZPick + t.pins.zOffset.Get()); err != nil {
			return err
		}
		if err := c.dwell(c.m.pins.posSettleTime.Get()); err != nil {
			return err
		}
		grip, err := c.closeAndCheck(pk)
		if err != nil {
			return err
		}
		if grip == gripHolding {
			picker.missing.Set(false)
			t.markPicked(slot)
			c.m.world.setHeld(pk, j.origin.id)
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

// pickFromProc takes the material out of a process station: grip it, then have
// the fixture unclamp before lifting, and confirm the fixture is holding again on
// the way out (D19).
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
	if err := c.retract(j.height); err != nil {
		return err
	}
	if err := c.travel(j, pk, s.cfg.Pos); err != nil {
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
	// world that says where the part is.
	s.setHasMaterial(false)
	c.m.world.setHeld(pk, j.origin.id)
	if err := c.zStroke(j.height); err != nil {
		return err
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
	if err := c.retract(j.height); err != nil {
		return err
	}
	if err := c.travel(j, pk, t.slotPos(slot)); err != nil {
		return err
	}
	if err := c.zStroke(t.cfg.ZPick + t.pins.zOffset.Get()); err != nil {
		return err
	}
	if err := c.dwell(c.m.pins.posSettleTime.Get()); err != nil {
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
	// gating for a place-to-proc.
	if err := c.busyGate(j, pk, s, c.in.procBusy[s.idx]); err != nil {
		return err
	}
	c.requestRelease(s, true)
	// See pickFromProc: no error path may leave the fixture commanded open.
	defer func() {
		if err != nil {
			c.requestRelease(s, false)
		}
	}()
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
