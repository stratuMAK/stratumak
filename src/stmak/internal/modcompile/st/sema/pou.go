// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// This file resolves the two reusable program organisation units: the stateful
// FUNCTION_BLOCK and the stateless FUNCTION.
//
// A function block is internal.  It has no HAL identity of its own -- only a
// PROGRAM's I/O reaches HAL -- which is what lets an instance nest inside
// another and lets an array of instances be unremarkable.  See
// docs/dev/MODCOMPILE_ST_FRONTEND.md §2.

// checkPOUs resolves the function blocks and functions of a file.
//
// Names are registered before any body, so one may call another regardless of
// declaration order.  Recursion is caught afterwards: the real-time stack is
// fixed and there is no useful way to bound the depth.
func (c *checker) checkPOUs() {
	for _, b := range c.file.Blocks {
		c.registerPOU(b.Name, b.Pos, symBlock)
	}
	for _, f := range c.file.Funcs {
		c.registerPOU(f.Name, f.Pos, symFunc)
	}

	// Signatures next: a body may call any of them, so every parameter list
	// has to exist before the first body is checked.
	for _, b := range c.file.Blocks {
		c.declareBlockSignature(b)
	}
	for _, f := range c.file.Funcs {
		c.declareFunctionSignature(f)
	}

	for _, b := range c.file.Blocks {
		c.checkBlockBody(b)
	}
	for _, f := range c.file.Funcs {
		c.checkFunctionBody(f)
	}
}

func (c *checker) registerPOU(name string, pos ast.Pos, kind symKind) {
	if name == "" {
		return
	}
	if prev := c.scope.lookup(name); prev != nil {
		c.errorf(pos, "%s is already declared at %s", name, prev.Pos)
		return
	}
	c.scope.declare(&symbol{Name: name, Kind: kind, Pos: pos})
}

// ---------------------------------------------------------------------------
// FUNCTION_BLOCK
// ---------------------------------------------------------------------------

// declareBlockSignature records a block's variables without checking its body.
//
// Inputs and outputs persist between calls, per IEC semantics, so they live in
// the instance struct alongside the internal VARs rather than being
// parameters.  That is what makes `r.out` a read of retained state, and what
// lets a timer or an edge detector work at all.
func (c *checker) declareBlockSignature(b *st.FunctionBlockDecl) {
	if b.Name == "" {
		return
	}
	def := ast.FunctionBlock{Pos: b.Pos, Name: b.Name}
	def.Doc = c.pouAttributes(b.Attrs, "FUNCTION_BLOCK", b.Name)

	// A block's CONSTANTs go into scope as they are declared, so a later
	// declaration may name one -- `aHist : ARRAY[1..MINUTES_PER_DAY]` is
	// ordinary, and refusing it would make constants useless where they are
	// most wanted.
	c.push()
	defer c.pop()

	seen := make(map[string]ast.Pos)
	for _, blk := range b.Vars {
		switch blk.Kind {
		case st.VarGlobal, st.VarExternal:
			c.errorf(blk.Pos, "a function block may not declare %s; "+
				"pass what it needs in through VAR_INPUT", blk.Kind)
			continue
		}
		for _, d := range blk.Decls {
			if d.Located != "" && blk.Kind.IsHAL() {
				c.errorf(d.Pos, "AT is redundant in %s: a function block's %s is bound at the "+
					"call site, not by HAL", blk.Kind, blk.Kind)
				continue
			}
			if d.Edge != st.EdgeNone {
				c.errorf(d.Pos, "R_EDGE and F_EDGE are not supported; "+
					"use the R_TRIG and F_TRIG function blocks once they land")
				continue
			}
			typ, ok := c.resolveType(d.Type)
			if !ok {
				continue
			}
			for _, name := range d.Names {
				if prev, dup := seen[key(name)]; dup {
					c.errorf(d.Pos, "%s is already declared at %s", name, prev)
					continue
				}
				seen[key(name)] = d.Pos

				v := ast.VarDecl{
					Pos:   d.Pos,
					Name:  name,
					Typ:   typ,
					Store: ast.StorageField,
					Doc:   attrValue(d.Attrs, "doc"),
					Const: blk.Constant,
				}
				if d.Located != "" {
					// The pin's full name is not known until an instance path
					// is, so only what the declaration said is recorded here.
					v.HAL = c.bindAT(d.Located, d.Pos, name, typ)
					if v.HAL == nil {
						continue
					}
				}
				if d.Init != nil {
					if typ.Kind == ast.TypeArray || typ.Kind == ast.TypeStruct || typ.Kind == ast.TypeFB {
						c.errorf(d.Init.Position(),
							"%s cannot be given an initial value in its declaration; %s is an aggregate",
							name, typ)
					} else {
						v.Init = c.checkExpr(d.Init, typ)
						if v.Init != nil {
							if _, ok := c.foldToC(v.Init); !ok {
								c.errorf(d.Init.Position(),
									"the initial value of %s must be a compile-time constant", name)
							} else if blk.Constant {
								// The declaration keeps the value, not the
								// expression that produced it, so the backend
								// can see it folded and drop the storage.
								if lit := foldToConst(v.Init, typ); lit != nil {
									v.Init = lit
								}
								c.scope.declare(constSymbol(v))
							}
						}
					}
				}
				switch blk.Kind {
				case st.VarInput:
					def.Inputs = append(def.Inputs, v)
				case st.VarOutput:
					def.Outputs = append(def.Outputs, v)
				case st.VarInOut:
					def.InOuts = append(def.InOuts, v)
				default:
					def.Vars = append(def.Vars, v)
				}
			}
		}
	}

	c.pkg.Blocks = append(c.pkg.Blocks, def)
}

