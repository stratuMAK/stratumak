// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// This file emits the reusable program organisation units: the stateful
// FUNCTION_BLOCK and the stateless FUNCTION.
//
// A function block's inputs and outputs persist between calls, so they live in
// its instance struct alongside its internal state rather than being
// parameters.  An instance is a plain member of inst_t, which is what lets one
// nest inside another and lets an array of instances cost nothing special.

// fbStructName is the C type of a function block's instance.
func fbStructName(name string) string { return toC(name) + "_t" }

// fbBodyName is the C function that runs one cycle of a block.
func fbBodyName(name string) string { return toC(name) + "_body" }

// fbInitName is the C function that puts one instance into its declared
// starting state.
func fbInitName(name string) string { return toC(name) + "_init" }

// fnName is the C name of a pure function.  The st_ prefix keeps an ST
// function called Clamp or Round from colliding with the C library, which is
// linked into every module.
func fnName(name string) string { return "st_" + toC(name) }

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

// emitFBStructs emits one struct per function block.  They come before inst_t,
// which has members of them, and sema has ordered the blocks so a block is
// declared before the one that contains it.
func (g *generator) emitFBStructs() {
	if len(g.pkg.Blocks) == 0 {
		return
	}
	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Function block instances\n")
	g.printf(" *\n")
	g.printf(" * Inputs and outputs persist between calls, per IEC semantics, so they sit\n")
	g.printf(" * here beside the internal state rather than being parameters.\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	for i := range g.pkg.Blocks {
		b := &g.pkg.Blocks[i]
		g.printf("typedef struct {\n")
		emitFBMembers(g, "VAR_INPUT", b.Inputs)
		emitFBMembers(g, "VAR_OUTPUT", b.Outputs)
		emitFBMembers(g, "VAR_IN_OUT", b.InOuts)
		emitFBMembers(g, "VAR", b.Vars)
		if len(b.Inputs)+len(b.Outputs)+len(b.InOuts)+len(b.Vars) == 0 {
			// C has no empty struct; a block with no state still needs a type.
			g.printf("    char __empty;\n")
		}
		g.printf("} %s;\n\n", fbStructName(b.Name))
	}
}

func emitFBMembers(g *generator, what string, set []ast.VarDecl) {
	var live []ast.VarDecl
	for _, v := range set {
		// A constant every reference folded away needs no storage.
		if v.FoldsAway() {
			continue
		}
		live = append(live, v)
	}
	if len(live) == 0 {
		return
	}
	g.printf("    /* %s */\n", what)
	for _, v := range live {
		g.printf("    %s %s%s;\n", v.Typ.CType(), toC(v.Name), cTypeDims(v.Typ))
	}
}

// emitPOUForwards declares every block body, block initialiser and function
// before the first is defined, so they may call each other in any order.
func (g *generator) emitPOUForwards() {
	if len(g.pkg.Blocks) == 0 && len(g.pkg.Functions) == 0 {
		return
	}
	for i := range g.pkg.Blocks {
		b := &g.pkg.Blocks[i]
		g.printf("static void %s(inst_t *__comp_inst, %s *__self) STMAK_NONBLOCKING;\n",
			fbBodyName(b.Name), fbStructName(b.Name))
		g.printf("static void %s(%s *__self);\n", fbInitName(b.Name), fbStructName(b.Name))
	}
	for i := range g.pkg.Functions {
		f := &g.pkg.Functions[i]
		g.printf("static %s %s(%s) STMAK_NONBLOCKING;\n",
			f.Result.CType(), fnName(f.Name), fnParams(f))
	}
	g.printf("\n")
}

// fnParams renders a function's parameter list.  Every function takes the
// instance as well, so the real-time guards can record a fault from inside one.
func fnParams(f *ast.PureFunction) string {
	s := "inst_t *__comp_inst"
	for _, p := range f.Params {
		s += fmt.Sprintf(", %s %s", p.Typ.CType(), toC(p.Name))
	}
	return s
}

// ---------------------------------------------------------------------------
// Bodies
// ---------------------------------------------------------------------------

// emitPOUBodies emits the block initialisers and bodies, and the functions.
// They come after inst_t, since a guard inside one needs the instance.
func (g *generator) emitPOUBodies() {
	for i := range g.pkg.Blocks {
		g.emitFBInit(&g.pkg.Blocks[i])
	}
	for i := range g.pkg.Blocks {
		g.emitFBBody(&g.pkg.Blocks[i])
	}
	for i := range g.pkg.Functions {
		g.emitFunction(&g.pkg.Functions[i])
	}
}

