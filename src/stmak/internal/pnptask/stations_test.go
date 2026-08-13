// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

func TestParseDirMode(t *testing.T) {
	cases := []struct {
		in   string
		want DirMode
	}{
		{"", defaultDirMode},
		{"C+R+", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true}},
		{"C+R+~", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true, Meander: true}},
		{"R-C+", DirMode{Primary: AxisRow, PrimaryUp: false, SecondaryUp: true}},
		{"C-R-~", DirMode{Primary: AxisCol, PrimaryUp: false, SecondaryUp: false, Meander: true}},
		// Case and surrounding whitespace are the INI's business, not the
		// operator's.
		{" c+r- ", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: false}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDirMode(tc.in)
			if err != nil {
				t.Fatalf("parseDirMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseDirMode(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			if got.Secondary() == got.Primary {
				t.Errorf("Secondary() = %v, must differ from Primary %v", got.Secondary(), got.Primary)
			}
			// The rendered form has to parse back to the same mode, which is
			// what makes String() usable in a diagnostic.
			back, err := parseDirMode(got.String())
			if err != nil || back != got {
				t.Errorf("round trip of %q via %q: %+v, %v", tc.in, got.String(), back, err)
			}
		})
	}
}

func TestParseDirModeErrors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"C+C-", "given twice"},
		{"X+R+", "unknown axis"},
		{"C*R+", "unknown direction"},
		{"C+", "expected an axis token"},
		{"C+R+~~", "trailing"},
		{"C+R+X", "trailing"},
		{"CR", "unknown direction"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := parseDirMode(tc.in)
			if err == nil {
				t.Fatalf("parseDirMode(%q) succeeded, want an error containing %q", tc.in, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseDirMode(%q) error = %v, want it to contain %q", tc.in, err, tc.want)
			}
		})
	}
}

// TestGridSpan checks the ANGLE convention: the span is resolved in the tray's
// own rotated frame, so slot (COLS-1, ROWS-1) lands exactly on LAST whatever
// the angle is, and both taught corners stay honest.
func TestGridSpan(t *testing.T) {
	cases := []struct {
		name             string
		angleDeg         float64
		last             [2]float64
		wantCol, wantRow float64
	}{
		{"axis aligned", 0, [2]float64{100, 40}, 100, 40},
		{"45 degrees along the column axis", 45, [2]float64{100, 100}, math.Sqrt2 * 100, 0},
		{"90 degrees swaps the axes", 90, [2]float64{0, 100}, 100, 0},
		{"negative angle", -90, [2]float64{0, 100}, -100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := TrayDef{
				First:   pnproute.Point{X: 0, Y: 0},
				Last:    pnproute.Point{X: tc.last[0], Y: tc.last[1]},
				HasLast: true,
				Angle:   tc.angleDeg * math.Pi / 180,
			}
			col, row := gridSpan(d)
			if math.Abs(col-tc.wantCol) > 1e-9 || math.Abs(row-tc.wantRow) > 1e-9 {
				t.Errorf("gridSpan = (%g, %g), want (%g, %g)", col, row, tc.wantCol, tc.wantRow)
			}
		})
	}
}

// nearPoint fails unless got is within a micron of want.
func nearPoint(t *testing.T, what string, got, want pnproute.Point) {
	t.Helper()
	if math.Abs(got.X-want.X) > 1e-6 || math.Abs(got.Y-want.Y) > 1e-6 {
		t.Errorf("%s = (%g, %g), want (%g, %g)", what, got.X, got.Y, want.X, want.Y)
	}
}

