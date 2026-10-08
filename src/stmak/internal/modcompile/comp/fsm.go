// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// This file parses fsm blocks: declarative state machines in the .comp
// header.  See docs/dev/MODCOMPILE_FSM_DESIGN.md for the language; the
// grammar comments below follow its names.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// ---------------------------------------------------------------------------
// Frontend-local FSM structure
// ---------------------------------------------------------------------------

// cfrag is verbatim C captured from the header, with the position of its
// first byte.
type cfrag struct {
	Pos  ast.Pos
	Text string
	// Indent is the source line in front of Text with every byte but tabs
	// blanked, so generated code can reproduce the column.
	Indent string
}

// refKind says how a list entry addresses its target.
type refKind int

const (
	refScalar  refKind = iota // name
	refPinElem                // name(N): pin or param array element
	refVarElem                // name[N]: array variable element
)

// fsmRef is a list entry: a pin or variable, or one element of an array.
type fsmRef struct {
	Pos   ast.Pos
	Name  string
	Kind  refKind
	Index int
}

// String renders the entry the way it is written in C.
func (r fsmRef) String() string {
	switch r.Kind {
	case refPinElem:
		return fmt.Sprintf("%s(%d)", r.Name, r.Index)
	case refVarElem:
		return fmt.Sprintf("%s[%d]", r.Name, r.Index)
	}
	return r.Name
}

// fsmDef is an outputs/latched entry with its optional default.
type fsmDef struct {
	fsmRef
	Default *cfrag // nil: 0
}

// fsmTrans is an "on" item, or the target of a "timeout" item.
type fsmTrans struct {
	Pos       ast.Pos
	Cond      cfrag // empty for a timeout
	Target    string
	TargetPos ast.Pos
	Action    *cfrag // nil: no action block
}

// fsmTimeout is a state's "timeout (expr)" item.  Trans.Target is empty when
// the any block handles it.
type fsmTimeout struct {
	Pos   ast.Pos
	Expr  cfrag
	Trans *fsmTrans
}

type fsmState struct {
	Pos     ast.Pos
	Name    string
	Enter   *cfrag
	Exit    *cfrag
	During  *cfrag
	On      []fsmTrans
	Timeout *fsmTimeout
}

type fsmDecl struct {
	Pos      ast.Pos
	Name     string
	Inputs   []fsmRef
	Outputs  []fsmDef
	Latched  []fsmDef
	Reset    *cfrag
	Enable   *cfrag
	StateVar *fsmRef
	TimerVar *fsmRef
	Initial  string
	InitPos  ast.Pos

	HasAny     bool
	AnyOn      []fsmTrans
	AnyTimeout *fsmTrans

	States []*fsmState

	// seen records which header items were given, for duplicate checks.
	seen map[string]ast.Pos
}

