// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// A METHOD is an operation on a function block instance: it has its own
// interface and its own locals, and it can reach the instance's variables.
//
// That is what distinguishes it from an ACTION, which shares the POU's
// variables and has no interface at all.  The two are different constructs
// that share the `inst.name()` call syntax, which is what CODESYS does and
// what makes both read the way they should.  A name is one or the other --
// they share a namespace within a block, so which one it is never depends on
// how it was called.  See docs/dev/MODCOMPILE_ST_STDLIB.md §7a.

// declareMethodSignatures resolves each method's result type and parameters,
// before any body is checked, so a body may call a method written after it.
func (c *checker) declareMethodSignatures(b *st.FunctionBlockDecl, def *ast.FunctionBlock) {
	seen := make(map[string]ast.Pos, len(b.Methods))
	for _, a := range b.Actions {
		if a.Name != "" {
			seen[key(a.Name)] = a.Pos
		}
	}

	for _, m := range b.Methods {
		if m.Name == "" {
			continue
		}
		if prev, dup := seen[key(m.Name)]; dup {
			c.errorf(m.Pos, "%s.%s is already declared at %s; a block's methods and actions "+
				"share one namespace, because both are called the same way",
				b.Name, m.Name, prev)
			continue
		}
		seen[key(m.Name)] = m.Pos
		if !c.checkCName(m.Name, m.Pos, "a method") {
			continue
		}

		out := ast.Method{Pos: m.Pos, Name: m.Name}
		out.Doc = c.pouAttributes(m.Attrs, "METHOD", m.Name)

		if m.Result != nil {
			result, ok := c.resolveType(m.Result)
			if !ok {
				continue
			}
			if !result.IsScalar() && result.Kind != ast.TypeString {
				c.errorf(m.Pos, "a method may not return %s; only a scalar or a STRING", result)
			}
			out.Result = result
		}

		params := make(map[string]ast.Pos)
		for _, blk := range m.Vars {
			switch blk.Kind {
			case st.VarOutput, st.VarInOut, st.VarGlobal, st.VarExternal:
				// A method's outputs would have to be bound at the call site,
				// which is the function block invocation syntax, and having
				// two ways to do that is worse than having one.  The instance
				// is right there for anything that has to persist.
				c.errorf(blk.Pos, "a method may not declare %s; it returns one result by name, "+
					"and anything else it produces belongs to the instance", blk.Kind)
				continue
			}
			for _, d := range blk.Decls {
				typ, ok := c.resolveType(d.Type)
				if !ok {
					continue
				}
				if typ.Kind == ast.TypeFB {
					c.errorf(d.Pos, "a method may not hold a function block instance; "+
						"declare it in the block's own VAR, which persists")
					continue
				}
				for _, name := range d.Names {
					if prev, dup := params[key(name)]; dup {
						c.errorf(d.Pos, "%s is already declared at %s", name, prev)
						continue
					}
					params[key(name)] = d.Pos
					if !c.checkCName(name, d.Pos, "a variable") {
						continue
					}
					v := ast.VarDecl{
						Pos: d.Pos, Name: name, Typ: typ,
						Store: ast.StorageLocal, Const: blk.Constant,
					}
					if d.Init != nil {
						v.Init = c.checkExpr(d.Init, typ)
					}
					if blk.Kind == st.VarInput {
						out.Params = append(out.Params, v)
					} else {
						out.Vars = append(out.Vars, v)
					}
				}
			}
		}
		def.Methods = append(def.Methods, out)
	}
}

// checkMethodBodies resolves each method's statements, in a scope holding the
// block's members and the method's own names.
func (c *checker) checkMethodBodies(b *st.FunctionBlockDecl, def *ast.FunctionBlock) {
	for i := range b.Methods {
		m := b.Methods[i]
		var out *ast.Method
		for j := range def.Methods {
			if ast.EqualName(def.Methods[j].Name, m.Name) {
				out = &def.Methods[j]
				break
			}
		}
		if out == nil {
			continue
		}

		c.push()
		c.declarePeriod(m.Pos)
		prev := c.blockOwner
		c.blockOwner = def

		// The block's own members, reached through the instance.
		for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts, def.Vars} {
			for _, v := range set {
				if v.Const {
					c.scope.declare(constSymbol(v))
					continue
				}
				c.scope.declare(&symbol{
					Name: v.Name, Kind: symVar, Type: v.Typ,
					Store: ast.StorageField, Pos: v.Pos,
				})
			}
		}

		// Then the method's own, which shadow them -- a parameter named for a
		// member is the method's parameter inside the method, as it is in
		// every language that has both.
		c.push()
		if out.Result.Kind != ast.TypeInvalid {
			// The result is assigned by name, as a FUNCTION's is.
			c.scope.declare(&symbol{
				Name: out.Name, Kind: symVar, Type: out.Result,
				Store: ast.StorageLocal, Pos: m.Pos,
			})
		}
		for _, set := range [][]ast.VarDecl{out.Params, out.Vars} {
			for _, v := range set {
				if v.Const {
					c.scope.declare(constSymbol(v))
					continue
				}
				c.scope.declare(&symbol{
					Name: v.Name, Kind: symVar, Type: v.Typ,
					Store: ast.StorageLocal, Pos: v.Pos,
				})
			}
		}

		out.Body = c.checkStmts(m.Body)
		c.blockOwner = prev
		c.pop()
		c.pop()
	}
}