func TestSlotPos(t *testing.T) {
	// A 3x2 grid spanning (100,200) to (300,300): 100 mm column pitch,
	// 100 mm row pitch.
	grid := TrayDef{
		Rows: 2, Cols: 3,
		First:   pnproute.Point{X: 100, Y: 200},
		Last:    pnproute.Point{X: 300, Y: 300},
		HasLast: true,
	}
	nearPoint(t, "slot(0,0)", grid.SlotPos(0, 0), grid.First)
	nearPoint(t, "slot(2,1)", grid.SlotPos(2, 1), grid.Last)
	nearPoint(t, "slot(1,0)", grid.SlotPos(1, 0), pnproute.Point{X: 200, Y: 200})
	nearPoint(t, "slot(1,1)", grid.SlotPos(1, 1), pnproute.Point{X: 200, Y: 300})
	if n := grid.SlotCount(); n != 6 {
		t.Errorf("SlotCount = %d, want 6", n)
	}

	// The same grid rotated by 90 degrees: the column axis now runs along +Y
	// and the row axis along -X, and LAST still holds the last slot (D24).
	rot := grid
	rot.Angle = math.Pi / 2
	nearPoint(t, "rotated slot(0,0)", rot.SlotPos(0, 0), rot.First)
	nearPoint(t, "rotated slot(2,1)", rot.SlotPos(2, 1), rot.Last)
	// Column span in the rotated frame is LAST-FIRST turned by -90 deg:
	// (200,100) -> (100,-200), so one column step is +50 along the tilted
	// column axis, which points at +Y.
	nearPoint(t, "rotated slot(1,0)", rot.SlotPos(1, 0), pnproute.Point{X: 100, Y: 250})

	// A single-position tray ignores the indices entirely.
	single := TrayDef{Rows: 1, Cols: 1, First: pnproute.Point{X: 42, Y: 43}}
	nearPoint(t, "single slot(0,0)", single.SlotPos(0, 0), single.First)
	if n := single.SlotCount(); n != 1 {
		t.Errorf("single-position SlotCount = %d, want 1", n)
	}

	endless := TrayDef{Rows: 0, Cols: 0, First: pnproute.Point{X: 7, Y: 8}}
	nearPoint(t, "endless slot(0,0)", endless.SlotPos(0, 0), endless.First)
	if n := endless.SlotCount(); n != 1 {
		t.Errorf("endless SlotCount = %d, want 1", n)
	}
}

// TestSlotOrder walks the DIR_MODE iteration over a 3x2 grid. Linear indices are
// row*Cols + col, so row 0 is 0,1,2 and row 1 is 3,4,5.
func TestSlotOrder(t *testing.T) {
	cases := []struct {
		mode string
		want []int
	}{
		// Columns left to right, then the next row.
		{"C+R+", []int{0, 1, 2, 3, 4, 5}},
		// Same, but every second pass runs backwards: the head ends a row where
		// the next one starts.
		{"C+R+~", []int{0, 1, 2, 5, 4, 3}},
		// Rows first: down one column, then the next column.
		{"R+C+", []int{0, 3, 1, 4, 2, 5}},
		{"R+C+~", []int{0, 3, 4, 1, 2, 5}},
		// Reversed directions.
		{"C-R+", []int{2, 1, 0, 5, 4, 3}},
		{"C+R-", []int{3, 4, 5, 0, 1, 2}},
		{"C-R-", []int{5, 4, 3, 2, 1, 0}},
		// Meander counts passes, not geometric rows: starting from the top row
		// the first pass still runs in the primary direction.
		{"C+R-~", []int{3, 4, 5, 2, 1, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			dir, err := parseDirMode(tc.mode)
			if err != nil {
				t.Fatalf("parseDirMode(%q): %v", tc.mode, err)
			}
			got := slotOrder(TrayDef{Rows: 2, Cols: 3, HasLast: true, Dir: dir})
			if len(got) != len(tc.want) {
				t.Fatalf("slotOrder(%s) = %v, want %v", tc.mode, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("slotOrder(%s) = %v, want %v", tc.mode, got, tc.want)
				}
			}
		})
	}

	// Every mode must be a permutation of the whole tray: a slot the order
	// forgets is a slot no pick will ever try and no place will ever fill.
	for _, mode := range []string{"C+R+", "C+R+~", "R-C+", "R-C-~", "C-R+~"} {
		dir, err := parseDirMode(mode)
		if err != nil {
			t.Fatalf("parseDirMode(%q): %v", mode, err)
		}
		d := TrayDef{Rows: 4, Cols: 10, HasLast: true, Dir: dir}
		order := slotOrder(d)
		seen := make(map[int]bool, len(order))
		for _, s := range order {
			if s < 0 || s >= d.SlotCount() {
				t.Fatalf("%s: slot index %d out of range", mode, s)
			}
			if seen[s] {
				t.Fatalf("%s: slot %d visited twice", mode, s)
			}
			seen[s] = true
		}
		if len(seen) != d.SlotCount() {
			t.Errorf("%s: order covers %d of %d slots", mode, len(seen), d.SlotCount())
		}
	}

	// A single-position tray has one slot whatever the mode says.
	if got := slotOrder(TrayDef{Rows: 1, Cols: 1}); len(got) != 1 || got[0] != 0 {
		t.Errorf("single-position slotOrder = %v, want [0]", got)
	}
	if got := slotOrder(TrayDef{Rows: 0, Cols: 0}); len(got) != 1 || got[0] != 0 {
		t.Errorf("endless slotOrder = %v, want [0]", got)
	}
}

