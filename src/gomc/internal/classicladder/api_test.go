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

// --- Expression writes ---

// The realtime engine evaluates bytecode. An expression stored without being
// recompiled leaves the scan running the previous program while the API cheerfully
// reports the new one.
func TestAPI_SetExpressionsRecompiles(t *testing.T) {
	m := newTestModule(t)

	exprs, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	exprs[0].Expr = "@200/0@:=7"
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set expressions: %v", err)
	}

	// Evaluating expression 0 must now assign 7 — not do nothing, which is what
	// an uncompiled expression does.
	l := &ladderRT{rt: m.rt}
	l.putBlock(0, 2, 0, eleOutputOperate, 0, 3, 1)
	m.rt.rungs[0].used = 1
	m.rt.sections[0].used = 1
	m.rt.sections[0].sub_routine_number = -1
	m.rt.sections[0].first_rung = 0
	m.rt.sections[0].last_rung = 0
	l.scan(1)

	if got := l.readVar(varMemWord, 0); got != 7 {
		t.Fatalf("%%W0 = %d after the write, want 7 — the expression was stored but not compiled", got)
	}

	// Replacing it must take effect too, rather than leaving the old bytecode.
	exprs[0].Expr = "@200/0@:=9"
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set expressions again: %v", err)
	}
	l.scan(1)
	if got := l.readVar(varMemWord, 0); got != 9 {
		t.Fatalf("%%W0 = %d after the second write, want 9 — stale bytecode", got)
	}
}

func TestAPI_SetExpressionsRejectsUncompilable(t *testing.T) {
	m := newTestModule(t)

	exprs, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	exprs[0].Expr = "@200/0@:=1"
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set a good expression: %v", err)
	}

	exprs[1].Expr = "@200/0@ := ((("
	rc, err := m.SetExpressions(exprs)
	if err == nil {
		t.Fatal("an uncompilable expression was accepted")
	}
	if rc == 0 {
		t.Error("failed write returned rc 0")
	}
	// EINVAL so the transport answers 400 rather than 500: a bad expression is
	// the caller's mistake, not a broken controller.
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}

	// The good expression must survive the refused batch.
	got, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	if got[0].Expr != "@200/0@:=1" {
		t.Errorf("expression 0 = %q after a refused write, want it unchanged", got[0].Expr)
	}
	if got[1].Expr != "" {
		t.Errorf("expression 1 = %q, want the refused write not to have stored it", got[1].Expr)
	}
}

// The same for a whole-program upload, which is the path an editor's "save"
// takes.
func TestAPI_SetProgramRecompiles(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	prog, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	prog.ArithmExprs[0].Expr = "@200/0@:=4"
	prog.Rungs[0].Used = true
	// [ expr ] operate block, head at column 2 of row 0.
	prog.Rungs[0].Elements[0] = api.Element{Type: eleUnusable}
	prog.Rungs[0].Elements[1] = api.Element{Type: eleUnusable}
	prog.Rungs[0].Elements[2] = api.Element{Type: eleOutputOperate, VarNum: 0}
	prog.Sections[0].Used = true
	prog.Sections[0].SubRoutineNumber = -1
	prog.Sections[0].FirstRung = 0
	prog.Sections[0].LastRung = 0
	if _, err := m.SetProgram(*prog); err != nil {
		t.Fatalf("set program: %v", err)
	}

	l.scan(1)
	if got := l.readVar(varMemWord, 0); got != 4 {
		t.Fatalf("%%W0 = %d after uploading the program, want 4 — the expressions were stored but not compiled", got)
	}

	// And a second upload must replace the bytecode, not leave the first.
	prog.ArithmExprs[0].Expr = "@200/0@:=6"
	if _, err := m.SetProgram(*prog); err != nil {
		t.Fatalf("set program again: %v", err)
	}
	l.scan(1)
	if got := l.readVar(varMemWord, 0); got != 6 {
		t.Fatalf("%%W0 = %d after the second upload, want 6 — stale bytecode", got)
	}
}

// A refused program must leave the running ladder alone — not half-replaced.
func TestAPI_SetProgramRefusedLeavesProgramIntact(t *testing.T) {
	m := newTestModule(t)

	prog, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	prog.Rungs[0].Used = true
	prog.Rungs[0].Label = "first"
	prog.ArithmExprs[0].Expr = "@200/0@:=1"
	if _, err := m.SetProgram(*prog); err != nil {
		t.Fatalf("set program: %v", err)
	}

	bad, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	bad.Rungs[0].Label = "second"
	bad.ArithmExprs[2].Expr = "@@@"
	if _, err := m.SetProgram(*bad); err == nil {
		t.Fatal("a program with an uncompilable expression was accepted")
	}

	after, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	if after.Rungs[0].Label != "first" {
		t.Errorf("rung label = %q after a refused program, want %q — the write was applied before it was validated",
			after.Rungs[0].Label, "first")
	}
	if after.ArithmExprs[2].Expr != "" {
		t.Errorf("expression 2 = %q after a refused program, want it empty", after.ArithmExprs[2].Expr)
	}
}

