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

// Structural editing: adding and removing rungs and sections.
//
// Rungs live in a fixed array and run in the order of a per-section linked
// list (firstRung, then nextRung, until lastRung). Keeping that list correct is
// the server's job, for two reasons. A broken chain is not a wrong answer, it
// is a scan that never terminates — the engine's runaway guard catches it by
// stopping the PLC, which is a safe outcome but a terrible one to reach by
// accident. And the realtime scan walks the same links concurrently without
// taking the lock, so the order in which they are rewritten is part of the
// contract: at every instant the chain a scan is walking must still lead
// somewhere valid.

// findFreeRung returns the index of an unused rung slot, or -1.
func (m *classicladder) findFreeRung() int {
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		if rtRungs(m.rt)[i].used == 0 {
			return i
		}
	}
	return -1
}

// sectionOfRung finds the section whose chain contains this rung.
func (m *classicladder) sectionOfRung(rung int) int {
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		sec := &rtSections(m.rt)[s]
		if sec.used == 0 || sec.language != C.CL_SECTION_LADDER {
			continue
		}
		idx := int(sec.first_rung)
		for n := 0; n <= int(m.rt.sizes.nbr_rungs); n++ {
			if idx == rung {
				return s
			}
			if idx == int(sec.last_rung) || idx < 0 || idx >= int(m.rt.sizes.nbr_rungs) {
				break
			}
			idx = int(rtRungs(m.rt)[idx].next_rung)
		}
	}
	return -1
}

// firstLadderSection returns the index of the first main ladder section, or -1.
func (m *classicladder) firstLadderSection() int {
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		if rtSections(m.rt)[s].used != 0 && rtSections(m.rt)[s].language == C.CL_SECTION_LADDER {
			return s
		}
	}
	return -1
}

// clearRung empties a rung, leaving it unused. The old chain links survive
// the wipe: a scan that read the forward chain just before the rung was
// unlinked may still be standing here, and following the stale successor
// walks it out of the section, while a self-link would spin it against the
// runaway guard — one enormous scan overrun and a stopped PLC. Callers that
// resurrect the slot set both links explicitly.
func (m *classicladder) clearRung(idx int) {
	r := &rtRungs(m.rt)[idx]
	prev, next := r.prev_rung, r.next_rung
	*r = C.cl_rung_t{}
	r.prev_rung = prev
	r.next_rung = next
}

// releaseRungExpressions blanks the arithmetic expressions a rung was the only
// user of. 2.9 frees them unconditionally when a rung is deleted; check first,
// because an expression index shared with another rung would otherwise be
// silently emptied underneath it.
func (m *classicladder) releaseRungExpressions(idx int) {
	for x := 0; x < C.CL_RUNG_WIDTH; x++ {
		for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
			e := &rtRungs(m.rt)[idx].elements[x][y]
			if e._type != C.CL_ELE_COMPAR && e._type != C.CL_ELE_OUTPUT_OPERATE {
				continue
			}
			num := int(e.var_num)
			if num < 0 || num >= int(m.rt.sizes.nbr_arithm_expr) {
				continue
			}
			if m.expressionUsedElsewhere(num, idx) {
				continue
			}
			rtArithmExprs(m.rt)[num].expr[0] = 0
			rtCompiledExprs(m.rt)[num].valid = 0
			rtCompiledExprs(m.rt)[num].len = 0
		}
	}
}

// expressionUsedElsewhere reports whether any rung other than exceptRung refers
// to this expression index.
func (m *classicladder) expressionUsedElsewhere(exprNum, exceptRung int) bool {
	for r := 0; r < int(m.rt.sizes.nbr_rungs); r++ {
		if r == exceptRung || rtRungs(m.rt)[r].used == 0 {
			continue
		}
		for x := 0; x < C.CL_RUNG_WIDTH; x++ {
			for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
				e := &rtRungs(m.rt)[r].elements[x][y]
				if (e._type == C.CL_ELE_COMPAR || e._type == C.CL_ELE_OUTPUT_OPERATE) &&
					int(e.var_num) == exprNum {
					return true
				}
			}
		}
	}
	return false
}