// newTestTray builds a tray station state around one geometry, with the
// geometry already selected — the pin-driven selection is exercised through the
// control loop in TestTraySelectionPins.
func newTestTray(t *testing.T, d TrayDef) *trayState {
	t.Helper()
	if d.MaxUnpopulated < 1 {
		d.MaxUnpopulated = 1
	}
	if d.ID == 0 {
		d.ID = 1
	}
	ts := &trayState{
		cfg:  TrayStation{ID: 10, ZPick: 2.5},
		defs: map[uint32]*trayGeometry{d.ID: newTrayGeometry(d)},
	}
	if !ts.selectDef(d.ID) {
		t.Fatalf("selectDef(%d) found no geometry", d.ID)
	}
	return ts
}

// grid3x2 is a plain axis-aligned 3x2 tray in C+R+ order.
func grid3x2() TrayDef {
	return TrayDef{
		ID: 1, Rows: 2, Cols: 3,
		First:   pnproute.Point{X: 100, Y: 200},
		Last:    pnproute.Point{X: 300, Y: 300},
		HasLast: true,
		Dir:     defaultDirMode,
	}
}

// TestTrayPickAndPlace walks the slot search of §7.1: picks match the process
// step, places take the first empty slot, and both follow the DIR_MODE order.
func TestTrayPickAndPlace(t *testing.T) {
	ts := newTestTray(t, grid3x2())

	// A fresh tray is all empty: nothing to pick, nowhere it is full.
	if _, _, ok := ts.nextPick(0, 0); ok {
		t.Error("an empty tray offered a slot to pick from")
	}
	if !ts.emptyFor(0) {
		t.Error("emptyFor on an all-empty tray = false")
	}
	if ts.full() {
		t.Error("full on an all-empty tray = true")
	}

	// set-full at step 0: every slot holds unprocessed material.
	ts.setAll(0)
	if !ts.full() || ts.emptyFor(0) {
		t.Errorf("after set-full: full = %v, empty = %v, want true/false", ts.full(), ts.emptyFor(0))
	}
	// ...but nothing at step 1, so a job asking for step-1 material finds none.
	if !ts.emptyFor(1) {
		t.Error("a tray full of step-0 material is not empty for a step-1 pick")
	}

	// Picks run in DIR_MODE order and empty the slot they took from.
	for want := 0; want < 6; want++ {
		slot, next, ok := ts.nextPick(0, 0)
		if !ok {
			t.Fatalf("pick %d: no slot offered", want)
		}
		if slot != want || next != want+1 {
			t.Fatalf("pick %d offered slot %d (resume %d), want %d (%d)", want, slot, next, want, want+1)
		}
		ts.markPicked(slot)
	}
	if _, _, ok := ts.nextPick(0, 0); ok {
		t.Error("the tray offered a seventh slot")
	}

	// Places fill the first empty slot in the same order and stamp the step.
	slot, ok := ts.freeSlot(-1)
	if !ok || slot != 0 {
		t.Fatalf("freeSlot = %d, %v; want slot 0", slot, ok)
	}
	ts.markPlaced(slot, 2)
	if ts.slots[0] != 2 {
		t.Errorf("slot 0 = %d after a step-2 place, want 2", ts.slots[0])
	}
	// The placed part is now what a step-2 pick finds, and nothing else is.
	if s, _, ok := ts.nextPick(2, 0); !ok || s != 0 {
		t.Errorf("step-2 pick = %d, %v; want slot 0", s, ok)
	}
	if s, ok := ts.freeSlot(-1); !ok || s != 1 {
		t.Errorf("freeSlot after one place = %d, %v; want slot 1", s, ok)
	}

	// Fill the rest: the tray reports full and offers no place.
	for {
		s, ok := ts.freeSlot(-1)
		if !ok {
			break
		}
		ts.markPlaced(s, 2)
	}
	if !ts.full() {
		t.Error("full = false with every slot occupied")
	}
}

