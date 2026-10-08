// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// Semantic checks for fsm blocks.  Errors stop the build; warnings go to
// ast.Package.Warnings.  See "Checks" in docs/dev/MODCOMPILE_FSM_DESIGN.md.

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// ---------------------------------------------------------------------------
// Symbols
// ---------------------------------------------------------------------------

type symKind int

const (
	symPin symKind = iota
	symParam
	symVar
)

// sym is a pin, param or variable under the C name user code uses.
type sym struct {
	Kind  symKind
	Pos   ast.Pos
	Dir   ast.PinDir  // pins
	Type  ast.HALType // pins and params
	CType string      // variables
	Ptr   bool        // pointer variable
	Array int         // element count, 0 for a scalar
	// Cond is set for a pin that may not exist: one with an 'if'
	// personality condition, or a personality-sized array.
	Cond bool
}

func (s *sym) describe() string {
	switch s.Kind {
	case symPin:
		return fmt.Sprintf("%s %s pin", s.Dir, s.Type)
	case symParam:
		return "param"
	}
	return fmt.Sprintf("%s variable", s.CType)
}

func buildSymbols(c *ast.Component) map[string]*sym {
	syms := map[string]*sym{}
	for _, p := range c.Pins {
		syms[ast.CName(p.Name)] = &sym{Kind: symPin, Pos: p.Pos, Dir: p.Dir, Type: p.Type,
			Array: p.ArraySize, Cond: p.Personality != "" || p.ArrayPersonality != ""}
	}
	for _, p := range c.Params {
		syms[ast.CName(p.Name)] = &sym{Kind: symParam, Pos: p.Pos, Type: p.Type, Array: p.ArraySize}
	}
	for _, v := range c.Variables {
		name := strings.TrimLeft(v.Name, "*")
		syms[name] = &sym{Kind: symVar, Pos: v.Pos, CType: v.CType, Ptr: name != v.Name, Array: v.Array}
	}
	return syms
}

// integerCTypes are the variable types accepted for state_var.  The type of
// a variable is a single NAME, so this list is the whole question.
var integerCTypes = map[string]bool{
	"int": true, "unsigned": true, "long": true, "short": true,
	"int32_t": true, "uint32_t": true, "int64_t": true, "uint64_t": true,
	"int16_t": true, "uint16_t": true, "rtapi_s32": true, "rtapi_u32": true,
	"hal_s32_t": true, "hal_u32_t": true, "stmak_hal_s32_t": true, "stmak_hal_u32_t": true,
}

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

type fsmChecker struct {
	comp *ast.Component
	syms map[string]*sym
	warn func(pos ast.Pos, format string, args ...interface{})
	// owner maps each written entry (outputs, latched, state_var,
	// timer_var, as written) to the fsm that writes it.
	owner map[string]string
}

// listRole names what a list entry is for; it decides what it may refer to.
type listRole int

const (
	roleInput listRole = iota
	roleWrite          // outputs, latched
	roleState
	roleTimer
)

