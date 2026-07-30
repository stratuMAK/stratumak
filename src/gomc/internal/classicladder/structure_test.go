// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
)

// rungChain walks a section the way the engine does and returns the rung
// indices in scan order. It gives up after more steps than there are rungs,
// which is how a broken chain shows itself.
func rungChain(t *testing.T, m *classicladder, section int) []int {
	t.Helper()

	sec := &m.rt.sections[section]
	var order []int
	idx := int(sec.first_rung)
	for n := 0; n <= int(m.rt.sizes.nbr_rungs)+1; n++ {
		if idx < 0 || idx >= int(m.rt.sizes.nbr_rungs) {
			t.Fatalf("chain of section %d left the array at %d", section, idx)
		}
		order = append(order, idx)
		if idx == int(sec.last_rung) {
			return order
		}
		idx = int(m.rt.rungs[idx].next_rung)
	}
	t.Fatalf("chain of section %d never reached lastRung; order so far %v", section, order)
	return nil
}

// newStructModule gives a module with one ladder section holding one rung.
func newStructModule(t *testing.T) *classicladder {
	t.Helper()
	m := newTestModule(t)
	if _, err := m.addSection("Main", 0, -1); err != nil {
		t.Fatalf("add the first section: %v", err)
	}
	return m
}

// --- Rungs ---

func TestStruct_InsertRungKeepsChainOrdered(t *testing.T) {
	m := newStructModule(t)
	first := int(m.rt.sections[0].first_rung)

	second, err := m.InsertRung(int32(first))
	if err != nil {
		t.Fatalf("insert after the first rung: %v", err)
	}
	middle, err := m.InsertRung(int32(first))
	if err != nil {
		t.Fatalf("insert between: %v", err)
	}

	got := rungChain(t, m, 0)
	want := []int{first, int(middle), int(second)}
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain = %v, want %v", got, want)
		}
	}
	if int(m.rt.sections[0].last_rung) != int(second) {
		t.Errorf("lastRung = %d, want %d", m.rt.sections[0].last_rung, second)
	}
	// The backward links have to agree with the forward ones, or an editor
	// walking up from a rung goes somewhere else.
	for i := 1; i < len(got); i++ {
		if int(m.rt.rungs[got[i]].prev_rung) != got[i-1] {
			t.Errorf("rung %d prevRung = %d, want %d",
				got[i], m.rt.rungs[got[i]].prev_rung, got[i-1])
		}
	}
}

func TestStruct_InsertRungAppendsWithMinusOne(t *testing.T) {
	m := newStructModule(t)
	first := int(m.rt.sections[0].first_rung)

	added, err := m.InsertRung(-1)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got := rungChain(t, m, 0)
	if len(got) != 2 || got[0] != first || got[1] != int(added) {
		t.Fatalf("chain = %v, want [%d %d]", got, first, added)
	}
}

func TestStruct_DeleteRungRelinks(t *testing.T) {
	m := newStructModule(t)
	a := int(m.rt.sections[0].first_rung)
	b, _ := m.InsertRung(int32(a))
	c, _ := m.InsertRung(b)

	if _, err := m.DeleteRung(b); err != nil {
		t.Fatalf("delete the middle rung: %v", err)
	}
	got := rungChain(t, m, 0)
	if len(got) != 2 || got[0] != a || got[1] != int(c) {
		t.Fatalf("chain after deleting the middle = %v, want [%d %d]", got, a, c)
	}
	if m.rt.rungs[b].used != 0 {
		t.Error("the deleted rung is still marked used")
	}

	// Deleting the head must move firstRung.
	if _, err := m.DeleteRung(int32(a)); err != nil {
		t.Fatalf("delete the first rung: %v", err)
	}
	if int(m.rt.sections[0].first_rung) != int(c) {
		t.Errorf("firstRung = %d after deleting the head, want %d",
			m.rt.sections[0].first_rung, c)
	}
	got = rungChain(t, m, 0)
	if len(got) != 1 || got[0] != int(c) {
		t.Fatalf("chain = %v, want [%d]", got, c)
	}
}

