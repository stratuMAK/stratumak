// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import "testing"

// --- Contacts and coils ---

// A coil must follow its contact down as well as up. The engine used to skip
// cells that carried no power, which left an energized coil latched forever.
func TestLadder_CoilFollowsContactBothWays(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	// %I0 --| |-------------( )-- %Q0
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 9, 0, eleOutput, varPhysOutput, 0)
	for x := 1; x < 9; x++ {
		l.put(0, x, 0, eleConnection, 0, 0)
	}

	l.input(0, true)
	l.scan(1)
	if !l.output(0) {
		t.Fatal("output should be on while the contact is closed")
	}

	l.input(0, false)
	l.scan(1)
	if l.output(0) {
		t.Fatal("output stayed on after the contact opened")
	}
}

func TestLadder_NormallyClosedContact(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleInputNot, varPhysInput, 0)
	l.put(0, 1, 0, eleOutput, varPhysOutput, 0)

	l.scan(1)
	if !l.output(0) {
		t.Fatal("NC contact should conduct while its variable is false")
	}
	l.input(0, true)
	l.scan(1)
	if l.output(0) {
		t.Fatal("NC contact should block while its variable is true")
	}
}

func TestLadder_NegatedCoil(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutputNot, varPhysOutput, 0)

	l.scan(1)
	if !l.output(0) {
		t.Fatal("negated coil should be on when un-powered")
	}
	l.input(0, true)
	l.scan(1)
	if l.output(0) {
		t.Fatal("negated coil should be off when powered")
	}
}

func TestLadder_SetResetCoils(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	// rung 0: %I0 --| |-- (S) %B0
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutputSet, varMemBit, 0)
	// rung 1: %I1 --| |-- (R) %B0
	l.put(1, 0, 0, eleInput, varPhysInput, 1)
	l.put(1, 1, 0, eleOutputReset, varMemBit, 0)

	l.input(0, true)
	l.scan(1)
	if !l.bit(0) {
		t.Fatal("(S) should have set the bit")
	}
	// Latch survives the set input going away.
	l.input(0, false)
	l.scan(1)
	if !l.bit(0) {
		t.Fatal("(S) latch should hold after the input opens")
	}
	l.input(1, true)
	l.scan(1)
	if l.bit(0) {
		t.Fatal("(R) should have reset the bit")
	}
}

func TestLadder_EdgeContacts(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	// rung 0: %I0 --|P|-- ( ) %B0
	l.put(0, 0, 0, eleRisingInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutput, varMemBit, 0)
	// rung 1: %I0 --|N|-- ( ) %B1
	l.put(1, 0, 0, eleFallingInput, varPhysInput, 0)
	l.put(1, 1, 0, eleOutput, varMemBit, 1)

	l.input(0, true)
	l.scan(1)
	if !l.bit(0) {
		t.Fatal("rising-edge contact should pulse on the 0->1 transition")
	}
	l.scan(1)
	if l.bit(0) {
		t.Fatal("rising-edge contact should be a single-scan pulse")
	}

	l.input(0, false)
	l.scan(1)
	if !l.bit(1) {
		t.Fatal("falling-edge contact should pulse on the 1->0 transition")
	}
	l.scan(1)
	if l.bit(1) {
		t.Fatal("falling-edge contact should be a single-scan pulse")
	}
}

// An edge contact whose variable is already true when the PLC starts must not
// fire on the first scan.
func TestLadder_PrepareSeedsEdgeHistory(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleRisingInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutput, varMemBit, 0)

	l.input(0, true)
	l.prepareRun()
	l.scan(1)
	if l.bit(0) {
		t.Fatal("edge contact fired on the first scan of an already-true input")
	}
}

// --- Vertical connections ---

func TestLadder_ParallelBranchOr(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	// Two contacts in parallel feeding one coil:
	//   row 0: %I0 --| |--+-- ( ) %Q0
	//   row 1: %I1 --| |--+
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 0, 1, eleInput, varPhysInput, 1)
	l.put(0, 1, 0, eleOutput, varPhysOutput, 0)
	l.connectTop(0, 1, 1) // link (col 1, row 1) up to (col 1, row 0)

	l.scan(1)
	if l.output(0) {
		t.Fatal("both branches open, output should be off")
	}
	// Power arriving on the *lower* branch has to travel up the link. This is
	// the direction the one-way top-down propagation used to miss.
	l.input(1, true)
	l.scan(1)
	if !l.output(0) {
		t.Fatal("lower parallel branch did not conduct upwards")
	}
	l.input(1, false)
	l.input(0, true)
	l.scan(1)
	if !l.output(0) {
		t.Fatal("upper parallel branch did not conduct")
	}
}

