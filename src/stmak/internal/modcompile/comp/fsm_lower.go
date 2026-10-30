// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// Lowering of fsm blocks to C.  Each fsm becomes hidden per-instance
// variables plus C text for ast.Component.GenPrologue (enum, prototypes,
// macros) and GenEpilogue (definitions).  The execution model is the one in
// docs/dev/MODCOMPILE_FSM_DESIGN.md, "Execution model"; the step numbers in
// the comments below are its steps.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// fragBuf collects generated text and user fragments in order.
type fragBuf struct {
	frags []ast.CodeFragment
	sb    strings.Builder
}

func (b *fragBuf) gen(format string, args ...interface{}) {
	fmt.Fprintf(&b.sb, format, args...)
}

func (b *fragBuf) flush() {
	if b.sb.Len() > 0 {
		b.frags = append(b.frags, ast.CodeFragment{Text: b.sb.String()})
		b.sb.Reset()
	}
}

// user appends verbatim user C.  The backend puts it on lines of its own;
// the indent reproduces the source columns in front of it.
func (b *fragBuf) user(c cfrag) {
	b.flush()
	pos := c.Pos
	pos.Col = 1
	b.frags = append(b.frags, ast.CodeFragment{Pos: pos, Text: c.Indent + c.Text})
}

func (b *fragBuf) done() []ast.CodeFragment {
	b.flush()
	return b.frags
}

// timeUnitRe matches a decimal number literal with a time unit suffix.
var timeUnitRe = regexp.MustCompile(`^((?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)(s|ms|us|ns)$`)

var timeUnitScale = map[string]string{"s": "", "ms": " * 1e-3", "us": " * 1e-6", "ns": " * 1e-9"}

// timeoutPiece is a part of a timeout expression: user text, or the
// generated replacement of a unit literal.
type timeoutPiece struct {
	user *cfrag
	gen  string
}

// timeoutPieces splits a timeout expression at its unit literals, which
// become generated scale factors in seconds: 60s -> (60), 5ms ->
// (5 * 1e-3).  The user text between them keeps its position and indent as
// fragments of its own, so the rewrite, which changes the length of the
// text, does not shift the columns gcc reports for what follows it.
func timeoutPieces(c cfrag) []timeoutPiece {
	var out []timeoutPiece
	last := 0
	user := func(start, end int) {
		if start < end {
			s := c.slice(start, end)
			out = append(out, timeoutPiece{user: &s})
		}
	}
	for _, t := range cTokenize(c.Text) {
		if t.Kind != ctNumber {
			continue
		}
		m := timeUnitRe.FindStringSubmatch(t.Text)
		if m == nil {
			continue
		}
		user(last, t.Off)
		out = append(out, timeoutPiece{gen: fmt.Sprintf("(%s%s)", m[1], timeUnitScale[m[2]])})
		last = t.Off + len(t.Text)
	}
	user(last, len(c.Text))
	return out
}

// rewriteTimeUnits returns a timeout expression with its unit literals
// rewritten, as one string.
func rewriteTimeUnits(text string) string {
	var b strings.Builder
	for _, p := range timeoutPieces(cfrag{Text: text}) {
		if p.user != nil {
			b.WriteString(p.user.Text)
		} else {
			b.WriteString(p.gen)
		}
	}
	return b.String()
}

// fsmLowering generates the code of one fsm.  The names come from
// fsm_names.go, which the collision check uses too.
type fsmLowering struct {
	f        *fsmDecl
	pre, epi *fragBuf
	// action maps each transition with an action block to its helper.
	action map[*fsmTrans]string
	// shadows are the output pins, each written through a shadow (below).
	shadows []outShadow
}

// outShadow is an output pin, or one element of a pin array, and the hidden
// variable that stands in for it while the fsm runs.
//
// A run sets every output to its default and then the current state's
// output block sets it again.  Written straight to the pin, a reader outside
// the thread -- a userspace task, halui, the REST API -- can see the default
// in between: a held output reads as a short pulse low, and a level that
// something takes edges from (a machine-on, a program run) is a fresh edge.
// So the fsm's own code writes the shadow, and the run copies it to the pin
// once, at its end: the pin only ever changes to the value the run decided.
type outShadow struct {
	ref    fsmRef
	ctype  string // the HAL value type
	shadow string // the hidden variable
}

// expr emits '(' user expression ')'.
func (l *fsmLowering) expr(c cfrag) {
	l.epi.gen("(")
	l.epi.user(c)
	l.epi.gen(")")
}

