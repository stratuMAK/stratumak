// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	api "github.com/stratuMAK/stratumak/src/stmak/generated/gmi/classicladder"
)

// --- Block parameters ---

// The API counts a preset in base units and the engine counts in milliseconds.
// Storing 5 for "5 seconds" would give a timer that elapses in 5ms.
func TestSetTimer_ScalesPresetByBase(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetTimer(2, 5, api.BASE_SECS); err != nil {
		t.Fatalf("set timer: %v", err)
	}
	if got := int(rtTimers(m.rt)[2].preset); got != 5000 {
		t.Errorf("engine preset = %dms, want 5000 — 5 units of a 1000ms base", got)
	}
	if got := int(rtTimers(m.rt)[2].base); got != 1000 {
		t.Errorf("engine base = %dms, want 1000", got)
	}

	// And it must read back in the units it was written in.
	prog, err := m.GetProgram()
	if err != nil {
		t.Fatalf("get program: %v", err)
	}
	if got := prog.Timers[2].Preset; got != 5 {
		t.Errorf("timer 2 preset reads back as %d, want 5", got)
	}
	if got := prog.Timers[2].Base; got != api.BASE_SECS {
		t.Errorf("timer 2 base reads back as %d, want BASE_SECS(%d)", got, api.BASE_SECS)
	}
}

func TestSetMonostable_ScalesPresetByBase(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetMonostable(0, 3, api.BASE_100MS); err != nil {
		t.Fatalf("set monostable: %v", err)
	}
	if got := int(rtMonostables(m.rt)[0].preset); got != 300 {
		t.Errorf("engine preset = %dms, want 300 — 3 units of a 100ms base", got)
	}

	prog, _ := m.GetProgram()
	if got := prog.Monostables[0].Preset; got != 3 {
		t.Errorf("monostable preset reads back as %d, want 3", got)
	}
	if got := prog.Monostables[0].Base; got != api.BASE_100MS {
		t.Errorf("monostable base reads back as %d, want BASE_100MS(%d)", got, api.BASE_100MS)
	}
}

// An IEC timer counts in base units, so unlike the old-style timer its preset
// is stored as given. Scaling it here would make it run `base` times too long.
func TestSetTimerIec_PresetIsUnscaled(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetTimerIec(1, 7, api.BASE_SECS, api.TimerIECMode_TOF); err != nil {
		t.Fatalf("set IEC timer: %v", err)
	}
	if got := int(rtTimersIec(m.rt)[1].preset); got != 7 {
		t.Errorf("engine preset = %d, want 7 — IEC presets count in base units", got)
	}
	if got := int(rtTimersIec(m.rt)[1].base); got != 1000 {
		t.Errorf("engine base = %dms, want 1000", got)
	}

	prog, _ := m.GetProgram()
	if got := prog.TimersIec[1].Mode; got != api.TimerIECMode_TOF {
		t.Errorf("IEC timer mode reads back as %d, want TOF(%d)", got, api.TimerIECMode_TOF)
	}
	if got := prog.TimersIec[1].Preset; got != 7 {
		t.Errorf("IEC timer preset reads back as %d, want 7", got)
	}
}

func TestSetCounter_Preset(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetCounter(4, 25); err != nil {
		t.Fatalf("set counter: %v", err)
	}
	if got := int(rtCounters(m.rt)[4].preset); got != 25 {
		t.Errorf("engine preset = %d, want 25", got)
	}
}

// The file parser falls back to seconds for a base it does not know, because a
// project that half-loads beats one that does not load. A write is the other
// case: silently substituting seconds hands back a timer that runs for a length
// nobody asked for.
func TestSetBlock_RejectsUnknownBase(t *testing.T) {
	m := newTestModule(t)

	rtTimers(m.rt)[0].preset = 1234
	rc, err := m.SetTimer(0, 5, 42)
	if err == nil {
		t.Fatal("set timer with base 42 succeeded; want a refusal")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error is %v, want it to wrap EINVAL so the transport answers 400", err)
	}
	if rc != -1 {
		t.Errorf("rc = %d, want -1", rc)
	}
	if got := int(rtTimers(m.rt)[0].preset); got != 1234 {
		t.Errorf("timer preset changed to %d despite the refusal", got)
	}

	if _, err := m.SetMonostable(0, 5, -1); err == nil {
		t.Error("set monostable with base -1 succeeded; want a refusal")
	}
	if _, err := m.SetTimerIec(0, 5, 3, api.TimerIECMode_TON); err == nil {
		t.Error("set IEC timer with base 3 succeeded; want a refusal")
	}
}

func TestSetTimerIec_RejectsUnknownMode(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetTimerIec(0, 5, api.BASE_SECS, api.TimerIECMode(9)); err == nil {
		t.Fatal("set IEC timer with mode 9 succeeded; want a refusal")
	}
}

// A block number past the configured count would index off the end of a fixed
// RT array.
func TestSetBlock_RejectsIndexOutOfRange(t *testing.T) {
	m := newTestModule(t)
	n := int32(m.rt.sizes.nbr_timers)

	for _, idx := range []int32{-1, n, n + 1} {
		rc, err := m.SetTimer(idx, 1, api.BASE_SECS)
		if err == nil {
			t.Errorf("set timer %d succeeded; only %d are configured", idx, n)
			continue
		}
		if !errors.Is(err, syscall.EINVAL) {
			t.Errorf("set timer %d: error is %v, want it to wrap EINVAL", idx, err)
		}
		if rc != -1 {
			t.Errorf("set timer %d: rc = %d, want -1", idx, rc)
		}
	}

	if _, err := m.SetCounter(int32(m.rt.sizes.nbr_counters), 1); err == nil {
		t.Error("set counter past the configured count succeeded")
	}
	if _, err := m.SetMonostable(-1, 1, api.BASE_SECS); err == nil {
		t.Error("set monostable -1 succeeded")
	}
	if _, err := m.SetTimerIec(int32(m.rt.sizes.nbr_timers_iec), 1, api.BASE_SECS, api.TimerIECMode_TON); err == nil {
		t.Error("set IEC timer past the configured count succeeded")
	}
}