// --- Expressions in written form ---

// The written and stored forms convert here so that no client needs a second
// parser. Reading, editing and writing back must all agree.
func TestAPI_ExpressionsTextRoundTrip(t *testing.T) {
	m := newTestModule(t)

	exprs, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	exprs[0].Expr = "@200/0@>5"
	exprs[1].Expr = "@200/1@:=@270/2@+1"
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set expressions: %v", err)
	}

	texts, err := m.GetExpressionsText()
	if err != nil {
		t.Fatalf("get expression text: %v", err)
	}
	if texts[0].Text != "%W0>5" {
		t.Errorf("expression 0 text = %q, want %q", texts[0].Text, "%W0>5")
	}
	if texts[1].Text != "%W1:=%IW2+1" {
		t.Errorf("expression 1 text = %q, want %q", texts[1].Text, "%W1:=%IW2+1")
	}
	if texts[2].Text != "" {
		t.Errorf("empty expression rendered as %q, want empty", texts[2].Text)
	}

	// Writing the text back unchanged must not change the program: an editor
	// that opens a rung and saves it without editing has to be a no-op.
	if _, err := m.SetExpressionsText(texts); err != nil {
		t.Fatalf("set expression text: %v", err)
	}
	after, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	for i := range exprs {
		if after[i].Expr != exprs[i].Expr {
			t.Errorf("expression %d = %q after a text round trip, want %q",
				i, after[i].Expr, exprs[i].Expr)
		}
	}
}

// Editing through the text form must reach the engine, not just the store.
func TestAPI_ExpressionsTextTakesEffect(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	texts, err := m.GetExpressionsText()
	if err != nil {
		t.Fatalf("get expression text: %v", err)
	}
	texts[0].Text = "%W0:=11"
	if _, err := m.SetExpressionsText(texts); err != nil {
		t.Fatalf("set expression text: %v", err)
	}

	l.putBlock(0, 2, 0, eleOutputOperate, 0, 3, 1)
	m.rt.rungs[0].used = 1
	m.rt.sections[0].used = 1
	m.rt.sections[0].sub_routine_number = -1
	m.rt.sections[0].first_rung = 0
	m.rt.sections[0].last_rung = 0
	l.scan(1)

	if got := l.readVar(varMemWord, 0); got != 11 {
		t.Fatalf("%%W0 = %d, want 11 — the written expression never reached the engine", got)
	}
}

// An expression holding a reference this implementation cannot name must still
// survive a read-and-write-back. Rendering keeps such a reference in its stored
// form, so the text that comes out has to be text that can go back in —
// otherwise opening a project written by another version and saving it without
// editing anything would fail.
func TestAPI_UnrenderableExpressionSurvivesTextRoundTrip(t *testing.T) {
	m := newTestModule(t)

	exprs, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	const stored = "@999/0@:=@200/0@"
	exprs[0].Expr = stored
	if _, err := m.SetExpressions(exprs); err != nil {
		t.Fatalf("set expressions: %v", err)
	}

	texts, err := m.GetExpressionsText()
	if err != nil {
		t.Fatalf("get expression text: %v", err)
	}
	if !strings.Contains(texts[0].Text, "@999/0@") {
		t.Fatalf("text = %q, want the unrenderable reference kept", texts[0].Text)
	}

	if _, err := m.SetExpressionsText(texts); err != nil {
		t.Fatalf("writing back the rendered text failed: %v", err)
	}
	after, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	if after[0].Expr != stored {
		t.Errorf("expression = %q after a text round trip, want %q", after[0].Expr, stored)
	}
}

func TestAPI_ExpressionsTextRejectsUnknownNames(t *testing.T) {
	m := newTestModule(t)

	texts, err := m.GetExpressionsText()
	if err != nil {
		t.Fatalf("get expression text: %v", err)
	}
	texts[0].Text = "%W0:=1"
	if _, err := m.SetExpressionsText(texts); err != nil {
		t.Fatalf("a valid written expression was refused: %v", err)
	}

	texts[1].Text = "%Z9:=1"
	if _, err := m.SetExpressionsText(texts); err == nil {
		t.Fatal("an expression naming a variable that does not exist was accepted")
	} else if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error = %v, want it to wrap EINVAL", err)
	}

	// The refused batch must leave the earlier expression alone.
	after, err := m.GetExpressions()
	if err != nil {
		t.Fatalf("get expressions: %v", err)
	}
	if after[0].Expr != "@200/0@:=1" {
		t.Errorf("expression 0 = %q after a refused batch, want it unchanged", after[0].Expr)
	}
	if after[1].Expr != "" {
		t.Errorf("expression 1 = %q, want the refused write not to have stored it", after[1].Expr)
	}
}

