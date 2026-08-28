// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"testing"
	"time"
)

// The alternating picker of §8: the pick that is skipped because a picker
// already holds the origin's material, the swap that empties an occupied
// process station before filling it again, the sequence constraint that follows
// from it, and the manual open/close interplay.

// secondProcSection is a second process station, which the shared fixture
// config does not have — §8's chain runs material from one station to the next,
// so a single station cannot express it. (300, 350) is inside the eroded travel
// envelope of both fixture drawings and clear of their dead zone.
const secondProcSection = `
[PNPTASK_PROC_1]
ID = 21
X = 300.0
Y = 350.0
Z_PICK = 4.0
`

// newAltFixture is a two-picker machine ready to run jobs, with the second
// process station and the tray selected and filled.
func newAltFixture(t *testing.T, seed func(*world)) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		ini:  secondProcSection,
		prep: func(_ *testing.T, m *pnptaskModule) {
			if seed != nil {
				seed(m.world)
			}
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	return f
}

// ---------------------------------------------------------------------------
// The pick phase (§8: a picker already holds the origin's material)
// ---------------------------------------------------------------------------

// TestAltPickSkippedWhenAPickerHoldsTheOrigin: the first §8 rule. A picker
// holding material that came from the job's origin makes the physical pick
// unnecessary — the job travels straight to the destination and places with
// that picker, and the origin is never touched.
func TestAltPickSkippedWhenAPickerHoldsTheOrigin(t *testing.T) {
	// Picker 1 holds material that came from the tray station.
	f := newAltFixture(t, func(w *world) { w.setHeld(1, 10, false, 0, true) })
	f.m.pins.pickers[1].xOffset.Set(40)

	f.runJob(10, 20, 0)
	f.requireOK("a job whose origin material is already in a picker")

	if !f.bit("proc.20.has-material") {
		t.Error("the station holds nothing although the held material was placed")
	}
	if f.bit("picker.0.close") {
		t.Error("picker 0 was actuated although picker 1 carried the job")
	}
	// The tray was never approached: no move at the first slot, and the slot
	// still holds its part.
	moves := f.mot.moveList()
	if zs := zAtXY(moves, 120, 430); len(zs) != 0 {
		t.Errorf("the skipped pick approached the tray anyway: %v", zs)
	}
	if f.bit("tray.10.empty") {
		t.Error("the tray lost a slot to a pick that never happened")
	}
	// The place ran with picker 1's offset: station 20 sits at (300, 200), so
	// the machine goes to (260, 200).
	if zs := zAtXY(moves, 260, 200); len(zs) == 0 {
		t.Errorf("no approach at picker 1's offset position for the station: moves %+v", moves)
	}

	w := f.stopped()
	if w.held[1].present {
		t.Error("picker 1 still holds material after placing it")
	}
}

// TestAltPickSkipNeedsNoOriginPrecondition: a job whose pick is skipped has no
// business failing on the origin's state — it never goes there. An empty tray
// is exactly that case: the part it would have picked is already in the picker.
func TestAltPickSkipNeedsNoOriginPrecondition(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.setHeld(0, 10, false, 0, true) })
	f.pulse(f.tray().setEmpty)
	f.eventually("tray empty", func() bool { return f.bit("tray.10.empty") })

	f.runJob(10, 20, 0)
	f.requireOK("a skipped pick from an empty tray")
	if !f.bit("proc.20.has-material") {
		t.Error("the station holds nothing after the place")
	}
}

// TestSkipPickRefusesWrongStep: the held record matches by station AND by
// process step where the step is known — a re-dispatched job asking for a
// different step must be refused, not silently served the wrong part.
func TestSkipPickRefusesWrongStep(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.setHeld(1, 10, false, 0, true) })

	f.runJob(10, 20, 3) // the held part is step 0, the job asks for step 3
	f.requireError("a skipPick whose step mismatches the held material", errInvalidOrigin)
	f.clearError()

	f.runJob(10, 20, 0)
	f.requireOK("the matching step places the held part")
}

