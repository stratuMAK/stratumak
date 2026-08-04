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
	"fmt"
	"log/slog"
	"math"
	"os"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// interpFixture is a live interpreter wired to a real canon + recording
// motion sink, ready for MDI-style ExecuteString calls.
type interpFixture struct {
	interp *CInterp
	task   *Task
	motion *recordingMotion
	// tools is the machine's tool table. It starts empty; a test that needs
	// T/M6/G43 populates it with setTool before executing anything.
	tools *fakeToolTable
	io    *fakeToolIO
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

	// A tool table and an IO controller that actually track state. Both are
	// installed before Init/Synch, which read them. They stay empty unless a
	// test populates f.tools, so this costs the offset tests nothing.
	tools := &fakeToolTable{}
	installFakeToolTable(t, tools)
	io := newFakeToolIO(tools)

	mot := &recordingMotion{}
	st := &testStatus{}
	st.inPosition.Store(true)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, io, st, logger)
	applyBlendLimits(task) // XYZ vel/acc limits, so the sequencer has real numbers
	task.maxAcceleration = 600
	task.linearUnits = linearUnits // feeds GET_EXTERNAL_LENGTH_UNITS

	// XYZABC, matching the 2019 harness (saicanon hardcoded
	// GET_EXTERNAL_AXIS_MASK() = 0x3f). The rotary axes carry no interesting
	// dynamics here — the offset tests only ever assert positions — but they
	// must exist or the interpreter rejects an A/B/C word outright.
	task.axisMask = 0x3f
	task.numJoints = 6
	for a := AxisA; a <= AxisC; a++ {
		task.axisMaxVel[a] = 100
		task.axisMaxAcc[a] = 1000
	}

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
		"[TRAJ]\nCOORDINATES=X Y Z A B C\nLINEAR_UNITS=" + iniUnits + "\n[EMCIO]\n")
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

	return &interpFixture{interp: interp, task: task, motion: mot, tools: tools, io: io}
}

// mdi executes one MDI line and fails the test if the interpreter rejects it.
//
// A line that returns INTERP_EXECUTE_FINISH (tool change, probe, dwell,
// synchronised M-code) is driven to completion using the same handshake the
// production MDI path uses (commands.go): queue a wait-for-motion, let the
// sequencer drain, resync the canon end point and the interpreter, then
// continue. Short-cutting that — just re-calling Execute — would let an M6
// "succeed" without the tool change ever happening.
func (f *interpFixture) mdi(t *testing.T, line string) {
	t.Helper()

	rc, err := f.interp.ExecuteString(line)
	if err != nil {
		t.Fatalf("execute %q: %v [%q]", line, err, f.interp.ErrorText(InterpError))
	}
	if rc == InterpExecuteFinish {
		f.drain(t, line)
	}

	// Continue any subroutine the line entered. The bound is CALL LEVEL, not
	// the return code: a plain MDI line (or a single top-level queue-buster
	// like M6) comes back EXECUTE_FINISH with call level 0 and its block is
	// already complete — calling Execute again there re-runs it forever.
	// finishMDI in commands.go has the same shape and the same reasoning.
	for f.interp.CallLevel() > 0 {
		before := f.task.canon.enqueued()
		if _, err := f.interp.Execute(); err != nil {
			t.Fatalf("execute %q: continuation: %v [%q]", line, err, f.interp.ErrorText(InterpError))
		}
		if f.task.canon.enqueued() != before {
			f.drain(t, line) // the continuation queued work; let it run
			continue
		}
		// Nothing queued — the interpreter still expects a synch per
		// EXECUTE_FINISH before the next continuation.
		f.task.canon.syncEndPointFromMachine()
		if err := f.interp.Synch(); err != nil {
			t.Fatalf("execute %q: interp synch during continuation: %v", line, err)
		}
	}
}

