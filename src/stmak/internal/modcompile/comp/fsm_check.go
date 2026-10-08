// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// Semantic checks for fsm blocks.  Errors stop the build; warnings go to
// ast.Package.Warnings.  See "Checks" in docs/dev/MODCOMPILE_FSM_DESIGN.md.

import (
	"fmt"
	"regexp"
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
			return fmt.Errorf("%s: fsm %s: state_var: %s is %s; need an out s32/u32 pin or an integer variable",
				r.Pos, f.Name, r.Name, withArticle(s.describe()))
		}
		if s.Kind == symPin && s.Dir != ast.PinOut {
			// An io pin could be set from outside: a goto around the
			// declared graph.
			return fmt.Errorf("%s: fsm %s: state_var: %s is an io pin; the state changes only "+
				"through transitions, so it must be an out pin", r.Pos, f.Name, r.Name)
		}
	case roleTimer:
		ok := s.Kind == symPin && s.Type == ast.HALFloat ||
			s.Kind == symVar && (s.CType == "double" || s.CType == "float")
		if !ok {
			return fmt.Errorf("%s: fsm %s: timer_var: %s is %s; need a float pin or a double/float variable",
				r.Pos, f.Name, r.Name, withArticle(s.describe()))
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

// useString renders a use the way it is written, for messages: x, x(1),
// x[1], or x(...) for a computed index.
func useString(u cUse) string {
	switch {
	case u.Kind == refScalar:
		return u.Name
	case u.Index >= 0:
		return fsmRef{Name: u.Name, Kind: u.Kind, Index: u.Index}.String()
	case u.Kind == refPinElem:
		return u.Name + "(...)"
	}
	return u.Name + "[...]"
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
	seenInput := map[string]bool{}
	for _, r := range f.Inputs {
		if seenInput[r.String()] {
			return fmt.Errorf("%s: fsm %s: inputs: %s is listed twice", r.Pos, f.Name, r)
		}
		seenInput[r.String()] = true
		if hit, _ := outputs.match(cUse{Name: r.Name, Kind: r.Kind, Index: r.Index}); hit {
			return fmt.Errorf("%s: fsm %s: inputs: %s is an output of this fsm, which conditions "+
				"always read as its default", r.Pos, f.Name, r)
		}
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
	// The expressions evaluated after the outputs are written: conditions,
	// enable and timeouts.  Only conditions take part in the sensitivity
	// check.
	type readExpr struct {
		frag cfrag
		what string
	}
	var exprs []readExpr
	for _, cond := range f.conditions() {
		exprs = append(exprs, readExpr{cond, "condition"})
	}
	if f.Enable != nil {
		exprs = append(exprs, readExpr{*f.Enable, "enable"})
	}
	for _, s := range f.States {
		if s.Timeout != nil {
			exprs = append(exprs, readExpr{s.Timeout.Expr, "timeout"})
		}
	}
	for _, e := range exprs {
		cond, what := e.frag, e.what
		for _, u := range cUses(cond.Text) {
			s := c.syms[u.Name]
			if s == nil || s.Kind == symParam {
				continue
			}
			pos := offsetPos(cond.Pos, cond.Text, u.Off)
			hit, computed := outputs.match(u)
			if hit {
				return fmt.Errorf("%s: fsm %s: %s reads output %s, which always holds its "+
					"default here", pos, f.Name, what, useString(u))
			}
			if computed {
				// Every element an output: the read is one for certain.
				var elems []string
				for _, r := range outputs[u.Name] {
					elems = append(elems, r.String())
				}
				if len(elems) == s.Array {
					return fmt.Errorf("%s: fsm %s: %s reads %s, and every element of %s is an "+
						"output, which always holds its default here", pos, f.Name, what, useString(u), u.Name)
				}
				c.warn(pos, "fsm %s: %s reads %s with a computed index; of its elements, "+
					"the outputs %s always hold their default here", f.Name, what, useString(u), strings.Join(elems, ", "))
				continue
			}
			if f.Inputs == nil || what != "condition" {
				continue
			}
			hit, computed = sensitive.match(u)
			switch {
			case hit:
			case computed:
				c.warn(pos, "fsm %s: condition reads %s with a computed index; it cannot be "+
					"matched against inputs", f.Name, useString(u))
			default:
				c.warn(pos, "fsm %s: condition reads %s, which is not in inputs", f.Name, useString(u))
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

	// Timeout literals with a unit: a leading 0 would make them octal.
	for _, s := range f.States {
		if s.Timeout == nil {
			continue
		}
		e := s.Timeout.Expr
		for _, t := range cTokenize(e.Text) {
			if t.Kind != ctNumber {
				continue
			}
			pos := offsetPos(e.Pos, e.Text, t.Off)
			m := timeUnitRe.FindStringSubmatch(t.Text)
			if m == nil && strings.HasSuffix(t.Text, "s") {
				// No C number ends in 's': a unit on a hex or suffixed
				// number.
				return fmt.Errorf("%s: fsm %s: timeout literal %s: a unit needs a decimal number",
					pos, f.Name, t.Text)
			}
			if m != nil && octalRe.MatchString(m[1]) {
				digits := strings.TrimLeft(m[1], "0")
				if digits == "" {
					digits = "0"
				}
				return fmt.Errorf("%s: fsm %s: timeout literal %s has a leading 0, which C reads as "+
					"octal; write %s%s", pos, f.Name, t.Text, digits, m[2])
			}
		}
	}

	c.checkGraph(f)
	return nil
}

// octalRe matches an integer literal C reads as octal.
var octalRe = regexp.MustCompile(`^0[0-9]+$`)

// checkGraph warns about unreachable states and states without a way out.
func (c *fsmChecker) checkGraph(f *fsmDecl) {
	var anyTargets []string
	for _, t := range f.AnyOn {
		anyTargets = append(anyTargets, t.Target)
	}
	// Reachable: from the initial state along the edges each state has.
	// The any on items are edges of every state; the any timeout is one of
	// the states whose timeout has no target.
	reached := map[string]bool{}
	todo := []string{f.initialState()}
	for len(todo) > 0 {
		name := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if reached[name] {
			continue
		}
		reached[name] = true
		s := f.state(name)
		todo = append(todo, anyTargets...)
		for _, t := range s.On {
			todo = append(todo, t.Target)
		}
		if to := s.Timeout; to != nil {
			if to.Trans != nil {
				todo = append(todo, to.Trans.Target)
			} else {
				todo = append(todo, f.AnyTimeout.Target)
			}
		}
	}
	usedAnyTimeout := false
	for _, s := range f.States {
		usedAnyTimeout = usedAnyTimeout || s.Timeout != nil && s.Timeout.Trans == nil
	}
	if f.AnyTimeout != nil && !usedAnyTimeout {
		c.warn(f.AnyTimeout.Pos, "fsm %s: any timeout is never used: no state has a timeout "+
			"without a target", f.Name)
	}
	for _, t := range f.AnyOn {
		// Skipped in its target state: it never fires when that is the
		// only state the machine reaches.
		if len(reached) == 1 && reached[t.Target] {
			c.warn(t.Pos, "fsm %s: any on -> %s never fires: %s is the only reachable state",
				f.Name, t.Target, t.Target)
		}
	}
	for _, s := range f.States {
		if !reached[s.Name] {
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

// ownedRef is a list entry an fsm owns: no one else writes it.
type ownedRef struct {
	fsmRef
	f    *fsmDecl
	role string // "output", "state_var", "timer_var"
}

// ownership maps what the fsms own by name: their outputs, state_var and
// timer_var.  Latched entries are left out; code outside may acknowledge
// them.
type ownership map[string][]ownedRef

func newOwnership(fsms []*fsmDecl) ownership {
	o := ownership{}
	add := func(f *fsmDecl, r fsmRef, role string) { o[r.Name] = append(o[r.Name], ownedRef{r, f, role}) }
	for _, f := range fsms {
		for _, d := range f.Outputs {
			add(f, d.fsmRef, "output")
		}
		if f.StateVar != nil {
			add(f, *f.StateVar, "state_var")
		}
		if f.TimerVar != nil {
			add(f, *f.TimerVar, "timer_var")
		}
	}
	return o
}

// match returns the owned entry write u by writer hits, and whether the hit
// is only possible (a computed index).  writer's own outputs are its to
// write and are passed over; an exact hit wins over a possible one.
func (o ownership) match(u cUse, writer *fsmDecl) (*ownedRef, bool) {
	var maybe *ownedRef
	for i := range o[u.Name] {
		r := &o[u.Name][i]
		if r.f == writer && r.role == "output" {
			continue
		}
		if r.Kind == refScalar || r.Kind == u.Kind && r.Index == u.Index {
			return r, false
		}
		if r.Kind == u.Kind && u.Index < 0 && maybe == nil {
			maybe = r
		}
	}
	return maybe, maybe != nil
}

// consequence says what happens to a write to an owned entry.
func (r *ownedRef) consequence() string {
	if r.role == "state_var" {
		return "the state changes only through transitions"
	}
	return "the fsm overwrites it on its next run"
}

// checkWrites warns about writes in src to entries owned by an fsm other
// than writer (nil for the verbatim C), and to writer's own state_var and
// timer_var.  writer's outputs are its own to write.
func (c *fsmChecker) checkWrites(o ownership, writer *fsmDecl, src string, base ast.Pos) {
	prefix := ""
	if writer != nil {
		prefix = "fsm " + writer.Name + ": "
	}
	for _, u := range cUses(src) {
		if !u.Write || c.syms[u.Name] == nil {
			continue
		}
		r, maybe := o.match(u, writer)
		if r == nil {
			continue
		}
		owner := "fsm " + r.f.Name
		if r.f == writer {
			owner = "this fsm"
		}
		pos := offsetPos(base, src, u.Off)
		if maybe {
			c.warn(pos, "%swrite to %s may hit %s, the %s of %s; %s", prefix, useString(u), r.fsmRef,
				r.role, owner, r.consequence())
			continue
		}
		c.warn(pos, "%s%s is the %s of %s; %s", prefix, useString(u), r.role, owner, r.consequence())
	}
}

// checkUserCode scans the verbatim C after ';;' and the fsm blocks for
// writes to what the fsms own, and the verbatim C for fsms that are never
// run.
func (c *fsmChecker) checkUserCode(fsms []*fsmDecl) {
	o := newOwnership(fsms)
	for _, f := range fsms {
		for _, frag := range f.fragments() {
			c.checkWrites(o, f, frag.Text, frag.Pos)
		}
	}
	src := c.comp.VerbatimC
	c.checkWrites(o, nil, src, c.comp.VerbatimCPos)

	called := map[string]bool{}
	for _, u := range cUses(src) {
		if u.Call && u.Index < 0 {
			called[u.Name] = true
		}
	}
	for _, f := range fsms {
		if !called[f.Name] {
			c.warn(f.Pos, "fsm %s: %s() is never called", f.Name, f.Name)
		}
	}

	// The timer and timeouts are floating point.  Which function calls an
	// fsm is not known here, but when every one is nofp, that one is too.
	allNoFP := len(c.comp.Functions) > 0
	for _, fn := range c.comp.Functions {
		allNoFP = allNoFP && !fn.FP
	}
	if !allNoFP {
		return
	}
	for _, f := range fsms {
		if f.usesFP() {
			c.warn(c.comp.Functions[0].Pos, "function %s is nofp, but fsm %s uses floating point "+
				"(timeout, timer_var)", c.comp.Functions[0].Name, f.Name)
		}
	}
}

// usesFP reports whether f's generated code uses floating point: timeouts
// and timer_var.  Everything else is integer code apart from the user's own.
func (f *fsmDecl) usesFP() bool {
	if f.TimerVar != nil {
		return true
	}
	for _, s := range f.States {
		if s.Timeout != nil {
			return true
		}
	}
	return false
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
		names[f.Name] = f
		if err := c.check(f); err != nil {
			return err
		}
	}
	if err := c.checkNames(p.fsms); err != nil {
		return err
	}
	c.checkUserCode(p.fsms)
	return nil
}
