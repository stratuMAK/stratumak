// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"strconv"
	"testing"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// cellAt reads one cell out of a RungState the way a client does.
func cellAt(t *testing.T, s api.RungState, col, row int) int32 {
	t.Helper()
	const width = 10
	i := row*width + col
	if i >= len(s.Cells) {
		t.Fatalf("cell (%d,%d) is index %d, but the rung has %d cells", col, row, i, len(s.Cells))
	}
	return s.Cells[i]
}

// stateOf finds the state of one rung in what get_rung_states returned.
func stateOf(t *testing.T, states []api.RungState, rung int32) api.RungState {
	t.Helper()
	for _, s := range states {
		if s.Rung == rung {
			return s
		}
	}
	t.Fatalf("rung %d is missing from %d reported states", rung, len(states))
	return api.RungState{}
}

// A parallel branch is the case that makes this API worth having: the coil is
// energized through the lower contact while the upper one is open, so where
// the power is cannot be read off the variable values — only the rung
// evaluation knows, and that runs in the engine.
//
//	     %I0
//	|----| |----+----( )----|   row 0   (coil %Q0)
//	     %I1    |
//	|----| |----+                row 1
func TestRungStates_ParallelBranch(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutput, varPhysOutput, 0)
	l.put(0, 0, 1, eleInput, varPhysInput, 1)
	l.connectTop(0, 1, 1)

	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)

	// Lower contact closed, upper open.
	l.input(0, false)
	l.input(1, true)
	l.scan(1)

	states, err := m.GetRungStates()
	if err != nil {
		t.Fatalf("get rung states: %v", err)
	}
	s := stateOf(t, states, 0)

	if got := cellAt(t, s, 0, 0); got&api.CELL_STATE != 0 {
		t.Errorf("upper contact reports CELL_STATE (cells=%d), but %%I0 is off", got)
	}
	if got := cellAt(t, s, 0, 1); got&api.CELL_STATE == 0 {
		t.Errorf("lower contact does not report CELL_STATE (cells=%d), but %%I1 is on", got)
	}
	if got := cellAt(t, s, 0, 1); got&api.CELL_OUTPUT == 0 {
		t.Errorf("lower contact does not report CELL_OUTPUT (cells=%d); it conducts from the rail", got)
	}
	coil := cellAt(t, s, 1, 0)
	if coil&api.CELL_INPUT == 0 {
		t.Errorf("coil does not report CELL_INPUT (cells=%d); power reaches it up the branch", coil)
	}
	if coil&api.CELL_STATE == 0 {
		t.Errorf("coil does not report CELL_STATE (cells=%d); it is energized", coil)
	}
	if !l.output(0) {
		t.Fatal("%Q0 is off — the rung did not evaluate as the test assumes")
	}

	// Open the branch: nothing conducts, and the reported state must follow.
	l.input(1, false)
	l.scan(1)

	states, err = m.GetRungStates()
	if err != nil {
		t.Fatalf("get rung states after opening: %v", err)
	}
	s = stateOf(t, states, 0)
	if got := cellAt(t, s, 1, 0); got&api.CELL_STATE != 0 {
		t.Errorf("coil still reports CELL_STATE (cells=%d) with both contacts open", got)
	}
	if got := cellAt(t, s, 0, 1); got&api.CELL_OUTPUT != 0 {
		t.Errorf("lower contact still reports CELL_OUTPUT (cells=%d) with %%I1 off", got)
	}
}

// A negated contact conducts while its variable is false. Reporting the
// variable instead of the element's conduction would colour it backwards.
func TestRungStates_NegatedContactReportsConduction(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	l.put(0, 0, 0, eleInputNot, varPhysInput, 0)
	l.put(0, 1, 0, eleOutput, varPhysOutput, 0)
	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)

	l.input(0, false)
	l.scan(1)
	states, _ := m.GetRungStates()
	if got := cellAt(t, stateOf(t, states, 0), 0, 0); got&api.CELL_STATE == 0 {
		t.Errorf("negated contact reports no CELL_STATE (cells=%d) while %%I0 is off, but it conducts", got)
	}

	l.input(0, true)
	l.scan(1)
	states, _ = m.GetRungStates()
	if got := cellAt(t, stateOf(t, states, 0), 0, 0); got&api.CELL_STATE != 0 {
		t.Errorf("negated contact reports CELL_STATE (cells=%d) while %%I0 is on, but it is open", got)
	}
}