// drain runs the wait-for-motion / resync handshake the production MDI and
// AUTO paths use after an INTERP_EXECUTE_FINISH.
func (f *interpFixture) drain(t *testing.T, line string) {
	t.Helper()

	if err := f.task.EnqueueCmd(waitForMotionSingleton); err != nil {
		t.Fatalf("execute %q: enqueue wait-for-motion: %v", line, err)
	}
	if f.task.waitSequencerDrain() {
		t.Fatalf("execute %q: sequencer aborted while draining", line)
	}
	f.task.canon.syncEndPointFromMachine()
	if err := f.interp.Synch(); err != nil {
		t.Fatalf("execute %q: interp synch after execute_finish: %v", line, err)
	}
}

// newInchFixture is the fixture the ported sections use: an inch machine
// running an inch program. The 2019 suite began every section with "g20" and
// asserted raw parameter values, which only holds when the machine's native
// units match the program's — parameters are stored in machine units (see
// TestInterpG52WithoutRotation). Matching that here keeps the ported
// expectations comparable to the originals digit for digit.
func newInchFixture(t *testing.T) *interpFixture {
	t.Helper()
	f := newInterpFixture(t, machineUnitsInch)
	f.mdi(t, "G20")
	if got := f.interp.LengthUnits(); got != LengthUnitsInches {
		t.Fatalf("program length units after G20 = %d, want %d (inches)", got, LengthUnitsInches)
	}
	return f
}

// interpFuzz is the tolerance the 2019 Catch suite used (INTERP_FUZZ).
const interpFuzz = 1e-10

func checkFuzz(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > interpFuzz {
		t.Errorf("%s = %.15g, want %.15g (tol %g)", what, got, want, interpFuzz)
	}
}

// allAxes is X Y Z A B C — the order the accessors and the work-offset
// parameter blocks both use.
var allAxes = [...]int{AxisX, AxisY, AxisZ, AxisA, AxisB, AxisC}

var axisNames = [...]string{"X", "Y", "Z", "A", "B", "C"}

// checkAxes asserts one value per axis from a per-axis getter.
func checkAxes(t *testing.T, get func(int) float64, want [6]float64, what string) {
	t.Helper()
	for i, ax := range allAxes {
		checkFuzz(t, get(ax), want[i], what+" "+axisNames[i])
	}
}

// workOffsetParam returns the parameter number for one axis of a coordinate
// system, given that system's X parameter (ParamG54X, ParamG55X, ...).
func workOffsetParam(csX, axis int) int { return csX + axis }

// checkWorkOffsetParams asserts the stored work-offset parameters of one
// coordinate system.
func checkWorkOffsetParams(t *testing.T, f *interpFixture, csX int, want [6]float64, what string) {
	t.Helper()
	checkAxes(t, func(ax int) float64 {
		return f.interp.Parameter(workOffsetParam(csX, ax))
	}, want, what)
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
// correct and is 2.9 parity, not a stratuMAK quirk: 2.9's INIT_CANON derives
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

// TestInterpInitPosition ports the "Interp Init" section: a freshly
// initialised interpreter sits at the origin.
func TestInterpInitPosition(t *testing.T) {
	f := newInchFixture(t)
	checkAxes(t, f.interp.CurrentPosition, [6]float64{0, 0, 0, 0, 0, 0}, "current position at init")
	checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{0, 0, 0, 0, 0, 0}, "work offset at init")
	checkAxes(t, f.interp.CurrentAxisOffset, [6]float64{0, 0, 0, 0, 0, 0}, "axis offset at init")
}

// TestInterpG92AndG5xRotation ports the "G92 X and G5x Rotation" section.
//
// A G52 axis offset and a G5x work offset stack, and rotating the coordinate
// system rotates the combined offset. The 2019 suite flagged the R90 result
// with "FIXME this is the wrong behavior but what the interpreter currently
// expects" — that judgement is preserved rather than re-litigated here: the
// test pins current behavior so a change is visible, it does not endorse it.
func TestInterpG92AndG5xRotation(t *testing.T) {
	f := newInchFixture(t)

	f.mdi(t, "G92.1") // clear any axis offset
	checkFuzz(t, f.interp.Parameter(ParamG92X), 0.0, "#5211 after G92.1")

	f.mdi(t, "G52 X1 Y0")
	f.mdi(t, "G10 L2 P0 X1 Y0 Z0 R0")
	checkFuzz(t, f.interp.CurrentPosition(AxisX), -2.0, "X with G52 X1 + G54 X1, unrotated")

	// FIXME (carried over from the 2019 suite): the interpreter rotates the
	// combined G92+G5x offset, which is arguably wrong. Pinned as-is.
	f.mdi(t, "G10 L2 P0 X1 Y0 Z0 R90")
	checkFuzz(t, f.interp.CurrentPosition(AxisX), 0.0, "X after R90")
	checkFuzz(t, f.interp.CurrentPosition(AxisY), 2.0, "Y after R90")
}

