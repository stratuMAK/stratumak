// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// GMI from Structured Text.  See docs/dev/MODCOMPILE_ST_STDLIB.md §7a.
//
// Almost nothing new is emitted here.  The header includes, the inst_t members
// holding a consumed API's callbacks, the api_get lookups and the registration
// of a provided one already existed for .comp and are driven by
// Component.GMIConsume and Component.GMIProvide, which sema now fills in from
// pragmas.  What is left is the call itself, and -- for a provider -- the
// thunks that give the generated header the free functions it expects.

// apiCall renders a call through a consumed interface.
//
// The .comp spelling of the same thing is HM2_SERIAL->fn(HM2_SERIAL->ctx, ...)
// written by hand; here it is the same expression, generated.
func (e *stEmitter) apiCall(v *ast.APICallExpr) string {
	handle := fmt.Sprintf("__comp_inst->__gmi_%s", toC(v.API))
	args := fmt.Sprintf("%s->ctx", handle)
	for _, a := range v.Args {
		args += ", " + e.expr(a)
	}
	return fmt.Sprintf("(%s->%s(%s))", handle, toC(v.Func), args)
}

// providedThunks reports the blocks that implement an interface, with the
// instance of each that the component registers.
//
// A provider is a function block, and the callbacks are free functions taking
// a void *ctx that the registration sets to the component instance -- so each
// thunk casts that back and calls the method on the one instance the component
// declared.  One instance per interface: two would need the registration to
// name which, and GMI registers one table per API per component.
func (g *generator) providedThunks() []providedAPI {
	var out []providedAPI
	for _, api := range g.comp.GMIProvide {
		for i := range g.pkg.Blocks {
			b := &g.pkg.Blocks[i]
			if !implementsAPI(b, api) {
				continue
			}
			if inst, ok := g.instanceOf(b.Name); ok {
				out = append(out, providedAPI{API: api, Block: b, Inst: inst})
			}
			break
		}
	}
	return out
}

type providedAPI struct {
	API   string
	Block *ast.FunctionBlock
	Inst  string
}

func implementsAPI(b *ast.FunctionBlock, api string) bool {
	for _, n := range b.Implements {
		if strings.EqualFold(n, api) {
			return true
		}
	}
	return false
}

// instanceOf finds the component variable holding an instance of this block.
//
// Matched on the C type rather than on anything sema recorded: a variable's
// declared type is Block_t exactly when it holds one, and inst_t is where the
// thunk has to reach.
func (g *generator) instanceOf(block string) (string, bool) {
	want := fbStructName(block)
	for _, v := range g.comp.Variables {
		if v.CType == want && v.Array == 0 {
			return v.Name, true
		}
	}
	return "", false
}

// emitGMIThunks emits, for each provided interface, the free functions its
// generated header's GMI_<API>_CALLBACKS macro names.
func (g *generator) emitGMIThunks() {
	thunks := g.providedThunks()
	if len(thunks) == 0 {
		return
	}
	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * GMI provider entry points\n")
	g.printf(" *\n")
	g.printf(" * The registration sets each callback's ctx to the component instance, so\n")
	g.printf(" * these cast it back and call the method on the instance that implements\n")
	g.printf(" * the interface.\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	for _, t := range thunks {
		for i := range t.Block.Methods {
			m := &t.Block.Methods[i]
			// The C spellings the header declares, which are the method's
			// own except where the interface uses a type ST maps onto
			// something else -- an enumeration is a DINT here and a typedef
			// there, and a thunk with the wrong one does not match the
			// callback's function pointer type.
			ret := methodReturn(m)
			if m.ResultCType != "" {
				ret = m.ResultCType
			}
			params := "void *ctx"
			call := fmt.Sprintf("%s(__comp_inst, &(__comp_inst->%s)",
				fbMethodCName(t.Block.Name, m.Name), toC(t.Inst))
			for pi, p := range m.Params {
				ctype := p.Typ.CType()
				if pi < len(m.ParamCTypes) && m.ParamCTypes[pi] != "" {
					ctype = m.ParamCTypes[pi]
				}
				params += fmt.Sprintf(", %s %s", ctype, toC(p.Name))
				call += ", " + toC(p.Name)
			}
			call += ")"

			g.printf("static %s gmi_%s_%s(%s) STMAK_NONBLOCKING;\n",
				ret, toC(t.API), toC(m.Name), params)
			g.printf("static %s gmi_%s_%s(%s) {\n", ret, toC(t.API), toC(m.Name), params)
			g.printf("    inst_t *__comp_inst = (inst_t *)ctx;\n")
			if ret == "void" {
				g.printf("    %s;\n", call)
			} else {
				g.printf("    return %s;\n", call)
			}
			g.printf("}\n\n")
		}
	}
}
