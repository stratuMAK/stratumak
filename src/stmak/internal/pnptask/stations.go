// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"math"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// SlotCount returns how many slots the tray has. An endless tray and a
// single-position tray both have exactly one.
func (d TrayDef) SlotCount() int {
	if d.Endless() {
		return 1
	}
	return d.Rows * d.Cols
}

// SlotPos returns the absolute machine coordinates of slot (col, row).
//
// The grid axes are tilted by Angle and the two pitches come from LAST−FIRST
// expressed in that rotated frame (D24), so slot (Cols−1, Rows−1) lands exactly
// on LAST at any angle. A tray without LAST — endless or single-position — has
// one position, at FIRST.
//
// Out-of-range indices are not rejected here: the callers (config validation
// and the slot search) iterate the tray's own extent, and clamping or erroring
// on an index would only hide a bug in them.
func (d TrayDef) SlotPos(col, row int) pnproute.Point {
	if !d.HasLast {
		return d.First
	}
	dx, dy := gridSpan(d)
	local := pnproute.Point{
		X: dx * gridFraction(col, d.Cols),
		Y: dy * gridFraction(row, d.Rows),
	}
	s, c := math.Sincos(d.Angle)
	return pnproute.Point{
		X: d.First.X + local.X*c - local.Y*s,
		Y: d.First.Y + local.X*s + local.Y*c,
	}
}

// gridFraction is how far along an axis index i sits, 0 at the first slot and 1
// at the last. A one-slot axis has no span to divide, so it stays at 0.
func gridFraction(i, n int) float64 {
	if n < 2 {
		return 0
	}
	return float64(i) / float64(n-1)
}

// gridSpan returns Last−First expressed in the tray's own (Angle-rotated)
// frame: the total travel along the column and the row axis of the grid.
func gridSpan(d TrayDef) (col, row float64) {
	dx := d.Last.X - d.First.X
	dy := d.Last.Y - d.First.Y
	s, c := math.Sincos(-d.Angle)
	return dx*c - dy*s, dx*s + dy*c
}

// SlotAxis names one of the two indices of a tray grid.
type SlotAxis int

const (
	// AxisCol is the column index, running FIRST -> LAST_X.
	AxisCol SlotAxis = iota
	// AxisRow is the row index, running FIRST -> LAST_Y.
	AxisRow
)

func (a SlotAxis) String() string {
	if a == AxisRow {
		return "R"
	}
	return "C"
}

// other returns the axis that is not a.
func (a SlotAxis) other() SlotAxis {
	if a == AxisRow {
		return AxisCol
	}
	return AxisRow
}

// DirMode is a parsed [PNPTASK_TRAYDEF_n]DIR_MODE: the order slots are visited
// in when a pick looks for material or a place looks for a free slot.
//
// The syntax is two axis tokens, each an axis letter (C or R) followed by a
// direction (+ or -), plus an optional trailing "~" for meander:
//
//	C+R+     columns left to right, then the next row upwards
//	R-C+     rows top to bottom within a column, then the next column
//	C+R+~    same as C+R+, but every second row runs backwards
//
// The first token is the fast-varying (inner) axis, the second the slow one.
// Both axes must appear exactly once.
type DirMode struct {
	// Primary is the inner axis — the one that advances from slot to slot.
	Primary SlotAxis
	// PrimaryUp and SecondaryUp are the directions the two axes run in.
	PrimaryUp   bool
	SecondaryUp bool
	// Meander reverses the primary direction on every second pass, so the
	// head does not travel back across the whole tray between passes.
	Meander bool
}

// Secondary is the outer axis, implied by Primary.
func (m DirMode) Secondary() SlotAxis { return m.Primary.other() }

// String renders the mode back in DIR_MODE syntax.
func (m DirMode) String() string {
	s := m.Primary.String() + sign(m.PrimaryUp) + m.Secondary().String() + sign(m.SecondaryUp)
	if m.Meander {
		s += "~"
	}
	return s
}

func sign(up bool) string {
	if up {
		return "+"
	}
	return "-"
}

// defaultDirMode is what an omitted DIR_MODE means: columns left to right, rows
// bottom to top, no meander — the reading order of a tray whose FIRST slot is
// the lower-left one.
var defaultDirMode = DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true}

// parseDirMode parses a DIR_MODE value. An empty string yields the default
// mode; anything that is not well-formed is an error rather than a fallback —
// a mis-typed mode would otherwise silently reorder a whole tray.
func parseDirMode(s string) (DirMode, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return defaultDirMode, nil
	}
	var m DirMode
	rest := s
	primary, primaryUp, rest, err := parseDirToken(rest)
	if err != nil {
		return m, err
	}
	secondary, secondaryUp, rest, err := parseDirToken(rest)
	if err != nil {
		return m, err
	}
	if primary == secondary {
		return m, fmt.Errorf("%q: axis %s given twice, expected one C and one R token", s, primary)
	}
	if rest == "~" {
		m.Meander = true
		rest = ""
	}
	if rest != "" {
		return m, fmt.Errorf("%q: trailing %q, expected two axis tokens and an optional %q", s, rest, "~")
	}
	m.Primary, m.PrimaryUp, m.SecondaryUp = primary, primaryUp, secondaryUp
	return m, nil
}

// parseDirToken consumes one "<axis><sign>" token from the front of s.
func parseDirToken(s string) (axis SlotAxis, up bool, rest string, err error) {
	if len(s) < 2 {
		return 0, false, s, fmt.Errorf("%q: expected an axis token (C+, C-, R+ or R-)", s)
	}
	switch s[0] {
	case 'C':
		axis = AxisCol
	case 'R':
		axis = AxisRow
	default:
		return 0, false, s, fmt.Errorf("%q: unknown axis %q, expected C or R", s, string(s[0]))
	}
	switch s[1] {
	case '+':
		up = true
	case '-':
		up = false
	default:
		return 0, false, s, fmt.Errorf("%q: unknown direction %q, expected + or -", s, string(s[1]))
	}
	return axis, up, s[2:], nil
}
