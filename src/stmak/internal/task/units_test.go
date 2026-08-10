// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"math"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// The machine-units->mm conversion of the joint/axis config pushed to motion is
// covered where that push lives, in internal/motsetup; what is left here is the
// canon-side half of the same unit question.

const inch = 25.4

func closeTo(got, want float64) bool {
	if math.Abs(want) > 1e50 { // sentinel (±1e99): just check same order of magnitude/sign
		return math.Signbit(got) == math.Signbit(want) && math.Abs(got) > 1e50
	}
	return math.Abs(got-want) < 1e-6*math.Max(1, math.Abs(want))
}

// The canonical modal length units must start in the MACHINE's units (G20 on
// an inch machine, G21 on mm), like the C canon's INIT_CANON — the interpreter
// reads GetExternalLengthUnitType at init. A hardcoded mm start would run
// unit-less G-code 25.4x small on an inch machine.
func TestMachineCanonUnits(t *testing.T) {
	cases := []struct {
		linearUnits float64
		want        int32
	}{
		{1.0, CanonUnitsMM},
		{1.0 / inch, CanonUnitsInches},
		{0, CanonUnitsMM},   // pre-config default
		{0.5, CanonUnitsMM}, // non-standard: mm fallback, like INIT_CANON
	}
	for _, c := range cases {
		if got := machineCanonUnits(c.linearUnits); got != c.want {
			t.Errorf("machineCanonUnits(%g) = %d, want %d", c.linearUnits, got, c.want)
		}
	}
}

func TestCanonStartsInMachineUnits(t *testing.T) {
	task := &Task{linearUnits: 1.0 / inch}
	c := NewCanon(task)
	if c.state.lengthUnits != CanonUnitsInches {
		t.Fatalf("NewCanon on inch machine: lengthUnits = %d, want inches", c.state.lengthUnits)
	}
	// A program's G21 changes the modal state; the reset must restore G20.
	c.UseLengthUnits(CanonUnitsMM)
	c.InitCanon()
	if c.state.lengthUnits != CanonUnitsInches {
		t.Fatalf("InitCanon on inch machine: lengthUnits = %d, want inches", c.state.lengthUnits)
	}
	// loadTraj refreshes the (pre-config) canon once LINEAR_UNITS is known.
	ini, err := inifile.ParseString(`[KINS]
JOINTS=3
[TRAJ]
COORDINATES=X Y Z
LINEAR_UNITS=inch
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	task2 := &Task{}
	task2.canon = NewCanon(task2) // pre-config: defaults to mm
	if err := loadTraj(ini, task2); err != nil {
		t.Fatalf("loadTraj: %v", err)
	}
	if task2.canon.state.lengthUnits != CanonUnitsInches {
		t.Fatalf("loadTraj(inch): lengthUnits = %d, want inches", task2.canon.state.lengthUnits)
	}
}

// Tool offsets arrive from the interpreter in PROGRAM units
// (interp_convert.cc applies USER_TO_PROGRAM_LEN before the canon call) and
// must be stored in internal mm — like the C canon's FROM_PROG_LEN — because
// toAbsolute adds them to mm coordinates. The getters hand them back in
// program units (TO_PROG_LEN). Angular components are degrees throughout.
func TestUseToolLengthOffsetProgramUnits(t *testing.T) {
	task := &Task{linearUnits: 1.0 / inch}
	c := NewCanon(task) // starts in G20 on the inch machine
	c.UseToolLengthOffset(1, 0, 0.5, 5, 0, 0, 0, 0, 2)
	got := c.state.toolOffset
	if !closeTo(got.X, 1*inch) || !closeTo(got.Z, 0.5*inch) || !closeTo(got.W, 2*inch) {
		t.Errorf("linear offsets not converted to mm: X=%g Z=%g W=%g", got.X, got.Z, got.W)
	}
	if !closeTo(got.A, 5) {
		t.Errorf("angular offset wrongly scaled: A=%g", got.A)
	}
	// Round-trip: getters return program units.
	if x, _ := c.GetExternalToolLengthXoffset(); !closeTo(x, 1) {
		t.Errorf("GetExternalToolLengthXoffset = %g, want 1 (program units)", x)
	}
	if a, _ := c.GetExternalToolLengthAoffset(); !closeTo(a, 5) {
		t.Errorf("GetExternalToolLengthAoffset = %g, want 5 (degrees)", a)
	}
	// After switching to G21 the getter reports the same physical offset in mm.
	c.UseLengthUnits(CanonUnitsMM)
	if x, _ := c.GetExternalToolLengthXoffset(); !closeTo(x, 1*inch) {
		t.Errorf("GetExternalToolLengthXoffset after G21 = %g, want 25.4", x)
	}
}
