// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"strings"
	"testing"
	"time"
)

// NEAR_HEIGHT (D37): a station's travel height while the head stays at it --
// between the remover and the placer of a swap, between tray slots, and from
// one job into the next when stay-near says the next one starts here.

func TestLoadConfigNearHeight(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)
	if cfg.Trays[0].HasNear || cfg.Procs[0].HasNear {
		t.Error("a NEAR_HEIGHT without the key")
	}
	with := func(tray, proc string) string {
		s := strings.Replace(stationSections, "Z_PICK = 2.5\n", "Z_PICK = 2.5\n"+tray, 1)
		s = strings.Replace(s, "Z_PICK = 5.0\n", "Z_PICK = 5.0\n"+proc, 1)
		return trajSection + pnptaskSection + s
	}
	cfg = mustLoad(t, with("NEAR_HEIGHT = 10\n", "NEAR_HEIGHT = 12\n"))
	if !cfg.Trays[0].HasNear || cfg.Trays[0].NearHeight != 10 || !cfg.Procs[0].HasNear || cfg.Procs[0].NearHeight != 12 {
		t.Errorf("NEAR_HEIGHT = %+v / %+v, want 10 / 12", cfg.Trays[0], cfg.Procs[0])
	}
	for _, tc := range []struct{ tray, what string }{
		{"NEAR_HEIGHT = 2.5\n", "at the pick height"},
		{"NEAR_HEIGHT = 1\n", "below the pick height"},
		{"NEAR_HEIGHT = 40\n", "above MOVE_HEIGHT"},
	} {
		if _, err := loadTestConfig(t, with(tc.tray, ""), "pnp.task"); err == nil || !strings.Contains(err.Error(), "NEAR_HEIGHT") {
			t.Errorf("NEAR_HEIGHT %s: err = %v, want a refusal", tc.what, err)
		}
	}
}

// TestNearHeightSwap: the remover comes up to the station's NEAR_HEIGHT and
// the placer travels in at it -- no trip to the movement height for the
// sideways shift between the two pickers.
func TestNearHeightSwap(t *testing.T) {
	f := newAltFixture(t, func(w *world) {
		w.procs[0].setHasMaterial(true)
		w.procs[0].cfg.NearHeight, w.procs[0].cfg.HasNear = 12, true
	})
	f.m.pins.pickers[1].xOffset.Set(40)
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJob(10, 20, 0)
	f.requireOK("a swap at a station with NEAR_HEIGHT")

	moves := f.mot.moveList()
	removed, placed := -1, -1
	for i, m := range moves {
		if m.pos.Z == 5 && m.pos.X == 260 && m.pos.Y == 200 && removed < 0 {
			removed = i // the remover (picker 1, offset 40) down in the station
		}
		if m.pos.Z == 5 && m.pos.X == 300 && m.pos.Y == 200 {
			placed = i // the placer (picker 0) down in the station
		}
	}
	if removed < 0 || placed < removed {
		t.Fatalf("no remover stroke before the placer stroke: %+v", moves)
	}
	for _, m := range moves[removed+1 : placed] {
		if m.pos.Z != 12 && m.pos.Z != 5 {
			t.Errorf("between the remover and the placer a move at Z %g, want only NEAR_HEIGHT 12: %+v", m.pos.Z, m)
		}
	}
}

// startJob starts one job and waits for it, with stay-near as given.
func (f *jobFixture) runJobNear(origin, dest, step uint32, stay bool) {
	f.t.Helper()
	f.m.pins.stayNear.Set(stay)
	f.runJob(origin, dest, step)
	time.Sleep(levelHold)
	f.m.pins.stayNear.Set(false)
}

// TestNearHeightAcrossJobs: a job told stay-near ends at its destination's
// NEAR_HEIGHT, and the next job, starting there, travels to its pick at it.
// Leaving the station it goes up to the movement height as always.
func TestNearHeightAcrossJobs(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.procs[0].setHasMaterial(true)
			m.world.trays[0].cfg.NearHeight, m.world.trays[0].cfg.HasNear = 10, true
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJobNear(20, 10, 7, true) // the part back to the tray; the next job starts there
	f.requireOK("proc -> tray with stay-near")
	moves := f.mot.moveList()
	if z := moves[len(moves)-1].pos.Z; z != 10 {
		t.Fatalf("the job ended at Z %g, want the tray's NEAR_HEIGHT 10", z)
	}

	f.runJobNear(10, 20, 7, false) // and the next one picks from the tray
	f.requireOK("tray -> proc after a stay-near")
	moves = f.mot.moveList()
	pick := -1
	for i, m := range moves {
		if m.pos.Z == 2.5 {
			pick = i
			break
		}
	}
	if pick < 0 {
		t.Fatalf("no pick stroke at the tray: %+v", moves)
	}
	for _, m := range moves[:pick] {
		if m.pos.Z != 10 {
			t.Errorf("before the pick a move at Z %g, want only the tray's NEAR_HEIGHT 10: %+v", m.pos.Z, m)
		}
	}
	if zs := zAtXY(moves, 300, 200); len(zs) == 0 || zs[0] != 15 {
		t.Errorf("arrival at the station at Z %v, want the movement height 15 first", zs)
	}
}

// TestNearHeightWithoutStay: without stay-near a job ends at the movement
// height, NEAR_HEIGHT or not -- the head is not left low where something may
// move under it (a tray lifted into it).
func TestNearHeightWithoutStay(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.procs[0].setHasMaterial(true)
			m.world.trays[0].cfg.NearHeight, m.world.trays[0].cfg.HasNear = 10, true
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })

	f.runJobNear(20, 10, 7, false)
	f.requireOK("proc -> tray without stay-near")
	moves := f.mot.moveList()
	if z := moves[len(moves)-1].pos.Z; z != 30 {
		t.Errorf("the job ended at Z %g, want the movement height 30", z)
	}
}