// A section always holds at least one rung: the scan reads firstRung without
// checking, so an empty section would evaluate a freed slot.
func TestStruct_DeleteLastRungOfSectionRefused(t *testing.T) {
	m := newStructModule(t)
	only := int(m.rt.sections[0].first_rung)

	_, err := m.DeleteRung(int32(only))
	if err == nil {
		t.Fatal("deleting the only rung of a section was allowed")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}
	if m.rt.rungs[only].used == 0 {
		t.Error("the rung was freed anyway")
	}
}

func TestStruct_DeleteRungReleasesItsExpressions(t *testing.T) {
	m := newStructModule(t)
	a := int(m.rt.sections[0].first_rung)
	b, _ := m.InsertRung(int32(a))

	exprs, _ := m.GetExpressions()
	exprs[0].Expr = "@200/0@>1"
	exprs[1].Expr = "@200/1@>2"
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set expressions: %v", err)
	}

	// Rung b uses expression 0; rung a uses both 0 and 1.
	putCompar := func(rung, exprNum int) {
		r, err := m.GetRung(int32(rung))
		if err != nil {
			t.Fatalf("get rung %d: %v", rung, err)
		}
		r.Elements[0] = api.Element{Type: eleUnusable}
		r.Elements[1] = api.Element{Type: eleUnusable}
		r.Elements[2] = api.Element{Type: eleCompar, VarNum: int32(exprNum)}
		if _, err := m.SetRung(int32(rung), *r); err != nil {
			t.Fatalf("set rung %d: %v", rung, err)
		}
	}
	putCompar(a, 0)
	putCompar(int(b), 0)

	if _, err := m.DeleteRung(b); err != nil {
		t.Fatalf("delete rung: %v", err)
	}
	after, _ := m.GetExpressions()
	if after[0].Expr != "@200/0@>1" {
		t.Errorf("expression 0 = %q after deleting one of two users, want it kept",
			after[0].Expr)
	}

	// Now rung a is the only user; it cannot be deleted (last in section), so
	// delete its element instead and check the shared-use logic the other way:
	// deleting a rung that is the sole user does release the expression.
	c, _ := m.InsertRung(int32(a))
	putCompar(int(c), 1)
	if _, err := m.DeleteRung(c); err != nil {
		t.Fatalf("delete rung: %v", err)
	}
	after, _ = m.GetExpressions()
	if after[1].Expr != "" {
		t.Errorf("expression 1 = %q after deleting its only user, want it released",
			after[1].Expr)
	}
}

// --- Sections ---

func TestStruct_AddSectionGivesItARung(t *testing.T) {
	m := newStructModule(t)

	idx, err := m.AddSection("Second", 0, -1)
	if err != nil {
		t.Fatalf("add section: %v", err)
	}
	sec := &m.rt.sections[idx]
	if sec.used == 0 {
		t.Fatal("the new section is not marked used")
	}
	if m.rt.rungs[sec.first_rung].used == 0 {
		t.Error("the new section's rung is not marked used")
	}
	if sec.first_rung != sec.last_rung {
		t.Errorf("a new section should hold exactly one rung, got first=%d last=%d",
			sec.first_rung, sec.last_rung)
	}
	// And it must not have taken the rung of the section that already existed.
	if int(sec.first_rung) == int(m.rt.sections[0].first_rung) {
		t.Error("the new section reused another section's rung")
	}
}

func TestStruct_AddSectionRejectsDuplicates(t *testing.T) {
	m := newStructModule(t)

	if _, err := m.AddSection("Sub", 0, 3); err != nil {
		t.Fatalf("add a sub-routine: %v", err)
	}
	if _, err := m.AddSection("Sub", 0, 4); err == nil {
		t.Error("a duplicate section name was accepted")
	}
	if _, err := m.AddSection("Other", 0, 3); err == nil {
		t.Error("a duplicate sub-routine number was accepted")
	}
	if _, err := m.AddSection("", 0, -1); err == nil {
		t.Error("an empty section name was accepted")
	}
}

