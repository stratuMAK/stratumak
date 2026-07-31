// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#include "classicladder_rt.h"
*/
import "C"

import (
	"fmt"
	"syscall"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// Sequential (SFC) chart validation and application.
//
// The engine reads a chart as four arrays of slot numbers per transition and
// never asks whether they describe anything an author could have drawn. A
// transition naming a step that is not there, or one on another page, still
// scans: it deactivates a slot nobody can see and the chart appears stuck for
// no visible reason. Refusing it here turns that into a message the editor can
// show, and — because nothing is applied until the whole chart passes — leaves
// the running chart alone when it does.

// seqRefs is one of the four link arrays of a transition, named for messages.
type seqRefs struct {
	name string
	vals *[C.CL_MAX_SWITCHS]int32
}

// validateSequential checks a chart an editor is about to store.
func (m *classicladder) validateSequential(seq *api.Sequential) error {
	if len(seq.Steps) != C.CL_MAX_STEPS {
		return fmt.Errorf("%w: chart has %d step slots, want %d",
			syscall.EINVAL, len(seq.Steps), C.CL_MAX_STEPS)
	}
	if len(seq.Transitions) != C.CL_MAX_TRANSITIONS {
		return fmt.Errorf("%w: chart has %d transition slots, want %d",
			syscall.EINVAL, len(seq.Transitions), C.CL_MAX_TRANSITIONS)
	}
	if len(seq.Comments) != C.CL_MAX_SEQ_COMMENTS {
		return fmt.Errorf("%w: chart has %d comment slots, want %d",
			syscall.EINVAL, len(seq.Comments), C.CL_MAX_SEQ_COMMENTS)
	}

	// occupied maps a page cell to what already claims it. Two elements in one
	// cell are indistinguishable to anyone reading the chart, and the editor
	// searches by position, so it would find only one of them.
	occupied := make(map[[3]int32]string)
	claim := func(what string, page, x, y int32) error {
		key := [3]int32{page, x, y}
		if prev, taken := occupied[key]; taken {
			return fmt.Errorf("%w: %s and %s are both at page %d (%d,%d)",
				syscall.EINVAL, what, prev, page, x, y)
		}
		occupied[key] = what
		return nil
	}

	for i := range seq.Steps {
		s := &seq.Steps[i]
		if !s.Used {
			continue
		}
		what := fmt.Sprintf("step %d", i)
		if err := checkSeqPosition(what, s.Page, s.PosiX, s.PosiY); err != nil {
			return err
		}
		// The step number is the n in %Xn, so it indexes the step-activity and
		// step-time regions. Out of range, the engine's write_var drops the
		// write and the step is simply invisible to every rung that reads it.
		if s.StepNumber < 0 || s.StepNumber >= C.CL_MAX_STEPS {
			return fmt.Errorf("%w: %s is numbered %d; %%X goes from 0 to %d",
				syscall.EINVAL, what, s.StepNumber, C.CL_MAX_STEPS-1)
		}
		if err := claim(what, s.Page, s.PosiX, s.PosiY); err != nil {
			return err
		}
	}

	// Two steps sharing a number share their %X bit, so one of them reports the
	// other's activity. That is worth refusing on its own: it is not a drawing
	// problem the author can see.
	byNumber := make(map[int32]int)
	for i := range seq.Steps {
		s := &seq.Steps[i]
		if !s.Used {
			continue
		}
		if prev, dup := byNumber[s.StepNumber]; dup {
			return fmt.Errorf("%w: steps %d and %d are both numbered %d; "+
				"they would share %%X%d", syscall.EINVAL, prev, i, s.StepNumber, s.StepNumber)
		}
		byNumber[s.StepNumber] = i
	}

	for i := range seq.Transitions {
		t := &seq.Transitions[i]
		if !t.Used {
			continue
		}
		what := fmt.Sprintf("transition %d", i)
		if err := checkSeqPosition(what, t.Page, t.PosiX, t.PosiY); err != nil {
			return err
		}
		if _, ok := m.formatVarName(int(t.VarTypeCondi), int(t.VarNumCondi)); !ok {
			return fmt.Errorf("%w: %s has condition variable type %d offset %d, which does not exist",
				syscall.EINVAL, what, t.VarTypeCondi, t.VarNumCondi)
		}
		if err := claim(what, t.Page, t.PosiX, t.PosiY); err != nil {
			return err
		}
		if err := m.validateTransitionLinks(seq, i, t); err != nil {
			return err
		}
	}

	for i := range seq.Comments {
		c := &seq.Comments[i]
		if !c.Used {
			continue
		}
		what := fmt.Sprintf("comment %d", i)
		if err := checkSeqPosition(what, c.Page, c.PosiX, c.PosiY); err != nil {
			return err
		}
		// A comment is drawn four cells wide, so it claims all four.
		if c.PosiX > C.CL_SEQ_PAGE_WIDTH-4 {
			return fmt.Errorf("%w: %s starts at column %d, but a comment is four cells wide",
				syscall.EINVAL, what, c.PosiX)
		}
		for x := c.PosiX; x < c.PosiX+4; x++ {
			if err := claim(what, c.Page, x, c.PosiY); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkSeqPosition bounds a chart element to a page and to that page's grid.
func checkSeqPosition(what string, page, x, y int32) error {
	if page < 0 || page >= C.CL_MAX_SEQ_PAGES {
		return fmt.Errorf("%w: %s is on page %d; there are %d pages",
			syscall.EINVAL, what, page, C.CL_MAX_SEQ_PAGES)
	}
	if x < 0 || x >= C.CL_SEQ_PAGE_WIDTH || y < 0 || y >= C.CL_SEQ_PAGE_HEIGHT {
		return fmt.Errorf("%w: %s is at (%d,%d), off a %dx%d page",
			syscall.EINVAL, what, x, y, C.CL_SEQ_PAGE_WIDTH, C.CL_SEQ_PAGE_HEIGHT)
	}
	return nil
}

// validateTransitionLinks checks the four slot arrays of one transition.
func (m *classicladder) validateTransitionLinks(seq *api.Sequential, index int, t *api.Transition) error {
	what := fmt.Sprintf("transition %d", index)

	stepArrays := []seqRefs{
		{"activates", &t.StepsToActivate},
		{"deactivates", &t.StepsToDeactivate},
	}
	for _, arr := range stepArrays {
		refs, err := seqRefList(what, arr)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if ref < 0 || ref >= int32(len(seq.Steps)) {
				return fmt.Errorf("%w: %s %s step %d, which is not a step slot",
					syscall.EINVAL, what, arr.name, ref)
			}
			s := &seq.Steps[ref]
			if !s.Used {
				return fmt.Errorf("%w: %s %s step %d, which is not on the chart",
					syscall.EINVAL, what, arr.name, ref)
			}
			if s.Page != t.Page {
				return fmt.Errorf("%w: %s is on page %d but %s step %d on page %d",
					syscall.EINVAL, what, t.Page, arr.name, ref, s.Page)
			}
		}
	}

	// A transition with no step above it has the same fault in a plainer form.
	// The engine checks that every step it deactivates is active and reads an
	// empty list as "they all are", so the transition fires whenever its
	// condition is true and reports a state change each time — and the page's
	// settle loop runs its full fifty iterations over every transition slot, on
	// every scan, for as long as the condition holds. One such transition takes
	// a page refresh from about a quarter of a microsecond to five.
	//
	// The link arrays have been checked for gaps by here, so an empty first
	// slot means the whole array is empty. Both the editor placing a transition
	// before the step above it and deleting the step that fed one leave this,
	// which is why it is worth a message rather than a silent cost.
	if t.StepsToDeactivate[0] == -1 {
		return fmt.Errorf("%w: %s has no step above it, so it would fire on every scan; "+
			"give it a step to advance from, or delete it", syscall.EINVAL, what)
	}

	// A transition that both takes and gives the same step fires on every scan
	// it is true, for ever: the step it needs active is the one it activates
	// again. The chart never settles, and the engine's runaway guard spins the
	// full fifty iterations every cycle to discover that.
	for _, off := range t.StepsToDeactivate {
		if off == -1 {
			break
		}
		for _, on := range t.StepsToActivate {
			if on == -1 {
				break
			}
			if on == off {
				return fmt.Errorf("%w: %s both deactivates and activates step %d, "+
					"so it would fire on every scan", syscall.EINVAL, what, on)
			}
		}
	}

	// The back-link lives in the same array on the far transition: a start link
	// pairs with a start link, so the pairing is carried alongside rather than
	// worked out from the message text.
	transArrays := []struct {
		refs seqRefs
		peer func(*api.Transition) *[C.CL_MAX_SWITCHS]int32
	}{
		{seqRefs{"is linked for a branch start to", &t.TransLinkedForStart},
			func(o *api.Transition) *[C.CL_MAX_SWITCHS]int32 { return &o.TransLinkedForStart }},
		{seqRefs{"is linked for a branch end to", &t.TransLinkedForEnd},
			func(o *api.Transition) *[C.CL_MAX_SWITCHS]int32 { return &o.TransLinkedForEnd }},
	}
	for _, entry := range transArrays {
		arr := entry.refs
		refs, err := seqRefList(what, arr)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if ref < 0 || ref >= int32(len(seq.Transitions)) {
				return fmt.Errorf("%w: %s %s %d, which is not a transition slot",
					syscall.EINVAL, what, arr.name, ref)
			}
			if ref == int32(index) {
				return fmt.Errorf("%w: %s %s itself", syscall.EINVAL, what, arr.name)
			}
			other := &seq.Transitions[ref]
			if !other.Used {
				return fmt.Errorf("%w: %s %s transition %d, which is not on the chart",
					syscall.EINVAL, what, arr.name, ref)
			}
			if other.Page != t.Page {
				return fmt.Errorf("%w: %s is on page %d but %s transition %d on page %d",
					syscall.EINVAL, what, t.Page, arr.name, ref, other.Page)
			}
			// The link is what tells the drawing where to run the bar, and the
			// two ends have to agree about which branch they belong to: a
			// one-sided link draws a bar from one transition to another that
			// does not draw one back.
			if !seqRefContains(entry.peer(other), int32(index)) {
				return fmt.Errorf("%w: %s %s transition %d, but %d does not link back",
					syscall.EINVAL, what, arr.name, ref, ref)
			}
		}
	}
	return nil
}

// seqRefList reads one link array up to its first -1, and insists the rest of
// it is -1 too. The engine stops at the first -1, so a slot number sitting
// after one is a link the author drew and the scan never sees.
func seqRefList(what string, arr seqRefs) ([]int32, error) {
	var out []int32
	end := false
	for i, v := range arr.vals {
		if v == -1 {
			end = true
			continue
		}
		if end {
			return nil, fmt.Errorf("%w: %s %s step/transition %d in slot %d, "+
				"after an empty slot; the engine stops at the first empty one",
				syscall.EINVAL, what, arr.name, v, i)
		}
		out = append(out, v)
	}
	// Duplicates would deactivate or activate the same step twice, which is
	// harmless, and take a slot from a branch that needs it, which is not.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[i] == out[j] {
				return nil, fmt.Errorf("%w: %s %s %d twice",
					syscall.EINVAL, what, arr.name, out[i])
			}
		}
	}
	return out, nil
}