// TestTrayPickResumesAfterAMiss covers the probing correction of D9: a slot that
// turns out to be empty is marked and the search continues after it, rather than
// starting over and offering the same slot again.
func TestTrayPickResumesAfterAMiss(t *testing.T) {
	def := grid3x2()
	def.MaxUnpopulated = 3
	ts := newTestTray(t, def)
	ts.setAll(0)

	slot, next, ok := ts.nextPick(0, 0)
	if !ok || slot != 0 {
		t.Fatalf("first candidate = %d, %v", slot, ok)
	}
	ts.markEmpty(slot)
	if ts.slots[0] != slotEmpty {
		t.Errorf("a missed slot is still marked %d, want empty", ts.slots[0])
	}
	if ts.misses != 1 || ts.probedEmpty {
		t.Errorf("after one miss: misses = %d, probedEmpty = %v", ts.misses, ts.probedEmpty)
	}

	slot, _, ok = ts.nextPick(0, next)
	if !ok || slot != 1 {
		t.Fatalf("second candidate = %d, %v; want slot 1", slot, ok)
	}
	// A successful pick clears the miss history: the tray is demonstrably not
	// exhausted.
	ts.markPicked(slot)
	if ts.misses != 0 {
		t.Errorf("misses = %d after a successful pick, want 0", ts.misses)
	}
}

// TestTrayProbedEmpty pins MAX_UNPOPULATED: successive misses declare the tray
// empty, and only an explicit reset takes that back — the tracked state cannot
// see a refill.
func TestTrayProbedEmpty(t *testing.T) {
	def := grid3x2()
	def.MaxUnpopulated = 2
	ts := newTestTray(t, def)
	ts.setAll(0)

	ts.markEmpty(0)
	if ts.probedEmpty {
		t.Fatal("declared empty after one miss with MAX_UNPOPULATED = 2")
	}
	ts.markEmpty(1)
	if !ts.probedEmpty {
		t.Fatal("not declared empty after two misses with MAX_UNPOPULATED = 2")
	}
	if !ts.emptyFor(0) {
		t.Error("emptyFor = false on a tray declared empty by probing")
	}
	// ...even though four slots still carry step-0 material in the model.
	if _, _, ok := ts.nextPick(0, 0); ok {
		t.Error("a tray declared empty still offered a slot")
	}
	ts.setAll(0)
	if ts.probedEmpty || ts.emptyFor(0) {
		t.Error("set-full did not clear the probing declaration")
	}
}

// TestEndlessTray covers ROWS = COLS = 0: one position, no bookkeeping — never
// full, never empty except by probing.
func TestEndlessTray(t *testing.T) {
	ts := newTestTray(t, TrayDef{
		ID: 2, Rows: 0, Cols: 0,
		First:          pnproute.Point{X: 50, Y: 60},
		MaxUnpopulated: 1,
	})
	if !ts.endless() {
		t.Fatal("endless() = false for a ROWS = COLS = 0 tray")
	}
	if ts.full() {
		t.Error("an endless tray reported full")
	}
	if ts.emptyFor(0) {
		t.Error("an endless tray reported empty before any probing")
	}
	// The single position is retried after a miss: only probing may end the
	// search (§7.1) — bailing after one attempt turned every transient
	// mis-feed into a latched TRAY_EMPTY while the empty pin stayed low.
	slot, next, ok := ts.nextPick(0, 0)
	if !ok || slot != 0 {
		t.Fatalf("endless pick = %d, %v", slot, ok)
	}
	if s, _, ok := ts.nextPick(0, next); !ok || s != 0 {
		t.Error("an endless tray refused to retry its position before probing declared it empty")
	}
	// A place is always possible, and does not record a state that would then
	// have to be picked at a matching step.
	if _, ok := ts.freeSlot(-1); !ok {
		t.Error("an endless tray refused a place")
	}
	ts.markPlaced(0, 7)
	if ts.slots[0] != slotEmpty {
		t.Errorf("an endless tray recorded slot state %d", ts.slots[0])
	}
	// Only probing can declare it empty.
	ts.markEmpty(0)
	if !ts.probedEmpty || !ts.emptyFor(0) {
		t.Error("probing did not declare an endless tray empty")
	}
	nearPoint(t, "endless position", ts.slotPos(0), pnproute.Point{X: 50, Y: 60})
}