// checkRef resolves a list entry and checks it against its role.
func (c *fsmChecker) checkRef(f *fsmDecl, list string, r fsmRef, role listRole) error {
	s := c.syms[r.Name]
	if s == nil {
		return fmt.Errorf("%s: fsm %s: %s: %s is not a declared pin or variable", r.Pos, f.Name, list, r.Name)
	}
	if s.Kind == symParam {
		// Params are configuration: never written by the fsm, and exempt
		// from the sensitivity check, so there is no list they belong in.
		return fmt.Errorf("%s: fsm %s: %s: %s is a param; only pins and variables are listed",
			r.Pos, f.Name, list, r.Name)
	}

	// Arrays: only single elements, accessed the way code accesses them.
	switch {
	case s.Array == 0 && r.Kind != refScalar:
		return fmt.Errorf("%s: fsm %s: %s: %s is not an array", r.Pos, f.Name, list, r.Name)
	case s.Array > 0 && r.Kind == refScalar:
		return fmt.Errorf("%s: fsm %s: %s: %s is an array; list single elements (%s)",
			r.Pos, f.Name, list, r.Name, map[bool]string{true: r.Name + "(0)", false: r.Name + "[0]"}[s.Kind == symPin])
	case s.Array > 0 && s.Kind == symPin && r.Kind != refPinElem:
		return fmt.Errorf("%s: fsm %s: %s: pin array elements are written %s(%d)", r.Pos, f.Name, list, r.Name, r.Index)
	case s.Array > 0 && s.Kind == symVar && r.Kind != refVarElem:
		return fmt.Errorf("%s: fsm %s: %s: variable array elements are written %s[%d]", r.Pos, f.Name, list, r.Name, r.Index)
	case s.Array > 0 && (r.Index < 0 || r.Index >= s.Array):
		return fmt.Errorf("%s: fsm %s: %s: index %d out of range for %s[%d]", r.Pos, f.Name, list, r.Index, r.Name, s.Array)
	}

	if role == roleInput {
		if s.Kind == symPin && s.Dir == ast.PinOut {
			return fmt.Errorf("%s: fsm %s: inputs: %s is an out pin", r.Pos, f.Name, r.Name)
		}
		return nil
	}

	// Written lists.
	if s.Kind == symPin && s.Dir == ast.PinIn {
		return fmt.Errorf("%s: fsm %s: %s: %s is an in pin", r.Pos, f.Name, list, r.Name)
	}
	if s.Cond {
		return fmt.Errorf("%s: fsm %s: %s: pin %s may not exist (personality condition); "+
			"the fsm writes it every cycle", r.Pos, f.Name, list, r.Name)
	}
	if s.Ptr {
		return fmt.Errorf("%s: fsm %s: %s: %s is a pointer variable", r.Pos, f.Name, list, r.Name)
	}
	switch role {
	case roleState:
		ok := s.Kind == symPin && (s.Type == ast.HALS32 || s.Type == ast.HALU32) ||
			s.Kind == symVar && integerCTypes[s.CType]
		if !ok {
			return fmt.Errorf("%s: fsm %s: state_var: %s is a %s; need an s32/u32 pin or an integer variable",
				r.Pos, f.Name, r.Name, s.describe())
		}
	case roleTimer:
		ok := s.Kind == symPin && s.Type == ast.HALFloat ||
			s.Kind == symVar && (s.CType == "double" || s.CType == "float")
		if !ok {
			return fmt.Errorf("%s: fsm %s: timer_var: %s is a %s; need a float pin or a double/float variable",
				r.Pos, f.Name, r.Name, s.describe())
		}
	}

	key := r.String()
	if prev, ok := c.owner[key]; ok {
		if prev == f.Name {
			return fmt.Errorf("%s: fsm %s: %s is written by more than one of outputs, latched, state_var, timer_var",
				r.Pos, f.Name, key)
		}
		return fmt.Errorf("%s: fsm %s: %s is already written by fsm %s", r.Pos, f.Name, key, prev)
	}
	c.owner[key] = f.Name
	return nil
}

// fragments returns every piece of user C in f, for the unread-input scan.
func (f *fsmDecl) fragments() []cfrag {
	var out []cfrag
	add := func(c *cfrag) {
		if c != nil {
			out = append(out, *c)
		}
	}
	addTrans := func(t *fsmTrans) {
		if t.Cond.Text != "" {
			out = append(out, t.Cond)
		}
		add(t.Action)
	}
	for _, d := range append(append([]fsmDef{}, f.Outputs...), f.Latched...) {
		add(d.Default)
	}
	add(f.Reset)
	add(f.Enable)
	for i := range f.AnyOn {
		addTrans(&f.AnyOn[i])
	}
	if f.AnyTimeout != nil {
		addTrans(f.AnyTimeout)
	}
	for _, s := range f.States {
		add(s.Enter)
		add(s.Exit)
		add(s.During)
		for i := range s.On {
			addTrans(&s.On[i])
		}
		if s.Timeout != nil {
			add(&s.Timeout.Expr)
			if s.Timeout.Trans != nil {
				addTrans(s.Timeout.Trans)
			}
		}
	}
	return out
}

// conditions returns every 'on' condition of f.
func (f *fsmDecl) conditions() []cfrag {
	var out []cfrag
	for _, t := range f.AnyOn {
		out = append(out, t.Cond)
	}
	for _, s := range f.States {
		for _, t := range s.On {
			out = append(out, t.Cond)
		}
	}
	return out
}

// refSet holds list entries by name, for matching uses against them.
type refSet map[string][]fsmRef

func (rs refSet) add(r fsmRef) { rs[r.Name] = append(rs[r.Name], r) }

