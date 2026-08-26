// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// An ACTION is a named piece of a POU's body sharing its variables, so it
// emits as a C function over whatever holds those variables.
//
// For a function block that is the instance, and the action's signature is the
// block body's exactly.  For a PROGRAM it is more involved: a program's pins
// are snapshotted into locals for the cycle, and a local is not reachable from
// another function.  So a program whose actions touch a pin holds the snapshot
// in a struct instead, and hands a pointer to it to each action -- which keeps
// the cycle semantics that made the snapshot worth having while letting the
// action read the same values the body sees.
//
// The struct is per function rather than per action: if any of a function's
// actions reads a pin they all take the pointer, because one signature for all
// of them is easier to follow than two and the pointer costs nothing.  A
// program none of whose actions touch a pin pays none of it.

// actionCName is the C name of one action of an exported function.
func (g *generator) actionCName(fn *ast.Function, action string) string {
	owner := toC(fn.Name)
	if fn.Name == "_" {
		owner = toC(g.comp.Name)
	}
	return owner + "_" + toC(action)
}

// fbActionCName is the C name of one action of a function block.
func fbActionCName(block, action string) string {
	return toC(block) + "_" + toC(action)
}

// pinsStructName is the type holding a function's pin snapshot, emitted only
// when one of its actions needs to see it.
func (g *generator) pinsStructName(fn *ast.Function) string {
	owner := toC(fn.Name)
	if fn.Name == "_" {
		owner = toC(g.comp.Name)
	}
	return owner + "_pins_t"
}

// needsPinStruct reports whether a function's pin snapshot must be reachable
// from its actions.
func needsPinStruct(fn *ast.Function) bool {
	for i := range fn.Actions {
		if fn.Actions[i].TouchesPins {
			return true
		}
	}
	return false
}

// hasActions reports whether the package declares any action at all.
func (g *generator) hasActions() bool {
	for i := range g.comp.Functions {
		if len(g.comp.Functions[i].Actions) > 0 {
			return true
		}
	}
	for i := range g.pkg.Blocks {
		if len(g.pkg.Blocks[i].Actions) > 0 {
			return true
		}
	}
	return false
}

// emitPinStructs emits the snapshot struct for each function that needs one.
// They come before the bodies, since both the body and its actions name them.
func (g *generator) emitPinStructs() {
	first := true
	for i := range g.comp.Functions {
		fn := &g.comp.Functions[i]
		if !needsPinStruct(fn) {
			continue
		}
		if first {
			g.printf("/* ---------------------------------------------------------------------------\n")
			g.printf(" * Pin snapshots shared with actions\n")
			g.printf(" *\n")
			g.printf(" * A pin is read once per cycle into one of these, so an action sees the\n")
			g.printf(" * same values the body does rather than re-reading shared memory.\n")
			g.printf(" * ------------------------------------------------------------------------- */\n\n")
			first = false
		}
		g.printf("typedef struct {\n")
		for _, pin := range g.ownPinsOf(fn) {
			if pin.ArraySize > 0 {
				g.printf("    %s %s[%d];\n", halCType(pin.Type), toC(pin.Name), pin.ArraySize)
				continue
			}
			if pin.Duration {
				g.printf("    int64_t %s;\n", toC(pin.Name))
				continue
			}
			g.printf("    %s %s;\n", halCType(pin.Type), toC(pin.Name))
		}
		g.printf("} %s;\n\n", g.pinsStructName(fn))
	}
}

// emitActionForwards declares every action before the first is defined, so a
// body may call one written after it.
func (g *generator) emitActionForwards() {
	if !g.hasActions() {
		return
	}
	for i := range g.comp.Functions {
		fn := &g.comp.Functions[i]
		for j := range fn.Actions {
			g.printf("static void %s(%s) STMAK_NONBLOCKING;\n",
				g.actionCName(fn, fn.Actions[j].Name), g.actionParams(fn))
		}
	}
	for i := range g.pkg.Blocks {
		b := &g.pkg.Blocks[i]
		for j := range b.Actions {
			g.printf("static void %s(inst_t *__comp_inst, %s *__self) STMAK_NONBLOCKING;\n",
				fbActionCName(b.Name, b.Actions[j].Name), fbStructName(b.Name))
		}
	}
	g.printf("\n")
}

// actionParams is the parameter list an action of fn takes.
func (g *generator) actionParams(fn *ast.Function) string {
	if needsPinStruct(fn) {
		return fmt.Sprintf("inst_t *__comp_inst, %s *__p", g.pinsStructName(fn))
	}
	return "inst_t *__comp_inst"
}

// emitActionBodies emits the actions of every function and block.
func (g *generator) emitActionBodies() {
	for i := range g.comp.Functions {
		fn := &g.comp.Functions[i]
		for j := range fn.Actions {
			act := &fn.Actions[j]
			e := &stEmitter{g: g, fn: fn}
			if needsPinStruct(fn) {
				e.pins = "__p"
			}
			g.printf("static void %s(%s) {\n", g.actionCName(fn, act.Name), g.actionParams(fn))
			g.printf("    (void)__comp_inst;\n")
			if needsPinStruct(fn) {
				g.printf("    (void)__p;\n")
			}
			e.emitStmts(act.Body, 1)
			e.lineBackToOutput()
			g.printf("}\n\n")
		}
	}

	for i := range g.pkg.Blocks {
		b := &g.pkg.Blocks[i]
		for j := range b.Actions {
			act := &b.Actions[j]
			e := &stEmitter{g: g, self: "__self", owner: b.Name}
			g.printf("static void %s(inst_t *__comp_inst, %s *__self) {\n",
				fbActionCName(b.Name, act.Name), fbStructName(b.Name))
			g.printf("    (void)__comp_inst; (void)__self;\n")
			e.emitStmts(act.Body, 1)
			e.lineBackToOutput()
			g.printf("}\n\n")
		}
	}
}

// emitActionCall runs one action, after any variable it was asked to set.
func (e *stEmitter) emitActionCall(v *ast.ActionCall, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	// `Act(v := x)` is shorthand for two statements, and sema lowered it to
	// exactly that, so the assignments emit like any other.
	for _, p := range v.Presets {
		g.printf("%s%s = %s;\n", in, e.expr(p.Target), e.expr(p.Value))
	}

	// `motor.stop()`: the action belongs to another instance, which the call
	// site names.  Its own body reaches its own members through that pointer,
	// exactly as an invocation does.
	if v.Inst != nil {
		g.printf("%s%s(__comp_inst, &(%s));\n", in, fbActionCName(v.Owner, v.Action), e.expr(v.Inst))
		return
	}
	if e.self != "" {
		g.printf("%s%s(__comp_inst, %s);\n", in, fbActionCName(e.owner, v.Action), e.self)
		return
	}
	if e.pins != "" {
		g.printf("%s%s(__comp_inst, %s);\n", in, g.actionCName(e.fn, v.Action), e.pins)
		return
	}
	g.printf("%s%s(__comp_inst);\n", in, g.actionCName(e.fn, v.Action))
}