func TestStruct_DeleteSectionFreesItsOwnRungs(t *testing.T) {
	m := newStructModule(t)
	keepRung := int(m.rt.sections[0].first_rung)

	idx, err := m.AddSection("Second", 0, -1)
	if err != nil {
		t.Fatalf("add section: %v", err)
	}
	r1 := int(m.rt.sections[idx].first_rung)
	r2, _ := m.InsertRung(int32(r1))

	if _, err := m.DeleteSection(idx); err != nil {
		t.Fatalf("delete section: %v", err)
	}
	if m.rt.sections[idx].used != 0 {
		t.Error("the section is still marked used")
	}
	if m.rt.rungs[r1].used != 0 || m.rt.rungs[int(r2)].used != 0 {
		t.Error("the section's rungs were not freed")
	}
	// 2.9 walks the on-screen section's globals here, so deleting any other
	// section frees the wrong rungs. The surviving section must be intact.
	if m.rt.rungs[keepRung].used == 0 {
		t.Error("deleting one section freed another section's rung")
	}
	if chain := rungChain(t, m, 0); len(chain) != 1 || chain[0] != keepRung {
		t.Errorf("surviving chain = %v, want [%d]", chain, keepRung)
	}
}

func TestStruct_DeleteLastSectionRefused(t *testing.T) {
	m := newStructModule(t)

	if _, err := m.DeleteSection(0); err == nil {
		t.Fatal("deleting the only section was allowed")
	}
	if m.rt.sections[0].used == 0 {
		t.Error("the section was freed anyway")
	}
}

func TestStruct_SequentialSectionGetsAFreePage(t *testing.T) {
	m := newStructModule(t)

	a, err := m.AddSection("Chart1", 1, -1)
	if err != nil {
		t.Fatalf("add a sequential section: %v", err)
	}
	b, err := m.AddSection("Chart2", 1, -1)
	if err != nil {
		t.Fatalf("add a second sequential section: %v", err)
	}
	if m.rt.sections[a].sequential_page == m.rt.sections[b].sequential_page {
		t.Errorf("both charts got page %d; they must not share one",
			m.rt.sections[a].sequential_page)
	}
}

// --- Rung validation ---

// The shipped project has to pass the validator. If it does not, the validator
// is describing a ladder nobody writes.
func TestValidate_ShippedProjectRoundTrips(t *testing.T) {
	m := newTestModule(t)
	src, err := filepath.Abs(demoProject)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if err := m.loadCLPFile(src); err != nil {
		t.Fatalf("load project: %v", err)
	}
	prog, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	if _, err := m.SetProgram(*prog); err != nil {
		t.Fatalf("the shipped project failed validation on the way back in: %v", err)
	}
}

func TestValidate_RejectsMalformedRungs(t *testing.T) {
	m := newStructModule(t)
	idx := int32(m.rt.sections[0].first_rung)

	base := func() *api.Rung {
		r, err := m.GetRung(idx)
		if err != nil {
			t.Fatalf("get rung: %v", err)
		}
		return r
	}

	for _, tc := range []struct {
		name  string
		build func(r *api.Rung)
	}{
		{"unknown element type", func(r *api.Rung) {
			r.Elements[0] = api.Element{Type: 77}
		}},
		{"block runs off the left edge", func(r *api.Rung) {
			// A 3-wide operate needs its head at column 2 or further right.
			r.Elements[1] = api.Element{Type: eleOutputOperate}
		}},
		{"block runs off the bottom", func(r *api.Rung) {
			// A counter is 4 rows; row 3 leaves only 3.
			r.Elements[3*10+2] = api.Element{Type: eleCounter}
			for _, i := range []int{3*10 + 1, 4*10 + 1, 4*10 + 2, 5*10 + 1, 5*10 + 2} {
				r.Elements[i] = api.Element{Type: eleUnusable}
			}
		}},
		{"body cell not marked as one", func(r *api.Rung) {
			r.Elements[2] = api.Element{Type: eleOutputOperate}
			r.Elements[0] = api.Element{Type: eleUnusable}
			r.Elements[1] = api.Element{Type: eleInput} // should be a body cell
		}},
		{"stray body cell", func(r *api.Rung) {
			r.Elements[5] = api.Element{Type: eleUnusable}
		}},
		{"overlapping blocks", func(r *api.Rung) {
			r.Elements[2] = api.Element{Type: eleOutputOperate}
			r.Elements[0] = api.Element{Type: eleUnusable}
			r.Elements[1] = api.Element{Type: eleUnusable}
			r.Elements[3] = api.Element{Type: eleOutputOperate, VarNum: 1}
			// its body would need cells 1 and 2, already taken
		}},
		{"timer number out of range", func(r *api.Rung) {
			r.Elements[1] = api.Element{Type: eleTimer, VarNum: 9999}
			r.Elements[0] = api.Element{Type: eleUnusable}
			r.Elements[10] = api.Element{Type: eleUnusable}
			r.Elements[11] = api.Element{Type: eleUnusable}
		}},
		{"contact on a variable that does not exist", func(r *api.Rung) {
			r.Elements[0] = api.Element{Type: eleInput, VarType: varPhysInput, VarNum: 9999}
		}},
		{"coil driving a read-only variable", func(r *api.Rung) {
			r.Elements[0] = api.Element{Type: eleOutput, VarType: varPhysInput, VarNum: 0}
		}},
		{"jump to a rung that does not exist", func(r *api.Rung) {
			r.Elements[0] = api.Element{Type: eleOutputJump, VarNum: 9999}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.build(r)
			if _, err := m.SetRung(idx, *r); err == nil {
				t.Errorf("%s was accepted", tc.name)
			} else if !errors.Is(err, syscall.EINVAL) {
				t.Errorf("error = %v, want it to wrap EINVAL", err)
			}
		})
	}
}

