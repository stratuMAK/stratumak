// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// Unit tests for interpreter offset arithmetic (G52/G92, G5x, G10), driven
// through the REAL rs274ngc interpreter over the same C shim milltask uses,
// and asserted against interpreter-internal state via the interp_inspect.go
// accessors.
//
// These replace the stalled C++/Catch suite in unit_tests/interp (which has
// not compiled since the embedded-Python removal — tests_main.cc still wants
// python_plugin.hh). Two deliberate differences from that suite:
//
//   - The canon is the real Go canon behind a recording motion sink, not
//     saicanon writing to a log file. That removes the global-state dance
//     (reset_internals() / _outfile / delete-and-recreate) the old harness
//     needed between sections — each fixture here is independent.
//   - Assertions go through the shim, so a shim regression fails the test.
//     The old suite linked the C++ Interp class directly and could stay green
//     with the ABI that milltask actually uses broken underneath it.
//
// Expected values are carried over from the 2019 suite where they still hold;
// where the interpreter's behavior differs, the divergence is stated inline
// rather than re-blessed silently.

import (
	"log/slog"
	"math"
	"os"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

// interpFixture is a live interpreter wired to a real canon + recording
// motion sink, ready for MDI-style ExecuteString calls.
type interpFixture struct {
	interp *CInterp
	task   *Task
	motion *recordingMotion
}

// Machine-native linear unit factors, as [TRAJ]LINEAR_UNITS yields them.
// This is what GET_EXTERNAL_LENGTH_UNITS reports (canon_getters.go), and it
// does NOT vary with the program's active G20/G21.
const (
	machineUnitsMM   = 1.0
	machineUnitsInch = 1.0 / 25.4
)

// newInterpFixture builds a hermetic interpreter instance: INI parsed from a
// string, parameter file in a temp dir, canon bound to a recording motion
// sink. Everything is torn down via t.Cleanup.
//
// linearUnits is the machine's native linear unit factor — pass
// machineUnitsMM or machineUnitsInch. It is the only knob that changes
// between the two offset fixtures.
//
// Mirrors runNGCViaInterpRec (blend_integration_test.go) but stops short of
// opening a program: these tests drive single MDI lines instead.
func newInterpFixture(t *testing.T, linearUnits float64) *interpFixture {
	t.Helper()

	mot := &recordingMotion{}
	st := &testStatus{}
	st.inPosition.Store(true)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, &mockIO{}, st, logger)
	applyBlendLimits(task)
	task.maxAcceleration = 600
	task.numJoints = 3
	task.linearUnits = linearUnits // feeds GET_EXTERNAL_LENGTH_UNITS

	iniUnits := "mm"
	if linearUnits != machineUnitsMM {
		iniUnits = "inch"
	}

	dir := t.TempDir()
	varPath := dir + "/params.var"
	if err := os.WriteFile(varPath, nil, 0o644); err != nil {
		t.Fatalf("write var file: %v", err)
	}
	ini, err := inifile.ParseString("[EMC]\nMACHINE=interptest\n" +
		"[RS274NGC]\nPARAMETER_FILE=" + varPath + "\n" +
		"[TRAJ]\nCOORDINATES=X Y Z\nLINEAR_UNITS=" + iniUnits + "\n[EMCIO]\n")
	if err != nil {
		t.Fatalf("parse ini: %v", err)
	}

	interp, err := NewCInterp()
	if err != nil {
		t.Fatalf("NewCInterp: %v", err)
	}
	t.Cleanup(func() { interp.Destroy() })

	ct := newCanonCallbackTable(task.canon)
	t.Cleanup(ct.release)
	interp.SetCanonCallbacks(ct.ptr())

	accHandle, err := interp.IniLoadAccessor(ini)
	if err != nil {
		t.Fatalf("IniLoadAccessor: %v", err)
	}
	t.Cleanup(func() { FreeIniAccessor(accHandle) })

	paramIO := newInterpParamIOFile(varPath)
	t.Cleanup(paramIO.destroy)
	paramIO.install(interp)

	interp.SetTaskMode(1)
	if err := interp.Init(); err != nil {
		t.Fatalf("interp.Init: %v [%q]", err, interp.ErrorText(InterpError))
	}
	task.SetInterpreter(interp)
	interp.RegisterAllMcodeSlots()
	if err := interp.Synch(); err != nil {
		t.Fatalf("interp.Synch: %v", err)
	}

	task.StartSequencer()
	t.Cleanup(task.StopSequencer)
	setActiveCanon(task.canon)
	t.Cleanup(clearActiveCanon)

	return &interpFixture{interp: interp, task: task, motion: mot}
}

