// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// The C names an fsm block generates, and the check that they do not
// collide with each other, with the component's declarations, or with what
// the generated file and C itself define.  A collision would otherwise only
// show up as a compiler error in the generated .c.

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// hidden returns the prefix of f's hidden names.  __fsm_ is reserved for
// them.
func (f *fsmDecl) hidden() string { return "__fsm_" + f.Name }

// enumName returns the enum constant of a state.
func (f *fsmDecl) enumName(state string) string { return f.Name + "_" + state }

// helperName returns the per-state helper function of kind enter, exit or
// during.
func (f *fsmDecl) helperName(kind, state string) string {
	return fmt.Sprintf("%s_%s_%s", f.hidden(), kind, state)
}

// stateVarName returns the C expression holding the state.
func (f *fsmDecl) stateVarName() string {
	if f.StateVar != nil {
		return f.StateVar.Name
	}
	return f.hidden() + "_state"
}

// actionTrans pairs a transition that has an action block with the state
// whose item it is (nil for an any item).
type actionTrans struct {
	From *fsmState
	T    *fsmTrans
}

// actions returns every transition with an action block, numbered by their
// index here: any items first, then the states in order.
func (f *fsmDecl) actions() []actionTrans {
	var out []actionTrans
	add := func(from *fsmState, t *fsmTrans) {
		if t != nil && t.Action != nil {
			out = append(out, actionTrans{from, t})
		}
	}
	for i := range f.AnyOn {
		add(nil, &f.AnyOn[i])
	}
	add(nil, f.AnyTimeout)
	for _, s := range f.States {
		for i := range s.On {
			add(s, &s.On[i])
		}
		if s.Timeout != nil {
			add(s, s.Timeout.Trans)
		}
	}
	return out
}

// actionName returns the helper function of the k-th action.
func (f *fsmDecl) actionName(k int) string { return fmt.Sprintf("%s_action_%d", f.hidden(), k) }

// genName is a generated identifier and what it is, for messages.
type genName struct {
	Name, What string
	Pos        ast.Pos
}

// generatedNames lists every identifier the lowering defines for f.
func (f *fsmDecl) generatedNames() []genName {
	n := f.Name
	names := []genName{
		{n, "the call macro of fsm " + n, f.Pos},
		{n + "_in", "the state test macro of fsm " + n, f.Pos},
		{n + "_run", "the run function of fsm " + n, f.Pos},
		{n + "_state_name", "the state name function of fsm " + n, f.Pos},
		{f.hidden() + "_init", "a hidden variable of fsm " + n, f.Pos},
		{f.hidden() + "_enter", "a hidden variable of fsm " + n, f.Pos},
		{f.hidden() + "_timer", "a hidden variable of fsm " + n, f.Pos},
	}
	if f.StateVar == nil {
		names = append(names, genName{f.stateVarName(), "a hidden variable of fsm " + n, f.Pos})
	}
	for _, s := range f.States {
		names = append(names, genName{f.enumName(s.Name), fmt.Sprintf("the constant of state %s of fsm %s", s.Name, n), s.Pos})
		for _, h := range []struct {
			kind string
			blk  *cfrag
		}{{"enter", s.Enter}, {"exit", s.Exit}, {"during", s.During}} {
			if h.blk != nil {
				names = append(names, genName{f.helperName(h.kind, s.Name),
					fmt.Sprintf("the %s helper of state %s of fsm %s", h.kind, s.Name, n), s.Pos})
			}
		}
	}
	for k, a := range f.actions() {
		names = append(names, genName{f.actionName(k), "an action helper of fsm " + n, a.T.Pos})
	}
	return names
}

// cKeywords are the C11/C23 keywords; a generated name may not be one.
var cKeywords = strings.Fields(`auto break case char const continue default do double else
	enum extern float for goto if inline int long register restrict return short signed
	sizeof static struct switch typedef union unsigned void volatile while
	_Alignas _Alignof _Atomic _Bool _Complex _Generic _Imaginary _Noreturn
	_Static_assert _Thread_local alignas alignof bool constexpr false nullptr
	static_assert thread_local true typeof typeof_unqual`)

// cgenNames are defined by every generated component, or by the headers it
// includes and user code commonly calls (math.h).
var cgenNames = strings.Fields(`inst_t __comp_inst period fperiod personality data
	FUNCTION FOR_ALL_INSTS EXTRA_SETUP EXTRA_CLEANUP extra_setup extra_cleanup
	MCODE MCODE_ABORTED MCODE_SLEEP user_mainloop TRUE FALSE rtapi_get_time
	STMAK_LOG STMAK_LOG_ERR STMAK_LOG_WARN STMAK_LOG_INFO STMAK_LOG_DBG
	STMAK_EXIT_FD STMAK_SHOULD_EXIT STMAK_NONBLOCKING
	acos asin atan atan2 ceil cos cosh exp fabs floor fmod frexp ldexp log log10
	modf pow sin sinh sqrt tan tanh fmin fmax round lround llround trunc rint
	lrint llrint copysign hypot cbrt exp2 log2 fma nan isnan isinf isfinite
	signbit fpclassify abs labs llabs NULL`)

// checkNames rejects generated names that collide.
func (c *fsmChecker) checkNames(fsms []*fsmDecl) error {
	taken := map[string]string{}
	for _, k := range cKeywords {
		taken[k] = "a C keyword"
	}
	for _, k := range cgenNames {
		taken[k] = "a name the generated code defines"
	}
	for name, s := range c.syms {
		taken[name] = fmt.Sprintf("the C name of %s declared at %s", withArticle(s.describe()), s.Pos)
	}
	for _, mp := range c.comp.Modparams {
		taken[mp.Name] = fmt.Sprintf("modparam %s declared at %s", mp.Name, mp.Pos)
	}
	for _, f := range fsms {
		if strings.HasPrefix(f.Name, "__") {
			return fmt.Errorf("%s: fsm %s: names starting with __ are reserved", f.Pos, f.Name)
		}
		for _, g := range f.generatedNames() {
			if prev, ok := taken[g.Name]; ok {
				return fmt.Errorf("%s: fsm %s: %s would be %s, but it is already %s",
					g.Pos, f.Name, g.Name, g.What, prev)
			}
			taken[g.Name] = g.What
		}
	}
	return nil
}

// withArticle prefixes s with "a" or "an".
func withArticle(s string) string {
	if s != "" && strings.ContainsRune("aeiouAEIOU", rune(s[0])) {
		return "an " + s
	}
	return "a " + s
}
