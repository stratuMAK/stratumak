// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// The rung chain is owned by the server: set_rung must neither follow nor
// trust the wire's link fields. A PUT carrying a NextRung into a freed slot
// used to splice the scan straight into it — the exposure
// validateSectionChain closed for set_section, reopened one call over.
func TestWrite_SetRungIgnoresWireLinks(t *testing.T) {
	m := newStructModule(t)
	first := int(rtSections(m.rt)[0].first_rung)
	second, err := m.InsertRung(int32(first))
	if err != nil {
		t.Fatal(err)
	}

	r, err := m.GetRung(int32(first))
	if err != nil {
		t.Fatal(err)
	}
	free := m.findFreeRung()
	r.NextRung = int32(free)
	r.PrevRung = int32(free)
	r.Used = false
	if _, err := m.SetRung(int32(first), *r, nil); err != nil {
		t.Fatalf("set_rung refused a rung over its ignored link fields: %v", err)
	}

	cr := &rtRungs(m.rt)[first]
	if int(cr.next_rung) != int(second) {
		t.Errorf("wire NextRung was applied: next_rung = %d, want %d", int(cr.next_rung), second)
	}
	if cr.used == 0 {
		t.Error("wire Used=false was applied")
	}
}

func TestWrite_SetRungRefusesUnusedSlot(t *testing.T) {
	m := newStructModule(t)
	free := m.findFreeRung()
	_, err := m.SetRung(int32(free), api.Rung{Used: true}, nil)
	if err == nil {
		t.Fatal("set_rung on an unused slot succeeded")
	}
	if !errors.Is(err, syscall.ENOENT) {
		t.Errorf("error = %v, want it to wrap ENOENT", err)
	}
}

// set_program carries the chain fields, so it gets the whole-program chain
// validation — and a shorter upload must clear the slots past it, or the old
// program's tail stays chained into the new one.
func TestWrite_SetProgramValidatesChainsAndClearsTails(t *testing.T) {
	m := newStructModule(t)
	// Grow the live program to three rungs.
	first := int(rtSections(m.rt)[0].first_rung)
	if _, err := m.InsertRung(int32(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InsertRung(int32(first)); err != nil {
		t.Fatal(err)
	}

	prog, err := m.GetProgram()
	if err != nil {
		t.Fatal(err)
	}

	// A chain that never reaches lastRung is refused before anything lands.
	bad := *prog
	bad.Sections = append([]api.Section(nil), prog.Sections...)
	bad.Sections[0].LastRung = int32(m.findFreeRung())
	if _, err := m.SetProgram(bad); err == nil {
		t.Fatal("a program whose chain never reaches lastRung was accepted")
	} else if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}

	// A one-rung upload replaces the three-rung program completely.
	small := api.Program{
		Rungs:    []api.Rung{{Used: true, NextRung: 0, PrevRung: 0}},
		Sections: []api.Section{{Used: true, Name: "Main", SubRoutineNumber: -1}},
	}
	if _, err := m.SetProgram(small); err != nil {
		t.Fatalf("a minimal program was refused: %v", err)
	}
	used := 0
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		if rtRungs(m.rt)[i].used != 0 {
			used++
		}
	}
	if used != 1 {
		t.Errorf("%d rungs used after a one-rung upload; the old tail was not cleared", used)
	}
}

func TestWrite_OverlongUploadsAreRefused(t *testing.T) {
	m := newStructModule(t)

	prog, err := m.GetProgram()
	if err != nil {
		t.Fatal(err)
	}
	over := *prog
	over.Rungs = make([]api.Rung, int(m.rt.sizes.nbr_rungs)+1)
	if _, err := m.SetProgram(over); err == nil {
		t.Error("an upload with too many rungs was accepted")
	} else if !errors.Is(err, syscall.ERANGE) {
		t.Errorf("error = %v, want it to wrap ERANGE", err)
	}

	syms := make([]api.Symbol, int(m.rt.sizes.nbr_symbols)+1)
	if _, err := m.SetSymbols(syms); err == nil {
		t.Error("an upload with too many symbols was accepted")
	} else if !errors.Is(err, syscall.ERANGE) {
		t.Errorf("error = %v, want it to wrap ERANGE", err)
	}

	exprs := make([]api.ArithmExpr, int(m.rt.sizes.nbr_arithm_expr)+1)
	if _, err := m.SetExpressions(exprs); err == nil {
		t.Error("an upload with too many expressions was accepted")
	} else if !errors.Is(err, syscall.ERANGE) {
		t.Errorf("error = %v, want it to wrap ERANGE", err)
	}
}

// Strings the line-oriented .clp format cannot carry are refused at the
// write: a label holding a newline reloads as a different program, a comma
// in a symbol field shifts every later column.
func TestWrite_FormatBreakingStringsAreRefused(t *testing.T) {
	m := newStructModule(t)
	first := int32(rtSections(m.rt)[0].first_rung)

	r, err := m.GetRung(first)
	if err != nil {
		t.Fatal(err)
	}
	r.Label = "a\nb"
	if _, err := m.SetRung(first, *r, nil); err == nil {
		t.Error("a rung label holding a newline was accepted")
	} else if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}

	if _, err := m.SetSymbols([]api.Symbol{{VarName: "%B0", Symbol: "a,b"}}); err == nil {
		t.Error("a symbol holding a comma was accepted")
	}

	if _, err := m.AddSection("bad\nname", 0, -1); err == nil {
		t.Error("a section name holding a newline was accepted")
	}

	s, err := m.GetSection(0)
	if err != nil {
		t.Fatal(err)
	}
	s.Name = "x\x7fy"
	if _, err := m.SetSection(0, *s); err == nil {
		t.Error("a section name holding a control character was accepted")
	}
}