// state returns the named state, or nil.
func (f *fsmDecl) state(name string) *fsmState {
	for _, s := range f.States {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// initialState returns the state the machine starts in: the named initial
// state, or the first one listed.
func (f *fsmDecl) initialState() string {
	if f.Initial != "" {
		return f.Initial
	}
	if len(f.States) > 0 {
		return f.States[0].Name
	}
	return ""
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// parseFSM parses fsm_decl := 'fsm' NAME '{' fsm_item* '}' ';'
func (p *parser) parseFSM() error {
	pos := p.cur.Pos
	// The lookahead is the 'fsm' keyword; nothing after it is scanned yet.
	p.sc.fsm = true
	p.next()

	name, err := p.expectName()
	if err != nil {
		return err
	}
	f := &fsmDecl{Pos: pos, Name: name, seen: map[string]ast.Pos{}}
	p.fsmName = name
	defer func() { p.fsmName = "" }()

	if _, err := p.expect(TokLBrace); err != nil {
		return err
	}
	for p.cur.Kind != TokRBrace {
		if p.cur.Kind == TokEOF {
			return p.errorf("missing '}'")
		}
		if err := p.parseFSMItem(f); err != nil {
			return err
		}
	}
	p.next() // skip }
	if p.cur.Kind != TokSemi {
		return p.errorf("expected ';' after '}', got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	// Back to header tokens before the token after ';' is scanned.
	p.sc.fsm = false
	p.next()

	p.fsms = append(p.fsms, f)
	return nil
}

func (p *parser) parseFSMItem(f *fsmDecl) error {
	if p.cur.Kind != TokIdent {
		return p.errorf("expected an item, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	kw, pos := p.cur.Val, p.cur.Pos

	switch kw {
	case "any":
		if f.HasAny {
			return p.errorAt(pos, "more than one any block")
		}
		f.HasAny = true
		p.next()
		return p.parseAny(f)
	case "state":
		p.next()
		return p.parseState(f, pos)
	case "inputs", "outputs", "latched", "reset", "enable", "state_var", "timer_var", "initial":
	default:
		return p.errorAt(pos, "unknown item %q", kw)
	}

	if prev, ok := f.seen[kw]; ok {
		return p.errorAt(pos, "duplicate %s (first at %s)", kw, prev)
	}
	f.seen[kw] = pos
	p.next()
	if _, err := p.expect(TokColon); err != nil {
		return err
	}

	var err error
	switch kw {
	case "inputs":
		f.Inputs, err = p.parseRefList()
	case "outputs":
		f.Outputs, err = p.parseDefList()
	case "latched":
		f.Latched, err = p.parseDefList()
	case "reset":
		f.Reset, err = p.parseParenC()
	case "enable":
		f.Enable, err = p.parseParenC()
	case "state_var", "timer_var":
		var r fsmRef
		if r, err = p.parseRef(); err != nil {
			return err
		}
		if kw == "state_var" {
			f.StateVar = &r
		} else {
			f.TimerVar = &r
		}
	case "initial":
		f.InitPos = p.cur.Pos
		f.Initial, err = p.expectName()
	}
	if err != nil {
		return err
	}
	return p.expectSemi()
}

// capture reads verbatim C up to stop (see Scanner.CaptureC); opened is the
// bracket in front of it.  Errors name the fsm.
func (p *parser) capture(stop string, opened ast.Pos) (*cfrag, error) {
	c, err := p.sc.CaptureC(stop, opened)
	if ce, ok := err.(*CaptureError); ok {
		return nil, p.errorAt(ce.Pos, "%s", ce.Msg)
	}
	return c, err
}

// parseRefList parses name_list := ref (',' ref)*
func (p *parser) parseRefList() ([]fsmRef, error) {
	var refs []fsmRef
	for {
		r, err := p.parseRef()
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
		if p.cur.Kind != TokComma {
			return refs, nil
		}
		p.next()
	}
}

// parseDefList parses def_list := def (',' def)* with def := ref ('=' cexpr)?
// A default runs to the first top-level ',' or ';'.
func (p *parser) parseDefList() ([]fsmDef, error) {
	var defs []fsmDef
	for {
		r, err := p.parseRef()
		if err != nil {
			return nil, err
		}
		d := fsmDef{fsmRef: r}
		if p.cur.Kind == TokEq {
			// The lookahead is '='; the scanner stands right after it.
			c, err := p.capture(",;", ast.Pos{})
			if err != nil {
				return nil, err
			}
			if isEmptyC(c.Text) {
				return nil, p.errorAt(c.Pos, "empty default for %s", r)
			}
			d.Default = c
			p.next()
		}
		defs = append(defs, d)
		if p.cur.Kind != TokComma {
			return defs, nil
		}
		p.next()
	}
}

// parseRef parses ref := NAME | NAME '(' INT ')' | NAME '[' INT ']'
func (p *parser) parseRef() (fsmRef, error) {
	r := fsmRef{Pos: p.cur.Pos}
	var err error
	if r.Name, err = p.expectName(); err != nil {
		return r, err
	}
	var closer TokenKind
	switch p.cur.Kind {
	case TokLParen:
		r.Kind, closer = refPinElem, TokRParen
	case TokLBrack:
		r.Kind, closer = refVarElem, TokRBrack
	default:
		return r, nil
	}
	p.next()
	if p.cur.Kind != TokNumber {
		return r, p.errorf("%s: array index must be an integer literal, got %q", r.Name, p.cur.Val)
	}
	n, ok := parseIndex(p.cur.Val)
	if !ok {
		return r, p.errorf("%s: invalid index %q", r.Name, p.cur.Val)
	}
	r.Index = n
	p.next()
	if _, err := p.expect(closer); err != nil {
		return r, err
	}
	return r, nil
}

// isEmptyC reports whether C text holds nothing but whitespace and comments.
func isEmptyC(text string) bool { return len(cTokenize(text)) == 0 }

// parseIndex parses an integer literal index; integer suffixes (1u, 2L)
// are accepted, as C accepts them.
func parseIndex(text string) (int, bool) {
	n, err := strconv.ParseInt(strings.TrimRight(text, "uUlL"), 0, 32)
	return int(n), err == nil && n >= 0
}

// parseParenC parses '(' cexpr ')' and returns the expression.
func (p *parser) parseParenC() (*cfrag, error) {
	if p.cur.Kind != TokLParen {
		return nil, p.errorf("expected '(', got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	c, err := p.capture(")", p.cur.Pos)
	if err != nil {
		return nil, err
	}
	if isEmptyC(c.Text) {
		return nil, p.errorAt(c.Pos, "empty expression")
	}
	p.next() // the ')'
	if _, err := p.expect(TokRParen); err != nil {
		return nil, err
	}
	return c, nil
}

// parseBlock parses block := '{' C statements '}' and returns the body.
func (p *parser) parseBlock() (*cfrag, error) {
	if p.cur.Kind != TokLBrace {
		return nil, p.errorf("expected '{', got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	c, err := p.capture("}", p.cur.Pos)
	if err != nil {
		return nil, err
	}
	p.next() // the '}'
	if _, err := p.expect(TokRBrace); err != nil {
		return nil, err
	}
	return c, nil
}

// parseTarget parses '->' STATE action, with action := ';' | block.
func (p *parser) parseTarget(t *fsmTrans) error {
	if _, err := p.expect(TokArrow); err != nil {
		return err
	}
	t.TargetPos = p.cur.Pos
	var err error
	if t.Target, err = p.expectName(); err != nil {
		return err
	}
	if p.cur.Kind == TokSemi {
		p.next()
		return nil
	}
	t.Action, err = p.parseBlock()
	return err
}

// parseAny parses 'any' '{' any_item* '}'.
func (p *parser) parseAny(f *fsmDecl) error {
	if _, err := p.expect(TokLBrace); err != nil {
		return err
	}
	for p.cur.Kind != TokRBrace {
		pos := p.cur.Pos
		switch {
		case p.cur.Kind == TokIdent && p.cur.Val == "on":
			p.next()
			t, err := p.parseOn(pos)
			if err != nil {
				return err
			}
			f.AnyOn = append(f.AnyOn, t)
		case p.cur.Kind == TokIdent && p.cur.Val == "timeout":
			if f.AnyTimeout != nil {
				return p.errorAt(pos, "more than one timeout in any")
			}
			p.next()
			t := &fsmTrans{Pos: pos}
			if err := p.parseTarget(t); err != nil {
				return err
			}
			f.AnyTimeout = t
		default:
			return p.errorf("expected 'on' or 'timeout' in any, got %q", p.cur.Val)
		}
	}
	p.next() // skip }
	return nil
}

// parseOn parses '(' cexpr ')' '->' STATE action after 'on'.
func (p *parser) parseOn(pos ast.Pos) (fsmTrans, error) {
	t := fsmTrans{Pos: pos}
	cond, err := p.parseParenC()
	if err != nil {
		return t, err
	}
	t.Cond = *cond
	return t, p.parseTarget(&t)
}

// parseState parses STATE '{' state_item* '}' after 'state'.
func (p *parser) parseState(f *fsmDecl, pos ast.Pos) error {
	name, err := p.expectName()
	if err != nil {
		return err
	}
	if prev := f.state(name); prev != nil {
		return p.errorAt(pos, "duplicate state %s (first at %s)", name, prev.Pos)
	}
	st := &fsmState{Pos: pos, Name: name}
	f.States = append(f.States, st)

	if _, err := p.expect(TokLBrace); err != nil {
		return err
	}
	for p.cur.Kind != TokRBrace {
		if p.cur.Kind != TokIdent {
			return p.errorf("state %s: expected an item, got %s (%q)", name, p.cur.Kind, p.cur.Val)
		}
		ipos, kw := p.cur.Pos, p.cur.Val
		var slot **cfrag
		switch kw {
		case "on_enter":
			slot = &st.Enter
		case "on_exit":
			slot = &st.Exit
		case "during":
			slot = &st.During
		case "on":
			p.next()
			t, err := p.parseOn(ipos)
			if err != nil {
				return err
			}
			st.On = append(st.On, t)
			continue
		case "timeout":
			if st.Timeout != nil {
				return p.errorAt(ipos, "state %s: duplicate timeout (first at %s)", name, st.Timeout.Pos)
			}
			p.next()
			expr, err := p.parseParenC()
			if err != nil {
				return err
			}
			to := &fsmTimeout{Pos: ipos, Expr: *expr}
			if p.cur.Kind == TokSemi {
				p.next()
			} else {
				to.Trans = &fsmTrans{Pos: ipos}
				if err := p.parseTarget(to.Trans); err != nil {
					return err
				}
			}
			st.Timeout = to
			continue
		default:
			return p.errorAt(ipos, "state %s: unknown item %q", name, kw)
		}
		if *slot != nil {
			return p.errorAt(ipos, "state %s: duplicate %s (first at %s)", name, kw, (*slot).Pos)
		}
		p.next()
		b, err := p.parseBlock()
		if err != nil {
			return err
		}
		*slot = b
	}
	p.next() // skip }
	return nil
}

// ---------------------------------------------------------------------------
// AST description
// ---------------------------------------------------------------------------

// oneLine collapses C text to a single line for documentation.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func describeDefs(defs []fsmDef) []string {
	var out []string
	for _, d := range defs {
		s := d.fsmRef.String()
		if d.Default != nil {
			s += " = " + oneLine(d.Default.Text)
		}
		out = append(out, s)
	}
	return out
}

func describeTrans(t fsmTrans, timeout bool) ast.FSMTransition {
	return ast.FSMTransition{
		Timeout: timeout,
		Cond:    oneLine(t.Cond.Text),
		Target:  t.Target,
		Action:  t.Action != nil,
	}
}

// describe returns the documentation view of f.
func (f *fsmDecl) describe() ast.FSM {
	d := ast.FSM{
		Pos:     f.Pos,
		Name:    f.Name,
		Initial: f.initialState(),
		Outputs: describeDefs(f.Outputs),
		Latched: describeDefs(f.Latched),
	}
	for _, r := range f.Inputs {
		d.Inputs = append(d.Inputs, r.String())
	}
	if f.Reset != nil {
		d.Reset = oneLine(f.Reset.Text)
	}
	if f.Enable != nil {
		d.Enable = oneLine(f.Enable.Text)
	}
	if f.StateVar != nil {
		d.StateVar = f.StateVar.String()
	}
	if f.TimerVar != nil {
		d.TimerVar = f.TimerVar.String()
	}
	for _, t := range f.AnyOn {
		d.Any = append(d.Any, describeTrans(t, false))
	}
	if f.AnyTimeout != nil {
		d.Any = append(d.Any, describeTrans(*f.AnyTimeout, true))
	}
	for i, s := range f.States {
		ds := ast.FSMState{
			Name:    s.Name,
			Number:  i,
			OnEnter: s.Enter != nil,
			OnExit:  s.Exit != nil,
			During:  s.During != nil,
		}
		for _, t := range s.On {
			ds.Transitions = append(ds.Transitions, describeTrans(t, false))
		}
		if to := s.Timeout; to != nil {
			dt := ast.FSMTransition{Timeout: true, Cond: oneLine(to.Expr.Text)}
			if to.Trans != nil {
				dt.Target = to.Trans.Target
				dt.Action = to.Trans.Action != nil
			}
			ds.Transitions = append(ds.Transitions, dt)
		}
		d.States = append(d.States, ds)
	}
	return d
}

// finishFSMs runs once the whole file is parsed: it checks the fsm blocks,
// lowers them to C and records each one's description in the AST.
func (p *parser) finishFSMs() error {
	for _, f := range p.fsms {
		if len(f.States) == 0 {
			return fmt.Errorf("%s: fsm %s has no states", f.Pos, f.Name)
		}
	}
	if err := p.checkFSMs(); err != nil {
		return err
	}
	p.lowerFSMs()
	for _, f := range p.fsms {
		p.pkg.Component.FSMs = append(p.pkg.Component.FSMs, f.describe())
	}
	return nil
}