// TestEndlessTrayRetriesUntilProbedEmpty: MAX_UNPOPULATED bounds the retries of
// the single position, and only its exhaustion — never a single miss — ends the
// search.
func TestEndlessTrayRetriesUntilProbedEmpty(t *testing.T) {
	ts := newTestTray(t, TrayDef{
		ID: 2, Rows: 0, Cols: 0,
		First:          pnproute.Point{X: 50, Y: 60},
		MaxUnpopulated: 3,
	})
	from := 0
	for i := 0; i < 3; i++ {
		if ts.probedEmpty {
			t.Fatalf("probed empty after %d of 3 misses", i)
		}
		slot, next, ok := ts.nextPick(0, from)
		if !ok || slot != 0 {
			t.Fatalf("miss %d: nextPick = %d, %v — the position must stay on offer", i, slot, ok)
		}
		ts.markEmpty(0)
		from = next
	}
	if !ts.probedEmpty {
		t.Fatal("MAX_UNPOPULATED misses did not declare the tray empty")
	}
	if _, _, ok := ts.nextPick(0, from); ok {
		t.Error("a probed-empty endless tray still offered its position")
	}
}

// TestTraySlotPos checks the linear-index to grid mapping the slot search hands
// to the motion side.
func TestTraySlotPos(t *testing.T) {
	ts := newTestTray(t, grid3x2())
	nearPoint(t, "slot 0", ts.slotPos(0), pnproute.Point{X: 100, Y: 200})
	nearPoint(t, "slot 2", ts.slotPos(2), pnproute.Point{X: 300, Y: 200})
	nearPoint(t, "slot 3", ts.slotPos(3), pnproute.Point{X: 100, Y: 300})
	nearPoint(t, "slot 5", ts.slotPos(5), pnproute.Point{X: 300, Y: 300})
}

// TestTrayProcessStepRange pins the reason slot states are int64: the process
// step comes off a u32 pin, and its top value must not be mistaken for the -1
// that means "empty".
func TestTrayProcessStepRange(t *testing.T) {
	ts := newTestTray(t, grid3x2())
	const top = int64(math.MaxUint32)
	ts.setAll(top)
	if ts.emptyFor(top) {
		t.Error("a tray full of top-value material reported empty")
	}
	if !ts.full() {
		t.Error("a tray full of top-value material reported not full")
	}
	if s, _, ok := ts.nextPick(top, 0); !ok || s != 0 {
		t.Errorf("top-value pick = %d, %v; want slot 0", s, ok)
	}
}

// TestHeldMaterialRecords covers the per-picker held records of D20 on a
// two-picker world: the engine asks which picker is free and which one holds the
// job's material, and never assumes picker 0 for either.
func TestHeldMaterialRecords(t *testing.T) {
	w := &world{logger: testLogger(), held: make([]heldMaterial, 2)}

	if n, ok := w.freePicker(); !ok || n != 0 {
		t.Errorf("freePicker on an empty world = %d, %v; want picker 0", n, ok)
	}
	if _, ok := w.holderOf(20); ok {
		t.Error("holderOf found a holder with nothing held")
	}

	// Picker 0 takes material from station 10: the next free picker is 1.
	w.setHeld(0, 10, false, 0, true)
	if n, ok := w.freePicker(); !ok || n != 1 {
		t.Errorf("freePicker = %d, %v; want picker 1", n, ok)
	}
	if n, ok := w.holderOf(10); !ok || n != 0 {
		t.Errorf("holderOf(10) = %d, %v; want picker 0", n, ok)
	}
	if !w.heldDirty {
		t.Error("setHeld did not mark the record for persistence")
	}

	// Picker 1 removes an occupant from station 20 — the §8 swap. Now no picker
	// is free, and each station has its own holder.
	w.setHeld(1, 20, false, 0, true)
	if _, ok := w.freePicker(); ok {
		t.Error("freePicker found one with both pickers loaded")
	}
	if n, ok := w.holderOf(20); !ok || n != 1 {
		t.Errorf("holderOf(20) = %d, %v; want picker 1", n, ok)
	}

	w.clearHeld(0)
	if n, ok := w.freePicker(); !ok || n != 0 {
		t.Errorf("freePicker after clearing 0 = %d, %v", n, ok)
	}
	if _, ok := w.holderOf(10); ok {
		t.Error("holderOf(10) still found a holder after clearHeld")
	}

	w.clearAllHeld()
	for n := range w.held {
		if w.held[n].present {
			t.Errorf("picker %d still holds material after clearAllHeld", n)
		}
	}
	// Out-of-range indices are ignored rather than panicking: they can only come
	// from a persisted record written by a differently configured instance.
	w.setHeld(5, 10, false, 0, true)
	w.clearHeld(-1)
}