// helperFn emits a helper function holding a user block.  As a function of
// its own, a return in the block ends only the block, and a stray break or
// continue cannot reach the generated switch.  It is marked unused: some
// are never called (the on_exit of a state without a way out, the action of
// an any item that never fires), which the checks warn about instead.
func (l *fsmLowering) helperFn(name, comment string, blk *cfrag) {
	l.epi.gen("/* %s */\n", comment)
	l.epi.gen("static void %s(inst_t *__comp_inst, long period) STMAK_NONBLOCKING __attribute__((unused));\n", name)
	l.epi.gen("static void %s(inst_t *__comp_inst, long period) {\n", name)
	l.epi.gen("    (void)__comp_inst; (void)period;\n")
	l.epi.user(*blk)
	l.epi.gen("}\n\n")
}

// transition emits the body of a firing transition from state from: exit,
// action, state change, timer reset, entry -- then leaves the switch.
func (l *fsmLowering) transition(indent string, from *fsmState, t *fsmTrans) {
	f := l.f
	if t.Internal {
		// The action, then out of the switch: the state is not left,
		// the timer runs on, and nothing after it is checked.
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, l.action[t])
		l.epi.gen("%sbreak;\n", indent)
		return
	}
	if from.Exit != nil {
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, f.helperName("exit", from.Name))
	}
	if t.Action != nil {
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, l.action[t])
	}
	l.epi.gen("%s%s = %s;\n", indent, f.stateVarName(), f.enumName(t.Target))
	l.epi.gen("%s%s_timer = 0;\n", indent, f.hidden())
	if f.TimerVar != nil {
		l.epi.gen("%s%s = 0;\n", indent, f.TimerVar.String())
	}
	if f.state(t.Target).Enter != nil {
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, f.helperName("enter", t.Target))
	}
	l.epi.gen("%sbreak;\n", indent)
}