// TestInterpG92OffAxisWithRotation ports the "G92 off-axis behavior with
// rotation" section: only X/Y take part in the XY rotation, Z/A/B/C do not.
func TestInterpG92OffAxisWithRotation(t *testing.T) {
	f := newInchFixture(t)

	f.mdi(t, "G92.1")
	f.mdi(t, "G52 X1 Y0")
	f.mdi(t, "G10 L2 P0 X1 Y2 Z3 A4 B5 C6 R90")

	// FIXME (carried over): the X/Y pair follows the same rotation of the
	// combined offset the section above flags.
	checkAxes(t, f.interp.CurrentPosition,
		[6]float64{-2, 2, -3, -4, -5, -6}, "position after rotated G10 L2")
}

// TestInterpG55CoordinateSystem ports the "G55 without rotation" and "G55
// with rotation" sections.
//
// Re-expressed: the originals hacked _setup.parameters[G55_*] directly and
// called the protected convert_coordinate_system(), "to avoid depending on
// other functions". Both are unreachable across a C ABI and neither is a path
// a program can take, so the offsets are established with G10 L2 P2 (which
// leaves the position alone while G55 is inactive) and the position with G0.
// Selecting G55 is then the public equivalent of the direct call.
func TestInterpG55CoordinateSystem(t *testing.T) {
	t.Run("without rotation", func(t *testing.T) {
		f := newInchFixture(t)

		f.mdi(t, "G10 L2 P2 X2 Y3 Z1") // G55 offsets, G55 not yet active
		f.mdi(t, "G0 X1 Y1 Z1")        // machine position 1,1,1 under G54 (no offset)
		checkAxes(t, f.interp.CurrentPosition, [6]float64{1, 1, 1, 0, 0, 0}, "position before G55")

		// Switching coordinate system holds the machine still, so the program
		// position becomes machinePos - newOffset.
		f.mdi(t, "G55")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{-1, -2, 0, 0, 0, 0}, "position after G55")
	})

	t.Run("with rotation", func(t *testing.T) {
		f := newInchFixture(t)

		f.mdi(t, "G10 L2 P2 X2 Y3 Z1 R90")
		f.mdi(t, "G0 X0 Y0 Z0") // machine position 0,0,0
		f.mdi(t, "G55")

		// machine 0,0,0 minus offset (2,3) is (-2,-3), rotated by -90 deg:
		//   x' = -2*cos(-90) - -3*sin(-90) = -3
		//   y' = -2*sin(-90) + -3*cos(-90) =  2
		// Z is untouched by the XY rotation: 0 - 1 = -1.
		checkAxes(t, f.interp.CurrentPosition, [6]float64{-3, 2, -1, 0, 0, 0}, "position after rotated G55")
	})
}

