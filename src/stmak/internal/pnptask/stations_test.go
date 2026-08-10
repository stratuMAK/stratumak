// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"strings"
	"testing"

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
