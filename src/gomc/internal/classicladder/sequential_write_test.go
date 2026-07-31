// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// Writing a chart back. The engine reads a chart as slot numbers and never asks
// whether they describe anything an author could have drawn, so everything that
// makes a chart wrong rather than merely odd has to be caught here.

// chartFrom loads a project and returns its chart in the shape set_sequential
// takes, which is the shape an editor round-trips.
func chartFrom(t *testing.T, m *classicladder, project string) api.Sequential {
	t.Helper()
	abs, err := filepath.Abs(project)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if err := m.loadCLPFile(abs); err != nil {
		t.Fatalf("load project: %v", err)
	}
	seq, err := m.GetSequential()
	if err != nil {
		t.Fatalf("get sequential: %v", err)
	}
	return *seq
}

// slotOfStepNumber finds the slot holding %Xn, which is how these tests name
// steps: the numbering is the author's and the slots are the file's.
func slotOfStepNumber(t *testing.T, seq *api.Sequential, number int32) int {
	t.Helper()
	for i := range seq.Steps {
		if seq.Steps[i].Used && seq.Steps[i].StepNumber == number {
			return i
		}
	}
	t.Fatalf("no step numbered %d in the chart", number)
	return -1
}

func TestSetSequential_RoundTripsAChart(t *testing.T) {
	m := newTestModule(t)
	seq := chartFrom(t, m, branchedChart)

	if _, err := m.SetSequential(seq); err != nil {
		t.Fatalf("set sequential: %v", err)
	}
	back, err := m.GetSequential()
	if err != nil {
		t.Fatalf("get sequential: %v", err)
	}

	for i := range seq.Steps {
		if seq.Steps[i] != back.Steps[i] {
			t.Fatalf("step %d changed: %+v -> %+v", i, seq.Steps[i], back.Steps[i])
		}
	}
	for i := range seq.Transitions {
		if seq.Transitions[i] != back.Transitions[i] {
			t.Fatalf("transition %d changed: %+v -> %+v", i, seq.Transitions[i], back.Transitions[i])
		}
	}
	for i := range seq.Comments {
		if seq.Comments[i] != back.Comments[i] {
			t.Fatalf("comment %d changed: %+v -> %+v", i, seq.Comments[i], back.Comments[i])
		}
	}
}

// Applying a chart starts it from its initial steps. The steps a client is
// replacing are named by slot, so carrying activity across an edit would light
// whatever moved into that slot.
func TestSetSequential_StartsFromTheInitialSteps(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}
	seq := chartFrom(t, m, branchedChart)
	rtSections(m.rt)[0].used = 1
	rtSections(m.rt)[0].language = 1 // SEQUENTIAL
	rtSections(m.rt)[0].sub_routine_number = -1
	rtSections(m.rt)[0].sequential_page = 0
	l.prepareRun()

	// Walk off the initial step, so there is something to reset. The first
	// transition hands %X1 over to %X2 and %X6 at once.
	l.writeVar(varMemBit, 0, 1)
	l.scan(1)
	l.writeVar(varMemBit, 0, 0)
	vars := m.buildVariables()
	if vars.Bools.StepActivity[1] {
		t.Fatal("the chart should have left its initial step")
	}

	if _, err := m.SetSequential(seq); err != nil {
		t.Fatalf("set sequential: %v", err)
	}
	// %X and %X.V are published by the scan, as they are in 2.9: applying a
	// chart resets the engine's own activity, and the next scan says so.
	l.scan(1)
	vars = m.buildVariables()
	if !vars.Bools.StepActivity[1] {
		t.Error("applying a chart should start it from its initial step")
	}
	if vars.Bools.StepActivity[2] || vars.Bools.StepActivity[6] {
		t.Error("applying a chart should leave no other step active")
	}
}

