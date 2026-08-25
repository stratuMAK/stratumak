// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// This file emits C from the lowered ST bodies in ast.Function.Body.  Because
// sema resolved every type, every name's storage class and every conversion,
// it is close to a direct transcription: nothing here looks anything up, and
// nothing here decides what a construct means.
//
// See docs/dev/MODCOMPILE_ST_FRONTEND.md §5.3.

// hasSTBody reports whether this component came from an .st source.
// Package.Validate guarantees a component never carries both an ST body and
// verbatim C, so this is also the switch that picks the body emission path.
func (g *generator) hasSTBody() bool {
	for i := range g.comp.Functions {
		if g.comp.Functions[i].Body != nil {
			return true
		}
	}
	return false
}

// stFeatures records what a component's bodies need, so headers, helpers and
// instance members are emitted only where they are used.  A component that
// never divides should not carry a division guard.
type stFeatures struct {
	pow       bool // uses **, so needs math.h
	boundLoop bool // WHILE or REPEAT, so needs the iteration counter

	// clampIndex records that some subscript is not knowable at compile time
	// and so needs the range helper.  It is discovered during emission rather
	// than by the pre-scan, because whether a given index folds is exactly the
	// question the emitter answers.
	clampIndex bool

	// guards records which division helpers are reached, keyed by the name
	// suffix ("div_s", "mod_u", ...).  The set has to be exact: the build
	// runs with -Werror=unused-function, so emitting a helper nothing calls
	// fails the compile.
	guards map[string]bool
}

// needsGuards reports whether any division guard is emitted.
func (f stFeatures) needsGuards() bool { return len(f.guards) > 0 }

// allBodies is every lowered body in the package: the exported HAL functions,
// the function block bodies and the pure functions.  A guard used only inside
// a block still has to be emitted, so every scan walks all of them.
func (g *generator) allBodies() [][]ast.Stmt {
	out := make([][]ast.Stmt, 0, len(g.comp.Functions)+len(g.pkg.Blocks)+len(g.pkg.Functions))
	for i := range g.comp.Functions {
		out = append(out, g.comp.Functions[i].Body)
	}
	for i := range g.pkg.Blocks {
		out = append(out, g.pkg.Blocks[i].Body)
	}
	for i := range g.pkg.Functions {
		out = append(out, g.pkg.Functions[i].Body)
	}
	return out
}

func (g *generator) stFeatures() stFeatures {
	f := stFeatures{guards: make(map[string]bool)}
	for _, body := range g.allBodies() {
		walkStmts(body, func(s ast.Stmt) {
			switch s.(type) {
			case *ast.WhileStmt, *ast.RepeatStmt:
				f.boundLoop = true
			}
		}, func(e ast.Expr) {
			b, ok := e.(*ast.BinaryExpr)
			if !ok {
				return
			}
			if b.Op == ast.BinPow {
				f.pow = true
			}
			if name, guarded := guardName(b); guarded {
				f.guards[name] = true
			}
		})
	}
	return f
}

// guardName is the helper a binary operator needs, if any.  It is the single
// definition of that mapping: the feature scan and the emitter both ask here,
// so the set emitted and the set called cannot drift apart.
func guardName(b *ast.BinaryExpr) (string, bool) {
	if !b.Op.NeedsZeroGuard() || !b.Typ.IsInteger() {
		return "", false
	}
	op := "div"
	if b.Op == ast.BinMod {
		op = "mod"
	}
	sign := "s"
	if !b.Typ.IsSigned() {
		sign = "u"
	}
	return op + "_" + sign, true
}

// needsFaultCounter reports whether any guard can fire, and so whether inst_t
// needs somewhere to count faults.
func (g *generator) needsFaultCounter() bool {
	f := g.stFeatures()
	return f.needsGuards() || f.boundLoop || g.anyRuntimeIndex()
}

// anyRuntimeIndex reports whether some subscript is not a compile-time
// constant, and so will be clamped.
func (g *generator) anyRuntimeIndex() bool {
	found := false
	for _, body := range g.allBodies() {
		walkStmts(body, func(ast.Stmt) {}, func(e ast.Expr) {
			ix, ok := e.(*ast.IndexExpr)
			if !ok {
				return
			}
			for _, sub := range ix.Indices {
				if _, isConst := constInt(sub); !isConst {
					found = true
				}
			}
		})
	}
	return found
}