// constSymbol builds the symbol for a CONSTANT, carrying the folded value that
// makes a reference to it resolve to that value rather than to a read.
func constSymbol(v ast.VarDecl) *symbol {
	sym := &symbol{
		Name: v.Name, Kind: symVar, Type: v.Typ,
		Store: v.Store, Pos: v.Pos, Const: true,
		ConstExpr: foldToConst(v.Init, v.Typ),
		// v.Init has already been folded by the caller where it could be, so
		// this is normally the same node's value.
	}
	if n, ok := foldInt(v.Init); ok {
		sym.ConstValue, sym.HasConstValue = n, true
	}
	return sym
}

// checkBlockBody resolves one block's statements.
//
// The scope hangs off the type scope, not the program's: a function block is
// self-contained and cannot see a program's variables.  That isolation is what
// makes it reusable.
func (c *checker) checkBlockBody(b *st.FunctionBlockDecl) {
	def := c.pkg.LookupBlock(b.Name)
	if def == nil {
		return
	}

	c.push()
	defer c.pop()
	c.declarePeriod(b.Pos)
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

	c.declareActions(b.Actions, "FUNCTION_BLOCK", b.Name)
	def.Body = c.checkStmts(b.Body)
	def.Actions = c.checkActions(b.Actions, "FUNCTION_BLOCK", b.Name)
}

// ---------------------------------------------------------------------------
// FUNCTION
// ---------------------------------------------------------------------------