func TestLadder_SeriesAnd(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleInput, varPhysInput, 1)
	l.put(0, 2, 0, eleOutput, varPhysOutput, 0)

	for _, tc := range []struct {
		a, b, want bool
	}{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{true, true, true},
	} {
		l.input(0, tc.a)
		l.input(1, tc.b)
		l.scan(1)
		if l.output(0) != tc.want {
			t.Fatalf("series %v AND %v = %v, want %v", tc.a, tc.b, l.output(0), tc.want)
		}
	}
}

// --- Blocks ---

// The old timer counts its preset down in milliseconds while both its E and C
// inputs are held; the C input lives on the row below the E input.
func TestLadder_TimerControlInputAndTimeout(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	// col 0/1 rows 0..1: E and C feeds, timer head at col 2 row 0.
	l.put(0, 0, 0, eleInput, varPhysInput, 0) // E
	l.put(0, 0, 1, eleInput, varPhysInput, 1) // C
	l.putBlock(0, 2, 0, eleTimer, 0, 2, 2)
	l.put(0, 3, 0, eleOutput, varPhysOutput, 0) // D output

	l.setTimer(0, timeBaseSecs, 100) // 100ms
	l.prepareRun()

	// Enabled but not controlled: the timer must not run.
	l.input(0, true)
	l.input(1, false)
	l.scanN(200, 1)
	if l.output(0) {
		t.Fatal("timer expired with its control input open")
	}

	// Controlled: expires after the preset elapses.
	l.input(1, true)
	l.scanN(99, 1)
	if l.output(0) {
		t.Fatal("timer expired early")
	}
	l.scanN(3, 1)
	if !l.output(0) {
		t.Fatal("timer did not expire after its preset elapsed")
	}

	// Dropping the enable reloads the preset and clears the outputs.
	l.input(0, false)
	l.scan(1)
	if l.output(0) {
		t.Fatal("timer output stayed set after the enable input opened")
	}
	if l.timerValue(0) != 100 {
		t.Fatalf("timer value = %d, want the preset reloaded (100)", l.timerValue(0))
	}
}

// The public %T preset/value are expressed in base units, the structure holds
// milliseconds.
func TestLadder_TimerPresetIsScaledByBase(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.setTimer(0, timeBaseSecs, 0)
	l.writeVar(varTimerPreset, 0, 5)
	if got := l.timerPresetMs(0); got != 5000 {
		t.Fatalf("preset stored as %d ms, want 5000", got)
	}
	if got := l.readVar(varTimerPreset, 0); got != 5 {
		t.Fatalf("preset read back as %d base units, want 5", got)
	}
}

func TestLadder_CounterAllInputs(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	// R/P/U/D feeds on rows 0..3, counter head at col 2 row 0.
	l.put(0, 0, 0, eleInput, varPhysInput, 0) // R
	l.put(0, 0, 1, eleInput, varPhysInput, 1) // P
	l.put(0, 0, 2, eleInput, varPhysInput, 2) // U
	l.put(0, 0, 3, eleInput, varPhysInput, 3) // D
	l.putBlock(0, 2, 0, eleCounter, 0, 2, 4)

	l.setCounterPreset(0, 3)
	l.prepareRun()

	// Count up three times (each pulse is an edge).
	for i := 0; i < 3; i++ {
		l.input(2, true)
		l.scan(1)
		l.input(2, false)
		l.scan(1)
	}
	if got := l.readVar(varCounterValue, 0); got != 3 {
		t.Fatalf("counter value = %d after three up pulses, want 3", got)
	}
	if l.readVar(varCounterDone, 0) == 0 {
		t.Fatal("counter done should be set when value reaches the preset")
	}

	// Count down once.
	l.input(3, true)
	l.scan(1)
	l.input(3, false)
	l.scan(1)
	if got := l.readVar(varCounterValue, 0); got != 2 {
		t.Fatalf("counter value = %d after a down pulse, want 2", got)
	}

	// Preset input loads the preset.
	l.input(1, true)
	l.scan(1)
	if got := l.readVar(varCounterValue, 0); got != 3 {
		t.Fatalf("counter value = %d after the preset input, want 3", got)
	}
	l.input(1, false)

	// Reset input wins over everything.
	l.input(0, true)
	l.scan(1)
	if got := l.readVar(varCounterValue, 0); got != 0 {
		t.Fatalf("counter value = %d after the reset input, want 0", got)
	}
}