func seqRefContains(arr *[C.CL_MAX_SWITCHS]int32, v int32) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}

// applySequential writes a validated chart into the engine.
func (m *classicladder) applySequential(seq *api.Sequential) {
	for i := range seq.Steps {
		src := &seq.Steps[i]
		dst := &m.rt.steps[i]
		if !src.Used {
			dst.num_page = -1
			dst.activated = 0
			dst.time_activated = 0
			continue
		}
		dst.num_page = C.int8_t(src.Page)
		dst.posi_x = C.char(src.PosiX)
		dst.posi_y = C.char(src.PosiY)
		if src.InitStep {
			dst.init_step = 1
		} else {
			dst.init_step = 0
		}
		dst.step_number = C.int(src.StepNumber)
	}

	for i := range seq.Transitions {
		src := &seq.Transitions[i]
		dst := &m.rt.transitions[i]
		if !src.Used {
			dst.num_page = -1
			dst.activated = 0
			continue
		}
		dst.num_page = C.int8_t(src.Page)
		dst.posi_x = C.char(src.PosiX)
		dst.posi_y = C.char(src.PosiY)
		dst.var_type_condi = C.int(src.VarTypeCondi)
		dst.var_num_condi = C.int(src.VarNumCondi)
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			dst.num_step_to_activ[j] = C.int16_t(src.StepsToActivate[j])
			dst.num_step_to_desactiv[j] = C.int16_t(src.StepsToDeactivate[j])
			dst.num_trans_linked_for_start[j] = C.int16_t(src.TransLinkedForStart[j])
			dst.num_trans_linked_for_end[j] = C.int16_t(src.TransLinkedForEnd[j])
		}
	}

	for i := range seq.Comments {
		src := &seq.Comments[i]
		dst := &m.rt.seq_comments[i]
		if !src.Used {
			dst.num_page = -1
			dst.comment[0] = 0
			continue
		}
		dst.num_page = C.int8_t(src.Page)
		dst.posi_x = C.char(src.PosiX)
		dst.posi_y = C.char(src.PosiY)
		copyGoStringToC(&dst.comment[0], src.Comment, C.CL_SEQ_COMMENT_LGT)
	}

	// The chart's steps are named by slot, so the activity a client is
	// replacing belongs to whatever used to be in those slots. Starting the new
	// chart from its initial steps is both what the original editor does on
	// apply and the only reading of the old activity that is not a guess.
	C.cl_prepare_sequential(m.rt)
}