// walkStmts calls onStmt for every statement and onExpr for every expression
// beneath list, in no particular order.
func walkStmts(list []ast.Stmt, onStmt func(ast.Stmt), onExpr func(ast.Expr)) {
	for _, s := range list {
		if s == nil {
			continue
		}
		onStmt(s)
		switch v := s.(type) {
		case *ast.AssignStmt:
			walkExpr(v.Target, onExpr)
			walkExpr(v.Value, onExpr)
		case *ast.IfStmt:
			walkExpr(v.Cond, onExpr)
			walkStmts(v.Then, onStmt, onExpr)
			for _, e := range v.ElseIf {
				walkExpr(e.Cond, onExpr)
				walkStmts(e.Body, onStmt, onExpr)
			}
			walkStmts(v.Else, onStmt, onExpr)
		case *ast.CaseStmt:
			walkExpr(v.Selector, onExpr)
			for _, cl := range v.Clauses {
				walkStmts(cl.Body, onStmt, onExpr)
			}
			walkStmts(v.Else, onStmt, onExpr)
		case *ast.ForStmt:
			walkExpr(v.Var, onExpr)
			walkExpr(v.From, onExpr)
			walkExpr(v.To, onExpr)
			walkExpr(v.By, onExpr)
			walkStmts(v.Body, onStmt, onExpr)
		case *ast.WhileStmt:
			walkExpr(v.Cond, onExpr)
			walkStmts(v.Body, onStmt, onExpr)
		case *ast.RepeatStmt:
			walkStmts(v.Body, onStmt, onExpr)
			walkExpr(v.Cond, onExpr)
		case *ast.InvokeStmt:
			walkExpr(v.Inst, onExpr)
			for _, a := range v.Args {
				walkExpr(a.Value, onExpr)
			}
		}
	}
}

func walkExpr(e ast.Expr, onExpr func(ast.Expr)) {
	if e == nil {
		return
	}
	onExpr(e)
	switch v := e.(type) {
	case *ast.BinaryExpr:
		walkExpr(v.X, onExpr)
		walkExpr(v.Y, onExpr)
	case *ast.UnaryExpr:
		walkExpr(v.X, onExpr)
	case *ast.ConvExpr:
		walkExpr(v.X, onExpr)
	case *ast.IndexExpr:
		walkExpr(v.Base, onExpr)
		for _, ix := range v.Indices {
			walkExpr(ix, onExpr)
		}
	case *ast.FieldExpr:
		walkExpr(v.Base, onExpr)
	case *ast.CallExpr:
		for _, a := range v.Args {
			walkExpr(a, onExpr)
		}
	}
}

// ---------------------------------------------------------------------------
// Runtime support
// ---------------------------------------------------------------------------

// faultLogEvery is how often a repeating fault is logged.  A clamped index or
// a zero divisor inside a 1 kHz thread would otherwise flood the journal and
// itself become the problem, so faults are always counted and only
// occasionally printed.  See docs/dev/MODCOMPILE_ST_FRONTEND.md §4.
const faultLogEvery = 1000