// --- Index handling ---

func TestAPI_RungIndexBounds(t *testing.T) {
	m := newTestModule(t)
	n := int32(m.rt.sizes.nbr_rungs)

	for _, idx := range []int32{-1, n, n + 1} {
		if _, err := m.GetRung(idx); err == nil {
			t.Errorf("GetRung(%d) succeeded, want an error", idx)
		}
		if _, err := m.SetRung(idx, api.Rung{}); err == nil {
			t.Errorf("SetRung(%d) succeeded, want an error", idx)
		}
	}
	if _, err := m.GetRung(0); err != nil {
		t.Errorf("GetRung(0) failed: %v", err)
	}
}

func TestAPI_SectionIndexBounds(t *testing.T) {
	m := newTestModule(t)
	n := int32(m.rt.sizes.nbr_sections)

	for _, idx := range []int32{-1, n, n + 1} {
		if _, err := m.GetSection(idx); err == nil {
			t.Errorf("GetSection(%d) succeeded, want an error", idx)
		}
		if _, err := m.SetSection(idx, api.Section{}); err == nil {
			t.Errorf("SetSection(%d) succeeded, want an error", idx)
		}
	}
}

// set_variable takes a type and an offset straight off the wire. Out-of-range
// offsets must be refused by the variable layer rather than indexing an array.
func TestAPI_SetVariableOutOfRangeIsInert(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	for _, tc := range []struct{ varType, offset int32 }{
		{varMemBit, -1},
		{varMemBit, int32(m.rt.sizes.nbr_bits)},
		{varMemWord, int32(m.rt.sizes.nbr_words) + 100},
		{varPhysOutput, 1 << 20},
		{9999, 0}, // unknown region
	} {
		if _, err := m.SetVariable(tc.varType, tc.offset, 1); err != nil {
			t.Errorf("SetVariable(%d, %d) returned %v; it should be inert, not an error",
				tc.varType, tc.offset, err)
		}
	}

	// Nothing in range should have been touched.
	for i := 0; i < int(m.rt.sizes.nbr_bits); i++ {
		if l.bit(i) {
			t.Fatalf("bit %d was set by an out-of-range write", i)
		}
	}
}

func TestAPI_SetVariableInRangeTakesEffect(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	if _, err := m.SetVariable(varMemBit, 3, 1); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	if !l.bit(3) {
		t.Error("%B3 was not set")
	}
	if _, err := m.SetVariable(varMemWord, 4, -12); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	if got := l.readVar(varMemWord, 4); got != -12 {
		t.Errorf("%%W4 = %d, want -12", got)
	}
}

// --- Round trip through the wire types ---

// A rung read out and written straight back must come back identical: the
// element grid is flattened row-major on the way out and rebuilt on the way in,
// and transposing it there would quietly rewire every program an editor saves.
func TestAPI_RungRoundTripPreservesGrid(t *testing.T) {
	m := newTestModule(t)
	l := &ladderRT{rt: m.rt}

	// A shape that is not symmetric under transposition.
	l.put(0, 0, 0, eleInput, varPhysInput, 1)
	l.put(0, 1, 0, eleInputNot, varMemBit, 2)
	l.put(0, 9, 0, eleOutput, varPhysOutput, 3)
	l.put(0, 0, 4, eleInput, varPhysInput, 4)
	l.connectTop(0, 1, 1)
	m.rt.rungs[0].used = 1

	before, err := m.GetRung(0)
	if err != nil {
		t.Fatalf("get rung: %v", err)
	}
	if _, err := m.SetRung(0, *before); err != nil {
		t.Fatalf("set rung: %v", err)
	}
	after, err := m.GetRung(0)
	if err != nil {
		t.Fatalf("get rung again: %v", err)
	}

	for i := range before.Elements {
		if before.Elements[i] != after.Elements[i] {
			row, col := i/10, i%10
			t.Fatalf("element at row %d col %d changed: %+v -> %+v",
				row, col, before.Elements[i], after.Elements[i])
		}
	}

	// And the grid must still mean the same thing to the engine.
	if got := m.rt.rungs[0].elements[0][0]._type; got != eleInput {
		t.Errorf("element [col 0][row 0] type = %d, want %d", got, eleInput)
	}
	if got := m.rt.rungs[0].elements[0][4].var_num; got != 4 {
		t.Errorf("element [col 0][row 4] varNum = %d, want 4", got)
	}
	if got := m.rt.rungs[0].elements[9][0]._type; got != eleOutput {
		t.Errorf("element [col 9][row 0] type = %d, want %d", got, eleOutput)
	}
}
