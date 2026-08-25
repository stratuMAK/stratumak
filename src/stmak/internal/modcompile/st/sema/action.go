// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// An ACTION is a named piece of a POU's body.  It shares every variable of the
// POU that declares it and has none of its own, which is what distinguishes it
// from a FUNCTION_BLOCK: there is no state to lay out and no interface to pass
// through.  See docs/dev/MODCOMPILE_ST_FRONTEND.md §3.4.
//
// Both the declarations and the calls are resolved in the POU's own scope, so
// the checker only has to open that scope once and hand it to both.

// declareActions registers the action names of a POU before any body is
// checked, so one may call another regardless of the order they were written.
func (c *checker) declareActions(decls []*st.ActionDecl, kind, owner string) {
	for _, a := range decls {
		if a.Name == "" {
			continue
		}
		if prev := c.scope.lookupLocal(a.Name); prev != nil {
			c.errorf(a.Pos, "%s is already declared at %s", a.Name, prev.Pos)
			continue
		}
		c.scope.declare(&symbol{Name: a.Name, Kind: symAction, Pos: a.Pos})
	}
}

// checkActions resolves the bodies, in the scope the POU's own body was
// checked in.
func (c *checker) checkActions(decls []*st.ActionDecl, kind, owner string) []ast.Action {
	var out []ast.Action
	for _, a := range decls {
		if a.Name == "" {
			continue
		}
		act := ast.Action{
			Pos:  a.Pos,
			Name: a.Name,
			Doc:  c.pouAttributes(a.Attrs, "ACTION", a.Name),
			Body: c.checkStmts(a.Body),
		}
		act.TouchesPins = bodyTouchesPins(act.Body)
		out = append(out, act)
	}
	return out
}

// bodyTouchesPins reports whether a body reaches one of the enclosing POU's
// pins.  A PROGRAM snapshots its pins into locals for the cycle, so an action
// that reads one has to be handed them; one that only touches instance state
// needs nothing but the instance.
func bodyTouchesPins(body []ast.Stmt) bool {
	found := false
	walkStmtExprs(body, func(e ast.Expr) {
		if ref, ok := e.(*ast.VarRef); ok && ref.Store == ast.StoragePin {
			found = true
		}
	})
	return found
}

// checkActionCall resolves `Act()` or `Act(v := x)`.
//
// The argument form assigns a variable of the POU before running the action,
// which is CODESYS shorthand for two statements.  It is lowered to exactly
// that here, so the backend sees assignments it already knows how to emit
// rather than a second kind of argument binding.
func (c *checker) checkActionCall(v *st.CallStmt, name string) ast.Stmt {
	out := &ast.ActionCall{Pos: v.Pos, Action: name}

	for _, a := range v.Args {
		switch a.Form {
		case st.ArgOutput:
			c.errorf(a.Pos, "an action takes no output binding; "+
				"read the variable after the call instead")
			continue
		case st.ArgPositional:
			c.errorf(a.Pos, "an action has no parameters; "+
				"write `%s(<variable> := <value>)` to set one of the POU's variables first", name)
			continue
		}

		// The name is a variable of the POU, not a parameter of the action.
		target := c.checkExpr(&st.Ident{Pos: a.Pos, Name: a.Name}, ast.Type{})
		if target == nil {
			continue
		}
		if !ast.IsLValue(target) {
			c.errorf(a.Pos, "%s cannot be assigned before the action runs", a.Name)
			continue
		}
		if sym := c.scope.lookup(a.Name); sym != nil && sym.Const {
			// The same rule an ordinary assignment gets: an input pin has one
			// writer, and it is the signal netted to it.
			switch sym.Store {
			case ast.StoragePin:
				c.errorf(a.Pos, "cannot assign to %s: it is an input pin, driven by whatever "+
					"HAL signal is netted to it", sym.Name)
			case ast.StorageParam:
				c.errorf(a.Pos, "cannot assign to %s: it is an rw parameter, which HAL sets "+
					"and the component reads", sym.Name)
			default:
				c.errorf(a.Pos, "cannot assign to %s: it is a constant", sym.Name)
			}
			continue
		}
		want := target.ExprType()
		value := c.checkExpr(a.Value, want)
		if value == nil {
			continue
		}
		value = c.assign(a.Pos, a.Name, want, value)
		if value == nil {
			continue
		}
		out.Presets = append(out.Presets, &ast.AssignStmt{Pos: a.Pos, Target: target, Value: value})
	}
	return out
}

// actionCallees lists the actions a body runs, for the recursion check.
func actionCallees(body []ast.Stmt) []namedRef {
	var out []namedRef
	walkBody(body, func(s ast.Stmt) {
		if call, ok := s.(*ast.ActionCall); ok {
			out = append(out, namedRef{call.Action, call.Pos})
		}
	})
	return out
}