// ---------------------------------------------------------------------------
// Through the control loop and the pins
// ---------------------------------------------------------------------------

// TestTraySelectionPins drives the tray-id selector the way a PLC does. The
// fixture's config has TRAYDEF 1 (a 10x4 grid) and TRAYDEF 2 (endless).
func TestTraySelectionPins(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]

	// tray-id starts at 0: no geometry, and the station says neither empty nor
	// full — it has no slot state to report on.
	f.consistently("no geometry without a tray-id", func() bool {
		return !f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})

	// A grid: 40 empty slots, so empty and not full.
	tray.trayID.Set(1)
	f.eventually("grid selected", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
	f.pulse(tray.setFull)
	f.eventually("tray full", func() bool {
		return f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})

	// A tray-id change is a different tray in the station: the slot state cannot
	// carry over (D17). TRAYDEF 2 is the endless one — never full, and only
	// probing can call it empty.
	tray.trayID.Set(2)
	f.eventually("endless tray selected", func() bool {
		return !f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})

	// Back to the grid: the state it had before is gone, not remembered.
	tray.trayID.Set(1)
	f.eventually("grid reselected empty", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})

	// An id no TRAYDEF matches leaves the station without geometry. It is a PLC
	// value, not configuration, so it does not fail anything here — a job against
	// the station is what raises INVALID_TRAY_ID.
	tray.trayID.Set(99)
	f.eventually("unknown tray-id leaves no geometry", func() bool {
		return !f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
	if w := f.stopped(); w.trays[0].geom != nil {
		t.Error("an unknown tray-id selected a geometry")
	}
}

// TestTrayDefaultTrayDef drives a station whose tray-id nobody wires:
// DEFAULT_TRAYDEF seeds the pin, so the station comes up with geometry and is
// usable without a selector. The fixture's own tray 10 has no default and is
// the control — it stays geometry-less right next to it.
func TestTrayDefaultTrayDef(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{ini: `
[PNPTASK_TRAY_FIXED]
ID = 11
Z_PICK = 2.5
DEFAULT_TRAYDEF = 1
`})
	// TRAYDEF 1 is the 10x4 grid: 40 empty slots, so empty and not full — the
	// state a freshly selected geometry starts in, reached with nothing wired.
	f.eventually("seeded station has geometry", func() bool {
		return f.bit("tray.11.empty") && !f.bit("tray.11.full")
	})
	f.consistently("unseeded station still has none", func() bool {
		return !f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
	w := f.stopped()
	if w.trays[1].geom == nil || w.trays[1].trayID != 1 {
		t.Errorf("seeded station: tray-id = %d, geom = %v; want 1 with geometry",
			w.trays[1].trayID, w.trays[1].geom)
	}
	if w.trays[0].geom != nil {
		t.Error("the station without a DEFAULT_TRAYDEF selected a geometry")
	}
}

// TestTraySetFullFollowsProcessStep pins D8: set-full writes the *current*
// process-step pin value into every slot, and "empty" is measured against the
// step the PLC is asking about.
func TestTraySetFullFollowsProcessStep(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]
	tray.trayID.Set(1)
	f.m.pins.processStep.Set(3)
	f.eventually("grid selected", func() bool { return f.bit("tray.10.empty") })

	f.pulse(tray.setFull)
	f.eventually("full of step-3 material", func() bool {
		return f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})

	// The same tray has nothing to offer a step-4 pick, while still being full.
	f.m.pins.processStep.Set(4)
	f.eventually("empty for step 4", func() bool {
		return f.bit("tray.10.empty") && f.bit("tray.10.full")
	})

	f.pulse(tray.setEmpty)
	f.eventually("emptied", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
}

// TestTrayResetPinsHeldAtStartupAreNotEdges covers D26 for the two tray reset
// pins: a level a PLC latched across a stmakd restart is not a fresh request, and
// acting on it would wipe (or forge) the slot state that was just restored.
func TestTrayResetPinsHeldAtStartupAreNotEdges(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.trays[0].setFull.Set(true)
			m.pins.processStep.Set(1)
		},
	})
	f.eventually("grid selected", func() bool { return f.bit("tray.10.empty") })
	f.consistently("a held set-full is not an edge", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
	// Released and pressed again it is a request.
	f.pulse(f.m.pins.trays[0].setFull)
	f.eventually("tray full after a real edge", func() bool { return f.bit("tray.10.full") })
}

