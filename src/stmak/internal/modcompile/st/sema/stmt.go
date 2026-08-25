// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// checkStmts lowers a statement list.
func (c *checker) checkStmts(list []st.Stmt) []ast.Stmt {
	// A body that lowers to nothing is still a body: the distinction between
	// nil and empty is what Package.Validate uses to tell an ST component
	// from a .comp, so never return nil for a body that was present.
	out := make([]ast.Stmt, 0, len(list))
	for _, s := range list {
		if lowered := c.checkStmt(s); lowered != nil {
			out = append(out, lowered)
		}
	}
	return out
}

func (c *checker) checkStmt(s st.Stmt) ast.Stmt {
	switch v := s.(type) {
	case *st.AssignStmt:
		return c.checkAssign(v)
	case *st.IfStmt:
		return c.checkIf(v)
	case *st.CaseStmt:
		return c.checkCase(v)
	case *st.ForStmt:
		return c.checkFor(v)
	case *st.WhileStmt:
		return c.checkWhile(v)
	case *st.RepeatStmt:
		return c.checkRepeat(v)
	case *st.ExitStmt:
		if c.loopDepth == 0 {
			c.errorf(v.Pos, "EXIT is only allowed inside a loop")
			return nil
		}
		return &ast.ExitStmt{Pos: v.Pos}
	case *st.ContinueStmt:
		c.errorf(v.Pos, "CONTINUE is not supported yet; restructure with IF, or use EXIT")
		return nil
	case *st.ReturnStmt:
		return &ast.ReturnStmt{Pos: v.Pos}
	case *st.CallStmt:
		return c.checkInvoke(v)
	case *st.EmptyStmt:
		return nil
	}
	c.errorf(s.Position(), "unsupported statement")
	return nil
}

func (c *checker) checkAssign(v *st.AssignStmt) ast.Stmt {
	target := c.checkExpr(v.Target, ast.Type{})
	if target == nil {
		return nil
	}
	if !ast.IsLValue(target) {
		c.errorf(v.Target.Position(), "cannot assign to this expression")
		return nil
	}

	// An input pin has exactly one writer, the HAL signal netted to it.  A
	// second writer in the body is the conflict that kept function block I/O
	// off HAL in the first place.
	if ref, ok := target.(*ast.VarRef); ok {
		if sym := c.scope.lookup(ref.Name); sym != nil && sym.Const {
			switch {
			case sym.Store == ast.StoragePin:
				c.errorf(v.Target.Position(),
					"cannot assign to %s: it is an input pin, driven by whatever HAL signal is "+
						"netted to it. Declare it in VAR_OUTPUT to write it, or in VAR for internal state",
					ref.Name)
			default:
				c.errorf(v.Target.Position(), "cannot assign to %s: it is a constant", ref.Name)
			}
			return nil
		}
	}

	want := target.ExprType()
	value := c.checkExpr(v.Value, want)
	if value == nil {
		return nil
	}
	name := "this target"
	if ref, ok := target.(*ast.VarRef); ok {
		name = ref.Name
	}
	value = c.assign(v.Value.Position(), name, want, value)
	if value == nil {
		return nil
	}
	return &ast.AssignStmt{Pos: v.Pos, Target: target, Value: value}
}

// checkCond checks an expression used as a condition.
func (c *checker) checkCond(e st.Expr, what string) ast.Expr {
	cond := c.checkExpr(e, ast.Type{Kind: ast.TypeBool})
	if cond == nil {
		return nil
	}
	if cond.ExprType().Kind != ast.TypeBool {
		c.errorf(e.Position(), "the %s condition must be BOOL, got %s. "+
			"ST has no truthiness: compare explicitly, as in `x <> 0`", what, cond.ExprType())
		return nil
	}
	return cond
}