// match reports whether use u reads an entry of the set: exact for a
// scalar or a literal index; for a computed index, computed is set
// instead, since it cannot be matched.
func (rs refSet) match(u cUse) (hit, computed bool) {
	refs, ok := rs[u.Name]
	if !ok {
		return false, false
	}
	for _, r := range refs {
		if r.Kind == refScalar {
			return true, false
		}
		if r.Kind == u.Kind && u.Index < 0 {
			return false, true
		}
		if r.Kind == u.Kind && r.Index == u.Index {
			return true, false
		}
	}
	return false, false
}

func (c *fsmChecker) check(f *fsmDecl) error {
	// Lists.
	for _, r := range f.Inputs {
		if err := c.checkRef(f, "inputs", r, roleInput); err != nil {
			return err
		}
	}
	for _, d := range f.Outputs {
		if err := c.checkRef(f, "outputs", d.fsmRef, roleWrite); err != nil {
			return err
		}
	}
	for _, d := range f.Latched {
		if err := c.checkRef(f, "latched", d.fsmRef, roleWrite); err != nil {
			return err
		}
	}
	if f.StateVar != nil {
		if err := c.checkRef(f, "state_var", *f.StateVar, roleState); err != nil {
			return err
		}
	}
	if f.TimerVar != nil {
		if err := c.checkRef(f, "timer_var", *f.TimerVar, roleTimer); err != nil {
			return err
		}
	}

	// States and targets.
	if f.Initial != "" && f.state(f.Initial) == nil {
		return fmt.Errorf("%s: fsm %s: initial state %s is not declared", f.InitPos, f.Name, f.Initial)
	}
	checkTarget := func(t *fsmTrans) error {
		if t != nil && t.Target != "" && f.state(t.Target) == nil {
			return fmt.Errorf("%s: fsm %s: unknown target state %s", t.TargetPos, f.Name, t.Target)
		}
		return nil
	}
	for i := range f.AnyOn {
		if err := checkTarget(&f.AnyOn[i]); err != nil {
			return err
		}
	}
	if err := checkTarget(f.AnyTimeout); err != nil {
		return err
	}
	for _, s := range f.States {
		for i := range s.On {
			if err := checkTarget(&s.On[i]); err != nil {
				return err
			}
		}
		if to := s.Timeout; to != nil {
			if to.Trans != nil {
				if err := checkTarget(to.Trans); err != nil {
					return err
				}
			} else if f.AnyTimeout == nil {
				return fmt.Errorf("%s: fsm %s: state %s: timeout without a target needs "+
					"'any { timeout -> STATE; }'", to.Pos, f.Name, s.Name)
			}
		}
	}

	// Conditions: an output read is an error, an unlisted read a warning.
	outputs, sensitive := refSet{}, refSet{}
	for _, d := range f.Outputs {
		outputs.add(d.fsmRef)
	}
	for _, r := range f.Inputs {
		sensitive.add(r)
	}
	for _, d := range f.Latched {
		sensitive.add(d.fsmRef)
	}
	for _, r := range []*fsmRef{f.StateVar, f.TimerVar} {
		if r != nil {
			sensitive.add(*r)
		}
	}
	for _, cond := range f.conditions() {
		for _, u := range cUses(cond.Text) {
			s := c.syms[u.Name]
			if s == nil || s.Kind == symParam {
				continue
			}
			pos := offsetPos(cond.Pos, cond.Text, u.Off)
			if hit, _ := outputs.match(u); hit {
				return fmt.Errorf("%s: fsm %s: condition reads output %s, which always holds its "+
					"default here", pos, f.Name, u.Name)
			}
			if f.Inputs == nil {
				continue
			}
			hit, computed := sensitive.match(u)
			switch {
			case hit:
			case computed:
				c.warn(pos, "fsm %s: condition reads %s with a computed index; it cannot be "+
					"matched against inputs", f.Name, u.Name)
			default:
				c.warn(pos, "fsm %s: condition reads %s, which is not in inputs", f.Name, u.Name)
			}
		}
	}

	// Unread inputs.  An element counts as read when it is read with its
	// literal index or when its array is read with a computed one.
	read, readComputed := map[string]bool{}, map[string]bool{}
	for _, frag := range f.fragments() {
		for _, u := range cUses(frag.Text) {
			if u.Kind != refScalar && u.Index < 0 {
				readComputed[u.Name] = true
				continue
			}
			read[fsmRef{Name: u.Name, Kind: u.Kind, Index: u.Index}.String()] = true
		}
	}
	for _, r := range f.Inputs {
		if !read[r.String()] && !(r.Kind != refScalar && readComputed[r.Name]) {
			c.warn(r.Pos, "fsm %s: input %s is never read", f.Name, r)
		}
	}

	c.checkGraph(f)
	return nil
}