// TestSkipPickSwapMaterialIsStepExempt: a swap's removed occupant has an
// unknown step (the model never tracked what the earlier place put there), so
// the obligated carry-away job runs whatever step the PLC declares for it.
func TestSkipPickSwapMaterialIsStepExempt(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.setHeld(1, 20, true, 0, false) })

	f.runJob(20, 21, 7)
	f.requireOK("the swap obligation is served regardless of the declared step")
	if !f.bit("proc.21.has-material") {
		t.Error("the station holds nothing after the place")
	}
}

// ---------------------------------------------------------------------------
// The swap (§8)
// ---------------------------------------------------------------------------

// TestAltSwapAtOccupiedStation is the core of §8: a job into an occupied
// process station has the free picker take the occupant out first, then places
// its own material. The fixture is opened once and stays open across both — no
// re-clamp on an empty nest in between.
func TestAltSwapAtOccupiedStation(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.procs[0].setHasMaterial(true) })
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })
	f.sim.resetReleaseRises()

	f.runJob(10, 20, 0)
	f.requireOK("tray -> occupied station")

	// The station holds the job's material now, and the part it held before is
	// in the other picker.
	if !f.bit("proc.20.has-material") {
		t.Error("the station reports empty although the job placed into it")
	}
	if f.bit("proc.20.release") {
		t.Error("release is still asserted at the end of the job (D19)")
	}
	if n := f.sim.releaseRiseCount(0); n != 1 {
		t.Errorf("the fixture was opened %d times, want 1 — the clamp cycled between the swap and the place", n)
	}

	// Picker 0 picked from the tray and placed; picker 1 took the occupant out
	// and still holds it (§8), recorded against the station it came from.
	if !f.bit("picker.1.close") {
		t.Error("the swapping picker is not holding the removed part")
	}
	if f.bit("picker.0.close") {
		t.Error("the placing picker did not let go of the job's material")
	}
	// ... and that is legible from outside: the swap obligation is what tells a
	// sequencer which job it is allowed to command next, and it cannot infer it
	// from its own history (the record is persisted across a restart).
	if f.bit("picker.0.holds") || f.get("picker.0.origin-id") != 0 {
		t.Errorf("picker 0 reports holds=%v origin-id=%v; want empty after placing",
			f.bit("picker.0.holds"), f.get("picker.0.origin-id"))
	}
	if !f.bit("picker.1.holds") || f.get("picker.1.origin-id") != 20 {
		t.Errorf("picker 1 reports holds=%v origin-id=%v; want material from station 20",
			f.bit("picker.1.holds"), f.get("picker.1.origin-id"))
	}
	w := f.stopped()
	if w.held[0].present {
		t.Errorf("the placing picker still holds material: %+v", w.held[0])
	}
	if !w.held[1].present || w.held[1].station != 20 || !w.held[1].swap {
		t.Errorf("swap record = %+v, want the material of station 20, marked as a swap", w.held[1])
	}
}

// firstAtXY is the index of the first move commanded to an XY, or -1.
func firstAtXY(moves []moveCall, x, y float64) int {
	for i, m := range moves {
		if math.Abs(m.pos.X-x) < 1e-6 && math.Abs(m.pos.Y-y) < 1e-6 {
			return i
		}
	}
	return -1
}