// emitSTRuntime emits the helpers the generated bodies call.  Only what this
// component actually needs is emitted, so a module that never divides carries
// no division guard.
func (g *generator) emitSTRuntime() {
	f := g.stFeatures()
	f.clampIndex = g.needClamp
	if !f.needsGuards() && !f.boundLoop && !f.clampIndex {
		return
	}

	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Structured Text runtime guards\n")
	g.printf(" *\n")
	g.printf(" * These run in the real-time thread, so a fault is counted and reported\n")
	g.printf(" * rather than raised: a component bug must not become a daemon bug, and a\n")
	g.printf(" * fault repeating every cycle must not flood the journal.\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	g.printf("#define __ST_FAULT_LOG_EVERY %d\n\n", faultLogEvery)
	g.printf("static void __st_fault(inst_t *__comp_inst, const char *what) STMAK_NONBLOCKING;\n")
	g.printf("static void __st_fault(inst_t *__comp_inst, const char *what) {\n")
	g.printf("    unsigned long __n = __comp_inst->__st_faults++;\n")
	g.printf("    if ((__n %% __ST_FAULT_LOG_EVERY) == 0) {\n")
	g.printf("        STMAK_LOG_ERR(\"%%s (occurrence %%lu)\", what, __n + 1UL);\n")
	g.printf("    }\n")
	g.printf("}\n\n")

	if f.clampIndex {
		// A runtime index is clamped rather than trusted.  The alternative is
		// an out-of-bounds access inside the real-time process, which corrupts
		// stmakd rather than the component that caused it -- a component bug
		// must not become a daemon bug.
		g.printf("static int __st_idx(inst_t *__comp_inst, int64_t i, int lo, int hi, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_idx(inst_t *__comp_inst, int64_t i, int lo, int hi, const char *what) {\n")
		g.printf("    if (i < lo) { __st_fault(__comp_inst, what); return 0; }\n")
		g.printf("    if (i > hi) { __st_fault(__comp_inst, what); return hi - lo; }\n")
		g.printf("    return (int)(i - lo);\n")
		g.printf("}\n\n")
	}

	// Integer division by zero is undefined behaviour and raises SIGFPE,
	// which kills the whole process rather than the component that caused it.
	// So does the one overflowing division, INT64_MIN / -1.  Both yield zero
	// and a recorded fault instead.
	//
	// Only the helpers actually called are emitted: the build runs with
	// -Werror=unused-function.
	for _, v := range []struct{ op, sign, typ string }{
		{"div", "s", "int64_t"},
		{"mod", "s", "int64_t"},
		{"div", "u", "uint64_t"},
		{"mod", "u", "uint64_t"},
	} {
		name := v.op + "_" + v.sign
		if !f.guards[name] {
			continue
		}
		signed := v.sign == "s"
		what := "integer division by zero"
		if v.op == "mod" {
			what = "MOD by zero"
		}

		g.printf("static %s __st_%s(inst_t *__comp_inst, %s a, %s b) STMAK_NONBLOCKING;\n",
			v.typ, name, v.typ, v.typ)
		g.printf("static %s __st_%s(inst_t *__comp_inst, %s a, %s b) {\n",
			v.typ, name, v.typ, v.typ)
		g.printf("    if (b == 0) { __st_fault(__comp_inst, \"%s\"); return 0; }\n", what)
		if signed && v.op == "div" {
			g.printf("    if (b == -1 && a == INT64_MIN) { __st_fault(__comp_inst, \"integer division overflow\"); return 0; }\n")
		}
		if signed && v.op == "mod" {
			// INT64_MIN %% -1 traps on x86 for the same reason the division
			// does; the mathematical answer is zero.
			g.printf("    if (b == -1) { return 0; }\n")
		}
		if v.op == "div" {
			g.printf("    return a / b;\n")
		} else {
			g.printf("    return a %% b;\n")
		}
		g.printf("}\n\n")
	}
}

// ---------------------------------------------------------------------------
// Function bodies
// ---------------------------------------------------------------------------

// stEmitter carries the state of emitting one function body.
type stEmitter struct {
	g *generator

	// self is the C name of the enclosing function block instance while a
	// block body is being emitted, empty elsewhere.  It is what a
	// StorageField reference resolves through.
	self string

	// lastLine is the .st line the most recent #line directive named, so a
	// run of statements on consecutive lines needs only one.
	lastLine int

	// loopSeq numbers the iteration counters, so nested bounded loops do not
	// collide.
	loopSeq int
}

// emitSTBodies emits every ST function body, in place of the verbatim C an
// .comp would have contributed.
func (g *generator) emitSTBodies() {
	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Structured Text bodies\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	for i := range g.comp.Functions {
		g.emitSTFunction(&g.comp.Functions[i])
	}
}

func (g *generator) emitSTFunction(fn *ast.Function) {
	e := &stEmitter{g: g}

	g.printf("FUNCTION(%s) {\n", fn.Name)

	// Pins are volatile shared memory, so reading one twice in a body could
	// see two different values.  Each pin is snapshotted into a local at
	// entry and written back at exit, which gives ST the cycle semantics a
	// PLC programmer expects and is faster besides.  An input's local is
	// const, so an assignment sema somehow let through fails to compile
	// rather than corrupting a signal.
	//
	// Only this function's own pins: a file may hold several PROGRAMs, each
	// exporting its own HAL function, and writing back another's outputs
	// would fight whichever thread that one runs in.
	pins := g.ownPinsOf(fn)
	if len(pins) > 0 || len(fn.PinBindings) > 0 {
		g.printf("    /* Snapshot pins: stable for the whole cycle. */\n")
		if anyArrayPin(pins) || anyArrayBinding(fn.PinBindings) {
			g.printf("    int __pi;\n")
		}
		for _, pin := range pins {
			if pin.ArraySize > 0 {
				// An array pin is copied element by element.  The loop is
				// bounded by a constant, so it costs a known handful of loads
				// and keeps array pins on the same footing as scalar ones.
				g.printf("    %s %s[%d];\n", halCType(pin.Type), pinLocal(pin.Name), pin.ArraySize)
				g.printf("    for (__pi = 0; __pi < %d; __pi++) %s[__pi] = (*(__comp_inst->hal->%s[__pi]));\n",
					pin.ArraySize, pinLocal(pin.Name), toC(pin.Name))
				continue
			}
			qual := ""
			if pin.Dir == ast.PinIn {
				qual = "const "
			}
			g.printf("    %s%s %s = (*(__comp_inst->hal->%s));\n",
				qual, halCType(pin.Type), pinLocal(pin.Name), toC(pin.Name))
		}
		g.printf("\n")
	}

	// A function block instance may publish pins of its own.  They are loaded
	// into the instance here and stored back at exit, which gives them the
	// same cycle semantics as the function's own pins while leaving the block
	// body reading a plain member.
	g.emitBindingLoads(fn)

	e.emitStmts(fn.Body, 1)

	if anyWritablePin(pins) || anyWritableBinding(fn.PinBindings) {
		g.printf("\n")
		e.lineBackToOutput()
		g.printf("    /* Write pins back. */\n")
		g.emitBindingStores(fn)
		for _, pin := range pins {
			if pin.Dir == ast.PinIn {
				continue
			}
			if pin.ArraySize > 0 {
				g.printf("    for (__pi = 0; __pi < %d; __pi++) (*(__comp_inst->hal->%s[__pi])) = %s[__pi];\n",
					pin.ArraySize, toC(pin.Name), pinLocal(pin.Name))
				continue
			}
			g.printf("    (*(__comp_inst->hal->%s)) = %s;\n", toC(pin.Name), pinLocal(pin.Name))
		}
	} else {
		e.lineBackToOutput()
	}

	g.printf("}\n\n")
}

// ownPinsOf is the pins one ST function snapshots into locals: the ones its
// own PROGRAM declared, not those a nested instance publishes -- those are
// loaded straight into the instance instead.
//
// A .comp names none, because its single body shares the whole component's
// pins; falling back to all pins for that case keeps one code path for both.
func (g *generator) ownPinsOf(fn *ast.Function) []ast.Pin {
	if len(fn.Pins) == 0 {
		return g.comp.Pins
	}
	bound := make(map[string]bool, len(fn.PinBindings))
	for _, b := range fn.PinBindings {
		bound[b.Pin] = true
	}
	out := make([]ast.Pin, 0, len(fn.Pins))
	for _, name := range fn.Pins {
		if bound[name] {
			continue
		}
		for _, p := range g.comp.Pins {
			if p.Name == name {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// emitBindingLoads copies every readable published pin into its instance.
func (g *generator) emitBindingLoads(fn *ast.Function) {
	for _, b := range fn.PinBindings {
		if b.Dir == ast.PinOut {
			continue
		}
		g.emitBindingCopy(b, true)
	}
}

// emitBindingStores copies every writable published pin back out of its
// instance.
func (g *generator) emitBindingStores(fn *ast.Function) {
	for _, b := range fn.PinBindings {
		if b.Dir == ast.PinIn {
			continue
		}
		g.emitBindingCopy(b, false)
	}
}

// emitBindingCopy moves one published pin between HAL and its instance.
func (g *generator) emitBindingCopy(b ast.PinBinding, load bool) {
	member := toC(b.Pin)
	if b.ArrayOf > 0 {
		path := strings.Replace(b.Path, "[%d]", "[__pi]", 1)
		if load {
			g.printf("    for (__pi = 0; __pi < %d; __pi++) __comp_inst->%s = (*(__comp_inst->hal->%s[__pi]));\n",
				b.ArrayOf, path, member)
		} else {
			g.printf("    for (__pi = 0; __pi < %d; __pi++) (*(__comp_inst->hal->%s[__pi])) = __comp_inst->%s;\n",
				b.ArrayOf, member, path)
		}
		return
	}
	if load {
		g.printf("    __comp_inst->%s = (*(__comp_inst->hal->%s));\n", b.Path, member)
		return
	}
	g.printf("    (*(__comp_inst->hal->%s)) = __comp_inst->%s;\n", member, b.Path)
}

func anyArrayBinding(bs []ast.PinBinding) bool {
	for _, b := range bs {
		if b.ArrayOf > 0 {
			return true
		}
	}
	return false
}

func anyWritableBinding(bs []ast.PinBinding) bool {
	for _, b := range bs {
		if b.Dir != ast.PinIn {
			return true
		}
	}
	return false
}

func anyArrayPin(pins []ast.Pin) bool {
	for _, p := range pins {
		if p.ArraySize > 0 {
			return true
		}
	}
	return false
}

func anyWritablePin(pins []ast.Pin) bool {
	for _, p := range pins {
		if p.Dir != ast.PinIn {
			return true
		}
	}
	return false
}

// pinLocal is the name of a pin's per-cycle local.
func pinLocal(halName string) string { return "__pin_" + toC(halName) }

// halCType is the plain C type a pin's value has once loaded out of shared
// memory.  It deliberately drops the volatile of the HAL typedef: the whole
// point of the snapshot is that the local does not change underfoot.
func halCType(t ast.HALType) string {
	switch t {
	case ast.HALBit:
		return "bool"
	case ast.HALS32:
		return "int32_t"
	case ast.HALU32:
		return "uint32_t"
	case ast.HALFloat:
		return "double"
	}
	return "unsigned"
}

// ---------------------------------------------------------------------------
// #line mapping
// ---------------------------------------------------------------------------

// stFile is the .st path bodies came from, or "" when it is unknown and no
// mapping is possible.
func (g *generator) stFile() string { return g.comp.Pos.File }

// stLineDirectives reports whether the source can be mapped back.  It needs
// both the .st path and the output path, for the same reason the .comp path
// does: a half-mapped file reports generated code against source lines that do
// not exist, which is worse than not mapping at all.
func (g *generator) stLineDirectives() bool {
	return g.outName != "" && g.stFile() != ""
}

// lineTo names a .st line as the source of what follows.
func (e *stEmitter) lineTo(pos ast.Pos) {
	if !e.g.stLineDirectives() || pos.Line == 0 || pos.Line == e.lastLine {
		return
	}
	e.g.printf("#line %d %s\n", pos.Line, cStringLiteral(e.g.stFile()))
	e.lastLine = pos.Line
}

// lineBackToOutput returns attribution to the generated file, for the epilogue
// the author did not write.
func (e *stEmitter) lineBackToOutput() {
	if !e.g.stLineDirectives() || e.lastLine == 0 {
		return
	}
	// The directive occupies the next physical line, so the line after it is
	// g.line+2.  g.line is read before printf updates it.
	e.g.printf("#line %d %s\n", e.g.line+2, cStringLiteral(e.g.outName))
	e.lastLine = 0
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func indent(n int) string { return strings.Repeat("    ", n) }

func (e *stEmitter) emitStmts(list []ast.Stmt, depth int) {
	for _, s := range list {
		e.emitStmt(s, depth)
	}
}

func (e *stEmitter) emitStmt(s ast.Stmt, depth int) {
	g := e.g
	in := indent(depth)

	switch v := s.(type) {
	case *ast.AssignStmt:
		e.lineTo(v.Pos)
		g.printf("%s%s = %s;\n", in, e.expr(v.Target), e.expr(v.Value))

	case *ast.IfStmt:
		e.lineTo(v.Pos)
		g.printf("%sif (%s) {\n", in, e.expr(v.Cond))
		e.emitStmts(v.Then, depth+1)
		for _, arm := range v.ElseIf {
			e.lineTo(arm.Pos)
			g.printf("%s} else if (%s) {\n", in, e.expr(arm.Cond))
			e.emitStmts(arm.Body, depth+1)
		}
		if v.Else != nil {
			g.printf("%s} else {\n", in)
			e.emitStmts(v.Else, depth+1)
		}
		g.printf("%s}\n", in)

	case *ast.CaseStmt:
		e.emitCase(v, depth)

	case *ast.InvokeStmt:
		e.emitInvoke(v, depth)

	case *ast.ForStmt:
		e.emitFor(v, depth)

	case *ast.WhileStmt:
		e.emitWhile(v, depth)

	case *ast.RepeatStmt:
		e.emitRepeat(v, depth)

	case *ast.ExitStmt:
		e.lineTo(v.Pos)
		g.printf("%sbreak;\n", in)

	case *ast.ReturnStmt:
		e.lineTo(v.Pos)
		// A return leaves the body before the pins are written back, so the
		// outputs computed so far would be lost.  Jump to the epilogue
		// instead -- but phase 3 has no epilogue label yet, and sema only
		// admits RETURN at the end of a body, so a plain return is correct
		// for now.
		g.printf("%sreturn;\n", in)

	default:
		g.err = fmt.Errorf("cgen: no emission for statement %T", s)
	}
}

// emitCase emits a CASE.  A switch is the better shape for plain labels, but C
// has no portable range label, so an arm with a range turns the whole
// statement into an if/else chain rather than relying on a GNU extension.
func (e *stEmitter) emitCase(v *ast.CaseStmt, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	hasRange := false
	for _, cl := range v.Clauses {
		for _, l := range cl.Labels {
			if l.IsRange() {
				hasRange = true
			}
		}
	}

	sel := e.expr(v.Selector)

	if !hasRange {
		g.printf("%sswitch (%s) {\n", in, sel)
		for _, cl := range v.Clauses {
			for _, l := range cl.Labels {
				g.printf("%scase %s:\n", indent(depth+1), e.caseLabel(v, l.Lo))
			}
			e.emitStmts(cl.Body, depth+2)
			g.printf("%sbreak;\n", indent(depth+2))
		}
		if v.Else != nil {
			g.printf("%sdefault:\n", indent(depth+1))
			e.emitStmts(v.Else, depth+2)
			g.printf("%sbreak;\n", indent(depth+2))
		}
		g.printf("%s}\n", in)
		return
	}

	// Range form.  The selector is evaluated once into a temporary: it may be
	// a pin read or an expression with a division guard, and a chain would
	// otherwise evaluate it per arm.
	tmp := "__sel"
	g.printf("%s{\n", in)
	g.printf("%s%s %s = %s;\n", indent(depth+1), v.Selector.ExprType().CType(), tmp, sel)
	for i, cl := range v.Clauses {
		var conds []string
		for _, l := range cl.Labels {
			if l.IsRange() {
				conds = append(conds, fmt.Sprintf("(%s >= %s && %s <= %s)",
					tmp, e.caseLabel(v, l.Lo), tmp, e.caseLabel(v, l.Hi)))
			} else {
				conds = append(conds, fmt.Sprintf("(%s == %s)", tmp, e.caseLabel(v, l.Lo)))
			}
		}
		kw := "if"
		if i > 0 {
			kw = "} else if"
		}
		g.printf("%s%s (%s) {\n", indent(depth+1), kw, strings.Join(conds, " || "))
		e.emitStmts(cl.Body, depth+2)
	}
	if len(v.Clauses) > 0 {
		if v.Else != nil {
			g.printf("%s} else {\n", indent(depth+1))
			e.emitStmts(v.Else, depth+2)
		}
		g.printf("%s}\n", indent(depth+1))
	} else if v.Else != nil {
		e.emitStmts(v.Else, depth+1)
	}
	g.printf("%s}\n", in)
}

// caseLabel renders a label value.  Sema folded an enumeration constant to its
// number, but generated C that says `case 0:` where the source said `case
// IDLE:` is materially harder to read on a machine, so the name is recovered
// from the selector's type.
func (e *stEmitter) caseLabel(v *ast.CaseStmt, value int64) string {
	t := v.Selector.ExprType()
	if t.Kind == ast.TypeEnum {
		if td := e.g.pkg.LookupType(t.Ref); td != nil {
			for _, k := range td.Consts {
				if k.Value == value {
					return enumConstName(td.Name, k.Name)
				}
			}
		}
	}
	return strconv.FormatInt(value, 10)
}

// emitFor emits a counting loop.  ST's limit is inclusive, and the comparison
// direction follows the sign of the step.
func (e *stEmitter) emitFor(v *ast.ForStmt, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	ctl := e.expr(v.Var)
	from := e.expr(v.From)
	to := e.expr(v.To)

	step := "1"
	cmp := "<="
	if v.By != nil {
		step = e.expr(v.By)
		if n, ok := constInt(v.By); ok && n < 0 {
			cmp = ">="
		} else if !ok {
			// A step whose sign is unknown until run time needs both
			// comparisons; sema required an explicit bound for this shape, so
			// the loop cannot run away regardless.
			g.printf("%s{\n", in)
			g.printf("%s%s __step = %s;\n", indent(depth+1), v.Var.ExprType().CType(), step)
			g.printf("%sfor (%s = %s; __step > 0 ? %s <= %s : %s >= %s; %s += __step) {\n",
				indent(depth+1), ctl, from, ctl, to, ctl, to, ctl)
			e.emitStmts(v.Body, depth+2)
			g.printf("%s}\n", indent(depth+1))
			g.printf("%s}\n", in)
			return
		}
	}

	g.printf("%sfor (%s = %s; %s %s %s; %s += %s) {\n", in, ctl, from, ctl, cmp, to, ctl, step)
	e.emitStmts(v.Body, depth+1)
	g.printf("%s}\n", in)
}

// emitWhile emits a pre-tested loop with the iteration cap sema required.
func (e *stEmitter) emitWhile(v *ast.WhileStmt, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	e.loopSeq++
	counter := fmt.Sprintf("__iter%d", e.loopSeq)

	g.printf("%s{\n", in)
	g.printf("%sunsigned long %s = 0;\n", indent(depth+1), counter)
	g.printf("%swhile (%s) {\n", indent(depth+1), e.expr(v.Cond))
	e.emitBoundCheck(counter, v.MaxIter, "WHILE", depth+2)
	e.emitStmts(v.Body, depth+2)
	g.printf("%s}\n", indent(depth+1))
	g.printf("%s}\n", in)
}

// emitRepeat emits a post-tested loop.  ST repeats *until* its condition holds,
// which is the negation of C's do/while.
func (e *stEmitter) emitRepeat(v *ast.RepeatStmt, depth int) {
	g := e.g
	in := indent(depth)
	e.lineTo(v.Pos)

	e.loopSeq++
	counter := fmt.Sprintf("__iter%d", e.loopSeq)

	g.printf("%s{\n", in)
	g.printf("%sunsigned long %s = 0;\n", indent(depth+1), counter)
	g.printf("%sdo {\n", indent(depth+1))
	e.emitBoundCheck(counter, v.MaxIter, "REPEAT", depth+2)
	e.emitStmts(v.Body, depth+2)
	g.printf("%s} while (!(%s));\n", indent(depth+1), e.expr(v.Cond))
	g.printf("%s}\n", in)
}

// emitBoundCheck is the cap that makes an unbounded loop bounded.  Exceeding it
// leaves the loop and records a fault; stalling the servo thread until the
// watchdog fires is the alternative.
func (e *stEmitter) emitBoundCheck(counter string, max int, kw string, depth int) {
	in := indent(depth)
	e.g.printf("%sif (++%s > %dUL) { __st_fault(__comp_inst, \"%s loop exceeded its %d iteration bound\"); break; }\n",
		in, counter, max, kw, max)
}

// constInt folds a constant integer, for the emission choices that depend on
// one (a loop's direction).  Sema has already folded what it needed; this is
// the narrower question of whether the C emitter can see a value here.
func constInt(e ast.Expr) (int64, bool) {
	switch v := e.(type) {
	case *ast.IntLit:
		return v.Value, true
	case *ast.ConvExpr:
		return constInt(v.X)
	case *ast.UnaryExpr:
		if n, ok := constInt(v.X); ok && v.Op == ast.UnNeg {
			return -n, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// expr renders one expression as C.  Everything is parenthesised: the ST
// precedence was settled by the parser and recorded in the tree, so re-deriving
// it through C's precedence would be a second chance to get it wrong.
func (e *stEmitter) expr(x ast.Expr) string {
	switch v := x.(type) {
	case nil:
		return ""

	case *ast.IntLit:
		return intLiteral(v.Value, v.Typ)

	case *ast.RealLit:
		return realLiteral(v.Value, v.Typ)

	case *ast.BoolLit:
		if v.Value {
			return "true"
		}
		return "false"

	case *ast.StringLit:
		return cStringLiteral(v.Value)

	case *ast.EnumLit:
		return enumConstName(v.Enum, v.Const)

	case *ast.VarRef:
		return e.varRef(v)

	case *ast.IndexExpr:
		return e.index(v)

	case *ast.FieldExpr:
		return fmt.Sprintf("%s.%s", e.expr(v.Base), toC(v.Field))

	case *ast.ConvExpr:
		return fmt.Sprintf("((%s)%s)", v.Typ.CType(), e.expr(v.X))

	case *ast.CallExpr:
		args := "__comp_inst"
		for _, a := range v.Args {
			args += ", " + e.expr(a)
		}
		return fmt.Sprintf("%s(%s)", fnName(v.Func), args)

	case *ast.UnaryExpr:
		return fmt.Sprintf("(%s%s)", v.Op.C(), e.expr(v.X))

	case *ast.BinaryExpr:
		return e.binary(v)
	}

	e.g.err = fmt.Errorf("cgen: no emission for expression %T", x)
	return "/* unsupported */"
}

func (e *stEmitter) varRef(v *ast.VarRef) string {
	switch v.Store {
	case ast.StoragePin:
		return pinLocal(v.Name)
	case ast.StorageParam:
		return fmt.Sprintf("(__comp_inst->hal->%s)", toC(v.Name))
	case ast.StorageVar, ast.StorageGlobal:
		return fmt.Sprintf("(__comp_inst->%s)", toC(v.Name))
	case ast.StorageField:
		if e.self == "" {
			e.g.err = fmt.Errorf("cgen: %s is a function block member but there is no instance in scope", v.Name)
			return v.Name
		}
		return fmt.Sprintf("(%s->%s)", e.self, toC(v.Name))
	case ast.StorageLocal:
		return toC(v.Name)
	}
	e.g.err = fmt.Errorf("cgen: unknown storage class %v for %s", v.Store, v.Name)
	return v.Name
}

// index emits an array subscript.
//
// The declared lower bound is subtracted at every site, so ARRAY[1..8] is a C
// array of eight starting at zero.  An index the compiler could see was
// checked by sema; one it could not is clamped here, because an out-of-range
// write inside the real-time process corrupts stmakd rather than the component
// that caused it.  See docs/dev/MODCOMPILE_ST_FRONTEND.md §4.
func (e *stEmitter) index(v *ast.IndexExpr) string {
	arrType := v.Base.ExprType()
	var b strings.Builder
	b.WriteString(e.expr(v.Base))

	for i, ix := range v.Indices {
		if i >= len(arrType.Dims) {
			break
		}
		d := arrType.Dims[i]
		raw := e.expr(ix)

		if n, ok := constInt(ix); ok {
			// Sema proved this one is in range, so it needs no check and no
			// arithmetic: emit the offset directly.
			fmt.Fprintf(&b, "[%d]", n-int64(d.Lo))
			continue
		}
		e.g.needClamp = true
		fmt.Fprintf(&b, "[__st_idx(__comp_inst, (int64_t)%s, %d, %d, \"%s\")]",
			raw, d.Lo, d.Hi, cEscape(indexWhat(v.Base, i)))
	}
	return b.String()
}

// indexWhat names an index site for the fault message, so a clamped write says
// which array and which dimension rather than only that one happened.
func indexWhat(base ast.Expr, dim int) string {
	name := "array"
	if ref, ok := base.(*ast.VarRef); ok {
		name = ref.Name
	}
	if base.ExprType().Kind == ast.TypeArray && len(base.ExprType().Dims) > 1 {
		return fmt.Sprintf("%s dimension %d", name, dim+1)
	}
	return name
}

// cEscape makes a Go string safe inside a C string literal.  The names here
// come from ST identifiers so they are already tame, but a generated literal
// should never depend on that.
func cEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}

func (e *stEmitter) binary(v *ast.BinaryExpr) string {
	x, y := e.expr(v.X), e.expr(v.Y)

	// Exponentiation has no C operator and goes through the C library, which
	// works in doubles -- sema has already given both operands LREAL.
	if v.Op == ast.BinPow {
		return fmt.Sprintf("pow(%s, %s)", x, y)
	}

	// Integer division and MOD go through a guard: an unguarded zero divisor
	// raises SIGFPE, which kills the whole process rather than the component
	// that caused it.  Floating point is exempt -- IEEE infinity is a defined
	// result, not a fault.
	if name, guarded := guardName(v); guarded {
		cast := "int64_t"
		if !v.Typ.IsSigned() {
			cast = "uint64_t"
		}
		return fmt.Sprintf("((%s)__st_%s(__comp_inst, (%s)%s, (%s)%s))",
			v.Typ.CType(), name, cast, x, cast, y)
	}

	op := v.Op.C()
	if op == "" {
		e.g.err = fmt.Errorf("cgen: no C operator for %s", v.Op)
		return "/* unsupported */"
	}

	// XOR on booleans is inequality, which is correct only because both
	// operands are already normalised to 0 or 1 -- which they are, being
	// bool.
	return fmt.Sprintf("(%s %s %s)", x, op, y)
}

// intLiteral renders an integer with the suffix its type needs, so a value is
// never silently narrowed by C's own literal typing.
func intLiteral(v int64, t ast.Type) string {
	s := strconv.FormatInt(v, 10)
	switch {
	case t.Bits() == 64 && t.IsSigned():
		return s + "LL"
	case t.Bits() == 64:
		return s + "ULL"
	case !t.IsSigned() && t.IsInteger():
		return s + "u"
	}
	return s
}

// realLiteral renders a float, keeping a decimal point so C reads it as one,
// and marking a REAL so it is not silently promoted to double.
func realLiteral(v float64, t ast.Type) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	if t.Kind == ast.TypeReal {
		s += "f"
	}
	return s
}

// cDims renders array extents as C declarator brackets: [2][3].
func cDims(dims []int) string {
	var b strings.Builder
	for _, d := range dims {
		fmt.Fprintf(&b, "[%d]", d)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// User-defined types
// ---------------------------------------------------------------------------

// emitSTTypedefs emits the enumeration constants and struct typedefs an ST
// component declared.  Sema has already ordered them so each is declared
// before its first use, which is what C requires.
func (g *generator) emitSTTypedefs() {
	if len(g.pkg.Types) == 0 {
		return
	}
	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Structured Text types\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	for i := range g.pkg.Types {
		td := &g.pkg.Types[i]
		switch td.Kind {
		case ast.DefEnum:
			// Constants become macros named <ENUM>_<CONST>, which keeps two
			// enumerations from colliding in C even though sema already
			// refuses that in ST.
			g.printf("/* enumeration %s */\n", td.Name)
			for _, k := range td.Consts {
				g.printf("#define %s %d\n", enumConstName(td.Name, k.Name), k.Value)
			}
			g.printf("\n")

		case ast.DefStruct:
			g.printf("typedef struct {\n")
			for _, f := range td.Fields {
				g.printf("    %s %s%s;\n", f.Typ.CType(), toC(f.Name), cTypeDims(f.Typ))
			}
			g.printf("} %s_t;\n\n", td.Name)

		case ast.DefAlias:
			// An alias resolves away in sema; nothing is emitted for it.  The
			// name survives only in diagnostics.
		}
	}
}

// enumConstName is the C spelling of an enumeration constant.
func enumConstName(enum, konst string) string {
	return strings.ToUpper(toC(enum)) + "_" + strings.ToUpper(toC(konst))
}

// cTypeDims renders the declarator brackets an array type needs.
func cTypeDims(t ast.Type) string {
	if t.Kind != ast.TypeArray {
		return ""
	}
	var b strings.Builder
	for _, d := range t.Dims {
		fmt.Fprintf(&b, "[%d]", d.Count())
	}
	return b.String()
}