// declareFunctionSignature records a function's result type and parameters.
func (c *checker) declareFunctionSignature(f *st.FunctionDecl) {
	if f.Name == "" {
		return
	}
	def := ast.PureFunction{Pos: f.Pos, Name: f.Name}
	def.Doc = c.pouAttributes(f.Attrs, "FUNCTION", f.Name)

	c.push()
	defer c.pop()

	result, ok := c.resolveType(f.Result)
	if !ok {
		c.pkg.Functions = append(c.pkg.Functions, def)
		return
	}
	if !result.IsScalar() && result.Kind != ast.TypeString {
		c.errorf(f.Pos, "a function may not return %s; only a scalar or a STRING", result)
	}
	def.Result = result

	seen := make(map[string]ast.Pos)
	for _, blk := range f.Vars {
		switch blk.Kind {
		case st.VarOutput, st.VarInOut, st.VarGlobal, st.VarExternal:
			c.errorf(blk.Pos, "a function may not declare %s; it keeps no state between calls "+
				"and returns its one result by name", blk.Kind)
			continue
		}
		for _, d := range blk.Decls {
			typ, ok := c.resolveType(d.Type)
			if !ok {
				continue
			}
			if typ.Kind == ast.TypeFB {
				c.errorf(d.Pos, "a function may not hold a function block instance; "+
					"a function keeps no state between calls")
				continue
			}
			for _, name := range d.Names {
				if prev, dup := seen[key(name)]; dup {
					c.errorf(d.Pos, "%s is already declared at %s", name, prev)
					continue
				}
				seen[key(name)] = d.Pos

				v := ast.VarDecl{
					Pos: d.Pos, Name: name, Typ: typ,
					Store: ast.StorageLocal, Const: blk.Constant,
				}
				if d.Init != nil {
					v.Init = c.checkExpr(d.Init, typ)
					if v.Init != nil && blk.Constant {
						if lit := foldToConst(v.Init, typ); lit != nil {
							v.Init = lit
						}
						c.scope.declare(constSymbol(v))
					}
				}
				if blk.Kind == st.VarInput {
					def.Params = append(def.Params, v)
				} else {
					def.Vars = append(def.Vars, v)
				}
			}
		}
	}

	c.pkg.Functions = append(c.pkg.Functions, def)
}