// TestAltSwapAtAWaitZoneStationUsesTheRemoversOffset: the gated approach to a
// WAIT_DEADZONE station drives all the way in once the station clears, so it
// has to be planned for the picker that is actually going in. On a swap that is
// the one taking the occupant out, not the one carrying the job's material —
// they sit 40 mm apart here, and asking for the free picker only after the gate
// (as the pre-D29 code did) would have driven the placer into the station and
// then shuffled sideways by the offset difference.
func TestAltSwapAtAWaitZoneStationUsesTheRemoversOffset(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		args:  []string{"pickers=2"},
		ini:   waitZoneSections,
		files: map[string]string{zonesA: fixtureOpen, zonesB: fixtureClear},
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.procs[1].setHasMaterial(true)
			// Two grips in one job — the tray pick and the swap removal — with
			// a wait in between that lets the loop fall behind on a loaded
			// machine. The shared fixture's handful of cycles is enough for one
			// grip; this gives the simulated gripper room for both.
			m.pins.pickSettleTime.Set(30 * pollInterval.Seconds())
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	// Picker 1 reaches 40 mm further in X than picker 0, so which picker a leg
	// was planned for is visible in the commanded coordinate.
	f.m.pins.pickers[1].xOffset.Set(40)
	f.m.pins.deadzoneSelect.Set(1)
	f.setBit(f.m.pins.procs[1].busy, true)

	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(21)
	f.m.pins.processStep.Set(0)
	f.mot.resetCalls()
	f.m.pins.startJob.Set(true)

	// The pick is done and the gated approach is holding short of the zone. The
	// wait point itself is not a fixed coordinate here — the approach starts
	// from whichever tray slot the pick used — so what is checked is that the
	// station was not entered, which is what the gate is for.
	f.eventually("the pick to complete", func() bool { return f.bit("picker.0.close") })
	f.consistently("the gated approach holding out of the station", func() bool {
		return firstAtXY(f.mot.moveList(), procCamX-40, procCamY) < 0
	})
	f.m.pins.deadzoneSelect.Set(0)
	f.setBit(f.m.pins.procs[1].busy, false)

	f.eventually("the job to complete", func() bool { return !f.bit("start-job") })
	f.m.pins.startJob.Set(false)
	f.requireOK("swap into a wait-zone station")

	moves := f.mot.moveList()
	// The remover goes in first, at the station minus ITS offset...
	remover := firstAtXY(moves, procCamX-40, procCamY)
	if remover < 0 {
		t.Fatalf("no move to the station for the removing picker (x = %v)", procCamX-40)
	}
	// ... and the placer follows, at the station minus its own.
	placer := firstAtXY(moves, procCamX, procCamY)
	if placer < 0 {
		t.Fatalf("no move to the station for the placing picker (x = %v)", procCamX)
	}
	if remover > placer {
		t.Errorf("the placer reached the station (move %d) before the remover (move %d)", placer, remover)
	}
	w := f.stopped()
	if !w.held[1].present || w.held[1].station != 21 || !w.held[1].swap {
		t.Errorf("swap record = %+v, want the material of station 21, marked as a swap", w.held[1])
	}
}

// TestAltSwapSequenceConstraint: while a picker holds swap-removed material,
// the next job has to be the one that carries it away (§8). The station is
// running its process on the piece that replaced it, so there is nowhere else
// for the removed part to go.
func TestAltSwapSequenceConstraint(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.procs[0].setHasMaterial(true) })
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJob(10, 20, 0)
	f.requireOK("the swapping job")

	// Any other origin is refused before the machine moves.
	f.runJob(10, 21, 0)
	f.requireError("a job that ignores the swap-removed part", errAltPickerSeq)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}

	// The job that takes it out of station 20 runs — and skips the pick,
	// because the part is already in a picker.
	f.clearError()
	f.runJob(20, 21, 0)
	f.requireOK("the job that carries the removed part away")
	if !f.bit("proc.21.has-material") {
		t.Error("the second station holds nothing after the place")
	}
	w := f.stopped()
	if ss := w.swapStations(); len(ss) != 0 {
		t.Errorf("swap records for %v survived the job that carried the part away", ss)
	}
}