// emitFBInit puts one instance into its declared starting state.  An instance
// arrives zeroed, so only a declaration with an initialiser needs anything --
// but a nested instance still has to be walked, since its own defaults are its
// business.
func (g *generator) emitFBInit(b *ast.FunctionBlock) {
	e := &stEmitter{g: g}

	g.printf("static void %s(%s *__self) {\n", fbInitName(b.Name), fbStructName(b.Name))
	g.printf("    (void)__self;\n")

	for _, set := range [][]ast.VarDecl{b.Inputs, b.Outputs, b.InOuts, b.Vars} {
		for _, v := range set {
			if v.FoldsAway() {
				continue
			}
			switch {
			case v.Typ.Kind == ast.TypeFB:
				g.printf("    %s(&__self->%s);\n", fbInitName(v.Typ.Ref), toC(v.Name))
			case v.Typ.Kind == ast.TypeArray && v.Typ.Elem != nil && v.Typ.Elem.Kind == ast.TypeFB:
				g.printf("    { int __i; for (__i = 0; __i < %d; __i++) %s(&((%s *)__self->%s)[__i]); }\n",
					v.Typ.Elements(), fbInitName(v.Typ.Elem.Ref),
					fbStructName(v.Typ.Elem.Ref), toC(v.Name))
			case v.Init != nil:
				g.printf("    __self->%s = %s;\n", toC(v.Name), e.expr(v.Init))
			}
		}
	}
	g.printf("}\n\n")
}

// emitFBBody emits one cycle of a block.
//
// Unlike a PROGRAM there is no pin snapshot: a block has no pins.  Its inputs
// were written into the instance by the caller just before the call, so they
// are already stable.
func (g *generator) emitFBBody(b *ast.FunctionBlock) {
	e := &stEmitter{g: g, self: "__self"}

	g.printf("static void %s(inst_t *__comp_inst, %s *__self) {\n",
		fbBodyName(b.Name), fbStructName(b.Name))
	g.printf("    (void)__comp_inst; (void)__self;\n")
	e.emitStmts(b.Body, 1)
	e.lineBackToOutput()
	g.printf("}\n\n")
}

// emitFunction emits a stateless function.  ST returns a value by assigning to
// a name matching the function, so that name is a local here and is returned
// at the end.
func (g *generator) emitFunction(f *ast.PureFunction) {
	e := &stEmitter{g: g}

	g.printf("static %s %s(%s) {\n", f.Result.CType(), fnName(f.Name), fnParams(f))
	g.printf("    (void)__comp_inst;\n")

	// The result variable, zeroed: a body that returns on some path without
	// assigning it would otherwise return whatever was on the stack.
	g.printf("    %s %s = 0;\n", f.Result.CType(), toC(f.Name))

	for _, v := range f.Vars {
		if v.FoldsAway() {
			continue
		}
		init := "0"
		if v.Init != nil {
			init = e.expr(v.Init)
		}
		if v.Typ.Kind == ast.TypeArray || v.Typ.Kind == ast.TypeStruct {
			g.printf("    %s %s%s;\n", v.Typ.CType(), toC(v.Name), cTypeDims(v.Typ))
			g.printf("    memset(&%s, 0, sizeof(%s));\n", toC(v.Name), toC(v.Name))
			continue
		}
		g.printf("    %s %s = %s;\n", v.Typ.CType(), toC(v.Name), init)
	}

	e.emitStmts(f.Body, 1)
	e.lineBackToOutput()
	g.printf("    return %s;\n", toC(f.Name))
	g.printf("}\n\n")
}

// ---------------------------------------------------------------------------
// Invocation
// ---------------------------------------------------------------------------

// emitInvoke emits a function block call: the inputs are written into the
// instance, the body runs, and any `=>` bindings copy outputs back.
func (e *stEmitter) emitInvoke(v *ast.InvokeStmt, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	inst := e.expr(v.Inst)
	block := v.Inst.ExprType().Ref

	for _, a := range v.Args {
		if a.Out {
			continue
		}
		g.printf("%s%s.%s = %s;\n", in, inst, toC(a.Name), e.expr(a.Value))
	}

	g.printf("%s%s(__comp_inst, &%s);\n", in, fbBodyName(block), inst)

	for _, a := range v.Args {
		if !a.Out {
			continue
		}
		g.printf("%s%s = %s.%s;\n", in, e.expr(a.Value), inst, toC(a.Name))
	}
}

// emitInstanceInit calls the initialiser of every function block instance the
// component holds, so a declared default is in place before the first cycle.
func (g *generator) emitInstanceInit() {
	for _, v := range g.comp.Variables {
		blockOf, arrayed := instanceBlock(g, v)
		if blockOf == "" {
			continue
		}
		if arrayed {
			g.printf("    { int __i; for (__i = 0; __i < %d; __i++) %s(&inst->%s[__i]); }\n",
				v.Array, fbInitName(blockOf), v.Name)
			continue
		}
		g.printf("    %s(&inst->%s);\n", fbInitName(blockOf), v.Name)
	}
}

// hasInstances reports whether the component holds any function block
// instance.
func (g *generator) hasInstances() bool {
	for _, v := range g.comp.Variables {
		if name, _ := instanceBlock(g, v); name != "" {
			return true
		}
	}
	return false
}

// instanceBlock reports which block a variable is an instance of, and whether
// it is an array of them.  The match is on the generated struct name, which is
// what the variable carries.
func instanceBlock(g *generator, v ast.Variable) (string, bool) {
	for i := range g.pkg.Blocks {
		if v.CType == fbStructName(g.pkg.Blocks[i].Name) {
			return g.pkg.Blocks[i].Name, v.Array > 0
		}
	}
	return "", false
}