// insert_rung and delete_rung exist so a client never writes the links, but
// set_section can still reach them. A chain that never arrives at lastRung is
// not a wrong program — it is a scan that only stops when the runaway guard
// trips and takes the PLC down with it.
func TestValidate_RejectsBrokenSectionChains(t *testing.T) {
	m := newStructModule(t)
	a := int(m.rt.sections[0].first_rung)
	b, _ := m.InsertRung(int32(a))

	good, err := m.GetSection(0)
	if err != nil {
		t.Fatalf("get section: %v", err)
	}

	for _, tc := range []struct {
		name  string
		build func(s *api.Section)
	}{
		{"firstRung out of range", func(s *api.Section) { s.FirstRung = 9999 }},
		{"lastRung out of range", func(s *api.Section) { s.LastRung = -5 }},
		{"lastRung unreachable from firstRung", func(s *api.Section) {
			// A rung that exists but is not on this chain.
			free := m.findFreeRung()
			m.rt.rungs[free].used = 1
			s.LastRung = int32(free)
		}},
		{"chain includes a freed rung", func(s *api.Section) {
			m.rt.rungs[b].used = 0
			s.LastRung = b
		}},
		{"sequential page out of range", func(s *api.Section) {
			s.Language = api.SectionLanguage_SEQUENTIAL
			s.SequentialPage = 99
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := *good
			tc.build(&s)
			if _, err := m.SetSection(0, s); err == nil {
				t.Errorf("%s was accepted", tc.name)
			} else if !errors.Is(err, syscall.EINVAL) {
				t.Errorf("error = %v, want it to wrap EINVAL", err)
			}
		})
		// Undo whatever the case did to the rung array.
		m.rt.rungs[b].used = 1
	}

	// The valid one must still go through, or the check is just refusing
	// everything.
	if _, err := m.SetSection(0, *good); err != nil {
		t.Errorf("a valid section was refused: %v", err)
	}
}

func TestValidate_AcceptsWellFormedRungs(t *testing.T) {
	m := newStructModule(t)
	idx := int32(m.rt.sections[0].first_rung)

	r, err := m.GetRung(idx)
	if err != nil {
		t.Fatalf("get rung: %v", err)
	}
	// %I0 --| |-- [ timer 0 ] -- ( ) %Q0, with the timer's body cells marked.
	r.Elements[0] = api.Element{Type: eleInput, VarType: varPhysInput, VarNum: 0}
	r.Elements[2] = api.Element{Type: eleTimer, VarNum: 0}
	r.Elements[1] = api.Element{Type: eleUnusable}
	r.Elements[10+1] = api.Element{Type: eleUnusable}
	r.Elements[10+2] = api.Element{Type: eleUnusable}
	r.Elements[3] = api.Element{Type: eleOutput, VarType: varPhysOutput, VarNum: 0}

	if _, err := m.SetRung(idx, *r); err != nil {
		t.Fatalf("a well-formed rung was refused: %v", err)
	}
}