// TestInterpG92SaveRestore ports the "Save / restore of G92 parameters"
// scenario: G92.2 suspends the offset without discarding it, G92.3 restores.
func TestInterpG92SaveRestore(t *testing.T) {
	f := newInchFixture(t)

	f.mdi(t, "G92 X3 Y4 Z5")
	// G92 records the offset that makes the CURRENT point read as X3 Y4 Z5,
	// so the stored parameters are negative.
	checkFuzz(t, f.interp.Parameter(ParamG92X), -3.0, "#5211 after G92")
	checkFuzz(t, f.interp.Parameter(ParamG92Y), -4.0, "#5212 after G92")
	checkFuzz(t, f.interp.Parameter(ParamG92Z), -5.0, "#5213 after G92")
	checkAxes(t, f.interp.CurrentPosition, [6]float64{3, 4, 5, 0, 0, 0}, "position under G92")

	f.mdi(t, "G92.2")
	checkAxes(t, f.interp.CurrentPosition, [6]float64{0, 0, 0, 0, 0, 0}, "position with G92 suspended")
	// Suspended, not cleared — the parameters must survive.
	checkFuzz(t, f.interp.Parameter(ParamG92X), -3.0, "#5211 after G92.2")
	checkFuzz(t, f.interp.Parameter(ParamG92Y), -4.0, "#5212 after G92.2")
	checkFuzz(t, f.interp.Parameter(ParamG92Z), -5.0, "#5213 after G92.2")

	f.mdi(t, "G92.3")
	checkAxes(t, f.interp.CurrentPosition, [6]float64{3, 4, 5, 0, 0, 0}, "position after G92.3")
	checkFuzz(t, f.interp.Parameter(ParamG92X), -3.0, "#5211 after G92.3")
	checkFuzz(t, f.interp.Parameter(ParamG92Y), -4.0, "#5212 after G92.3")
	checkFuzz(t, f.interp.Parameter(ParamG92Z), -5.0, "#5213 after G92.3")
}

// TestInterpConvertG20G21 ports the "Convert G20 / G21" scenario: switching
// program units rescales the linear position and both offset families, while
// the angular axes are left alone.
func TestInterpConvertG20G21(t *testing.T) {
	const mmPerInch = 25.4

	t.Run("current position", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G0 X1 Y2 Z3 A4 B5 C6")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{1, 2, 3, 4, 5, 6}, "position in G20")

		f.mdi(t, "G21")
		if got := f.interp.LengthUnits(); got != LengthUnitsMM {
			t.Fatalf("length units after G21 = %d, want %d", got, LengthUnitsMM)
		}
		checkAxes(t, f.interp.CurrentPosition,
			[6]float64{1 * mmPerInch, 2 * mmPerInch, 3 * mmPerInch, 4, 5, 6}, "position in G21")

		f.mdi(t, "G20")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{1, 2, 3, 4, 5, 6}, "position back in G20")
	})

	t.Run("work offsets", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G10 L2 P1 X1 Y2 Z3 A4 B5 C6")
		f.mdi(t, "G54")
		f.mdi(t, "G0 X0.5 Y0.5")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{1, 2, 3, 4, 5, 6}, "work offset in G20")

		f.mdi(t, "G21")
		checkAxes(t, f.interp.CurrentWorkOffset,
			[6]float64{1 * mmPerInch, 2 * mmPerInch, 3 * mmPerInch, 4, 5, 6}, "work offset in G21")

		f.mdi(t, "G20")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{1, 2, 3, 4, 5, 6}, "work offset back in G20")
	})

	t.Run("axis offsets", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G92 X1 Y2 Z3 A4 B5 C6")
		checkAxes(t, f.interp.CurrentAxisOffset, [6]float64{-1, -2, -3, -4, -5, -6}, "axis offset in G20")

		f.mdi(t, "G21")
		checkAxes(t, f.interp.CurrentAxisOffset,
			[6]float64{-1 * mmPerInch, -2 * mmPerInch, -3 * mmPerInch, -4, -5, -6}, "axis offset in G21")

		f.mdi(t, "G20")
		checkAxes(t, f.interp.CurrentAxisOffset, [6]float64{-1, -2, -3, -4, -5, -6}, "axis offset back in G20")
	})
}

// TestInterpWorkOffsetWhileActive ports the "Applying a work offset while
// active" scenario: G10 L20 sets the offset that makes the current point read
// as the given coordinate, and that offset lands rotated in a rotated system.
func TestInterpWorkOffsetWhileActive(t *testing.T) {
	f := newInchFixture(t)

	f.mdi(t, "G54")
	f.mdi(t, "G10 L2 P1 X0 Y0 Z0 R45")
	f.mdi(t, "G0 G53 X0 Y0 Z0") // machine origin
	checkAxes(t, f.interp.CurrentPosition, [6]float64{0, 0, 0, 0, 0, 0}, "position at machine origin")

	f.mdi(t, "G10 L20 P1 X-1")
	checkAxes(t, f.interp.CurrentWorkOffset,
		[6]float64{math.Sqrt2 / 2, math.Sqrt2 / 2, 0, 0, 0, 0}, "work offset after L20 X-1")

	f.mdi(t, "G0 X0")
	f.mdi(t, "G10 L20 P1 Y-1")
	checkAxes(t, f.interp.CurrentWorkOffset,
		[6]float64{0, math.Sqrt2, 0, 0, 0, 0}, "work offset after L20 Y-1")
}