func (l *fsmLowering) lower(comp *ast.Component) {
	f := l.f
	n := f.Name
	hid := f.hidden()
	stateVar := f.stateVarName()

	// Hidden per-instance state, as ordinary variables so every instance
	// has its own machine.
	hidden := []ast.Variable{
		{Pos: f.Pos, CType: "int", Name: hid + "_init"},
		{Pos: f.Pos, CType: "int", Name: hid + "_enter"},
		{Pos: f.Pos, CType: "int64_t", Name: hid + "_timer"},
	}
	if f.StateVar == nil || f.StateIO {
		hidden = append(hidden, ast.Variable{Pos: f.Pos, CType: "int", Name: stateVar})
	}
	// A shadow per output pin; variables are not shared, they need none.
	syms := buildSymbols(comp)
	for _, d := range f.Outputs {
		s := syms[d.Name]
		if s == nil || s.Kind != symPin {
			continue
		}
		name := shadowName(hid, d)
		l.shadows = append(l.shadows, outShadow{ref: d.fsmRef, ctype: s.Type.CType(), shadow: name})
		hidden = append(hidden, ast.Variable{Pos: f.Pos, CType: s.Type.CType(), Name: name})
	}
	comp.Variables = append(comp.Variables, hidden...)

	// Prologue: what user code may name.
	pre := l.pre
	pre.gen("/* fsm %s (%s) */\n", n, f.Pos)
	pre.gen("enum %s_state {\n", n)
	for i, s := range f.States {
		pre.gen("    %s = %d,\n", f.enumName(s.Name), i)
	}
	pre.gen("};\n")
	// Unused when the fsm is never called; the check warns about that.
	pre.gen("static void %s_run(inst_t *__comp_inst, long period) STMAK_NONBLOCKING __attribute__((unused));\n", n)
	pre.gen("static const char *%s_state_name(int s) STMAK_NONBLOCKING __attribute__((unused));\n", n)
	pre.gen("static void %s(inst_t *__comp_inst, long period) STMAK_NONBLOCKING __attribute__((unused));\n", f.resetOutputsName())
	pre.gen("#define %s() %s_run(__comp_inst, period)\n", n, n)
	pre.gen("#define %s_reset_outputs() %s(__comp_inst, period)\n", n, f.resetOutputsName())
	// The parameter has a reserved name: one spelled like the fsm name
	// plus '_' would be pasted on both sides of the ##.
	pre.gen("#define %s_in(__fsm_st) ((%s) == %s_ ## __fsm_st)\n\n", n, stateVar, n)

	// Epilogue: definitions, after the user code so blocks can call the
	// user's helpers.
	epi := l.epi
	epi.gen("/* fsm %s (%s) */\n", n, f.Pos)
	l.redirectOutputs()
	epi.gen("static const char *%s_state_name(int s) {\n", n)
	epi.gen("    switch (s) {\n")
	for _, s := range f.States {
		epi.gen("    case %s: return \"%s\";\n", f.enumName(s.Name), s.Name)
	}
	epi.gen("    }\n    return \"?\";\n}\n\n")

	// Per-state helpers.
	type helperKind struct {
		kind, item string
		blk        func(*fsmState) *cfrag
	}
	kinds := []helperKind{
		{"enter", "on_enter", func(s *fsmState) *cfrag { return s.Enter }},
		{"exit", "on_exit", func(s *fsmState) *cfrag { return s.Exit }},
		{"during", "during", func(s *fsmState) *cfrag { return s.During }},
		{"output", "output", func(s *fsmState) *cfrag { return s.Output }},
	}
	for _, k := range kinds {
		for _, s := range f.States {
			if k.blk(s) != nil {
				l.helperFn(f.helperName(k.kind, s.Name), fmt.Sprintf("%s: %s", s.Name, k.item), k.blk(s))
			}
		}
	}

	// Transition actions.
	l.action = map[*fsmTrans]string{}
	for k, a := range f.actions() {
		from := "any"
		if a.From != nil {
			from = a.From.Name
		}
		what := "on"
		if a.T.Cond.Text == "" {
			what = "timeout"
		}
		name := f.actionName(k)
		l.action[a.T] = name
		desc := fmt.Sprintf("%s: %s -> %s (%s)", from, what, a.T.Target, a.T.Pos)
		if a.T.Internal {
			desc = fmt.Sprintf("%s: %s, internal (%s)", from, what, a.T.Pos)
		}
		l.helperFn(name, desc, a.T.Action)
	}

	// helperSwitch calls the kind's helper of the current state.
	helperSwitch := func(indent, kind string, blk func(*fsmState) *cfrag) {
		var cases []*fsmState
		for _, s := range f.States {
			if blk(s) != nil {
				cases = append(cases, s)
			}
		}
		if len(cases) == 0 {
			return
		}
		epi.gen("%sswitch (%s) {\n", indent, stateVar)
		for _, s := range cases {
			epi.gen("%scase %s: %s(__comp_inst, period); break;\n", indent, f.enumName(s.Name), f.helperName(kind, s.Name))
		}
		epi.gen("%sdefault: break;\n%s}\n", indent, indent)
	}

	writeDefaults := func(indent string, defs []fsmDef) {
		for _, d := range defs {
			if d.Default == nil {
				epi.gen("%s%s = 0;\n", indent, d.fsmRef)
				continue
			}
			epi.gen("%s%s = ", indent, d.fsmRef)
			l.expr(*d.Default)
			epi.gen(";\n")
		}
	}
	// The outputs to their defaults: step 1 of a run.  Through the shadows:
	// the pins are written once, by publishOutputs.
	epi.gen("static void %s_defaults(inst_t *__comp_inst, long period) STMAK_NONBLOCKING __attribute__((unused));\n", hid)
	epi.gen("static void %s_defaults(inst_t *__comp_inst, long period) {\n", hid)
	epi.gen("    (void)__comp_inst; (void)period;\n")
	writeDefaults("    ", f.Outputs)
	epi.gen("}\n\n")
	// <fsm>_reset_outputs(): the defaults, published at once.
	epi.gen("static void %s(inst_t *__comp_inst, long period) {\n", f.resetOutputsName())
	epi.gen("    %s_defaults(__comp_inst, period);\n", hid)
	epi.gen("    %s_publish(__comp_inst);\n", hid)
	epi.gen("}\n\n")

	epi.gen("static void %s_run(inst_t *__comp_inst, long period) {\n", n)

	// The shadows start at zero while the pins hold their declared defaults:
	// loaded from the pins once, so the reset expression of the first run
	// sees the pins' initial values, as it did when it read the pins.
	if len(l.shadows) > 0 {
		epi.gen("    if (!%s_init) %s_load(__comp_inst);\n", hid, hid)
	}
	// The reset expression is read before the outputs are written, so it
	// sees their values from the previous run.
	if f.Reset != nil {
		epi.gen("    int __rst = ")
		l.expr(*f.Reset)
		epi.gen(";\n")
	} else {
		epi.gen("    const int __rst = 0;\n")
	}
	// 1. Outputs to their defaults, every run.
	epi.gen("    %s_defaults(__comp_inst, period);\n", hid)

	// publish writes an io state_var from the hidden state.
	publish := func(indent string) {
		if f.StateIO {
			epi.gen("%s%s = %s;\n", indent, f.StateVar.String(), stateVar)
		}
	}

	// 2. Reset / init.  An io state_var holding a valid state on the first
	// run is a restore: the fsm continues there, entering it anew.
	epi.gen("    if (!%s_init || __rst) {\n", hid)
	if f.StateIO {
		epi.gen("        int __restore = !%s_init && !__rst && (unsigned long long)(long long)(%s) < %du;\n",
			hid, f.StateVar.String(), len(f.States))
	}
	epi.gen("        %s_init = 1;\n", hid)
	if f.StateIO {
		epi.gen("        %s = __restore ? (int)(%s) : %s;\n", stateVar, f.StateVar.String(), f.enumName(f.initialState()))
	} else {
		epi.gen("        %s = %s;\n", stateVar, f.enumName(f.initialState()))
	}
	epi.gen("        %s_timer = 0;\n", hid)
	if f.TimerVar != nil {
		epi.gen("        %s = 0;\n", f.TimerVar.String())
	}
	writeDefaults("        ", f.Latched)
	if len(f.Persistent) > 0 {
		// Reset only: init and a restore keep them.
		epi.gen("        if (__rst) {\n")
		writeDefaults("            ", f.Persistent)
		epi.gen("        }\n")
	}
	epi.gen("        %s_enter = 1;\n", hid)
	epi.gen("    }\n")

	// 3. Enable.  A disabled fsm skips 4.-6. and during; its output
	// block still runs.  A run in reset is a disabled run in the initial
	// state.
	if f.Enable != nil {
		epi.gen("    const int __en = !__rst && ")
		l.expr(*f.Enable)
		epi.gen(";\n")
	} else {
		epi.gen("    const int __en = !__rst;\n")
	}

	// 4. Pending entry: on_enter only; during and the transitions start
	// with the next run, as after a transition.
	epi.gen("    if (__en && %s_enter) {\n", hid)
	epi.gen("        %s_enter = 0;\n", hid)
	helperSwitch("        ", "enter", kinds[0].blk)
	epi.gen("    } else if (__en) {\n")

	// 5. Timer, then during of the current state: what it does while it
	// stays, before the transitions decide, so an action overrides it.
	epi.gen("        %s_timer += period;\n", hid)
	if f.TimerVar != nil {
		epi.gen("        %s = %s_timer * 1e-9;\n", f.TimerVar.String(), hid)
	}
	helperSwitch("        ", "during", kinds[2].blk)

	// 6. Transitions, first match fires.
	epi.gen("        switch (%s) {\n", stateVar)
	for _, s := range f.States {
		epi.gen("        case %s:\n", f.enumName(s.Name))
		on := func(t *fsmTrans) {
			epi.gen("            if ")
			l.expr(t.Cond)
			epi.gen(" {\n")
			l.transition("                ", s, t)
			epi.gen("            }\n")
		}
		for i := range f.AnyOn {
			if f.AnyOn[i].Target != s.Name {
				on(&f.AnyOn[i])
			}
		}
		for i := range s.On {
			on(&s.On[i])
		}
		if to := s.Timeout; to != nil {
			t := to.Trans
			if t == nil {
				t = f.AnyTimeout
			}
			// The timeout is rounded to whole nanoseconds, so one that is a
			// multiple of the period fires exactly on it whatever the
			// rounding of the double.  Beyond int64 nanoseconds (292
			// years) it never fires.
			epi.gen("            {\n                double __to = (")
			for _, p := range timeoutPieces(to.Expr) {
				if p.user != nil {
					epi.user(*p.user)
				} else {
					epi.gen("%s", p.gen)
				}
			}
			epi.gen(");\n")
			epi.gen("                if (__to > 0 && __to < 9.2e9 && %s_timer >= (int64_t)(__to * 1e9 + 0.5)) {\n", hid)
			l.transition("                    ", s, t)
			epi.gen("                }\n            }\n")
		}
		epi.gen("            break;\n")
	}
	epi.gen("        default:\n            break;\n        }\n")
	epi.gen("    }\n")

	publish("    ")

	// 7. Output of the current state, enabled or not.
	helperSwitch("    ", "output", kinds[3].blk)

	// 8. The outputs to their pins, once.
	epi.gen("    %s_publish(__comp_inst);\n", hid)
	epi.gen("}\n\n")
	l.restoreOutputs()
}