// checkGraph warns about unreachable states and states without a way out.
func (c *fsmChecker) checkGraph(f *fsmDecl) {
	incoming := map[string]bool{f.initialState(): true}
	var anyTargets []string
	for _, t := range f.AnyOn {
		incoming[t.Target] = true
		anyTargets = append(anyTargets, t.Target)
	}
	if f.AnyTimeout != nil {
		incoming[f.AnyTimeout.Target] = true
	}
	for _, s := range f.States {
		for _, t := range s.On {
			if t.Target != s.Name {
				incoming[t.Target] = true
			}
		}
		if s.Timeout != nil && s.Timeout.Trans != nil && s.Timeout.Trans.Target != s.Name {
			incoming[s.Timeout.Trans.Target] = true
		}
	}
	for _, s := range f.States {
		if !incoming[s.Name] {
			c.warn(s.Pos, "fsm %s: state %s is unreachable", f.Name, s.Name)
		}
		exits := append([]string{}, anyTargets...)
		for _, t := range s.On {
			exits = append(exits, t.Target)
		}
		if to := s.Timeout; to != nil {
			if to.Trans != nil {
				exits = append(exits, to.Trans.Target)
			} else {
				exits = append(exits, f.AnyTimeout.Target)
			}
		}
		out := false
		for _, e := range exits {
			out = out || e != s.Name
		}
		if !out {
			c.warn(s.Pos, "fsm %s: state %s has no way out", f.Name, s.Name)
		}
	}
}

// checkUserCode scans the verbatim C after ';;' for writes to what the fsms
// own, and for fsms that are never run.
func (c *fsmChecker) checkUserCode(fsms []*fsmDecl) {
	owned := refSet{}
	for _, f := range fsms {
		for _, d := range f.Outputs {
			owned.add(d.fsmRef)
		}
		for _, r := range []*fsmRef{f.StateVar, f.TimerVar} {
			if r != nil {
				owned.add(*r)
			}
		}
	}
	called := map[string]bool{}
	src := c.comp.VerbatimC
	for _, u := range cUses(src) {
		if u.Call && u.Index < 0 {
			called[u.Name] = true
		}
		if !u.Write || c.syms[u.Name] == nil {
			continue
		}
		pos := offsetPos(c.comp.VerbatimCPos, src, u.Off)
		hit, computed := owned.match(u)
		switch {
		case hit:
			c.warn(pos, "%s is written by an fsm and overwritten on its next run", u.Name)
		case computed:
			c.warn(pos, "%s may overwrite an element written by an fsm", u.Name)
		}
	}
	for _, f := range fsms {
		if !called[f.Name] {
			c.warn(f.Pos, "fsm %s: %s() is never called", f.Name, f.Name)
		}
	}
}

// checkFSMs runs every check on the parsed fsm blocks.
func (p *parser) checkFSMs() error {
	c := &fsmChecker{
		comp:  &p.pkg.Component,
		syms:  buildSymbols(&p.pkg.Component),
		owner: map[string]string{},
		warn: func(pos ast.Pos, format string, args ...interface{}) {
			p.pkg.Warnings = append(p.pkg.Warnings, fmt.Sprintf("%s: %s", pos, fmt.Sprintf(format, args...)))
		},
	}
	names := map[string]*fsmDecl{}
	for _, fn := range p.pkg.Component.Functions {
		names[fn.Name] = nil
	}
	for _, f := range p.fsms {
		if prev, ok := names[f.Name]; ok {
			if prev != nil {
				return fmt.Errorf("%s: fsm %s already declared at %s", f.Pos, f.Name, prev.Pos)
			}
			return fmt.Errorf("%s: fsm %s has the name of a function", f.Pos, f.Name)
		}
		if s := c.syms[f.Name]; s != nil {
			return fmt.Errorf("%s: fsm %s has the name of a %s declared at %s", f.Pos, f.Name, s.describe(), s.Pos)
		}
		names[f.Name] = f
		if err := c.check(f); err != nil {
			return err
		}
	}
	c.checkUserCode(p.fsms)
	return nil
}