// checkMethodCall resolves `instance.method(args)`.
//
// Returns nil when the name is not a method of the block, so the caller can
// go on to try a member or an action -- which is what keeps one spelling
// working for all three without any of them having to know about the others.
func (c *checker) checkMethodCall(inst ast.Expr, pos ast.Pos, name string, args []st.Arg) *ast.MethodCallExpr {
	block := inst.ExprType().Ref
	def := c.pkg.LookupBlock(block)
	if def == nil {
		return nil
	}

	var m *ast.Method
	for i := range def.Methods {
		if ast.EqualName(def.Methods[i].Name, name) {
			m = &def.Methods[i]
			break
		}
	}
	if m == nil {
		return nil
	}

	return c.buildMethodCall(inst, def, m, pos, args)
}

// buildMethodCall checks the arguments and builds the call.  inst is nil for a
// call on the instance already in scope.
func (c *checker) buildMethodCall(inst ast.Expr, def *ast.FunctionBlock, m *ast.Method, pos ast.Pos, args []st.Arg) *ast.MethodCallExpr {
	out := &ast.MethodCallExpr{
		Pos: pos, Inst: inst, Owner: def.Name, Method: m.Name, Typ: m.Result,
	}
	if len(args) != len(m.Params) {
		c.errorf(pos, "%s.%s takes %d %s, got %d",
			def.Name, m.Name, len(m.Params),
			plural(len(m.Params), "argument", "arguments"), len(args))
		return out
	}
	for i, a := range args {
		if a.Form == st.ArgOutput {
			c.errorf(a.Pos, "%s.%s has no output parameters; it returns one result",
				def.Name, m.Name)
			continue
		}
		if a.Form == st.ArgInput && !ast.EqualName(a.Name, m.Params[i].Name) {
			// Matched by position, as a function's arguments are: reordering
			// them would need a temporary each to evaluate safely.
			c.errorf(a.Pos, "%s.%s: argument %d is %s, not %s; "+
				"a method's arguments are matched by position",
				def.Name, m.Name, i+1, m.Params[i].Name, a.Name)
			return out
		}
		e := c.checkExpr(a.Value, m.Params[i].Typ)
		if e == nil {
			continue
		}
		e = c.assign(a.Pos, def.Name+"."+m.Name+"."+m.Params[i].Name, m.Params[i].Typ, e)
		if e == nil {
			continue
		}
		out.Args = append(out.Args, e)
	}
	return out
}

// ownMethod finds a method of the block currently being checked.
//
// Anywhere blockOwner is set -- the block's body, one of its actions, one of
// its methods -- there is an instance in scope, so a bare method name is a
// call on it.  IEC spells that THIS^.name(); the bare form is what CODESYS
// accepts and what reads best, and both reach here.
func (c *checker) ownMethod(name string) *ast.Method {
	if c.blockOwner == nil {
		return nil
	}
	for i := range c.blockOwner.Methods {
		if ast.EqualName(c.blockOwner.Methods[i].Name, name) {
			return &c.blockOwner.Methods[i]
		}
	}
	return nil
}

// checkOwnMethodCall resolves a call on the instance in scope, with no name
// for it: `helper(x := 1)` inside the same block, or THIS^.helper(x := 1).
func (c *checker) checkOwnMethodCall(pos ast.Pos, name string, args []st.Arg) *ast.MethodCallExpr {
	m := c.ownMethod(name)
	if m == nil {
		return nil
	}
	// Inst nil means the instance in scope: the backend has __self and needs
	// no expression to reach it.
	return c.buildMethodCall(nil, c.blockOwner, m, pos, args)
}

// checkThisCall resolves `THIS^.name(args)`, IEC's spelling for a call on the
// instance in scope.  It reports whether the expression was a THIS call at
// all, so an ordinary member access falls through untouched.
//
// THIS is accepted here and nowhere else.  In IEC it is a pointer and THIS^
// dereferences it, and stratuMAK has no pointers -- letting THIS be a value
// would put one back, which §8 of the frontend note refuses on purpose.  As
// the base of a call it needs to be no such thing: the instance is already a
// parameter of the generated function.
func (c *checker) checkThisCall(fld *st.FieldExpr, pos ast.Pos, args []st.Arg) (*ast.MethodCallExpr, bool) {
	deref, ok := fld.Base.(*st.DerefExpr)
	if !ok {
		return nil, false
	}
	id, ok := deref.Base.(*st.Ident)
	if !ok || !strings.EqualFold(id.Name, "THIS") {
		return nil, false
	}

	if c.blockOwner == nil {
		c.errorf(pos, "THIS names the function block instance a body is running on, "+
			"and there is none here: only a FUNCTION_BLOCK's body, action or method has one")
		return nil, true
	}
	call := c.checkOwnMethodCall(pos, fld.Name, args)
	if call == nil {
		c.errorf(pos, "%s has no method %s", c.blockOwner.Name, fld.Name)
		return nil, true
	}
	return call, true
}