// TestInterpG10WithG92Active ports the "Call G10 with G92 active" scenario
// (itself derived from the runtests g10-with-g92 case). Each WHEN gets a
// fresh fixture, mirroring how Catch re-runs the GIVEN per section.
func TestInterpG10WithG92Active(t *testing.T) {
	t.Run("no offsets applied", func(t *testing.T) {
		f := newInchFixture(t)
		checkAxes(t, f.interp.CurrentPosition, [6]float64{0, 0, 0, 0, 0, 0}, "position")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{0, 0, 0, 0, 0, 0}, "work offset")
	})

	t.Run("G10 L2 on the active system applies immediately", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G10 L2 P0 X7 Y8 Z9 A10 B11 C12") // P0 = whichever system is active

		if got := f.interp.OriginIndex(); got != 1 {
			t.Fatalf("origin index = %d, want 1 (G54)", got)
		}
		checkWorkOffsetParams(t, f, ParamG54X, [6]float64{7, 8, 9, 10, 11, 12}, "#5221.. after G10 L2 P0")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{7, 8, 9, 10, 11, 12}, "work offset")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{-7, -8, -9, -10, -11, -12}, "position")
	})

	t.Run("G10 L2 on an inactive system stores but does not apply", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G54")
		f.mdi(t, "G10 L2 P2 X7 Y8 Z9 A10 B11 C12") // P2 = G55, not active

		if got := f.interp.OriginIndex(); got != 1 {
			t.Fatalf("origin index = %d, want 1 (G54)", got)
		}
		checkWorkOffsetParams(t, f, ParamG55X, [6]float64{7, 8, 9, 10, 11, 12}, "#5241.. after G10 L2 P2")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{0, 0, 0, 0, 0, 0}, "work offset (unchanged)")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{0, 0, 0, 0, 0, 0}, "position (unchanged)")
	})

	t.Run("G92 active, no work offsets", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G92 X3 Y4 Z5")
		checkAxes(t, f.interp.CurrentPosition, [6]float64{3, 4, 5, 0, 0, 0}, "position due to G92 alone")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{0, 0, 0, 0, 0, 0}, "work offset")
	})

	t.Run("G92 active plus rotated work offset", func(t *testing.T) {
		f := newInchFixture(t)
		f.mdi(t, "G92 X3 Y4 Z5")
		f.mdi(t, "G10 L2 P0 X7 Y8 Z9 A10 B11 C12 R45")

		checkWorkOffsetParams(t, f, ParamG54X, [6]float64{7, 8, 9, 10, 11, 12}, "#5221.. after rotated G10 L2")
		checkAxes(t, f.interp.CurrentWorkOffset, [6]float64{7, 8, 9, 10, 11, 12}, "work offset")

		// FIXME (carried over from the 2019 suite): "this math is wrong but
		// what the current interpreter expects" — the G92 axis offset is
		// folded into the total BEFORE the work-offset rotation is applied,
		// so it gets rotated too. Reproduced exactly, not endorsed.
		const rot = math.Pi / 4
		const axisOffsetX, axisOffsetY = -3.0, -4.0
		const workOffsetX, workOffsetY = 7.0, 8.0
		totalX := axisOffsetX + workOffsetX
		totalY := axisOffsetY + workOffsetY
		wantX := math.Cos(-rot)*-totalX + -math.Sin(-rot)*-totalY
		wantY := math.Sin(-rot)*-totalX + math.Cos(-rot)*-totalY

		checkFuzz(t, f.interp.CurrentPosition(AxisX), wantX, "position X")
		checkFuzz(t, f.interp.CurrentPosition(AxisY), wantY, "position Y")
		checkAxes(t, f.interp.CurrentPosition,
			[6]float64{wantX, wantY, -4, -10, -11, -12}, "position")
	})
}

