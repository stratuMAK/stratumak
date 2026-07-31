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
	"strings"
	"syscall"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// Rung validation.
//
// The realtime engine bounds-checks what it must — it will not index a block
// array out of range — but it does not police whether a rung makes sense. A
// block whose footprint runs off the grid, or whose body cells are not marked
// as body cells, evaluates as something the author did not draw: power flows
// through cells that should be inert, or a block reads its inputs from
// whatever happens to be to its left. Catching that here turns a malformed
// upload into an error the editor can show, instead of a program that runs and
// misbehaves.

// blockFootprint gives the size of an element in cells. The head sits at the
// right-hand column of the footprint and at its top row.
func blockFootprint(eleType int) (width, height int) {
	switch eleType {
	case C.CL_ELE_TIMER, C.CL_ELE_MONOSTABLE, C.CL_ELE_TIMER_IEC:
		return 2, 2
	case C.CL_ELE_COUNTER:
		return 2, 4
	case C.CL_ELE_COMPAR, C.CL_ELE_OUTPUT_OPERATE:
		return 3, 1
	default:
		return 1, 1
	}
}

// blockVarLimit gives the number of blocks of this kind that exist, for the
// element types whose varNum selects a block rather than a variable.
func (m *classicladder) blockVarLimit(eleType int) (limit int, isBlock bool) {
	switch eleType {
	case C.CL_ELE_TIMER:
		return int(m.rt.sizes.nbr_timers), true
	case C.CL_ELE_MONOSTABLE:
		return int(m.rt.sizes.nbr_monostables), true
	case C.CL_ELE_COUNTER:
		return int(m.rt.sizes.nbr_counters), true
	case C.CL_ELE_TIMER_IEC:
		return int(m.rt.sizes.nbr_timers_iec), true
	case C.CL_ELE_COMPAR, C.CL_ELE_OUTPUT_OPERATE:
		return int(m.rt.sizes.nbr_arithm_expr), true
	}
	return 0, false
}

// knownElementType reports whether the engine has a case for this type.
func knownElementType(t int) bool {
	switch t {
	case C.CL_ELE_FREE, C.CL_ELE_INPUT, C.CL_ELE_INPUT_NOT,
		C.CL_ELE_RISING_INPUT, C.CL_ELE_FALLING_INPUT, C.CL_ELE_CONNECTION,
		C.CL_ELE_TIMER, C.CL_ELE_MONOSTABLE, C.CL_ELE_COUNTER, C.CL_ELE_TIMER_IEC,
		C.CL_ELE_COMPAR, C.CL_ELE_OUTPUT, C.CL_ELE_OUTPUT_NOT,
		C.CL_ELE_OUTPUT_SET, C.CL_ELE_OUTPUT_RESET, C.CL_ELE_OUTPUT_JUMP,
		C.CL_ELE_OUTPUT_CALL, C.CL_ELE_OUTPUT_OPERATE, C.CL_ELE_UNUSABLE:
		return true
	}
	return false
}

// elementReadsAVariable reports whether varType/varNum name a variable for this
// element, as opposed to a block, an expression or a jump target.
func elementReadsAVariable(t int) bool {
	switch t {
	case C.CL_ELE_INPUT, C.CL_ELE_INPUT_NOT, C.CL_ELE_RISING_INPUT,
		C.CL_ELE_FALLING_INPUT, C.CL_ELE_OUTPUT, C.CL_ELE_OUTPUT_NOT,
		C.CL_ELE_OUTPUT_SET, C.CL_ELE_OUTPUT_RESET:
		return true
	}
	return false
}

// validateRung checks a rung an editor is about to store. The index is only
// used in messages.
func (m *classicladder) validateRung(index int, r *api.Rung) error {
	const w, h = C.CL_RUNG_WIDTH, C.CL_RUNG_HEIGHT

	if len(r.Elements) != w*h {
		return fmt.Errorf("%w: rung %d has %d elements, want %d",
			syscall.EINVAL, index, len(r.Elements), w*h)
	}
	at := func(col, row int) *api.Element { return &r.Elements[row*w+col] }

	// covered marks cells claimed by a block, so body cells can be checked
	// against the blocks that should own them.
	covered := make([]bool, w*h)

	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			e := at(col, row)
			t := int(e.Type)
			if !knownElementType(t) {
				return fmt.Errorf("%w: rung %d (%d,%d): unknown element type %d",
					syscall.EINVAL, index, col, row, t)
			}
			if t == C.CL_ELE_FREE || t == C.CL_ELE_UNUSABLE {
				continue
			}

			bw, bh := blockFootprint(t)
			if bw > 1 || bh > 1 {
				left, bottom := col-bw+1, row+bh-1
				if left < 0 || bottom >= h {
					return fmt.Errorf("%w: rung %d (%d,%d): a %dx%d block does not fit; "+
						"its head must leave room to the left and below",
						syscall.EINVAL, index, col, row, bw, bh)
				}
				for x := left; x <= col; x++ {
					for y := row; y <= bottom; y++ {
						if covered[y*w+x] {
							return fmt.Errorf("%w: rung %d (%d,%d): blocks overlap at (%d,%d)",
								syscall.EINVAL, index, col, row, x, y)
						}
						covered[y*w+x] = true
						if x == col && y == row {
							continue
						}
						if int(at(x, y).Type) != C.CL_ELE_UNUSABLE {
							return fmt.Errorf("%w: rung %d (%d,%d): cell (%d,%d) is inside the "+
								"block but is type %d, not a body cell (%d)",
								syscall.EINVAL, index, col, row, x, y,
								at(x, y).Type, C.CL_ELE_UNUSABLE)
						}
					}
				}
			} else {
				if covered[row*w+col] {
					return fmt.Errorf("%w: rung %d (%d,%d): element sits inside a block",
						syscall.EINVAL, index, col, row)
				}
				covered[row*w+col] = true
			}

			if err := m.validateElementTarget(index, col, row, e); err != nil {
				return err
			}
		}
	}

	// Every body cell must belong to some block. A stray one is inert in the
	// engine, but it means the editor lost a block and the author's rung is
	// not what they drew.
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			if int(at(col, row).Type) == C.CL_ELE_UNUSABLE && !covered[row*w+col] {
				return fmt.Errorf("%w: rung %d (%d,%d): body cell belongs to no block",
					syscall.EINVAL, index, col, row)
			}
		}
	}
	return nil
}