// Unused rungs are absent rather than reported blank: a client keys off the
// program for what exists, and a blank entry for a rung slot that holds nothing
// would invite it to draw one.
func TestRungStates_SkipsUnusedRungs(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	rtRungs(m.rt)[0].used = 1
	rtRungs(m.rt)[3].used = 1
	l.setMainSection(0, 0, 3)
	rtRungs(m.rt)[0].next_rung = 3
	rtRungs(m.rt)[3].prev_rung = 0

	states, err := m.GetRungStates()
	if err != nil {
		t.Fatalf("get rung states: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d states, want 2 — only rungs 0 and 3 are in use", len(states))
	}
	stateOf(t, states, 0)
	stateOf(t, states, 3)

	for _, s := range states {
		if len(s.Cells) != 60 {
			t.Errorf("rung %d reports %d cells, want 60 (RUNG_WIDTH * RUNG_HEIGHT)", s.Rung, len(s.Cells))
		}
	}
}

// The watch returns the same states keyed by rung index, so the push loop can
// diff them per rung. A key that did not match RungState.rung would make the
// client animate the wrong rung.
func TestWatchRungStates_KeyedByRungIndex(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	l.put(2, 0, 0, eleInput, varPhysInput, 0)
	rtRungs(m.rt)[2].used = 1
	rtRungs(m.rt)[5].used = 1
	l.setMainSection(0, 2, 5)
	rtRungs(m.rt)[2].next_rung = 5
	rtRungs(m.rt)[5].prev_rung = 2

	l.input(0, true)
	l.scan(1)

	byKey, err := m.WatchRungStates()
	if err != nil {
		t.Fatalf("watch rung states: %v", err)
	}
	if len(byKey) != 2 {
		t.Fatalf("got %d keys, want 2", len(byKey))
	}
	for key, s := range byKey {
		if key != strconv.Itoa(int(s.Rung)) {
			t.Errorf("key %q holds rung %d — the key must be the rung index", key, s.Rung)
		}
	}
	s, ok := byKey["2"]
	if !ok {
		t.Fatal(`no key "2" — rung 2 is in use`)
	}
	if got := cellAt(t, s, 0, 0); got&api.CELL_STATE == 0 {
		t.Errorf("rung 2's contact reports no CELL_STATE (cells=%d) with %%I0 on", got)
	}
}