// insertRungAfter links a fresh rung in after the given one and returns its
// index. afterRung of -1 appends to the end of the first ladder section.
func (m *classicladder) insertRungAfter(afterRung int) (int, error) {
	sec := -1
	if afterRung < 0 {
		sec = m.firstLadderSection()
		if sec < 0 {
			return -1, fmt.Errorf("%w: no ladder section to add a rung to", syscall.EINVAL)
		}
		afterRung = int(rtSections(m.rt)[sec].last_rung)
	} else {
		if afterRung >= int(m.rt.sizes.nbr_rungs) || rtRungs(m.rt)[afterRung].used == 0 {
			return -1, fmt.Errorf("%w: rung %d is not in use", syscall.EINVAL, afterRung)
		}
		sec = m.sectionOfRung(afterRung)
		if sec < 0 {
			return -1, fmt.Errorf("%w: rung %d belongs to no section", syscall.EINVAL, afterRung)
		}
	}

	newRung := m.findFreeRung()
	if newRung < 0 {
		return -1, fmt.Errorf("%w: no free rung slot", syscall.ENOSPC)
	}

	section := &rtSections(m.rt)[sec]
	wasLast := afterRung == int(section.last_rung)
	next := int(rtRungs(m.rt)[afterRung].next_rung)

	// Fill the new rung in completely before anything points at it: a
	// concurrent scan cannot reach it yet, so it sees no intermediate state.
	m.clearRung(newRung)
	rtRungs(m.rt)[newRung].used = 1
	rtRungs(m.rt)[newRung].prev_rung = C.int(afterRung)
	if wasLast {
		rtRungs(m.rt)[newRung].next_rung = C.int(newRung)
	} else {
		rtRungs(m.rt)[newRung].next_rung = C.int(next)
		rtRungs(m.rt)[next].prev_rung = C.int(newRung)
	}

	// Publish. The scan follows next_rung, so this is the instant the new rung
	// joins the program; lastRung moves after it, never before, or the scan
	// would look for an end that nothing points to yet.
	rtRungs(m.rt)[afterRung].next_rung = C.int(newRung)
	if wasLast {
		section.last_rung = C.int(newRung)
	}
	return newRung, nil
}

// deleteRung unlinks a rung from its section and frees it.
func (m *classicladder) deleteRung(idx int) error {
	if idx < 0 || idx >= int(m.rt.sizes.nbr_rungs) || rtRungs(m.rt)[idx].used == 0 {
		return fmt.Errorf("%w: rung %d is not in use", syscall.EINVAL, idx)
	}
	sec := m.sectionOfRung(idx)
	if sec < 0 {
		return fmt.Errorf("%w: rung %d belongs to no section", syscall.EINVAL, idx)
	}
	section := &rtSections(m.rt)[sec]

	// A section always holds at least one rung: the scan starts at firstRung
	// unconditionally, so an empty section would evaluate a freed slot.
	if int(section.first_rung) == idx && int(section.last_rung) == idx {
		return fmt.Errorf("%w: rung %d is the only rung of its section; delete the section instead",
			syscall.EINVAL, idx)
	}

	prev := int(rtRungs(m.rt)[idx].prev_rung)
	next := int(rtRungs(m.rt)[idx].next_rung)

	// Take it out of the forward chain first — that is the direction the scan
	// walks, so after this it can no longer reach the rung.
	if int(section.first_rung) == idx {
		section.first_rung = C.int(next)
	} else {
		rtRungs(m.rt)[prev].next_rung = C.int(next)
	}
	if int(section.last_rung) == idx {
		section.last_rung = C.int(prev)
	} else {
		rtRungs(m.rt)[next].prev_rung = C.int(prev)
	}

	// New scans can no longer reach the rung, but one that read the old links
	// may still be inside it — outwait the scan in flight before wiping.
	m.waitScanSettled()

	m.releaseRungExpressions(idx)
	m.clearRung(idx)
	return nil
}

// addSection creates a section and returns its index.
func (m *classicladder) addSection(name string, language int, subRoutineNumber int) (int, error) {
	if name == "" {
		return -1, fmt.Errorf("%w: section name must not be empty", syscall.EINVAL)
	}
	if m.sectionNamed(name) >= 0 {
		return -1, fmt.Errorf("%w: a section named %q already exists", syscall.EEXIST, name)
	}
	if subRoutineNumber >= 0 && m.subRoutineSection(subRoutineNumber) >= 0 {
		return -1, fmt.Errorf("%w: sub-routine number %d is already taken",
			syscall.EEXIST, subRoutineNumber)
	}

	free := -1
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		if rtSections(m.rt)[s].used == 0 {
			free = s
			break
		}
	}
	if free < 0 {
		return -1, fmt.Errorf("%w: no free section slot", syscall.ENOSPC)
	}

	sec := &rtSections(m.rt)[free]
	*sec = C.cl_section_t{}
	copyStringToC(&sec.name[0], name, C.CL_LGT_SECTION_NAME)
	sec.language = C.int(language)
	sec.sub_routine_number = C.int(subRoutineNumber)

	switch language {
	case C.CL_SECTION_LADDER:
		// A ladder section owns at least one rung from the moment it exists,
		// because the scan reads firstRung without checking for emptiness.
		rung := m.findFreeRung()
		if rung < 0 {
			*sec = C.cl_section_t{}
			return -1, fmt.Errorf("%w: no free rung slot for the new section", syscall.ENOSPC)
		}
		m.clearRung(rung)
		rtRungs(m.rt)[rung].used = 1
		sec.first_rung = C.int(rung)
		sec.last_rung = C.int(rung)
	case C.CL_SECTION_SEQUENTIAL:
		page := m.freeSequentialPage()
		if page < 0 {
			*sec = C.cl_section_t{}
			return -1, fmt.Errorf("%w: no free sequential page", syscall.ENOSPC)
		}
		sec.sequential_page = C.int(page)
		sec.sub_routine_number = -1 // a chart cannot be called as a sub-routine
	default:
		*sec = C.cl_section_t{}
		return -1, fmt.Errorf("%w: unknown section language %d", syscall.EINVAL, language)
	}

	// Used last: a section is only visible to the scan once it is complete.
	sec.used = 1
	return free, nil
}

