// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import "testing"

// The runaway budget is shared down the whole (C)all tree: with a
// per-invocation counter, a caller that loops can re-run its callee's full
// 99999-iteration allowance on every pass — 99999^depth rung evaluations
// before anything stops. This construction (a self-jumping main rung calling
// a self-jumping sub-routine) finishes in well under a second with a shared
// budget and effectively never without it, so a regression shows up as this
// test hanging, not merely failing.
func TestLadder_RunawayBudgetSharedAcrossCalls(t *testing.T) {
	l := newLadderRT(2)
	defer l.free()

	l.setMainSection(0, 0, 0)
	l.setSubRoutine(1, 3, 1, 1)

	// Main rung: row 0 calls sub-routine 3, row 1 jumps back to itself.
	l.put(0, 0, 0, eleOutputCall, 0, 3)
	l.put(0, 0, 1, eleOutputJump, 0, 0)
	// Sub-routine rung: jumps to itself.
	l.put(1, 0, 0, eleOutputJump, 0, 1)

	l.setState(stateRun)
	l.scan(1)
	if l.state() != stateStop {
		t.Fatalf("PLC state = %d after a nested runaway, want STOP (%d)",
			l.state(), stateStop)
	}
}

// A transition whose deactivate list names no valid step must never fire.
// 2.9 read the empty list as "all upstream steps are active": the transition
// fired on every scan, reported a change every pass, and ran the settle loop
// to its full 50 iterations — the fires-forever defect the API validator
// already refuses, reachable here through a lenient file load.
func TestSFC_DanglingDeactivateNeverFires(t *testing.T) {
	rt := newTestRT()
	defer freeTestRT(rt)

	// A target step the transition would activate if it fired.
	rt.steps[1].num_page = 0
	rt.steps[1].step_number = 1

	// Transition with a true condition and an empty deactivate list.
	rt.transitions[0].num_page = 0
	rt.transitions[0].var_type_condi = 0 // CL_VAR_MEM_BIT
	rt.transitions[0].var_num_condi = 0
	rt.transitions[0].num_step_to_activ[0] = 1
	rt.transitions[0].num_step_to_activ[1] = -1
	// num_step_to_desactiv stays all -1 (init_data default).

	testPrepareSequential(rt)
	rtVarBits(rt)[0] = 1
	testRefreshSequentialPage(rt, 0)

	if rt.steps[1].activated != 0 {
		t.Error("a transition with no upstream steps fired")
	}
}

// Division and modulo by zero leave the left operand as the result — 2.9
// skips the operation entirely (arithm_eval.c), and only that choice keeps
// the differential oracle quiet. INT32_MIN / -1 must wrap, not SIGFPE.
func TestExpr_DivisionEdgeCasesMatch29(t *testing.T) {
	l := newLadderRT(1)
	defer l.free()

	if err := l.compileInto(0, "@200/0@ := @200/1@ / @200/2@", 1); err != nil {
		t.Fatal(err)
	}
	l.put(0, 9, 0, eleOutputOperate, 0, 0)
	for x := 0; x < 9; x++ {
		l.put(0, x, 0, eleConnection, 0, 0)
	}

	check := func(dividend, divisor, want int) {
		t.Helper()
		l.writeVar(varMemWord, 1, dividend)
		l.writeVar(varMemWord, 2, divisor)
		l.writeVar(varMemWord, 0, 12345)
		l.scan(1)
		if got := l.readVar(varMemWord, 0); got != want {
			t.Errorf("%d / %d = %d, want %d", dividend, divisor, got, want)
		}
	}

	check(42, 0, 42) // 2.9: zero divisor leaves the dividend
	check(42, 2, 21)
	check(-2147483648, -1, -2147483648) // wraps instead of SIGFPE
}