func (c *checker) checkIf(v *st.IfStmt) ast.Stmt {
	out := &ast.IfStmt{Pos: v.Pos}
	out.Cond = c.checkCond(v.Cond, "IF")

	c.push()
	out.Then = c.checkStmts(v.Then)
	c.pop()

	for _, e := range v.ElseIfs {
		arm := ast.ElseIf{Pos: e.Pos, Cond: c.checkCond(e.Cond, "ELSIF")}
		c.push()
		arm.Body = c.checkStmts(e.Body)
		c.pop()
		out.ElseIf = append(out.ElseIf, arm)
	}

	if v.HasElse {
		c.push()
		out.Else = c.checkStmts(v.Else)
		c.pop()
	}
	if out.Cond == nil {
		return nil
	}
	return out
}

func (c *checker) checkCase(v *st.CaseStmt) ast.Stmt {
	sel := c.checkExpr(v.Selector, ast.Type{})
	if sel == nil {
		return nil
	}
	selType := sel.ExprType()
	if !selType.IsInteger() && selType.Kind != ast.TypeEnum {
		c.errorf(v.Selector.Position(),
			"CASE needs an integer or enumeration selector, got %s", selType)
		return nil
	}

	out := &ast.CaseStmt{Pos: v.Pos, Selector: sel}

	// Labels must not overlap: two arms claiming one value is ambiguous, and
	// C would silently take the first.
	type span struct {
		lo, hi int64
		pos    ast.Pos
	}
	var seen []span

	for _, cl := range v.Clauses {
		clause := ast.CaseClause{Pos: cl.Pos}
		for _, l := range cl.Labels {
			lo, ok := c.constLabel(l.Value, selType)
			if !ok {
				continue
			}
			hi := lo
			if l.High != nil {
				hi, ok = c.constLabel(l.High, selType)
				if !ok {
					continue
				}
				if hi < lo {
					c.errorf(l.Pos, "CASE range %d..%d is empty; the bounds are the wrong way round", lo, hi)
					continue
				}
			}
			for _, p := range seen {
				if lo <= p.hi && p.lo <= hi {
					c.errorf(l.Pos, "CASE label %s overlaps the one at %s", labelText(lo, hi), p.pos)
					break
				}
			}
			seen = append(seen, span{lo, hi, l.Pos})
			clause.Labels = append(clause.Labels, ast.CaseLabel{Pos: l.Pos, Lo: lo, Hi: hi})
		}

		c.push()
		clause.Body = c.checkStmts(cl.Body)
		c.pop()
		out.Clauses = append(out.Clauses, clause)
	}

	if v.HasElse {
		c.push()
		out.Else = c.checkStmts(v.Else)
		c.pop()
	}
	return out
}

func labelText(lo, hi int64) string {
	if lo == hi {
		return strconv.FormatInt(lo, 10)
	}
	return strconv.FormatInt(lo, 10) + ".." + strconv.FormatInt(hi, 10)
}

// constLabel folds one CASE label, which must be a compile-time constant.
func (c *checker) constLabel(e st.Expr, sel ast.Type) (int64, bool) {
	checked := c.checkExpr(e, sel)
	if checked == nil {
		return 0, false
	}
	n, ok := foldInt(checked)
	if !ok {
		c.errorf(e.Position(), "a CASE label must be a compile-time constant")
		return 0, false
	}
	return n, true
}

// ---------------------------------------------------------------------------
// Loops, and the bound every one of them has to carry
// ---------------------------------------------------------------------------