// validateElementTarget checks what an element points at: a variable, a block,
// an expression or a jump target.
func (m *classicladder) validateElementTarget(index, col, row int, e *api.Element) error {
	t := int(e.Type)
	num := int(e.VarNum)

	if limit, isBlock := m.blockVarLimit(t); isBlock {
		if num < 0 || num >= limit {
			return fmt.Errorf("%w: rung %d (%d,%d): refers to number %d, but only %d exist",
				syscall.EINVAL, index, col, row, num, limit)
		}
		return nil
	}

	switch t {
	case C.CL_ELE_OUTPUT_JUMP:
		if num < 0 || num >= int(m.rt.sizes.nbr_rungs) {
			return fmt.Errorf("%w: rung %d (%d,%d): jumps to rung %d, which does not exist",
				syscall.EINVAL, index, col, row, num)
		}
		return nil
	case C.CL_ELE_OUTPUT_CALL:
		// The sub-routine may legitimately not exist yet — a program can be
		// assembled in any order — so only the shape is checked here. An
		// unresolved call is inert at scan time.
		if num < 0 {
			return fmt.Errorf("%w: rung %d (%d,%d): calls sub-routine %d",
				syscall.EINVAL, index, col, row, num)
		}
		return nil
	}

	if elementReadsAVariable(t) {
		name, ok := m.formatVarName(int(e.VarType), num)
		if !ok {
			return fmt.Errorf("%w: rung %d (%d,%d): variable type %d offset %d does not exist",
				syscall.EINVAL, index, col, row, e.VarType, num)
		}
		// A coil has to be able to write what it drives.
		switch t {
		case C.CL_ELE_OUTPUT, C.CL_ELE_OUTPUT_NOT, C.CL_ELE_OUTPUT_SET, C.CL_ELE_OUTPUT_RESET:
			if !varIsWritable(int(e.VarType)) {
				return fmt.Errorf("%w: rung %d (%d,%d): coil drives %s, which is read-only",
					syscall.EINVAL, index, col, row, name)
			}
		}
	}
	return nil
}

// validateText refuses strings the line-oriented .clp format cannot carry
// round-trip. A control character breaks the line structure on save — a label
// holding a newline reloads as a different program, an SFC comment holding
// "\nS0,1,99,0,0,0" reloads as a step — and in comma-split fields a comma
// silently shifts every later column. 2.9's single-line GTK entries made
// these unreachable; the REST surface makes them reachable, so load stays
// lenient and the write path refuses.
func validateText(field, s string, noComma bool) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s must not contain control characters", syscall.EINVAL, field)
		}
	}
	if noComma && strings.Contains(s, ",") {
		return fmt.Errorf("%w: %s must not contain ','", syscall.EINVAL, field)
	}
	return nil
}

// validateProgramChains is validateSectionChain for a whole-program upload:
// every used section's chain is walked against the UPLOADED rungs, because
// after apply those are the rungs the scan will follow. Without this,
// set_program was the one door left open to a chain that never reaches
// lastRung — a scan that only stops when the runaway guard trips.
func (m *classicladder) validateProgramChains(prog *api.Program) error {
	for si := range prog.Sections {
		s := &prog.Sections[si]
		if !s.Used {
			continue
		}
		if s.Language == api.SectionLanguage_SEQUENTIAL {
			if s.SequentialPage < 0 || s.SequentialPage >= C.CL_MAX_SEQ_PAGES {
				return fmt.Errorf("%w: section %d: sequential page %d does not exist",
					syscall.EINVAL, si, s.SequentialPage)
			}
			continue
		}
		idx := int(s.FirstRung)
		for steps := 0; ; steps++ {
			if idx < 0 || idx >= len(prog.Rungs) || !prog.Rungs[idx].Used {
				return fmt.Errorf("%w: section %d: rung chain reaches rung %d, which the upload does not define",
					syscall.EINVAL, si, idx)
			}
			if idx == int(s.LastRung) {
				break
			}
			if steps >= len(prog.Rungs) {
				return fmt.Errorf("%w: section %d: rung chain never reaches lastRung %d",
					syscall.EINVAL, si, s.LastRung)
			}
			idx = int(prog.Rungs[idx].NextRung)
		}
	}
	return nil
}