func TestLadder_MonostablePulse(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.putBlock(0, 2, 0, eleMonostable, 0, 2, 2)
	l.put(0, 3, 0, eleOutput, varPhysOutput, 0)

	l.setMonostable(0, timeBaseSecs, 50) // 50ms
	l.prepareRun()

	l.input(0, true)
	l.scan(1)
	if !l.output(0) {
		t.Fatal("monostable should fire on the rising edge of its input")
	}
	// Output holds for the preset even though the input stays high.
	l.scanN(40, 1)
	if !l.output(0) {
		t.Fatal("monostable dropped before its preset elapsed")
	}
	l.scanN(20, 1)
	if l.output(0) {
		t.Fatal("monostable did not drop after its preset elapsed")
	}
}

func TestLadder_TimerIECOnDelay(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.putBlock(0, 2, 0, eleTimerIEC, 0, 2, 2)
	l.put(0, 3, 0, eleOutput, varPhysOutput, 0)

	l.setTimerIEC(0, timeBase100ms, 3, timerIECOn) // 3 * 100ms
	l.prepareRun()

	l.input(0, true)
	l.scanN(250, 1)
	if l.output(0) {
		t.Fatal("IEC on-delay expired early")
	}
	l.scanN(120, 1)
	if !l.output(0) {
		t.Fatal("IEC on-delay did not expire after 300ms")
	}

	// Dropping the input resets it.
	l.input(0, false)
	l.scan(1)
	if l.output(0) {
		t.Fatal("IEC on-delay stayed set after its input dropped")
	}
}

// --- Jumps, calls and sub-routines ---

func TestLadder_JumpSkipsRungs(t *testing.T) {
	l := newLadderRT(3)
	defer l.free()

	// rung 0: always jump to rung 2
	l.put(0, 0, 0, eleOutputJump, 0, 2)
	// rung 1: would set %B0 — must be skipped
	l.put(1, 0, 0, eleOutput, varMemBit, 0)
	// rung 2: sets %B1
	l.put(2, 0, 0, eleOutput, varMemBit, 1)

	l.scan(1)
	if l.bit(0) {
		t.Fatal("the jumped-over rung was executed")
	}
	if !l.bit(1) {
		t.Fatal("the jump target rung was not executed")
	}
}

// A taken jump abandons the rest of its own rung.
func TestLadder_JumpAbortsRestOfRung(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	l.put(0, 0, 0, eleOutputJump, 0, 1)
	l.put(0, 0, 1, eleOutput, varMemBit, 0)
	l.put(1, 0, 0, eleOutput, varMemBit, 1)

	l.scan(1)
	if l.bit(0) {
		t.Fatal("a cell after the taken jump was still evaluated")
	}
	if !l.bit(1) {
		t.Fatal("the jump target rung was not executed")
	}
}

// An un-powered jump coil does not jump, and the rest of the rung still runs.
func TestLadder_JumpNotTakenWhenUnpowered(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	// row 0: %I0 --| |-- (J) 0        (the jump coil, fed by an open contact)
	// row 1: -----------------( ) %B0 (a probe evaluated after the jump cell)
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutputJump, 0, 0)
	l.put(0, 0, 1, eleConnection, 0, 0)
	l.put(0, 1, 1, eleConnection, 0, 0)
	l.put(0, 2, 1, eleOutput, varMemBit, 0)

	l.scan(1)
	if !l.bit(0) {
		t.Fatal("the rung stopped at an un-powered jump coil")
	}

	// Closing the contact takes the jump, which abandons the rest of the rung
	// before the probe coil at (2,1) is reached — and a self-jump keeps the
	// section spinning, so the runaway guard stops the PLC.
	l.input(0, true)
	l.setState(stateRun)
	l.scan(1)
	if l.state() != stateStop {
		t.Fatal("a self-jump did not trip the runaway guard")
	}
}

// A runaway jump loop stops the PLC instead of hanging the RT thread.
func TestLadder_MadJumpLoopStopsThePLC(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	// rung 0 jumps to rung 1, rung 1 jumps back to rung 0, forever.
	l.put(0, 0, 0, eleOutputJump, 0, 1)
	l.put(1, 0, 0, eleOutputJump, 0, 0)

	l.setState(stateRun)
	l.scan(1)
	if l.state() != stateStop {
		t.Fatalf("PLC state = %d after a runaway jump loop, want STOP (%d)",
			l.state(), stateStop)
	}
}

// Sub-routine sections run only when a (C)all coil reaches them.
func TestLadder_SubRoutineRunsOnlyWhenCalled(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	// Main section: rung 0 only. Sub-routine 3: rung 1, sets %B0.
	l.setMainSection(0, 0, 0)
	l.setSubRoutine(1, 3, 1, 1)
	l.put(1, 0, 0, eleOutput, varMemBit, 0)

	// Not called yet.
	l.scan(1)
	if l.bit(0) {
		t.Fatal("a sub-routine section ran without being called")
	}

	// rung 0: %I0 --| |-- (C) 3
	l.put(0, 0, 0, eleInput, varPhysInput, 0)
	l.put(0, 1, 0, eleOutputCall, 0, 3)
	l.input(0, true)
	l.scan(1)
	if !l.bit(0) {
		t.Fatal("the called sub-routine did not run")
	}
}