// setupToolOffsetFixture builds the shared preamble of the three tool-offset
// tests: an inch machine with tool 1 in slot 1, zeroed G54 and zeroed tool
// offsets, tool 1 loaded and G43 active.
//
// The original suite got a tool by writing settings->tool_table[0].toolno = 1
// directly. That is the spindle slot, so what it was really doing was faking
// the result of a tool change. Here the change actually runs: T preps, M6
// loads (the fake IO copies the tool into slot 0, as iocontrol does), and the
// interpreter picks the tool up through its own synch.
func setupToolOffsetFixture(t *testing.T, rotation string) *interpFixture {
	t.Helper()

	f := newInchFixture(t)
	f.tools.setTool(1, 1, 0, 0, 0, 0.5)

	f.mdi(t, "g10 l2 p1 x0 y0 z0"+rotation)
	f.mdi(t, "g54")
	f.mdi(t, "g10 l1 p1 x0 y0 z0")
	f.mdi(t, "t1 m6")
	f.mdi(t, "g43")

	checkToolOffsets(t, f, 0, 0, 0, "tool offsets after setup")
	return f
}

func checkToolOffsets(t *testing.T, f *interpFixture, x, y, z float64, what string) {
	t.Helper()
	checkFuzz(t, f.interp.Parameter(ParamToolOffsetX), x, what+" X (#5401)")
	checkFuzz(t, f.interp.Parameter(ParamToolOffsetY), y, what+" Y (#5402)")
	checkFuzz(t, f.interp.Parameter(ParamToolOffsetZ), z, what+" Z (#5403)")
}

// TestInterpG10L1DirectOffsets ports the "G10 init" test case and its
// "G10 L1 direct offsets" section: G10 L1 writes tool-table offsets directly,
// one axis at a time, and the values persist across G49/G43.
func TestInterpG10L1DirectOffsets(t *testing.T) {
	f := setupToolOffsetFixture(t, "")

	// Two passes: the offsets must survive G49 (tool length compensation off)
	// and be there again on the next pass. That is what the original loop was
	// checking — G43/G49 state must not disturb the stored offsets.
	for pass := 0; pass < 2; pass++ {
		f.mdi(t, "g10 l1 p1 x0 y0 z0 ")
		f.mdi(t, "g10 l1 p1 x1")
		checkToolOffsets(t, f, 1, 0, 0, fmt.Sprintf("pass %d: after L1 x1", pass))

		f.mdi(t, "g10 l1 p1 y2")
		checkToolOffsets(t, f, 1, 2, 0, fmt.Sprintf("pass %d: after L1 y2", pass))

		f.mdi(t, "g10 l1 p1 z3")
		checkToolOffsets(t, f, 1, 2, 3, fmt.Sprintf("pass %d: after L1 z3", pass))

		f.mdi(t, "g49")
	}
}

// TestInterpG10L10RelativeToPosition ports the "tool offsets relative to
// position no rotation" section: G10 L10 sets the tool offset such that the
// CURRENT point reads as the commanded coordinate, so the stored offset is
// (current - commanded) — negative here.
func TestInterpG10L10RelativeToPosition(t *testing.T) {
	f := setupToolOffsetFixture(t, "")

	f.mdi(t, "g0 x.1 y.2 z.3")

	f.mdi(t, "g10 l10 p1 x1")
	checkToolOffsets(t, f, -0.9, 0, 0, "after L10 x1 at x=.1")

	f.mdi(t, "g10 l10 p1 y2")
	checkToolOffsets(t, f, -0.9, -1.8, 0, "after L10 y2 at y=.2")

	f.mdi(t, "g10 l10 p1 z3")
	checkToolOffsets(t, f, -0.9, -1.8, -2.7, "after L10 z3 at z=.3")
}

