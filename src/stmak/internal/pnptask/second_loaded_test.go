package pnptask

import "testing"

// D55: every stroke takes all pickers down, so a job that goes down at a tray
// is refused while a picker other than its own carrier holds a part.

// secondLoadedFixture is the two-picker machine with the pickers loaded as the
// seed says -- where a job that failed between its swap-out and its place
// leaves them -- and tray 10 selected, filled or empty.
func secondLoadedFixture(t *testing.T, allow, fill bool, seed func(*world)) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		ini:  secondProcSection,
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.station(10).tray.cfg.AllowSecondLoaded = allow
			seed(m.world)
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	if fill {
		f.fillTray(0)
	}
	return f
}

// TestTrayPlaceRefusedWithSecondPickerLoaded: the crash of 2026-11-13 as a
// refusal. Picker 0 carries the job's part to the tray, picker 1 is loaded:
// its part would come down on the parts standing there.
func TestTrayPlaceRefusedWithSecondPickerLoaded(t *testing.T) {
	f := secondLoadedFixture(t, false, false, func(w *world) {
		w.setHeld(0, 20, false, 0, true)
		w.setHeld(1, 21, false, 0, true)
	})

	f.runJob(20, 10, 0)
	f.requireError("a place on the tray with the other picker loaded", errSecondLoaded)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}
	if got := f.get("error-station"); got != 10 {
		t.Errorf("error-station = %v, want the tray (10)", got)
	}
}

// TestTrayPickRefusedWithAPickerLoaded: the same on the pick side. The free
// picker would pick, the loaded one goes down with it.
func TestTrayPickRefusedWithAPickerLoaded(t *testing.T) {
	f := secondLoadedFixture(t, false, true, func(w *world) {
		w.setHeld(1, 21, false, 0, true)
	})

	f.runJob(10, 20, 0)
	f.requireError("a pick on the tray with the other picker loaded", errSecondLoaded)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("the refused job dispatched %d moves, want none", n)
	}
}

// TestTraySkippedPickIsNoStroke: a job whose tray pick is skipped (a picker
// already holds the tray's part) does not go down at the tray, so the other
// picker's load does not matter to it.
func TestTraySkippedPickIsNoStroke(t *testing.T) {
	f := secondLoadedFixture(t, false, true, func(w *world) {
		w.setHeld(0, 10, false, 0, true)
		w.setHeld(1, 21, false, 0, true)
	})

	f.runJob(10, 20, 0)
	f.requireOK("a place at a process station, the tray pick skipped, the other picker loaded")
}

// TestAllowSecondLoadedLiftsIt: a tray that says ALLOW_2ND_LOADED (a bin)
// takes the place with the other picker loaded.
func TestAllowSecondLoadedLiftsIt(t *testing.T) {
	f := secondLoadedFixture(t, true, false, func(w *world) {
		w.setHeld(0, 20, false, 0, true)
		w.setHeld(1, 21, false, 0, true)
	})

	f.runJob(20, 10, 0)
	f.requireOK("a place on an ALLOW_2ND_LOADED tray with the other picker loaded")
}