// TestTrayBothResetPinsAtOnce: a contradictory request is ignored rather than
// resolved on the operator's behalf, like the manual picker pins.
func TestTrayBothResetPinsAtOnce(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]
	tray.trayID.Set(1)
	f.eventually("grid selected", func() bool { return f.bit("tray.10.empty") })

	tray.setFull.Set(false)
	tray.setEmpty.Set(false)
	time.Sleep(10 * pollInterval)
	tray.setFull.Set(true)
	tray.setEmpty.Set(true)
	f.consistently("both edges at once change nothing", func() bool {
		return f.bit("tray.10.empty") && !f.bit("tray.10.full")
	})
}

// TestTrayResetsDuringHomingSnapshotAndLastWin: a reset pressed while a long
// operation keeps step() from running must (a) apply the process-step the
// operator saw at press time, not the one the PLC staged later for its next
// job, and (b) let a later press overwrite an earlier one instead of the two
// colliding as a "contradictory pair" at consumption.
func TestTrayResetsDuringHomingSnapshotAndLastWin(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]
	tray.trayID.Set(1)
	f.eventually("grid selected", func() bool { return f.bit("tray.10.empty") })

	f.mot.setHomingCycles(1000) // ~1 s of homing at the test poll rate
	f.machineOn()
	f.setBit(f.m.pins.home, true)
	f.eventually("homing started", func() bool { return f.mot.called("JointHome") })

	// Operator: empty... no, full of step-3 material (the correction wins).
	f.pulse(tray.setEmpty)
	time.Sleep(5 * pollInterval)
	f.m.pins.processStep.Set(3)
	f.pulse(tray.setFull)
	time.Sleep(5 * pollInterval)
	// The PLC stages the step for its NEXT job while homing still runs.
	f.m.pins.processStep.Set(4)

	f.eventually("homed", func() bool { return f.bit("homed") })
	// Slots carry the press-time step 3: full, and "empty" is true for the
	// staged step 4 but false once the PLC asks about step 3 again.
	f.eventually("full of step-3 material", func() bool {
		return f.bit("tray.10.full") && f.bit("tray.10.empty")
	})
	f.m.pins.processStep.Set(3)
	f.eventually("has step-3 material", func() bool {
		return f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})
}

// TestTrayIDBlipKeepsState: a selector dropout (PLC reboot while stmakd runs,
// tray-id X -> 0 -> X) is not a tray change — the live slot state is parked and
// adopted back, exactly like a restart parks a restored record.
func TestTrayIDBlipKeepsState(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]
	tray.trayID.Set(1)
	f.m.pins.processStep.Set(1)
	f.eventually("grid selected", func() bool { return f.bit("tray.10.empty") })
	f.pulse(tray.setFull)
	f.eventually("full", func() bool { return f.bit("tray.10.full") })

	tray.trayID.Set(0)
	f.eventually("geometry parked", func() bool {
		return !f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})
	tray.trayID.Set(1)
	f.eventually("state adopted back after the blip", func() bool { return f.bit("tray.10.full") })
	f.consistently("still full of step-1 material", func() bool {
		return f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})
}

