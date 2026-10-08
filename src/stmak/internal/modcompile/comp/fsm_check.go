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

// integerCTypes are the variable types accepted for state_var: the C
// integer types of any width, the <stdint.h> names and the HAL/RTAPI
// integer typedefs.  The type of a variable is a single NAME, so this list
// is the whole question.  bool is left out: it holds two states at most.
var integerCTypes = map[string]bool{
	"char": true, "short": true, "int": true, "long": true, "signed": true, "unsigned": true,
	"int8_t": true, "uint8_t": true, "int16_t": true, "uint16_t": true,
	"int32_t": true, "uint32_t": true, "int64_t": true, "uint64_t": true,
	"intptr_t": true, "uintptr_t": true, "size_t": true, "ssize_t": true, "ptrdiff_t": true,
	"rtapi_s8": true, "rtapi_u8": true, "rtapi_s16": true, "rtapi_u16": true,
	"rtapi_s32": true, "rtapi_u32": true, "rtapi_s64": true, "rtapi_u64": true,
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
	// errf prefixes every message with the entry's position, fsm and list.
	errf := func(format string, args ...interface{}) error {
		return fmt.Errorf("%s: fsm %s: %s: %s", r.Pos, f.Name, list, fmt.Sprintf(format, args...))
	}
	s := c.syms[r.Name]
	if s == nil {
		return errf("%s is not a declared pin or variable", r.Name)
	}
	if s.Kind == symParam {
		// Params are configuration: never written by the fsm, and exempt
		// from the sensitivity check, so there is no list they belong in.
		return errf("%s is a param; only pins and variables are listed", r.Name)
	}

	// Arrays: only single elements, accessed the way code accesses them.
	switch {
	case s.Array == 0 && r.Kind != refScalar:
		return errf("%s is not an array", r.Name)
	case s.Array > 0 && r.Kind == refScalar:
		return errf("%s is an array; list single elements (%s)",
			r.Name, map[bool]string{true: r.Name + "(0)", false: r.Name + "[0]"}[s.Kind == symPin])
	case s.Array > 0 && s.Kind == symPin && r.Kind != refPinElem:
		return errf("pin array elements are written %s(%d)", r.Name, r.Index)
	case s.Array > 0 && s.Kind == symVar && r.Kind != refVarElem:
		return errf("variable array elements are written %s[%d]", r.Name, r.Index)
	case s.Array > 0 && (r.Index < 0 || r.Index >= s.Array):
		return errf("index %d out of range for %s[%d]", r.Index, r.Name, s.Array)
	}

	if role == roleInput {
		if s.Kind == symPin && s.Dir == ast.PinOut {
			return errf("%s is an out pin", r.Name)
		}
		return nil
	}

	// Written lists.
	if s.Kind == symPin && s.Dir == ast.PinIn {
		return errf("%s is an in pin", r.Name)
	}
	if s.Cond {
		return errf("pin %s may not exist (personality condition); the fsm writes it every cycle", r.Name)
	}
	if s.Ptr {
		return errf("%s is a pointer variable", r.Name)
	}
	switch role {
	case roleState:
		ok := s.Kind == symPin && (s.Type == ast.HALS32 || s.Type == ast.HALU32) ||
			s.Kind == symVar && integerCTypes[s.CType]
		if !ok {
			return errf("%s is %s; need an out s32/u32 pin or an integer variable", r.Name, withArticle(s.describe()))
		}
		if s.Kind == symPin && s.Dir != ast.PinOut {
			// An io pin could be set from outside: a goto around the
			// declared graph.
			return errf("%s is an io pin; the state changes only through transitions, so it must be an out pin", r.Name)
		}
	case roleTimer:
		ok := s.Kind == symPin && s.Type == ast.HALFloat ||
			s.Kind == symVar && (s.CType == "double" || s.CType == "float")
		if !ok {
			return errf("%s is %s; need a float pin or a double/float variable", r.Name, withArticle(s.describe()))
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
// scalar or a literal index.  For a computed index, or an array read as a
// whole (passed to a function), computed is set instead, since the elements
// read cannot be matched.
func (rs refSet) match(u cUse) (hit, computed bool) {
	refs, ok := rs[u.Name]
	if !ok {
		return false, false
	}
	for _, r := range refs {
		if r.Kind == refScalar {
			return true, false
		}
		if u.Kind == refScalar || r.Kind == u.Kind && u.Index < 0 {
			return false, true
		}
		if r.Kind == u.Kind && r.Index == u.Index {
			return true, false
		}
	}
	return false, false
}

// howRead says how a use that match reports as computed reads its array.
func howRead(u cUse) string {
	if u.Kind == refScalar {
		return "as a whole"
	}
	return "with a computed index"
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
				c.warn(pos, "fsm %s: %s reads %s %s; of its elements, the outputs %s always "+
					"hold their default here", f.Name, what, useString(u), howRead(u), strings.Join(elems, ", "))
				continue
			}
			if f.Inputs == nil || what != "condition" {
				continue
			}
			hit, computed = sensitive.match(u)
			switch {
			case hit:
			case computed:
				c.warn(pos, "fsm %s: condition reads %s %s; it cannot be matched against inputs",
					f.Name, useString(u), howRead(u))
			default:
				c.warn(pos, "fsm %s: condition reads %s, which is not in inputs", f.Name, useString(u))
			}
		}
	}

	// Unread inputs.  An element counts as read when it is read with its
	// literal index, or when its array is read with a computed one or as a
	// whole.
	read, readComputed := map[string]bool{}, map[string]bool{}
	for _, frag := range f.fragments() {
		for _, u := range cUses(frag.Text) {
			s := c.syms[u.Name]
			if u.Kind != refScalar && u.Index < 0 || u.Kind == refScalar && s != nil && s.Array > 0 {
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

	// Timeout literals: a leading 0 makes an integer octal, with or without
	// a unit; the expression is in seconds, where nobody means that.
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
			number, suffix := t.Text, ""
			if m := timeUnitRe.FindStringSubmatch(t.Text); m != nil {
				number, suffix = m[1], m[2]
			} else if strings.HasSuffix(t.Text, "s") {
				// No C number ends in 's': a unit on a hex or suffixed
				// number.
				return fmt.Errorf("%s: fsm %s: timeout literal %s: a unit needs a decimal number",
					pos, f.Name, t.Text)
			} else {
				// A plain integer may carry C's integer suffixes.
				number = strings.TrimRight(number, "uUlL")
				suffix = t.Text[len(number):]
			}
			if octalRe.MatchString(number) {
				digits := strings.TrimLeft(number, "0")
				if digits == "" {
					digits = "0"
				}
				return fmt.Errorf("%s: fsm %s: timeout literal %s has a leading 0, which C reads as "+
					"octal; write %s%s", pos, f.Name, t.Text, digits, suffix)
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

// checkWrites warns about writes among uses, the identifier uses of src, to
// entries owned by an fsm other than writer (nil for the verbatim C), and
// to writer's own state_var and timer_var.  writer's outputs are its own to
// write.
func (c *fsmChecker) checkWrites(o ownership, writer *fsmDecl, uses []cUse, src string, base ast.Pos) {
	prefix := ""
	if writer != nil {
		prefix = "fsm " + writer.Name + ": "
	}
	for _, u := range uses {
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
// writes to what the fsms own, for fsms that are never run, and for fsms
// with floating point run from a nofp function.
func (c *fsmChecker) checkUserCode(fsms []*fsmDecl) {
	o := newOwnership(fsms)
	byName := map[string]*fsmDecl{}
	for _, f := range fsms {
		byName[f.Name] = f
	}
	// callers maps each fsm to the functions whose bodies run it, directly
	// or through another fsm's blocks; "" stands for a caller that is not
	// known: a call from a helper of the user's.
	callers := map[*fsmDecl]map[string]bool{}
	addCaller := func(f *fsmDecl, fn string) {
		if callers[f] == nil {
			callers[f] = map[string]bool{}
		}
		callers[f][fn] = true
	}
	isCall := func(u cUse) *fsmDecl {
		if u.Call && u.Index < 0 {
			return byName[u.Name]
		}
		return nil
	}

	// Calls between fsms: a child run from a parent's block runs where the
	// parent runs.  They are resolved once the direct calls are known.
	type edge struct{ from, to *fsmDecl }
	var edges []edge
	for _, f := range fsms {
		for _, frag := range f.fragments() {
			uses := cUses(frag.Text)
			c.checkWrites(o, f, uses, frag.Text, frag.Pos)
			for _, u := range uses {
				if g := isCall(u); g != nil && g != f {
					edges = append(edges, edge{f, g})
				}
			}
		}
	}

	src := c.comp.VerbatimC
	toks := cTokenize(src)
	uses := usesOf(toks)
	c.checkWrites(o, nil, uses, src, c.comp.VerbatimCPos)
	bodies := functionBodies(toks)
	for _, u := range uses {
		f := isCall(u)
		if f == nil {
			continue
		}
		fn := ""
		for _, b := range bodies {
			if b.start <= u.Off && u.Off < b.end {
				fn = b.name
			}
		}
		if len(bodies) == 0 && len(c.comp.Functions) == 1 {
			// No FUNCTION(): cgen wraps the whole verbatim C in the one
			// function.
			fn = c.comp.Functions[0].Name
		}
		addCaller(f, fn)
	}
	for changed := true; changed; {
		changed = false
		for _, e := range edges {
			for fn := range callers[e.from] {
				if !callers[e.to][fn] {
					addCaller(e.to, fn)
					changed = true
				}
			}
		}
	}
	for _, f := range fsms {
		called := callers[f] != nil
		for _, e := range edges {
			// Called by an fsm that is never called: that one is warned
			// about.
			called = called || e.to == f
		}
		if !called {
			c.warn(f.Pos, "fsm %s: %s() is never called", f.Name, f.Name)
		}
	}

	// The timer and timeouts are floating point.
	fns := map[string]*ast.Function{}
	allNoFP := len(c.comp.Functions) > 0
	for i := range c.comp.Functions {
		fn := &c.comp.Functions[i]
		fns[fn.Name] = fn
		allNoFP = allNoFP && !fn.FP
	}
	for _, f := range fsms {
		if !f.usesFP() {
			continue
		}
		for name := range callers[f] {
			if fn := fns[name]; fn != nil && !fn.FP {
				c.warn(fn.Pos, "function %s is nofp, but runs fsm %s, which uses floating point "+
					"(timeout, timer_var)", fn.Name, f.Name)
			}
		}
		if callers[f][""] && allNoFP {
			c.warn(f.Pos, "fsm %s uses floating point (timeout, timer_var), but every function is nofp", f.Name)
		}
	}
}

// functionBody is the range of the verbatim C a FUNCTION(name) { ... }
// body spans, as byte offsets.
type functionBody struct {
	name       string
	start, end int
}

// functionBodies finds the FUNCTION(name) { ... } bodies in the verbatim C.
func functionBodies(toks []ctok) []functionBody {
	var out []functionBody
	for i := 0; i+4 < len(toks); i++ {
		if toks[i].Kind != ctIdent || toks[i].Text != "FUNCTION" ||
			toks[i+1].Text != "(" || toks[i+2].Kind != ctIdent || toks[i+3].Text != ")" || toks[i+4].Text != "{" {
			continue
		}
		close := matching(toks, i+4)
		out = append(out, functionBody{toks[i+2].Text, toks[i+4].Off, toks[close].Off + 1})
	}
	return out
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