// TestAltSwapNeedsAFreePicker: a swap needs a picker of its own, on top of the
// one carrying the job's material (§8). A picker left loaded by a manual
// intervention is what this guards against, and the job is refused before the
// machine moves rather than discovered with a part in the air.
func TestAltSwapNeedsAFreePicker(t *testing.T) {
	f := newAltFixture(t, func(w *world) {
		w.procs[0].setHasMaterial(true)
		w.setHeld(1, 99, false, 0, true) // a picker loaded by hand, from nowhere in this config
	})
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJob(10, 20, 0)
	f.requireError("a swap with only one free picker", errNoFreePicker)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}
}

// TestSwapRefusedWithOnePicker: with pickers=1 there is no swap — the only
// picker is already carrying the job's material — so an occupied destination
// stays PROC_HAS_MATERIAL (§7.4), which says what the operator has to do about
// it: run a job that empties the station.
func TestSwapRefusedWithOnePicker(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.procs[0].setHasMaterial(true) })
	f.selectTray(1)
	f.fillTray(0)

	f.runJob(10, 20, 0)
	f.requireError("an occupied destination on a single-picker machine", errProcHasMaterial)
}

// TestAltSwapAbortLeavesTheStationEmptyInTheModel: an estop between the removal
// and the place leaves the station physically empty, and has-material has to say
// so — the removed part is in a picker, and the next job must not be sent for a
// part that is no longer in the fixture.
func TestAltSwapAbortLeavesTheStationEmptyInTheModel(t *testing.T) {
	f := newAltFixture(t, func(w *world) { w.procs[0].setHasMaterial(true) })
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })
	// Slow the moves down so the estop lands inside the swap.
	f.mot.setMoveCycles(20)

	f.m.pins.startJob.Set(false)
	time.Sleep(10 * pollInterval)
	f.m.pins.originID.Set(10)
	f.m.pins.destID.Set(20)
	f.m.pins.processStep.Set(0)
	f.m.pins.startJob.Set(true)

	// The removal commits the moment the fixture has let go.
	f.eventually("the swap to take the occupant out", func() bool {
		return !f.bit("proc.20.has-material")
	})
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("the job to end", func() bool { return !f.bit("busy") })
	f.requireError("estop during a swap", errEstop)

	f.consistently("the station stays empty in the model", func() bool {
		return !f.bit("proc.20.has-material") && !f.bit("proc.20.release")
	})
}

// ---------------------------------------------------------------------------
// The §8 reference flow
// ---------------------------------------------------------------------------

// TestAltPickerReferenceFlow walks the chain of §8 end to end on two process
// stations: the pickers change roles every job, the swap keeps each station
// occupied, and the sequence constraint threads the removed parts through.
func TestAltPickerReferenceFlow(t *testing.T) {
	f := newAltFixture(t, nil)

	// 1. Both pickers empty: the tray part goes into the (empty) first station.
	f.runJob(10, 20, 0)
	f.requireOK("job 1: tray -> station 20")

	// 2. The next tray part swaps station 20's part out — one picker places, the
	//    other keeps the finished piece.
	f.runJob(10, 20, 0)
	f.requireOK("job 2: tray -> occupied station 20")
	if !f.bit("proc.20.has-material") {
		t.Fatal("station 20 is empty after the swap job")
	}

	// 3. The removed part travels on to the second station, with no pick.
	f.runJob(20, 21, 0)
	f.requireOK("job 3: station 20 -> station 21")

	// 4. Another tray part swaps station 20 again.
	f.runJob(10, 20, 0)
	f.requireOK("job 4: tray -> occupied station 20")

	// 5. That part goes to station 21, which is occupied: swap there too, with
	//    the picker that is free again after job 4.
	f.runJob(20, 21, 0)
	f.requireOK("job 5: station 20 -> occupied station 21")
	if !f.bit("proc.21.has-material") {
		t.Fatal("station 21 is empty after its swap job")
	}

	// 6. The finished part comes back into the tray, and the machine is idle
	//    with both pickers empty — the state the flow started from.
	f.runJob(21, 10, 1)
	f.requireOK("job 6: station 21 -> tray")

	if f.bit("picker.0.close") || f.bit("picker.1.close") {
		t.Error("a picker is still closed at the end of the flow")
	}
	w := f.stopped()
	for n := range w.held {
		if w.held[n].present {
			t.Errorf("picker %d still holds material at the end of the flow: %+v", n, w.held[n])
		}
	}
	// Both stations are still loaded — that is what the swaps were for.
	if !w.procs[0].hasMaterial || !w.procs[1].hasMaterial {
		t.Errorf("stations occupied at the end = %v/%v, want both",
			w.procs[0].hasMaterial, w.procs[1].hasMaterial)
	}
	// The part that came back is in the tray at the step the job carried.
	if w.trays[0].emptyFor(1) {
		t.Error("the tray holds no step-1 material although the flow put a part back")
	}
}