// TestTrayResetWithoutGeometryIgnored: set-full/set-empty on a station whose
// tray-id names no geometry is a refusal, not a phantom success — there are no
// slots to set, and "success" would clobber the persisted record.
func TestTrayResetWithoutGeometryIgnored(t *testing.T) {
	f := newMachineFixture(t)
	tray := f.m.pins.trays[0]

	f.pulse(tray.setFull)
	f.consistently("no geometry, nothing declared", func() bool {
		return !f.bit("tray.10.full") && !f.bit("tray.10.empty")
	})

	w := f.stopped()
	if w.trays[0].dirty {
		t.Error("a geometry-less reset armed a persistence write")
	}
}

// TestEstopClearsHeldRecords: estop drops the picker outputs (D14), so whatever
// they held is on the table and the held records would be fiction.
func TestEstopClearsHeldRecords(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{
		args: []string{"pickers=2"},
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.setHeld(1, 20, false, 0, true)
			m.pins.pickers[1].close.Set(true)
		},
	})
	f.machineOn()
	f.setBit(f.m.pins.estopOn, true)
	f.eventually("machine off on estop", func() bool { return !f.bit("machine-is-on") })
	f.eventually("picker released", func() bool { return !f.bit("picker.1.close") })

	w := f.stopped()
	if w.held[1].present {
		t.Error("a held-material record survived the estop that released the picker")
	}
}

// TestManualOpenClearsHeldRecord: an operator opening a picker by hand has taken
// the material out of the machine's hands, so the record goes with it.
func TestManualOpenClearsHeldRecord(t *testing.T) {
	f := newMachineFixtureOpts(t, fixtureOpts{
		prep: func(_ *testing.T, m *pnptaskModule) {
			m.world.setHeld(0, 10, false, 0, true)
			m.pins.pickers[0].close.Set(true)
		},
	})
	f.setBit(f.m.pins.autoEnable, false)
	f.pulse(f.m.pins.pickers[0].manualOpen)
	f.eventually("picker opened", func() bool { return !f.bit("picker.0.close") })

	w := f.stopped()
	if w.held[0].present {
		t.Error("the held-material record survived a manual open")
	}
}

// TestMarkPlacedClearsProbing: material the module itself placed refutes a
// probed-empty verdict. A tray latched empty by probing that stayed latched
// through a place would refuse every later pick with TRAY_EMPTY — an endless
// place-then-pick transfer station would be permanently dead.
func TestMarkPlacedClearsProbing(t *testing.T) {
	d := grid3x2()
	d.MaxUnpopulated = 2
	ts := newTestTray(t, d)
	ts.setAll(0)

	ts.markEmpty(0)
	ts.markEmpty(1)
	if !ts.probedEmpty || !ts.emptyFor(0) {
		t.Fatalf("probing did not latch: misses = %d, probedEmpty = %v", ts.misses, ts.probedEmpty)
	}

	ts.markPlaced(0, 0)
	if ts.probedEmpty || ts.misses != 0 {
		t.Errorf("after a place: misses = %d, probedEmpty = %v, want 0/false", ts.misses, ts.probedEmpty)
	}
	if ts.emptyFor(0) {
		t.Error("the tray still reports empty for the step that was just placed")
	}
	if slot, _, ok := ts.nextPick(0, 0); !ok || slot != 0 {
		t.Errorf("nextPick = %d, %v; want the placed slot 0", slot, ok)
	}

	// The endless tray has no slot state, only the probing counter — the same
	// rule applies: a place re-arms the retries.
	et := newTestTray(t, TrayDef{ID: 2, First: pnproute.Point{X: 50, Y: 50}, MaxUnpopulated: 1, Dir: defaultDirMode})
	et.markEmpty(0)
	if !et.probedEmpty {
		t.Fatal("endless tray did not latch probed-empty")
	}
	et.markPlaced(0, 3)
	if et.probedEmpty || et.emptyFor(3) {
		t.Error("the endless tray stayed probed-empty although material was placed into it")
	}
}