// Section lifecycle belongs to add_section/delete_section: set_section cannot
// un-use a section (orphaning its rungs unreclaimably), change its language,
// or conjure one out of a free slot.
func TestWrite_SetSectionLifecycleRules(t *testing.T) {
	m := newStructModule(t)
	s, err := m.GetSection(0)
	if err != nil {
		t.Fatal(err)
	}

	un := *s
	un.Used = false
	if _, err := m.SetSection(0, un); err == nil {
		t.Error("set_section un-used a live section")
	} else if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}

	lang := *s
	lang.Language = api.SectionLanguage_SEQUENTIAL
	lang.SequentialPage = 0
	if _, err := m.SetSection(0, lang); err == nil {
		t.Error("set_section changed a live section's language")
	}

	free := -1
	for i := 0; i < int(m.rt.sizes.nbr_sections); i++ {
		if rtSections(m.rt)[i].used == 0 {
			free = i
			break
		}
	}
	create := *s
	if _, err := m.SetSection(int32(free), create); err == nil {
		t.Error("set_section brought an unused section slot into use")
	} else if !errors.Is(err, syscall.ENOENT) {
		t.Errorf("error = %v, want it to wrap ENOENT", err)
	}

	// The plain rename still works, or the rules above are refusing everything.
	ok := *s
	ok.Name = "Renamed"
	if _, err := m.SetSection(0, ok); err != nil {
		t.Errorf("a plain rename was refused: %v", err)
	}
	if got := m.sectionToAPI(0).Name; got != "Renamed" {
		t.Errorf("section name = %q after rename", got)
	}
}

// Index refusals are the 404 class (the slot does not exist), capacity
// refusals the 503 class — neither is a controller failure.
func TestWrite_RefusalErrnoClasses(t *testing.T) {
	m := newStructModule(t)

	if _, err := m.GetRung(int32(m.rt.sizes.nbr_rungs)); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("out-of-range get_rung error = %v, want ENOENT", err)
	}

	// Fill the rung table and check the capacity refusal class.
	first := int(rtSections(m.rt)[0].first_rung)
	var lastErr error
	for {
		if _, lastErr = m.InsertRung(int32(first)); lastErr != nil {
			break
		}
	}
	if !errors.Is(lastErr, syscall.ENOSPC) {
		t.Errorf("full-table insert_rung error = %v, want ENOSPC", lastErr)
	}
	if !strings.Contains(lastErr.Error(), "no free rung slot") {
		t.Errorf("capacity refusal does not say what ran out: %v", lastErr)
	}
}

// A rung and the expressions it references land as one transaction: a
// refusal anywhere leaves the controller exactly as it was — this is what
// lets the editor's Cancel actually mean cancel.
func TestWrite_SetRungExpressionTransaction(t *testing.T) {
	m := newStructModule(t)
	first := int32(rtSections(m.rt)[0].first_rung)
	r, err := m.GetRung(first)
	if err != nil {
		t.Fatal(err)
	}

	// A bad expression refuses the whole write, rung edit included.
	r.Comment = "edited"
	if _, err := m.SetRung(first, *r, []api.ExprSlot{{Index: 0, Text: "%W0 >"}}); err == nil {
		t.Fatal("a write carrying an uncompilable expression was accepted")
	}
	if got := m.rungToAPI(int(first)).Comment; got == "edited" {
		t.Error("the rung half of a refused transaction was applied")
	}

	// A bad rung refuses the expression half.
	bad := *r
	bad.Elements[0] = api.Element{Type: 9999}
	if _, err := m.SetRung(first, bad, []api.ExprSlot{{Index: 0, Text: "%W0 > 5"}}); err == nil {
		t.Fatal("a write with a bad rung was accepted")
	}
	if got := storedExpr(m, 0); got != "" {
		t.Errorf("the expression half of a refused transaction was applied: %q", got)
	}

	// The valid transaction lands both halves.
	if _, err := m.SetRung(first, *r, []api.ExprSlot{{Index: 0, Text: "%W0 > 5"}}); err != nil {
		t.Fatalf("a valid transaction was refused: %v", err)
	}
	if got := m.rungToAPI(int(first)).Comment; got != "edited" {
		t.Errorf("rung comment = %q after the transaction", got)
	}
	if got := storedExpr(m, 0); got == "" {
		t.Error("the expression did not land")
	}

	// An empty text releases the slot in the same write.
	if _, err := m.SetRung(first, *r, []api.ExprSlot{{Index: 0, Text: ""}}); err != nil {
		t.Fatal(err)
	}
	if got := storedExpr(m, 0); got != "" {
		t.Errorf("slot not released: %q", got)
	}
}

// The generation counter is the only signal that another client or a load
// rewrote the program under a viewer's feet — every mutation must move it.
func TestWrite_GenerationMovesOnEveryMutation(t *testing.T) {
	m := newStructModule(t)
	first := int32(rtSections(m.rt)[0].first_rung)

	gen := func() uint32 {
		s, err := m.GetStatus()
		if err != nil {
			t.Fatal(err)
		}
		return s.Generation
	}

	before := gen()
	r, err := m.GetRung(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetRung(first, *r, nil); err != nil {
		t.Fatal(err)
	}
	if gen() == before {
		t.Error("set_rung did not move the generation")
	}

	before = gen()
	if _, err := m.InsertRung(first); err != nil {
		t.Fatal(err)
	}
	if gen() == before {
		t.Error("insert_rung did not move the generation")
	}
}