// --- Single-expression writes ---

// Writing one expression must compile it, not just store the text: the scan
// runs bytecode, and an uncompiled expression is inert.
func TestSetExpressionText_CompilesTheEntry(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetExpressionText(0, "%W0:=7"); err != nil {
		t.Fatalf("set expression text: %v", err)
	}

	l := &ladderRT{rt: m.rt}
	l.putBlock(0, 2, 0, eleOutputOperate, 0, 3, 1)
	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)
	l.scan(1)

	if got := l.readVar(varMemWord, 0); got != 7 {
		t.Fatalf("%%W0 = %d after the write, want 7 — stored but not compiled", got)
	}

	// It must read back in written form, through the same conversion.
	texts, err := m.GetExpressionsText()
	if err != nil {
		t.Fatalf("get expressions text: %v", err)
	}
	if texts[0].Text != "%W0:=7" {
		t.Errorf("expression 0 reads back as %q, want %q", texts[0].Text, "%W0:=7")
	}
}

// An empty text clears the expression and its code — leaving the old bytecode
// installed would keep a deleted assignment running.
func TestSetExpressionText_EmptyClears(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetExpressionText(0, "%W0:=7"); err != nil {
		t.Fatalf("set expression text: %v", err)
	}
	if _, err := m.SetExpressionText(0, ""); err != nil {
		t.Fatalf("clear expression text: %v", err)
	}

	l := &ladderRT{rt: m.rt}
	l.putBlock(0, 2, 0, eleOutputOperate, 0, 3, 1)
	rtRungs(m.rt)[0].used = 1
	l.setMainSection(0, 0, 0)
	l.writeVar(varMemWord, 0, 3)
	l.scan(1)

	if got := l.readVar(varMemWord, 0); got != 3 {
		t.Errorf("%%W0 = %d, want 3 — the cleared expression still assigns", got)
	}
	texts, _ := m.GetExpressionsText()
	if texts[0].Text != "" {
		t.Errorf("expression 0 reads back as %q, want empty", texts[0].Text)
	}
}

// The reason this call exists rather than a read-modify-write of the whole
// table: a project file loads leniently, keeping an expression it could not
// compile and leaving it inert. The whole-table call stands behind everything
// it is given, so it would refuse every later edit until that one was fixed —
// including edits to unrelated rungs.
func TestSetExpressionText_UnaffectedByABrokenSibling(t *testing.T) {
	m := newTestModule(t)

	// Expression 1 is what a lenient load leaves behind: stored, uncompilable.
	copyStringToC(&rtArithmExprs(m.rt)[1].expr[0], "@200/0@ := (((", 50)
	rtCompiledExprs(m.rt)[1].valid = 0

	if _, err := m.SetExpressionText(0, "%W0:=7"); err != nil {
		t.Fatalf("editing expression 0 was refused because expression 1 is broken: %v", err)
	}
	if got := storedExpr(m, 0); got != "@200/0@:=7" {
		t.Errorf("expression 0 stored as %q, want %q", got, "@200/0@:=7")
	}
	// The broken one is left exactly as it was, not silently repaired or wiped.
	if got := storedExpr(m, 1); got != "@200/0@ := (((" {
		t.Errorf("expression 1 became %q; an edit to another entry must not touch it", got)
	}
}

func TestSetExpressionText_RejectsUnknownName(t *testing.T) {
	m := newTestModule(t)

	rc, err := m.SetExpressionText(0, "%W0:=%ZZ9")
	if err == nil {
		t.Fatal("an expression naming %ZZ9 was accepted")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("error is %v, want it to wrap EINVAL", err)
	}
	if rc != -1 {
		t.Errorf("rc = %d, want -1", rc)
	}
	if got := storedExpr(m, 0); got != "" {
		t.Errorf("expression 0 was written as %q despite the refusal", got)
	}
}

func TestSetExpressionText_RejectsUncompilable(t *testing.T) {
	m := newTestModule(t)

	if _, err := m.SetExpressionText(0, "%W0:=1"); err != nil {
		t.Fatalf("set a good expression: %v", err)
	}
	if _, err := m.SetExpressionText(0, "%W0:=((("); err == nil {
		t.Fatal("an uncompilable expression was accepted")
	}
	if got := storedExpr(m, 0); got != "@200/0@:=1" {
		t.Errorf("expression 0 is %q; a refused write must leave the running one alone", got)
	}
}

func TestSetExpressionText_RejectsIndexOutOfRange(t *testing.T) {
	m := newTestModule(t)
	n := int32(m.rt.sizes.nbr_arithm_expr)

	for _, idx := range []int32{-1, n} {
		_, err := m.SetExpressionText(idx, "%W0:=1")
		if err == nil {
			t.Errorf("expression %d was accepted; only %d are configured", idx, n)
			continue
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expression %d: error is %v, want it to say the index does not exist", idx, err)
		}
	}
}