// redirectOutputs points the macros of the output pins at their shadows for
// the fsm's own code: its helpers, its blocks and its run.  restoreOutputs
// puts them back, so user code and the other fsms see the pins.  A pin array
// with some elements in the outputs goes through an accessor that picks the
// shadow for those and the pin for the rest.
//
// <fsm>_publish, which copies the shadows to the pins, <fsm>_load, which
// does the reverse for the first run, and the accessors come first: the pin
// and variable names are macros, so they have to be spelled while the pin
// macros are still the pins.
func (l *fsmLowering) redirectOutputs() {
	epi, hid := l.epi, l.f.hidden()
	epi.gen("static void %s_publish(inst_t *__comp_inst) STMAK_NONBLOCKING __attribute__((unused));\n", hid)
	epi.gen("static void %s_publish(inst_t *__comp_inst) {\n", hid)
	epi.gen("    (void)__comp_inst;\n")
	for _, o := range l.shadows {
		epi.gen("    %s = %s;\n", o.ref.String(), o.shadow)
	}
	epi.gen("}\n\n")
	if len(l.shadows) == 0 {
		return
	}
	// <hidden>_load: the pins into the shadows, for the first run.
	epi.gen("static void %s_load(inst_t *__comp_inst) STMAK_NONBLOCKING __attribute__((unused));\n", hid)
	epi.gen("static void %s_load(inst_t *__comp_inst) {\n", hid)
	for _, o := range l.shadows {
		epi.gen("    %s = %s;\n", o.shadow, o.ref.String())
	}
	epi.gen("}\n\n")
	var arrays []string
	elems := map[string][]outShadow{}
	for _, o := range l.shadows {
		if o.ref.Kind != refPinElem {
			continue
		}
		if _, ok := elems[o.ref.Name]; !ok {
			arrays = append(arrays, o.ref.Name)
		}
		elems[o.ref.Name] = append(elems[o.ref.Name], o)
	}
	for _, a := range arrays {
		ctype := elems[a][0].ctype
		epi.gen("static %s *%s_oref_%s(inst_t *__comp_inst, int __i) STMAK_NONBLOCKING __attribute__((unused));\n", ctype, hid, a)
		epi.gen("static %s *%s_oref_%s(inst_t *__comp_inst, int __i) {\n", ctype, hid, a)
		epi.gen("    switch (__i) {\n")
		for _, o := range elems[a] {
			epi.gen("    case %d: return &%s;\n", o.ref.Index, o.shadow)
		}
		epi.gen("    default: return &%s(__i);\n    }\n}\n", a)
	}
	done := map[string]bool{}
	for _, o := range l.shadows {
		if done[o.ref.Name] {
			continue
		}
		done[o.ref.Name] = true
		epi.gen("#pragma push_macro(\"%s\")\n#undef %s\n", o.ref.Name, o.ref.Name)
		if o.ref.Kind == refPinElem {
			epi.gen("#define %s(__i) (*%s_oref_%s(__comp_inst, (__i)))\n", o.ref.Name, hid, o.ref.Name)
		} else {
			epi.gen("#define %s %s\n", o.ref.Name, o.shadow)
		}
	}
	epi.gen("\n")
}