// ---------------------------------------------------------------------------
// Manual interplay (§8, resolved O8)
// ---------------------------------------------------------------------------

// TestManualOpenRetainsTheStationAndCloseRestoresIt: an operator opening a
// picker to reseat a part and closing it again on the same part must end up with
// the record the machine started with — otherwise the engine would send a loaded
// picker to pick.
func TestManualOpenRetainsTheStationAndCloseRestoresIt(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 10, false, 0, true) }, "pickers=2")
	f.setBit(f.m.pins.autoEnable, false) // manual mode (§6.4)

	f.press(f.m.pins.pickers[0].manualOpen)

	// The record is gone as far as the engine is concerned: the picker is free.
	f.consistently("the opened picker holds nothing", func() bool {
		return !f.bit("picker.0.close")
	})

	// Closed again, onto the part: the record comes back with its station id.
	f.press(f.m.pins.pickers[0].manualClose)
	f.eventually("the picker to close", func() bool { return f.bit("picker.0.close") })
	// Give the grip validation its settle time plus a margin.
	time.Sleep(30 * pollInterval)

	w := f.stopped()
	if !w.held[0].present || w.held[0].station != 10 {
		t.Errorf("held record after open/close = %+v, want the material of station 10 back", w.held[0])
	}
}

// TestManualCloseOnNothingClearsTheRecord: the other half of §8's manual rule —
// a picker closed onto nothing gripped nothing, so the retained id describes a
// part the operator now has in their hand, and the machine must forget it.
func TestManualCloseOnNothingClearsTheRecord(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 10, false, 0, true) }, "pickers=2")
	f.setBit(f.m.pins.autoEnable, false)

	f.press(f.m.pins.pickers[0].manualOpen)

	// The next close runs all the way shut: there is nothing between the jaws.
	f.sim.set(func(s *machineSim) { s.missesLeft = 1 })
	f.press(f.m.pins.pickers[0].manualClose)
	f.eventually("the picker to report fully closed", func() bool {
		return f.m.pins.pickers[0].closed.Get()
	})
	// The verdict reopens the picker along with dropping the record: a free
	// picker whose jaws stayed commanded shut would make the next job's
	// closeAndCheck read "closed" whatever sits under the head.
	f.eventually("the picker reopened with the verdict", func() bool {
		return !f.bit("picker.0.close")
	})

	w := f.stopped()
	if w.held[0].present || w.held[0].retained {
		t.Errorf("held record after a close onto nothing = %+v, want it forgotten", w.held[0])
	}
}