// Every refusal below is a chart the engine would run: it scans, and it does
// something other than what is drawn.
func TestSetSequential_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mangle func(t *testing.T, seq *api.Sequential)
		want   string
	}{
		{
			"a transition naming a step that is not on the chart",
			func(t *testing.T, seq *api.Sequential) {
				seq.Transitions[0].StepsToActivate[0] = 99
			},
			"not on the chart",
		},
		{
			"a transition naming a step on another page",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps[slotOfStepNumber(t, seq, 2)].Page = 1
			},
			"on page 1",
		},
		{
			"a slot number after an empty slot",
			func(t *testing.T, seq *api.Sequential) {
				// The engine stops at the first empty slot, so this link is one
				// the author drew and the scan never sees.
				seq.Transitions[0].StepsToActivate[5] = int32(slotOfStepNumber(t, seq, 4))
			},
			"after an empty slot",
		},
		{
			"the same step twice in one link array",
			func(t *testing.T, seq *api.Sequential) {
				seq.Transitions[0].StepsToActivate[1] = seq.Transitions[0].StepsToActivate[0]
			},
			"twice",
		},
		{
			// The engine reads "no steps to deactivate" as "they are all
			// active", so such a transition fires whenever its condition is
			// true and reports a change every time — the page's settle loop
			// then runs all fifty iterations on every scan. Placing a
			// transition before the step above it, or deleting the step that
			// fed one, both leave this.
			"a transition with no step above it",
			func(t *testing.T, seq *api.Sequential) {
				for i := range seq.Transitions[0].StepsToDeactivate {
					seq.Transitions[0].StepsToDeactivate[i] = -1
				}
			},
			"fire on every scan",
		},
		{
			"two steps sharing a number",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps[slotOfStepNumber(t, seq, 3)].StepNumber = 2
			},
			"share %X2",
		},
		{
			"a step number outside the %X range",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps[slotOfStepNumber(t, seq, 3)].StepNumber = 500
			},
			"%X goes from 0 to 127",
		},
		{
			"two elements in one cell",
			func(t *testing.T, seq *api.Sequential) {
				s := slotOfStepNumber(t, seq, 6)
				seq.Steps[s].PosiX = seq.Steps[slotOfStepNumber(t, seq, 2)].PosiX
				seq.Steps[s].PosiY = seq.Steps[slotOfStepNumber(t, seq, 2)].PosiY
			},
			"both at page 0",
		},
		{
			"an element off the page",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps[slotOfStepNumber(t, seq, 1)].PosiX = 40
			},
			"off a 16x16 page",
		},
		{
			"an element on a page that does not exist",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps[slotOfStepNumber(t, seq, 1)].Page = 9
			},
			"there are 5 pages",
		},
		{
			"a transition linked to one that does not link back",
			func(t *testing.T, seq *api.Sequential) {
				// The bar the drawing runs between them has two ends.
				seq.Transitions[5].TransLinkedForStart[0] = -1
			},
			"does not link back",
		},
		{
			"a transition linked to itself",
			func(t *testing.T, seq *api.Sequential) {
				seq.Transitions[2].TransLinkedForStart[0] = 2
			},
			"itself",
		},
		{
			"a transition that takes and gives the same step",
			func(t *testing.T, seq *api.Sequential) {
				// It fires on every scan it is true, for ever: the step it
				// needs active is the one it activates again.
				seq.Transitions[0].StepsToActivate[0] = seq.Transitions[0].StepsToDeactivate[0]
				seq.Transitions[0].StepsToActivate[1] = -1
			},
			"fire on every scan",
		},
		{
			"a condition variable this PLC does not have",
			func(t *testing.T, seq *api.Sequential) {
				seq.Transitions[0].VarNumCondi = 4000
			},
			"does not exist",
		},
		{
			"a comment running off the right of the page",
			func(t *testing.T, seq *api.Sequential) {
				seq.Comments[0].PosiX = 15
			},
			"four cells wide",
		},
		{
			"a comment covering an element",
			func(t *testing.T, seq *api.Sequential) {
				seq.Comments[0].PosiX = 1
				seq.Comments[0].PosiY = 1
			},
			"are both at page 0",
		},
		{
			"a chart with the wrong number of slots",
			func(t *testing.T, seq *api.Sequential) {
				seq.Steps = seq.Steps[:10]
			},
			"want 128",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModule(t)
			seq := chartFrom(t, m, branchedChart)
			before, err := m.GetSequential()
			if err != nil {
				t.Fatalf("get sequential: %v", err)
			}

			tc.mangle(t, &seq)
			_, err = m.SetSequential(seq)
			if err == nil {
				t.Fatal("the chart was accepted")
			}
			if !errors.Is(err, syscall.EINVAL) {
				t.Errorf("error = %v, want it to wrap EINVAL", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}

			// A refused chart leaves the running one untouched — the whole
			// reason the check comes before anything is applied.
			after, err := m.GetSequential()
			if err != nil {
				t.Fatalf("get sequential: %v", err)
			}
			for i := range before.Steps {
				if before.Steps[i] != after.Steps[i] {
					t.Fatalf("step %d changed despite the refusal: %+v -> %+v",
						i, before.Steps[i], after.Steps[i])
				}
			}
			for i := range before.Transitions {
				if before.Transitions[i] != after.Transitions[i] {
					t.Fatalf("transition %d changed despite the refusal: %+v -> %+v",
						i, before.Transitions[i], after.Transitions[i])
				}
			}
		})
	}
}

// An empty chart is a chart: it is what an author starts from, and what is left
// after deleting everything.
func TestSetSequential_AcceptsAnEmptyChart(t *testing.T) {
	m := newTestModule(t)
	seq := chartFrom(t, m, branchedChart)
	for i := range seq.Steps {
		seq.Steps[i].Used = false
	}
	for i := range seq.Transitions {
		seq.Transitions[i].Used = false
	}
	for i := range seq.Comments {
		seq.Comments[i].Used = false
	}
	if _, err := m.SetSequential(seq); err != nil {
		t.Fatalf("an empty chart was refused: %v", err)
	}
	back, err := m.GetSequential()
	if err != nil {
		t.Fatalf("get sequential: %v", err)
	}
	for i := range back.Steps {
		if back.Steps[i].Used {
			t.Fatalf("step %d survived: %+v", i, back.Steps[i])
		}
	}
}

// The chart the sim demo ships must survive being written back unchanged.
//
// Every rule in the validator is a rule some existing chart could fall foul of,
// and a chart that runs but cannot be saved is worse than one that was never
// accepted: the author finds out only when they try to keep their work. This is
// the cheap guard — if a new refusal would make the shipped demo unsaveable,
// this fails before anyone meets it.
func TestSetSequential_TheShippedDemoChartApplies(t *testing.T) {
	m := newTestModule(t)
	seq := chartFrom(t, m, demoProject)

	if _, err := m.SetSequential(seq); err != nil {
		t.Fatalf("the demo's own chart was refused: %v", err)
	}

	// And it is still the same chart afterwards.
	after, err := m.GetSequential()
	if err != nil {
		t.Fatalf("get sequential: %v", err)
	}
	for i := range seq.Steps {
		if seq.Steps[i] != after.Steps[i] {
			t.Fatalf("step slot %d changed: %+v -> %+v", i, seq.Steps[i], after.Steps[i])
		}
	}
	for i := range seq.Transitions {
		if seq.Transitions[i] != after.Transitions[i] {
			t.Fatalf("transition slot %d changed: %+v -> %+v", i, seq.Transitions[i], after.Transitions[i])
		}
	}
}