// TestInterpG10L10WithRotation ports the "G10 L10 tool offsets relative to
// position + 45 deg rotation" section: in a rotated coordinate system the
// offset G10 L10 stores is rotated too, and re-applying G43 moves the current
// position to the commanded coordinate.
func TestInterpG10L10WithRotation(t *testing.T) {
	f := setupToolOffsetFixture(t, " r45")

	f.mdi(t, "g0 x0 y0 z0")

	f.mdi(t, "g10 l10 p1 x1")
	f.mdi(t, "g43")
	checkToolOffsets(t, f, -math.Sqrt2/2, -math.Sqrt2/2, 0, "after L10 x1 in R45")
	checkFuzz(t, f.interp.CurrentPosition(AxisX), 1.0, "X reads as commanded")
	checkFuzz(t, f.interp.CurrentPosition(AxisY), 0.0, "Y unchanged")

	f.mdi(t, "g10 l10 p1 y1")
	f.mdi(t, "g43")
	checkToolOffsets(t, f, 0, -math.Sqrt2, 0, "after L10 y1 in R45")
	checkFuzz(t, f.interp.CurrentPosition(AxisX), 1.0, "X still commanded")
	checkFuzz(t, f.interp.CurrentPosition(AxisY), 1.0, "Y reads as commanded")

	// G49 cancels the compensation: the stored offsets stay put, the position
	// returns to where it was without them.
	f.mdi(t, "g49")
	checkToolOffsets(t, f, 0, -math.Sqrt2, 0, "offsets survive G49")
	checkFuzz(t, f.interp.CurrentPosition(AxisX), 0.0, "X back to uncompensated")
	checkFuzz(t, f.interp.CurrentPosition(AxisY), 0.0, "Y back to uncompensated")
}

// TestToolTableWriteVisibleBeforeDrain pins the defect the three tests above
// were blocked on, independently of them.
//
// The interpreter reads tool entries on demand and caches what it reads
// (find_tool_index), while the matching write is queued to the sequencer. If a
// read between the two sees the pre-write store, the cache-fill wipes the
// interpreter's own uncommitted edit. Two G10 L1 blocks naming different axes
// is the shortest way to show it: the second block must not undo the first.
//
// No drain between the blocks — that is the point. AUTO reads ahead without
// draining unless a line returns EXECUTE_FINISH, and G10 L1 does not.
func TestToolTableWriteVisibleBeforeDrain(t *testing.T) {
	f := setupToolOffsetFixture(t, "")

	f.mdi(t, "g10 l1 p1 x1")
	f.mdi(t, "g10 l1 p1 y2")
	checkToolOffsets(t, f, 1, 2, 0, "back-to-back G10 L1 with no drain")

	// And once the queued writes have executed, the store agrees — the
	// pending copy is a read-through, not a shadow that diverges.
	f.task.DrainQueue()
	entry, err := f.tools.GetTool(1)
	if err != nil {
		t.Fatalf("read slot 1: %v", err)
	}
	checkFuzz(t, entry.XOffset, 1, "store slot 1 X after drain")
	checkFuzz(t, entry.YOffset, 2, "store slot 1 Y after drain")
}

// PARAMETER_FILE is optional: with the persist-backed parameter I/O backend
// (the stratuMAK default) numbered parameters live in the persistence service, so
// ini_load must succeed when [RS274NGC]PARAMETER_FILE is absent.
func TestIniLoadAccessor_ParameterFileOptional(t *testing.T) {
	ini, err := inifile.ParseString("[EMC]\nMACHINE=noparamfile\n" +
		"[TRAJ]\nCOORDINATES=X Z\nLINEAR_UNITS=mm\n[EMCIO]\n")
	if err != nil {
		t.Fatalf("parse ini: %v", err)
	}

	interp, err := NewCInterp()
	if err != nil {
		t.Fatalf("NewCInterp: %v", err)
	}
	t.Cleanup(func() { interp.Destroy() })

	accHandle, err := interp.IniLoadAccessor(ini)
	if err != nil {
		t.Fatalf("IniLoadAccessor without PARAMETER_FILE = %v, want success", err)
	}
	t.Cleanup(func() { FreeIniAccessor(accHandle) })
}
