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

// rewriteTimeUnits rewrites number literals with a unit suffix in a timeout
// expression to seconds: 60s -> (60), 5ms -> (5 * 1e-3).  Lines and the
// rest of the text are unchanged.
func rewriteTimeUnits(text string) string {
	var b strings.Builder
	last := 0
	for _, t := range cTokenize(text) {
		if t.Kind != ctNumber {
			continue
		}
		m := timeUnitRe.FindStringSubmatch(t.Text)
		if m == nil {
			continue
		}
		b.WriteString(text[last:t.Off])
		fmt.Fprintf(&b, "(%s%s)", m[1], timeUnitScale[m[2]])
		last = t.Off + len(t.Text)
	}
	b.WriteString(text[last:])
	return b.String()
}

// fsmLowering generates the code of one fsm.  The names come from
// fsm_names.go, which the collision check uses too.
type fsmLowering struct {
	f        *fsmDecl
	pre, epi *fragBuf
	// action maps each transition with an action block to its helper.
	action map[*fsmTrans]string
}

// expr emits '(' user expression ')'.
func (l *fsmLowering) expr(c cfrag) {
	l.epi.gen("(")
	l.epi.user(c)
	l.epi.gen(")")
}

// helperFn emits a helper function holding a user block.  As a function of
// its own, a return in the block ends only the block, and a stray break or
// continue cannot reach the generated switch.
func (l *fsmLowering) helperFn(name, comment string, blk *cfrag) {
	l.epi.gen("/* %s */\n", comment)
	l.epi.gen("static void %s(inst_t *__comp_inst, long period) STMAK_NONBLOCKING;\n", name)
	l.epi.gen("static void %s(inst_t *__comp_inst, long period) {\n", name)
	l.epi.gen("    (void)__comp_inst; (void)period;\n")
	l.epi.user(*blk)
	l.epi.gen("}\n\n")
}

// transition emits the body of a firing transition from state from: exit,
// action, state change, timer reset, entry -- then leaves the switch.
func (l *fsmLowering) transition(indent string, from *fsmState, t *fsmTrans) {
	f := l.f
	if from.Exit != nil {
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, f.helperName("exit", from.Name))
	}
	if t.Action != nil {
		l.epi.gen("%s%s(__comp_inst, period);\n", indent, l.action[t])
	}
	l.epi.gen("%s%s = %s;\n", indent, f.stateVarName(), f.enumName(t.Target))
	l.epi.gen("%s%s_timer = 0;\n", indent, f.hidden())
	if f.TimerVar != nil {
		l.epi.gen("%s%s = 0;\n", indent, f.TimerVar.Name)
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
	if f.StateVar == nil {
		hidden = append(hidden, ast.Variable{Pos: f.Pos, CType: "int", Name: stateVar})
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
	pre.gen("static void %s_run(inst_t *__comp_inst, long period) STMAK_NONBLOCKING;\n", n)
	pre.gen("static const char *%s_state_name(int s) STMAK_NONBLOCKING __attribute__((unused));\n", n)
	pre.gen("#define %s() %s_run(__comp_inst, period)\n", n, n)
	// The parameter has a reserved name: one spelled like the fsm name
	// plus '_' would be pasted on both sides of the ##.
	pre.gen("#define %s_in(__fsm_st) ((%s) == %s_ ## __fsm_st)\n\n", n, stateVar, n)

	// Epilogue: definitions, after the user code so blocks can call the
	// user's helpers.
	epi := l.epi
	epi.gen("/* fsm %s (%s) */\n", n, f.Pos)
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
		l.helperFn(name, fmt.Sprintf("%s: %s -> %s (%s)", from, what, a.T.Target, a.T.Pos), a.T.Action)
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

	epi.gen("static void %s_run(inst_t *__comp_inst, long period) {\n", n)

	// The reset expression is read before the outputs are written, so it
	// sees their values from the previous run.
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
	if f.Reset != nil {
		epi.gen("    int __rst = ")
		l.expr(*f.Reset)
		epi.gen(";\n")
	} else {
		epi.gen("    const int __rst = 0;\n")
	}
	// 1. Outputs to their defaults, every run.
	writeDefaults("    ", f.Outputs)

	// 2. Reset / init.
	epi.gen("    if (!%s_init || __rst) {\n", hid)
	epi.gen("        %s_init = 1;\n", hid)
	epi.gen("        %s = %s;\n", stateVar, f.enumName(f.initialState()))
	epi.gen("        %s_timer = 0;\n", hid)
	if f.TimerVar != nil {
		epi.gen("        %s = 0;\n", f.TimerVar.Name)
	}
	writeDefaults("        ", f.Latched)
	epi.gen("        %s_enter = 1;\n", hid)
	epi.gen("        if (__rst) return;\n")
	epi.gen("    }\n")

	// 3. Enable.
	if f.Enable != nil {
		epi.gen("    if (!")
		l.expr(*f.Enable)
		epi.gen(") return;\n")
	}

	// 4. Pending entry: on_enter, then during below; no transitions.
	epi.gen("    if (%s_enter) {\n", hid)
	epi.gen("        %s_enter = 0;\n", hid)
	helperSwitch("        ", "enter", kinds[0].blk)
	epi.gen("    } else {\n")

	// 5. Timer.
	epi.gen("        %s_timer += period;\n", hid)
	if f.TimerVar != nil {
		epi.gen("        %s = %s_timer * 1e-9;\n", f.TimerVar.Name, hid)
	}

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
			expr := to.Expr
			expr.Text = rewriteTimeUnits(expr.Text)
			// The timeout is rounded to whole nanoseconds, so one that is a
			// multiple of the period fires exactly on it whatever the
			// rounding of the double.  Beyond int64 nanoseconds (292
			// years) it never fires.
			epi.gen("            {\n                double __to = ")
			l.expr(expr)
			epi.gen(";\n")
			epi.gen("                if (__to > 0 && __to < 9.2e9 && %s_timer >= (int64_t)(__to * 1e9 + 0.5)) {\n", hid)
			l.transition("                    ", s, t)
			epi.gen("                }\n            }\n")
		}
		epi.gen("            break;\n")
	}
	epi.gen("        default:\n            break;\n        }\n")
	epi.gen("    }\n")

	// 7. During of the current state.
	helperSwitch("    ", "during", kinds[2].blk)
	epi.gen("}\n\n")
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
		epi.gen("#undef %s\n#undef %s_in\n", f.Name, f.Name)
	}
	epi.gen("\n")
	p.pkg.Component.GenPrologue = pre.done()
	p.pkg.Component.GenEpilogue = epi.done()
}