func (l *fsmLowering) restoreOutputs() {
	done := map[string]bool{}
	for _, o := range l.shadows {
		if done[o.ref.Name] {
			continue
		}
		done[o.ref.Name] = true
		l.epi.gen("#pragma pop_macro(\"%s\")\n", o.ref.Name)
	}
	if len(l.shadows) > 0 {
		l.epi.gen("\n")
	}
}

// lowerFSMs generates the code for every fsm block.
func (p *parser) lowerFSMs() {
	if len(p.fsms) == 0 {
		return
	}
	pre, epi := &fragBuf{}, &fragBuf{}
	pre.gen("/* ---------------------------------------------------------------------------\n")
	pre.gen(" * State machines (fsm blocks)\n")
	pre.gen(" * ------------------------------------------------------------------------- */\n\n")
	epi.gen("/* ---------------------------------------------------------------------------\n")
	epi.gen(" * State machines (fsm blocks)\n")
	epi.gen(" * ------------------------------------------------------------------------- */\n\n")
	for _, f := range p.fsms {
		l := &fsmLowering{f: f, pre: pre, epi: epi}
		l.lower(&p.pkg.Component)
	}
	// The call macros stay defined until every fsm is emitted: one fsm's
	// enable may test another's state (mode_in(RUNNING)).
	for _, f := range p.fsms {
		epi.gen("#undef %s\n#undef %s_in\n#undef %s_reset_outputs\n", f.Name, f.Name, f.Name)
	}
	epi.gen("\n")
	p.pkg.Component.GenPrologue = pre.done()
	p.pkg.Component.GenEpilogue = epi.done()
}