// checkFunctionBody resolves one function's statements.
//
// ST returns a value by assigning to a variable named after the function, so
// that name is declared as an ordinary local here and the backend returns it.
func (c *checker) checkFunctionBody(f *st.FunctionDecl) {
	def := c.pkg.LookupFunction(f.Name)
	if def == nil {
		return
	}

	c.push()
	defer c.pop()

	c.scope.declare(&symbol{
		Name: def.Name, Kind: symVar, Type: def.Result,
		Store: ast.StorageLocal, Pos: def.Pos,
	})
	for _, set := range [][]ast.VarDecl{def.Params, def.Vars} {
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

	prev := c.result
	c.result = def.Name
	def.Body = c.checkStmts(f.Body)
	c.result = prev

	if !assignsResult(def.Body, def.Name) {
		c.warnf("%s: function %s never assigns its result, so it returns an undefined value",
			def.Pos, def.Name)
	}
}

// assignsResult reports whether a body assigns the function's result anywhere.
func assignsResult(body []ast.Stmt, name string) bool {
	found := false
	walkBody(body, func(s ast.Stmt) {
		as, ok := s.(*ast.AssignStmt)
		if !ok {
			return
		}
		if ref, ok := as.Target.(*ast.VarRef); ok && ast.EqualName(ref.Name, name) {
			found = true
		}
	})
	return found
}

// walkBody calls f for every statement, nested ones included.
func walkBody(list []ast.Stmt, f func(ast.Stmt)) {
	for _, s := range list {
		if s == nil {
			continue
		}
		f(s)
		switch v := s.(type) {
		case *ast.IfStmt:
			walkBody(v.Then, f)
			for _, e := range v.ElseIf {
				walkBody(e.Body, f)
			}
			walkBody(v.Else, f)
		case *ast.CaseStmt:
			for _, cl := range v.Clauses {
				walkBody(cl.Body, f)
			}
			walkBody(v.Else, f)
		case *ast.ForStmt:
			walkBody(v.Body, f)
		case *ast.WhileStmt:
			walkBody(v.Body, f)
		case *ast.RepeatStmt:
			walkBody(v.Body, f)
		}
	}
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// checkInvoke resolves a call used as a statement.  At this level a function
// block invocation and a discarded function call look the same, so what the
// target resolves to decides which it is.
func (c *checker) checkInvoke(v *st.CallStmt) ast.Stmt {
	// A bare name that is an action or a function has to be caught before the
	// target is resolved as a value, since neither is one and that message
	// would win over the useful one.
	if ref, ok := v.Target.(*st.Ident); ok {
		if sym := c.scope.lookup(ref.Name); sym != nil {
			switch sym.Kind {
			case symAction:
				return c.checkActionCall(v, sym.Name)
			case symFunc:
				c.errorf(v.Pos, "%s returns a value, so calling it as a statement discards it; "+
					"assign the result", ref.Name)
				return nil
			}
		}
	}

	// A function block instance is a value: resolve it as one, so
	// `avg[i](...)` and `outer.inner(...)` work like any other reference.
	inst := c.checkExpr(v.Target, ast.Type{})
	if inst == nil {
		return nil
	}
	if inst.ExprType().Kind != ast.TypeFB {
		c.errorf(v.Pos, "%s is not a function block instance, so it cannot be invoked",
			exprName(inst, inst.ExprType()))
		return nil
	}

	def := c.pkg.LookupBlock(inst.ExprType().Ref)
	if def == nil {
		return nil
	}

	out := &ast.InvokeStmt{Pos: v.Pos, Inst: inst}
	bound := make(map[string]ast.Pos)
	positional := 0

	for _, a := range v.Args {
		switch a.Form {
		case st.ArgOutput:
			c.bindOutput(out, def, a, bound)
		case st.ArgInput:
			c.bindInput(out, def, a.Name, a.Pos, a.Value, bound)
		default:
			// Positional arguments fill the inputs in declaration order.
			if positional >= len(def.Inputs) {
				c.errorf(a.Pos, "%s takes %d %s, and this is one too many",
					def.Name, len(def.Inputs), plural(len(def.Inputs), "input", "inputs"))
				continue
			}
			c.bindInput(out, def, def.Inputs[positional].Name, a.Pos, a.Value, bound)
			positional++
		}
	}
	return out
}

// bindInput binds one value to a named input of the instance.
func (c *checker) bindInput(out *ast.InvokeStmt, def *ast.FunctionBlock, name string, pos ast.Pos, value st.Expr, bound map[string]ast.Pos) {
	decl, ok := lookupIO(def, name, st.VarInput)
	if !ok {
		if _, isOut := lookupIO(def, name, st.VarOutput); isOut {
			c.errorf(pos, "%s is an output of %s; read it after the call as %s, "+
				"or bind it with '=>'", name, def.Name, name)
			return
		}
		c.errorf(pos, "%s has no input %s", def.Name, name)
		return
	}
	if prev, dup := bound[key(name)]; dup {
		c.errorf(pos, "input %s is already bound at %s", name, prev)
		return
	}
	bound[key(name)] = pos

	e := c.checkExpr(value, decl.Typ)
	if e == nil {
		return
	}
	e = c.assign(pos, def.Name+"."+decl.Name, decl.Typ, e)
	if e == nil {
		return
	}
	out.Args = append(out.Args, ast.InvokeArg{Pos: pos, Name: decl.Name, Value: e})
}

// bindOutput binds `name => target`, which copies an output back after the
// call.
func (c *checker) bindOutput(out *ast.InvokeStmt, def *ast.FunctionBlock, a st.Arg, bound map[string]ast.Pos) {
	decl, ok := lookupIO(def, a.Name, st.VarOutput)
	if !ok {
		c.errorf(a.Pos, "%s has no output %s", def.Name, a.Name)
		return
	}
	target := c.checkExpr(a.Value, decl.Typ)
	if target == nil {
		return
	}
	if !ast.IsLValue(target) {
		c.errorf(a.Value.Position(), "the target of '=>' must be assignable")
		return
	}
	if c.assign(a.Pos, exprName(target, target.ExprType()), target.ExprType(), &ast.VarRef{
		Pos: a.Pos, Name: decl.Name, Store: ast.StorageField, Typ: decl.Typ,
	}) == nil {
		return
	}
	out.Args = append(out.Args, ast.InvokeArg{Pos: a.Pos, Name: decl.Name, Value: target, Out: true})
}

// lookupIO finds a declaration in one section of a block.  VAR_IN_OUT counts
// as both, since it is read and written.
func lookupIO(def *ast.FunctionBlock, name string, kind st.VarKind) (ast.VarDecl, bool) {
	sets := [][]ast.VarDecl{def.InOuts}
	switch kind {
	case st.VarInput:
		sets = append(sets, def.Inputs)
	case st.VarOutput:
		sets = append(sets, def.Outputs)
	}
	for _, set := range sets {
		for _, v := range set {
			if ast.EqualName(v.Name, name) {
				return v, true
			}
		}
	}
	return ast.VarDecl{}, false
}

// checkCall resolves a call used as an expression, which must be a FUNCTION.
func (c *checker) checkCall(v *st.CallExpr) ast.Expr {
	name, ok := v.Func.(*st.Ident)
	if !ok {
		c.errorf(v.Pos, "only a plain function name may be called in an expression")
		return nil
	}
	// The duration conversions are built in, so they resolve before the
	// scope: a file declaring its own FUNCTION of one of these names is a
	// redeclaration, not a shadow.
	if e, handled := c.checkTimeConversion(v, name.Name); handled {
		return e
	}
	sym := c.scope.lookup(name.Name)
	if sym == nil {
		c.errorf(v.Pos, "%s is not declared", name.Name)
		return nil
	}
	if sym.Kind == symBlock {
		c.errorf(v.Pos, "%s is a function block: invoke an instance of it as a statement, "+
			"then read its outputs", name.Name)
		return nil
	}
	if sym.Kind != symFunc {
		c.errorf(v.Pos, "%s is a %s, not a function", name.Name, sym.Kind)
		return nil
	}

	def := c.pkg.LookupFunction(sym.Name)
	if def == nil {
		return nil
	}

	out := &ast.CallExpr{Pos: v.Pos, Func: def.Name, Typ: def.Result}
	if len(v.Args) != len(def.Params) {
		c.errorf(v.Pos, "%s takes %d %s, got %d",
			def.Name, len(def.Params), plural(len(def.Params), "argument", "arguments"), len(v.Args))
		return nil
	}
	for i, a := range v.Args {
		if a.Form != st.ArgPositional && !ast.EqualName(a.Name, def.Params[i].Name) {
			// A named argument out of order would need a temporary per
			// argument to evaluate safely; refuse rather than silently
			// reordering side effects.
			c.errorf(a.Pos, "%s: argument %d is %s, not %s; "+
				"a function's arguments are matched by position",
				def.Name, i+1, def.Params[i].Name, a.Name)
			return nil
		}
		e := c.checkExpr(a.Value, def.Params[i].Typ)
		if e == nil {
			return nil
		}
		e = c.assign(a.Pos, def.Name+"."+def.Params[i].Name, def.Params[i].Typ, e)
		if e == nil {
			return nil
		}
		out.Args = append(out.Args, e)
	}
	return out
}

// ---------------------------------------------------------------------------
// Recursion
// ---------------------------------------------------------------------------

// containedBlocks lists the function blocks one block holds an instance of,
// through an array or directly.
func containedBlocks(b *ast.FunctionBlock) []namedRef {
	var refs []namedRef
	for _, set := range [][]ast.VarDecl{b.Inputs, b.Outputs, b.InOuts, b.Vars} {
		for _, v := range set {
			t := v.Typ
			for t.Kind == ast.TypeArray && t.Elem != nil {
				t = *t.Elem
			}
			if t.Kind == ast.TypeFB {
				refs = append(refs, namedRef{t.Ref, v.Pos})
			}
		}
	}
	return refs
}

// checkContainmentCycles refuses a function block that holds an instance of
// itself, directly or through another block.  Such a block is infinitely large
// before it ever runs.
//
// This is separate from checkCallCycles, and runs much earlier, because it is
// a property of the declarations alone: every later pass walks the instance
// tree -- publishing AT pins down it, laying out inst_t -- and an infinite tree
// does not terminate.  Catching it here is what lets those passes descend
// without a depth guard of their own.  It reports whether the tree is finite.
func (c *checker) checkContainmentCycles() bool {
	edges := make(map[string][]namedRef, len(c.pkg.Blocks))
	for i := range c.pkg.Blocks {
		b := &c.pkg.Blocks[i]
		edges[key(b.Name)] = containedBlocks(b)
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make(map[string]int, len(edges))
	ok := true

	var visit func(name string, pos ast.Pos)
	visit = func(name string, pos ast.Pos) {
		k := key(name)
		switch state[k] {
		case grey:
			c.errorf(pos, "%s can reach itself; recursion is not allowed in a real-time body, "+
				"because the stack depth cannot be bounded", displayName(name))
			ok = false
			return
		case black:
			return
		}
		state[k] = grey
		for _, ref := range edges[k] {
			visit(ref.name, ref.pos)
		}
		state[k] = black
	}
	for i := range c.pkg.Blocks {
		visit(c.pkg.Blocks[i].Name, c.pkg.Blocks[i].Pos)
	}
	return ok
}

// checkCallCycles refuses anything that can reach itself by calling.  The
// real-time stack is fixed and there is no useful way to bound the depth, so
// recursion is a compile error rather than a runtime hazard.
//
// Containment is the other way a block can reach itself, and is settled
// earlier by checkContainmentCycles; the edges are repeated here so this check
// stands on its own if it is ever run first.
func (c *checker) checkCallCycles() {
	edges := make(map[string][]namedRef)

	for i := range c.pkg.Blocks {
		b := &c.pkg.Blocks[i]
		refs := containedBlocks(b)
		refs = append(refs, calleesOf(b.Body)...)
		for i := range b.Actions {
			refs = append(refs, calleesOf(b.Actions[i].Body)...)
			refs = append(refs, actionEdges(b.Name, b.Actions[i].Body)...)
		}
		edges[key(b.Name)] = refs
		for i := range b.Actions {
			edges[actionKey(b.Name, b.Actions[i].Name)] =
				append(calleesOf(b.Actions[i].Body), actionEdges(b.Name, b.Actions[i].Body)...)
		}
	}
	for i := range c.pkg.Functions {
		f := &c.pkg.Functions[i]
		edges[key(f.Name)] = calleesOf(f.Body)
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make(map[string]int, len(edges))

	// A program's actions can reach each other too, and a program is not a
	// block, so its edges go in under the same qualified keys.  This is why
	// the check runs after the programs are checked rather than with the
	// other POUs: their actions do not exist until then.
	at := make(map[string]ast.Pos, len(edges))
	for i := range c.pkg.Component.Functions {
		fn := &c.pkg.Component.Functions[i]
		for j := range fn.Actions {
			k := actionKey(fn.Name, fn.Actions[j].Name)
			edges[k] = append(calleesOf(fn.Actions[j].Body), actionEdges(fn.Name, fn.Actions[j].Body)...)
			at[k] = fn.Actions[j].Pos
		}
	}
	for i := range c.pkg.Blocks {
		b := &c.pkg.Blocks[i]
		for j := range b.Actions {
			at[actionKey(b.Name, b.Actions[j].Name)] = b.Actions[j].Pos
		}
	}

	var visit func(name string, pos ast.Pos)
	visit = func(name string, pos ast.Pos) {
		k := key(name)
		if _, qualified := edges[name]; qualified {
			// An action's key is already qualified and case-folded.
			k = name
		}
		switch state[k] {
		case grey:
			c.errorf(pos, "%s can reach itself; recursion is not allowed in a real-time body, "+
				"because the stack depth cannot be bounded", displayName(name))
			return
		case black:
			return
		}
		state[k] = grey
		for _, ref := range edges[k] {
			visit(ref.name, ref.pos)
		}
		state[k] = black
	}

	for i := range c.pkg.Blocks {
		visit(c.pkg.Blocks[i].Name, c.pkg.Blocks[i].Pos)
	}
	for i := range c.pkg.Functions {
		visit(c.pkg.Functions[i].Name, c.pkg.Functions[i].Pos)
	}
	for k := range edges {
		visit(k, at[k])
	}
}

// displayName renders a call-graph key for a diagnostic.  An action's key is
// qualified and upper-cased for lookup; the message wants the action's name.
func displayName(k string) string {
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		return k[i+1:]
	}
	return k
}

// actionKey names an action in the call graph, qualified by its POU so two
// POUs may each have one called `init`.
func actionKey(owner, action string) string { return key(owner) + "." + key(action) }

// actionEdges lists the actions a body runs, qualified by their owner.
func actionEdges(owner string, body []ast.Stmt) []namedRef {
	var out []namedRef
	for _, ref := range actionCallees(body) {
		out = append(out, namedRef{owner + "." + ref.name, ref.pos})
	}
	return out
}

// calleesOf lists what a body invokes or calls.
func calleesOf(body []ast.Stmt) []namedRef {
	var out []namedRef
	walkBody(body, func(s ast.Stmt) {
		if inv, ok := s.(*ast.InvokeStmt); ok {
			t := inv.Inst.ExprType()
			if t.Kind == ast.TypeFB {
				out = append(out, namedRef{t.Ref, inv.Pos})
			}
		}
	})
	walkStmtExprs(body, func(e ast.Expr) {
		if call, ok := e.(*ast.CallExpr); ok {
			out = append(out, namedRef{call.Func, call.Pos})
		}
	})
	return out
}

// walkStmtExprs calls f for every expression in a body.
func walkStmtExprs(body []ast.Stmt, f func(ast.Expr)) {
	var expr func(ast.Expr)
	expr = func(e ast.Expr) {
		if e == nil {
			return
		}
		f(e)
		switch v := e.(type) {
		case *ast.BinaryExpr:
			expr(v.X)
			expr(v.Y)
		case *ast.UnaryExpr:
			expr(v.X)
		case *ast.ConvExpr:
			expr(v.X)
		case *ast.TimeConvExpr:
			expr(v.X)
		case *ast.IndexExpr:
			expr(v.Base)
			for _, ix := range v.Indices {
				expr(ix)
			}
		case *ast.FieldExpr:
			expr(v.Base)
		case *ast.CallExpr:
			for _, a := range v.Args {
				expr(a)
			}
		}
	}
	walkBody(body, func(s ast.Stmt) {
		switch v := s.(type) {
		case *ast.AssignStmt:
			expr(v.Target)
			expr(v.Value)
		case *ast.IfStmt:
			expr(v.Cond)
			for _, e := range v.ElseIf {
				expr(e.Cond)
			}
		case *ast.CaseStmt:
			expr(v.Selector)
		case *ast.ForStmt:
			expr(v.Var)
			expr(v.From)
			expr(v.To)
			expr(v.By)
		case *ast.WhileStmt:
			expr(v.Cond)
		case *ast.RepeatStmt:
			expr(v.Cond)
		case *ast.InvokeStmt:
			expr(v.Inst)
			for _, a := range v.Args {
				expr(a.Value)
			}
		}
	})
}

// blockFieldType finds the type of a name inside a function block, and whether
// it is readable from outside.  A block's internal VARs are private: exposing
// them would make every instance's implementation part of its interface.
func (c *checker) blockFieldType(def *ast.FunctionBlock, name string) (ast.VarDecl, bool, bool) {
	for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts} {
		for _, v := range set {
			if ast.EqualName(v.Name, name) {
				return v, true, true
			}
		}
	}
	for _, v := range def.Vars {
		if ast.EqualName(v.Name, name) {
			return v, true, false
		}
	}
	return ast.VarDecl{}, false, false
}

// blockNames lists a block's readable members, for a diagnostic that can
// suggest what the author might have meant.
func blockNames(def *ast.FunctionBlock) string {
	var names []string
	for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts} {
		for _, v := range set {
			names = append(names, v.Name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
