// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import "testing"

// A chart loaded from a project must be readable over the API with its
// structure intact — in particular the step indices transitions refer to.
func TestSequentialAPI_ReadsChartStructure(t *testing.T) {
	m := newTestModule(t)

	// Two steps and one transition between them, on page 0. Step numbers are
	// deliberately not equal to the step indices: transitions refer to steps by
	// index, while %Xn names them by number, and conflating the two is the
	// easy mistake here.
	m.parseSequential(seqFixture)

	seq, err := m.GetSequential()
	if err != nil {
		t.Fatalf("get sequential: %v", err)
	}

	if !seq.Steps[0].Used || !seq.Steps[1].Used {
		t.Fatalf("steps 0 and 1 should be in use: %+v %+v", seq.Steps[0], seq.Steps[1])
	}
	if seq.Steps[2].Used {
		t.Errorf("step 2 should be unused: %+v", seq.Steps[2])
	}
	if !seq.Steps[0].InitStep {
		t.Error("step 0 should be the init step")
	}
	if seq.Steps[0].StepNumber != 5 || seq.Steps[1].StepNumber != 9 {
		t.Errorf("step numbers = %d, %d; want 5, 9 — the author's numbering, not the index",
			seq.Steps[0].StepNumber, seq.Steps[1].StepNumber)
	}
	if seq.Steps[0].PosiX != 2 || seq.Steps[0].PosiY != 3 {
		t.Errorf("step 0 position = (%d,%d), want (2,3)", seq.Steps[0].PosiX, seq.Steps[0].PosiY)
	}

	tr := seq.Transitions[0]
	if !tr.Used {
		t.Fatalf("transition 0 should be in use: %+v", tr)
	}
	if tr.VarTypeCondi != varMemBit || tr.VarNumCondi != 4 {
		t.Errorf("transition condition = %d/%d, want %d/4", tr.VarTypeCondi, tr.VarNumCondi, varMemBit)
	}
	if tr.StepsToDeactivate[0] != 0 || tr.StepsToDeactivate[1] != -1 {
		t.Errorf("stepsToDeactivate = %v, want [0, -1, ...]", tr.StepsToDeactivate)
	}
	if tr.StepsToActivate[0] != 1 || tr.StepsToActivate[1] != -1 {
		t.Errorf("stepsToActivate = %v, want [1, -1, ...]", tr.StepsToActivate)
	}

	// Unused slots come back too, so the indices above keep meaning what they
	// say. A compacted array would renumber every reference.
	if len(seq.Steps) != 128 || len(seq.Transitions) != 256 {
		t.Errorf("returned %d steps and %d transitions; the arrays must not be compacted",
			len(seq.Steps), len(seq.Transitions))
	}
}

// The live half: which step is active, and for how long. Without it a chart
// view is a static drawing.
func TestSequentialAPI_ReportsLiveActivity(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	m.parseSequential(seqFixture)
	// A sequential section on page 0.
	rtSections(m.rt)[0].used = 1
	rtSections(m.rt)[0].language = 1 // SEQUENTIAL
	rtSections(m.rt)[0].sub_routine_number = -1
	rtSections(m.rt)[0].sequential_page = 0
	l.prepareRun()

	l.scan(1000)
	vars := m.buildVariables()
	// Step 0 is the init step and carries number 5.
	if !vars.Bools.StepActivity[5] {
		t.Fatal("step X5 should be active after the init")
	}
	if vars.Bools.StepActivity[9] {
		t.Error("step X9 should not be active yet")
	}
	if vars.Words.StepTimes[5] < 1 {
		t.Errorf("step %%X5 time = %d after a second, want at least 1",
			vars.Words.StepTimes[5])
	}

	// Fire the transition: %B4 true hands over from step 5 to step 9.
	l.writeVar(varMemBit, 4, 1)
	l.scan(1)
	vars = m.buildVariables()
	if vars.Bools.StepActivity[5] {
		t.Error("step X5 should have been deactivated by the transition")
	}
	if !vars.Bools.StepActivity[9] {
		t.Error("step X9 should have been activated by the transition")
	}
	if vars.Words.StepTimes[5] != 0 {
		t.Errorf("step %%X5 time = %d after deactivating, want it reset",
			vars.Words.StepTimes[5])
	}
}

// %X.V step time shares the word array with the s32 pins; a chart running
// alongside word I/O must not have its timers scribbled on.
func TestSequentialAPI_StepTimesDoNotAliasWordPins(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	l.writeVar(varStepTime, 7, 4242)
	for i := 0; i < int(m.rt.sizes.nbr_s32_in); i++ {
		l.writeVar(varPhysWordInput, i, 111)
	}
	for i := 0; i < int(m.rt.sizes.nbr_s32_out); i++ {
		l.writeVar(varPhysWordOutput, i, 222)
	}

	vars := m.buildVariables()
	if vars.Words.StepTimes[7] != 4242 {
		t.Errorf("step time 7 = %d after writing every word pin, want 4242",
			vars.Words.StepTimes[7])
	}
}

// sequential.csv fixture: two steps, one transition.
// Line kinds are keyed by their first field, as in 2.9's sequential.csv.
const seqFixture = `#VER=1.0
S0,1,5,0,2,3
S1,0,9,0,2,6
T0,1,-1,-1,-1,-1,-1,-1,-1,-1,-1,0,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,-1,0,2,4
C0,0,0/4
`