// TestManualHandlingBlocksJobsUntilJudged (§8.1): a manual open puts the record
// into retention — a manual intervention in progress, a reservation, not a mere
// memory. Jobs are refused until a manual close judges the outcome; a close
// that grips nothing clears the record, and only then is the picker the
// engine's again. (Counting the picker free while retained was the settle-race
// hole of the phase-6 review: a job could grab a picker whose part was about to
// be re-gripped.)
func TestManualHandlingBlocksJobsUntilJudged(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 99, false, 0, true) })
	f.selectTray(1)
	f.fillTray(0)

	// With the record standing, the single picker is loaded and no job can run.
	f.runJob(10, 20, 0)
	f.requireError("a job with the only picker loaded", errNoFreePicker)
	f.clearError()

	// The operator opens the picker: retention. Jobs are still refused — the
	// part is in the operator's hands and nothing is decided yet.
	f.setBit(f.m.pins.autoEnable, false)
	f.press(f.m.pins.pickers[0].manualOpen)
	f.setBit(f.m.pins.autoEnable, true)
	f.runJob(10, 20, 0)
	f.requireError("a job while the picker is mid manual handling", errNoFreePicker)
	f.clearError()

	// The operator keeps the part: a close onto nothing judges the record gone.
	f.sim.set(func(s *machineSim) { s.missesLeft = 1 })
	f.setBit(f.m.pins.autoEnable, false)
	f.press(f.m.pins.pickers[0].manualClose)
	f.eventually("the record judged empty and cleared", func() bool {
		return !f.bit("error") // no pin shows the record; the job below is the proof
	})
	time.Sleep(20 * pollInterval) // let the grip judgement expire
	f.press(f.m.pins.pickers[0].manualOpen)
	f.setBit(f.m.pins.autoEnable, true)

	f.runJob(10, 20, 0)
	f.requireOK("a job after the manual handling was resolved")
}

// TestManualOpenTwiceKeepsRetained (§8.1): a second open press on an already
// open picker is physically a no-op and must not wipe the retained id — the
// close that follows still restores the record, station and all.
func TestManualOpenTwiceKeepsRetained(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 10, false, 0, true) })
	f.setBit(f.m.pins.autoEnable, false)

	f.press(f.m.pins.pickers[0].manualOpen)
	f.press(f.m.pins.pickers[0].manualOpen) // the idempotent second press
	// The part goes back in; the sim's default close grips material.
	f.press(f.m.pins.pickers[0].manualClose)
	time.Sleep(20 * pollInterval) // the grip judgement window

	w := f.stopped()
	if !w.held[0].present || w.held[0].station != 10 {
		t.Errorf("held record after open/open/close = %+v, want restored material from station 10", w.held[0])
	}
}

// TestRetainedRecordSurvivesRestart (§8.1): a restart in the middle of a manual
// intervention must not forget the loaded picker or the swap obligation — the
// record comes back retained, jobs stay refused, and the operator's close still
// judges the outcome.
func TestRetainedRecordSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_retained"

	first := newJobFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.world.setHeld(1, 20, true, 0, true) // swap-removed material from station 20
		}),
	})
	first.setBit(first.m.pins.autoEnable, false)
	first.press(first.m.pins.pickers[1].manualOpen)
	first.m.Stop()

	second := newJobFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		prep: withPersist(persistName, ns, nil),
	})
	second.homed()
	second.mot.setPos(100, 100, 60)
	second.selectTray(1)
	second.fillTray(0)

	// Mid-handling: jobs are refused, and the close output was NOT re-driven
	// (the part is in the operator's hands, not the picker).
	if second.bit("picker.1.close") {
		t.Error("a retained record re-drove the close output")
	}
	second.runJob(10, 20, 0)
	second.requireError("a job while the restored handling is unresolved", errNoFreePicker)

	// The operator closes onto the part: record, station and swap obligation
	// are back.
	second.clearError()
	second.setBit(second.m.pins.autoEnable, false)
	second.press(second.m.pins.pickers[1].manualClose)
	time.Sleep(20 * pollInterval)

	w := second.stopped()
	if !w.held[1].present || w.held[1].station != 20 || !w.held[1].swap {
		t.Errorf("held record after the restart and close = %+v, want swap material from station 20", w.held[1])
	}
}