// deleteSection frees a section and the rungs it holds.
func (m *classicladder) deleteSection(idx int) error {
	if idx < 0 || idx >= int(m.rt.sizes.nbr_sections) || rtSections(m.rt)[idx].used == 0 {
		return fmt.Errorf("%w: section %d is not in use", syscall.EINVAL, idx)
	}
	if m.usedSectionCount() <= 1 {
		return fmt.Errorf("%w: section %d is the last one; a program needs somewhere to put a rung",
			syscall.EINVAL, idx)
	}

	sec := &rtSections(m.rt)[idx]
	// Unlink first: once used is clear the scan skips the section — then
	// outwait the scan that may already be walking it before dismantling.
	sec.used = 0
	m.waitScanSettled()

	if sec.language == C.CL_SECTION_LADDER {
		// 2.9 frees rungs by walking the globals of whichever section is on
		// screen, so deleting any other section frees the wrong rungs. Walk
		// this section's own chain.
		idxRung := int(sec.first_rung)
		for n := 0; n <= int(m.rt.sizes.nbr_rungs); n++ {
			if idxRung < 0 || idxRung >= int(m.rt.sizes.nbr_rungs) {
				break
			}
			last := idxRung == int(sec.last_rung)
			next := int(rtRungs(m.rt)[idxRung].next_rung)
			m.releaseRungExpressions(idxRung)
			m.clearRung(idxRung)
			if last {
				break
			}
			idxRung = next
		}
	}
	*sec = C.cl_section_t{}
	sec.sub_routine_number = -1
	return nil
}

// validateSectionChain checks that a section a client is about to store
// describes a rung chain the scan can actually walk.
//
// insert_rung and delete_rung exist so a client never has to touch these
// fields, but set_section can still write them — and a firstRung that never
// reaches lastRung is not a wrong program, it is a scan that only stops
// because the runaway guard trips. Refuse it here instead.
func (m *classicladder) validateSectionChain(index int, s *api.Section) error {
	if !s.Used {
		return nil
	}
	if s.Language == api.SectionLanguage_SEQUENTIAL {
		if s.SequentialPage < 0 || s.SequentialPage >= C.CL_MAX_SEQ_PAGES {
			return fmt.Errorf("%w: section %d: sequential page %d does not exist",
				syscall.EINVAL, index, s.SequentialPage)
		}
		return nil
	}

	nbr := int(m.rt.sizes.nbr_rungs)
	first, last := int(s.FirstRung), int(s.LastRung)
	if first < 0 || first >= nbr {
		return fmt.Errorf("%w: section %d: firstRung %d does not exist",
			syscall.EINVAL, index, first)
	}
	if last < 0 || last >= nbr {
		return fmt.Errorf("%w: section %d: lastRung %d does not exist",
			syscall.EINVAL, index, last)
	}

	// Walk it the way the engine does, and require it to end at lastRung
	// within the number of rungs that exist.
	idx := first
	for n := 0; n <= nbr; n++ {
		if rtRungs(m.rt)[idx].used == 0 {
			return fmt.Errorf("%w: section %d: rung %d in its chain is not in use",
				syscall.EINVAL, index, idx)
		}
		if idx == last {
			return nil
		}
		next := int(rtRungs(m.rt)[idx].next_rung)
		if next < 0 || next >= nbr {
			return fmt.Errorf("%w: section %d: rung %d links to %d, which does not exist",
				syscall.EINVAL, index, idx, next)
		}
		idx = next
	}
	return fmt.Errorf("%w: section %d: its rung chain never reaches lastRung %d",
		syscall.EINVAL, index, last)
}

// sectionNamed returns the index of the section with this name, or -1.
func (m *classicladder) sectionNamed(name string) int {
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		if rtSections(m.rt)[s].used == 0 {
			continue
		}
		if C.GoString(&rtSections(m.rt)[s].name[0]) == name {
			return s
		}
	}
	return -1
}

// subRoutineSection returns the index of the section carrying this sub-routine
// number, or -1.
func (m *classicladder) subRoutineSection(nbr int) int {
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		if rtSections(m.rt)[s].used != 0 && int(rtSections(m.rt)[s].sub_routine_number) == nbr {
			return s
		}
	}
	return -1
}

func (m *classicladder) usedSectionCount() int {
	n := 0
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		if rtSections(m.rt)[s].used != 0 {
			n++
		}
	}
	return n
}

// freeSequentialPage returns a chart page no section is using, or -1.
func (m *classicladder) freeSequentialPage() int {
	used := make([]bool, C.CL_MAX_SEQ_PAGES)
	for s := 0; s < int(m.rt.sizes.nbr_sections); s++ {
		sec := &rtSections(m.rt)[s]
		if sec.used == 0 || sec.language != C.CL_SECTION_SEQUENTIAL {
			continue
		}
		if p := int(sec.sequential_page); p >= 0 && p < len(used) {
			used[p] = true
		}
	}
	for p, taken := range used {
		if !taken {
			return p
		}
	}
	return -1
}