// Mutually recursive calls stop the PLC instead of blowing the RT stack.
func TestLadder_RecursiveCallStopsThePLC(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	l.setMainSection(0, 0, 0)
	l.setSubRoutine(1, 3, 1, 1)
	// main calls sub-routine 3, which calls itself.
	l.put(0, 0, 0, eleOutputCall, 0, 3)
	l.put(1, 0, 0, eleOutputCall, 0, 3)

	l.setState(stateRun)
	l.scan(1)
	if l.state() != stateStop {
		t.Fatalf("PLC state = %d after unbounded call recursion, want STOP (%d)",
			l.state(), stateStop)
	}
}

// --- Variable access ---

// %IF/%QF used to be rejected outright, so float pins never reached the ladder.
func TestLadder_FloatVarsRoundTrip(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.writeVar(varPhysFloatIn, 2, 42)
	if got := l.readVar(varPhysFloatIn, 2); got != 42 {
		t.Fatalf("float input read back as %d, want 42", got)
	}
	l.writeVar(varPhysFloatOut, 1, -7)
	if got := l.readVar(varPhysFloatOut, 1); got != -7 {
		t.Fatalf("float output read back as %d, want -7", got)
	}
	// The two regions must not overlap.
	if got := l.readVar(varPhysFloatIn, 1); got != 0 {
		t.Fatalf("float input 1 = %d, aliased by the output region", got)
	}
}

// %X<n> step time shared storage with %IW, so a word input clobbered it.
func TestLadder_StepTimeDoesNotAliasWordInput(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.writeVar(varStepTime, 0, 1234)
	l.writeVar(varPhysWordInput, 0, 99)
	if got := l.readVar(varStepTime, 0); got != 1234 {
		t.Fatalf("step time = %d after writing word input 0, want 1234", got)
	}
	if got := l.readVar(varPhysWordInput, 0); got != 99 {
		t.Fatalf("word input = %d, want 99", got)
	}
}

// --- Compare and operate ---

func TestLadder_CompareAndOperate(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	// rung 0: [ %W0 > 5 ] --( ) %B0
	if err := l.compileInto(0, "@200/0@>5", 0); err != nil {
		t.Fatalf("compile compare: %v", err)
	}
	l.putBlock(0, 2, 0, eleCompar, 0, 3, 1)
	l.put(0, 3, 0, eleOutput, varMemBit, 0)

	// rung 1: %I0 --| |--[ %W1 := 7 ]
	if err := l.compileInto(1, "@200/1@:=7", 1); err != nil {
		t.Fatalf("compile operate: %v", err)
	}
	l.put(1, 0, 0, eleInput, varPhysInput, 0)
	l.putBlock(1, 3, 0, eleOutputOperate, 1, 3, 1)

	l.writeVar(varMemWord, 0, 3)
	l.scan(1)
	if l.bit(0) {
		t.Fatal("compare was true for 3 > 5")
	}
	if got := l.readVar(varMemWord, 1); got != 0 {
		t.Fatalf("operate ran un-powered, %%W1 = %d", got)
	}

	l.writeVar(varMemWord, 0, 9)
	l.input(0, true)
	l.scan(1)
	if !l.bit(0) {
		t.Fatal("compare was false for 9 > 5")
	}
	if got := l.readVar(varMemWord, 1); got != 7 {
		t.Fatalf("operate did not assign, %%W1 = %d, want 7", got)
	}
}

// --- Prepare before run ---

func TestLadder_PrepareResetsDynamicState(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	l.setTimer(0, timeBaseSecs, 2000)
	l.setTimerRaw(0, 5, true)
	l.setCounterRaw(0, 17, true)
	l.setTimerIECRaw(0, 9, true)

	l.prepareRun()

	if l.timerValue(0) != 2000 || l.timerDone(0) {
		t.Fatalf("timer not reset: value=%d done=%v", l.timerValue(0), l.timerDone(0))
	}
	if l.counterValue(0) != 0 || l.counterDone(0) {
		t.Fatalf("counter not reset: value=%d done=%v", l.counterValue(0), l.counterDone(0))
	}
	if l.timerIECValue(0) != 0 || l.timerIECOutput(0) {
		t.Fatalf("IEC timer not reset: value=%d output=%v",
			l.timerIECValue(0), l.timerIECOutput(0))
	}
}