// A block's input terminals arrive at its body cells, which carry no logic of
// their own — the engine evaluates them anyway, precisely so a display can
// colour those wires (2.9 draws E and C from the same power). This checks that
// what the body cells report is the power the block acted on, since a client
// has no other way to know and must not be left recomputing it.
func TestRungStates_BlockInputTerminals(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	// %I0 --| |--+--[ Timer 0 ]--   E on row 0
	// %I1 --| |--+                  C on row 1
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 0, 1, eleInput, varPhysInput, 1)
	l.putBlock(0, 2, 0, eleTimer, 0, 2, 2)
	l.setTimer(0, 1000, 10000)
	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)
	l.prepareRun() // loads the preset, as a cold start does

	// Enable only: the E terminal is powered, the C terminal is not, and a
	// timer held enabled but not controlled sits still.
	l.input(0, true)
	l.input(1, false)
	l.scan(1)

	s := stateOf(t, mustStates(t, m), 0)
	if got := cellAt(t, s, 1, 0); got&api.CELL_INPUT == 0 {
		t.Errorf("the E terminal reports no CELL_INPUT (cells=%d) while %%I0 conducts", got)
	}
	if got := cellAt(t, s, 1, 1); got&api.CELL_INPUT != 0 {
		t.Errorf("the C terminal reports CELL_INPUT (cells=%d) while %%I1 is open", got)
	}
	if got := cellAt(t, s, 2, 1); got&api.CELL_OUTPUT != 0 {
		t.Errorf("the R output reports CELL_OUTPUT (cells=%d) while the timer is not counting", got)
	}

	// Both: the timer counts, and its R output leaves the block.
	l.input(1, true)
	l.scan(1)

	if l.timerValue(0) >= 10000 || l.timerDone(0) {
		t.Fatalf("the timer is not counting (value=%d done=%v) — the rung does not do what the test assumes",
			l.timerValue(0), l.timerDone(0))
	}

	s = stateOf(t, mustStates(t, m), 0)
	if got := cellAt(t, s, 1, 1); got&api.CELL_INPUT == 0 {
		t.Errorf("the C terminal reports no CELL_INPUT (cells=%d) while %%I1 conducts", got)
	}
	if got := cellAt(t, s, 2, 1); got&api.CELL_OUTPUT == 0 {
		t.Errorf("the R output reports no CELL_OUTPUT (cells=%d) while the timer runs", got)
	}
	if got := cellAt(t, s, 2, 0); got&api.CELL_OUTPUT != 0 {
		t.Errorf("the D output reports CELL_OUTPUT (cells=%d) before the timer elapsed", got)
	}
}

func mustStates(t *testing.T, m *classicladder) []api.RungState {
	t.Helper()
	states, err := m.GetRungStates()
	if err != nil {
		t.Fatalf("get rung states: %v", err)
	}
	return states
}

// A spy window and an animated block both need the block state pushed, not
// read off the program: the program is fetched on load and after an edit,
// while these change every scan.
func TestVariables_CarriesLiveBlockState(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	// The block sits on the left rail, so both its terminals are powered and it
	// counts from the first scan.
	l.putBlock(0, 1, 0, eleTimer, 0, 2, 2)
	l.setTimer(0, 1000, 3000) // 3s in a 1s base
	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)
	l.prepareRun()

	vars, err := m.GetVariables()
	if err != nil {
		t.Fatalf("get variables: %v", err)
	}
	if len(vars.Words.TimerValues) != int(m.rt.sizes.nbr_timers) {
		t.Fatalf("got %d timer values, want %d", len(vars.Words.TimerValues), m.rt.sizes.nbr_timers)
	}
	// Values travel in base units, as the presets do. Reporting the engine's
	// milliseconds would show "3000" for a three-second timer.
	if got := vars.Words.TimerValues[0]; got != 3 {
		t.Errorf("timer 0 value = %d, want 3 — the value must be in units of its base", got)
	}
	if vars.Bools.TimerRunning[0] {
		t.Error("timer 0 reports running before anything drove it")
	}

	// Run it down and watch the reported state follow.
	l.scanN(2, 1000)
	vars, _ = m.GetVariables()
	if !vars.Bools.TimerRunning[0] {
		t.Error("timer 0 reports not running while it counts down")
	}
	if got := vars.Words.TimerValues[0]; got != 1 {
		t.Errorf("timer 0 value = %d after 2s of a 3s timer, want 1", got)
	}

	l.scanN(2, 1000)
	vars, _ = m.GetVariables()
	if !vars.Bools.TimerDone[0] {
		t.Error("timer 0 reports not done after its preset elapsed")
	}

	// The counter and IEC arrays must be sized and present too — a spy window
	// indexes them by block number.
	if len(vars.Bools.CounterFull) != int(m.rt.sizes.nbr_counters) {
		t.Errorf("got %d counter-full flags, want %d", len(vars.Bools.CounterFull), m.rt.sizes.nbr_counters)
	}
	if len(vars.Words.TimerIecValues) != int(m.rt.sizes.nbr_timers_iec) {
		t.Errorf("got %d IEC timer values, want %d", len(vars.Words.TimerIecValues), m.rt.sizes.nbr_timers_iec)
	}
}