func (c *checker) checkFor(v *st.ForStmt) ast.Stmt {
	loopVar := c.checkExpr(v.Var, ast.Type{})
	if loopVar == nil {
		return nil
	}
	if !ast.IsLValue(loopVar) {
		c.errorf(v.Var.Position(), "the FOR control variable must be assignable")
		return nil
	}
	vt := loopVar.ExprType()
	if !vt.IsInteger() {
		c.errorf(v.Var.Position(), "the FOR control variable must be an integer type, got %s", vt)
		return nil
	}

	from := c.checkExpr(v.From, vt)
	to := c.checkExpr(v.To, vt)
	if from == nil || to == nil {
		return nil
	}
	from = c.assign(v.From.Position(), "the FOR start value", vt, from)
	to = c.assign(v.To.Position(), "the FOR limit", vt, to)

	var by ast.Expr
	if v.By != nil {
		by = c.checkExpr(v.By, vt)
		if by == nil {
			return nil
		}
		by = c.assign(v.By.Position(), "the FOR step", vt, by)
		if n, ok := foldInt(by); ok && n == 0 {
			c.errorf(v.By.Position(), "a FOR step of zero never terminates")
			return nil
		}
	}
	if from == nil || to == nil {
		return nil
	}

	// A FOR is bounded by construction only when its limit and step are
	// constants.  With a limit read from a pin the iteration count is bounded
	// by nothing better than the integer range, and two billion iterations in
	// a servo thread is indistinguishable from a hang -- so such a loop needs
	// the same explicit cap as a WHILE.  See §4.
	_, toConst := foldInt(to)
	byConst := true
	if by != nil {
		_, byConst = foldInt(by)
	}
	if !toConst || !byConst {
		if _, ok := c.loopBound(v.Pos, nil, "FOR"); !ok {
			return nil
		}
	}

	c.push()
	c.loopDepth++
	body := c.checkStmts(v.Body)
	c.loopDepth--
	c.pop()

	return &ast.ForStmt{Pos: v.Pos, Var: loopVar, From: from, To: to, By: by, Body: body}
}

func (c *checker) checkWhile(v *st.WhileStmt) ast.Stmt {
	max, ok := c.loopBound(v.Pos, v.Attrs, "WHILE")
	cond := c.checkCond(v.Cond, "WHILE")

	c.push()
	c.loopDepth++
	body := c.checkStmts(v.Body)
	c.loopDepth--
	c.pop()

	if !ok || cond == nil {
		return nil
	}
	return &ast.WhileStmt{Pos: v.Pos, Cond: cond, Body: body, MaxIter: max}
}

func (c *checker) checkRepeat(v *st.RepeatStmt) ast.Stmt {
	max, ok := c.loopBound(v.Pos, v.Attrs, "REPEAT")

	c.push()
	c.loopDepth++
	body := c.checkStmts(v.Body)
	c.loopDepth--
	c.pop()

	cond := c.checkCond(v.Cond, "UNTIL")
	if !ok || cond == nil {
		return nil
	}
	return &ast.RepeatStmt{Pos: v.Pos, Body: body, Cond: cond, MaxIter: max}
}

// maxIterationsCap is the largest bound a loop may declare.  It is not a
// correctness limit but a plausibility one: a loop asking for ten million
// iterations of a servo thread has misunderstood where it is running, and
// saying so at compile time beats finding out from the watchdog.
const maxIterationsCap = 1000000

// loopBound reads the mandatory iteration cap from a loop's attributes.
//
// This is the rule from docs/dev/MODCOMPILE_ST_FRONTEND.md §4: a HAL function
// body may not contain a loop whose termination the compiler cannot bound.  A
// flat ban on WHILE was considered and rejected -- iterative settling is a real
// pattern and a bounded WHILE expresses it more honestly than a FOR with a
// break -- so the loop carries its bound instead.
func (c *checker) loopBound(pos ast.Pos, attrs []st.Attribute, kw string) (int, bool) {
	var found *st.Attribute
	for i := range attrs {
		a := attrs[i]
		if strings.EqualFold(a.Name, "max_iterations") {
			found = &attrs[i]
			continue
		}
		c.warnf("%s: unknown attribute %q on a %s loop, ignored", a.Pos, a.Name, kw)
	}

	if found == nil {
		c.errorf(pos, "this %s needs an iteration bound the compiler can see, because it runs "+
			"in the real-time thread. Write {attribute 'max_iterations' := '<n>'} before it",
			kw)
		return 0, false
	}

	n, err := strconv.Atoi(strings.TrimSpace(found.Value))
	if err != nil {
		c.errorf(found.Pos, "max_iterations must be a whole number, got %q", found.Value)
		return 0, false
	}
	if n <= 0 {
		c.errorf(found.Pos, "max_iterations must be positive, got %d", n)
		return 0, false
	}
	if n > maxIterationsCap {
		c.errorf(found.Pos, "max_iterations of %d is too large for a real-time thread "+
			"(the limit is %d); this loop belongs outside the servo path", n, maxIterationsCap)
		return 0, false
	}
	return n, true
}