// TestSlowGripperKeepsJudging (§8.1): a gripper that has not actuated when the
// settle window expires is still a close request over a retained part — the
// judgement re-arms instead of silently giving up, and decides once the
// gripper answers.
func TestSlowGripperKeepsJudging(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 10, false, 0, true) })
	f.setBit(f.m.pins.autoEnable, false)

	f.press(f.m.pins.pickers[0].manualOpen)
	// The gripper answers nothing: opened stays high through several settle
	// windows.
	f.sim.set(func(s *machineSim) { s.jammedClosed = true })
	f.press(f.m.pins.pickers[0].manualClose)
	time.Sleep(40 * pollInterval)

	// The air comes back; the grip completes and the record is restored.
	f.sim.set(func(s *machineSim) { s.jammedClosed = false })
	time.Sleep(40 * pollInterval)

	w := f.stopped()
	if !w.held[0].present || w.held[0].station != 10 {
		t.Errorf("held record after a slow grip = %+v, want restored material from station 10", w.held[0])
	}
}

// TestFailedPlaceLeavesTwoRecoverableSwaps: a place that fails after its
// swap-out leaves BOTH pickers holding swap material — both parts really are in
// pickers, each with its obligation. The sequence constraint then accepts a job
// from either station, and two ordinary jobs put the world back together.
func TestFailedPlaceLeavesTwoRecoverableSwaps(t *testing.T) {
	f := newAltFixture(t, func(w *world) {
		w.setHeld(1, 20, true, 0, true) // picker 1: swap material from station 20
		w.procs[1].setHasMaterial(true) // station 21 occupied
	})

	// The job that should carry 20's material into 21: the swap-out succeeds,
	// the place fails (the picker will not open).
	f.sim.set(func(s *machineSim) { s.jammedOpen = true })
	f.runJob(20, 21, 0)
	f.requireError("a place that could not open the picker", errPlaceFailed)
	f.sim.set(func(s *machineSim) { s.jammedOpen = false })
	f.clearError()

	// Both obligations stand: a job from an unrelated origin is refused...
	f.runJob(10, 20, 0)
	f.requireError("an unrelated job with two swap obligations standing", errAltPickerSeq)
	f.clearError()

	// ...but a job from either swap station recovers. First 21's part goes
	// into the free station 20, then 20's part into the now-free station 21.
	f.runJob(21, 20, 0)
	f.requireOK("carrying the removed occupant of 21 away")
	f.runJob(20, 21, 0)
	f.requireOK("retrying the original job")

	w := f.stopped()
	if ss := w.swapStations(); len(ss) != 0 {
		t.Errorf("swap obligations %v survived the recovery", ss)
	}
	if !f.bit("proc.21.has-material") {
		t.Error("station 21 holds nothing after the recovered place")
	}
}

// TestSelfExchangeRefused: a job whose material came OUT of an occupied station
// and whose destination is that same station would swap the parts back and
// forth forever — no §8 flow describes it, so it is refused like every other
// mis-sequence.
func TestSelfExchangeRefused(t *testing.T) {
	f := newAltFixture(t, func(w *world) {
		w.setHeld(1, 20, true, 0, true) // picker 1 holds what came out of 20
		w.procs[0].setHasMaterial(true) // and 20 is occupied again
	})
	f.runJob(20, 20, 0)
	f.requireError("a self-exchange on an occupied station", errProcHasMaterial)
}

// TestEstopClearsARetainedRecord: estop opens every picker (D14), which is also
// the end of whatever manual handling was in progress — a retained id would
// otherwise resurrect a part the operator has since taken away.
func TestEstopClearsARetainedRecord(t *testing.T) {
	f := newJobFixtureSeeded(t, func(w *world) { w.setHeld(0, 10, false, 0, true) }, "pickers=2")
	f.setBit(f.m.pins.autoEnable, false)

	f.press(f.m.pins.pickers[0].manualOpen)

	f.setBit(f.m.pins.estopOn, true)
	f.eventually("estop taken", func() bool { return !f.bit("machine-is-on") })

	w := f.stopped()
	if w.held[0] != (heldMaterial{}) {
		t.Errorf("held record after an estop = %+v, want it cleared entirely", w.held[0])
	}
}