// mdi executes one MDI line and fails the test if the interpreter rejects it.
func (f *interpFixture) mdi(t *testing.T, line string) {
	t.Helper()
	if _, err := f.interp.ExecuteString(line); err != nil {
		t.Fatalf("execute %q: %v [%q]", line, err, f.interp.ErrorText(InterpError))
	}
}

// interpFuzz is the tolerance the 2019 Catch suite used (INTERP_FUZZ).
const interpFuzz = 1e-10

func checkFuzz(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > interpFuzz {
		t.Errorf("%s = %.15g, want %.15g (tol %g)", what, got, want, interpFuzz)
	}
}

// TestInterpG52WithoutRotation ports the "G52 without rotation" section of
// unit_tests/interp/test_interp_basics.cc.
//
// G52 sets the axis offset. The live offset (_setup.axis_offset_*) is held in
// PROGRAM units, but the numbered parameters 5211.. are written through
// PROGRAM_TO_USER_LEN, i.e. converted to the MACHINE's native linear units:
//
//	pars[5211] = axis_offset_x / progFactor * GET_EXTERNAL_LENGTH_UNITS()
//
// So the same "G52 X25.4" under G21 stores 25.4 on a metric machine and 1.0
// on an inch machine. That crossing is the point of the test and is entirely
// invisible from the canon stream, which only carries absolute positions.
//
// The 2019 Catch suite asserted the 1.0 case only. It never established a
// machine unit factor at all (saicanon's _external_length_units is set in
// driver.cc, which the test harness does not link), so that expectation
// silently encoded an inch machine. Both machines are pinned here.
//
// Note the program is forced to G21 in both cases rather than relying on the
// startup default. An inch machine starts the interpreter in G20 — that is
// correct and is 2.9 parity, not a gomc quirk: 2.9's INIT_CANON derives
// canon.lengthUnits from [TRAJ]LINEAR_UNITS the same way machineCanonUnits
// does, and Interp::init reads it back via get_external_length_unit_type().
// Without the explicit G21 the inch case would run an inch program on an inch
// machine, the conversion would collapse to an identity, and the test would
// prove nothing.
func TestInterpG52WithoutRotation(t *testing.T) {
	tests := []struct {
		name string
		// machine native units and the resulting #5211/#5212 value for a
		// 25.4 mm program offset
		linearUnits float64
		wantParam   float64
	}{
		{"metric machine", machineUnitsMM, 25.4},
		{"inch machine", machineUnitsInch, 1.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newInterpFixture(t, tc.linearUnits)

			// Run the program in mm in both cases — only the machine's
			// native units differ, so the crossing is isolated.
			f.mdi(t, "G21")
			if got := f.interp.LengthUnits(); got != LengthUnitsMM {
				t.Fatalf("program length units after G21 = %d, want %d (mm)", got, LengthUnitsMM)
			}
			// All offsets start clear.
			checkFuzz(t, f.interp.Parameter(ParamG92X), 0.0, "#5211 at init")
			checkFuzz(t, f.interp.Parameter(ParamG92Y), 0.0, "#5212 at init")

			f.mdi(t, "G52 X25.4 Y0")
			checkFuzz(t, f.interp.Parameter(ParamG92X), tc.wantParam, "#5211 after G52 X25.4")
			checkFuzz(t, f.interp.Parameter(ParamG92Y), 0.0, "#5212 after G52 Y0")

			// A second G52 naming only Y must leave the established X offset alone.
			f.mdi(t, "G52 Y25.4")
			checkFuzz(t, f.interp.Parameter(ParamG92X), tc.wantParam, "#5211 after G52 Y25.4")
			checkFuzz(t, f.interp.Parameter(ParamG92Y), tc.wantParam, "#5212 after G52 Y25.4")

			// The live axis offsets are in PROGRAM units (mm here) on both
			// machines — they must not follow the parameter conversion. The
			// old suite checked only the parameters, so a drift between the
			// stored value and the offset that actually moves the machine
			// would have gone unseen.
			checkFuzz(t, f.interp.CurrentAxisOffset(AxisX), 25.4, "axis offset X (program mm)")
			checkFuzz(t, f.interp.CurrentAxisOffset(AxisY), 25.4, "axis offset Y (program mm)")
		})
	}
}
