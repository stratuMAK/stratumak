// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// Error is one positioned diagnostic.
type Error struct {
	Pos ast.Pos
	Msg string
}

func (e Error) Error() string { return e.Pos.String() + ": " + e.Msg }

// ErrorList is the set of diagnostics from one check, in source order.
type ErrorList []Error

func (l ErrorList) Error() string {
	switch len(l) {
	case 0:
		return "no errors"
	case 1:
		return l[0].Error()
	}
	var b strings.Builder
	for i, e := range l {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// Check resolves file into a package for the backend, where component is the
// component's name (the caller derives it from the filename, as .comp does).
//
// It reports as many problems as it can rather than stopping at the first: a
// component with three type errors should take one compile to find them.
func Check(component string, file *st.File) (*ast.Package, error) {
	c := &checker{
		component: component,
		file:      file,
		pkg:       &ast.Package{},
	}
	c.run()
	if len(c.errs) > 0 {
		sort.SliceStable(c.errs, func(i, j int) bool {
			if c.errs[i].Pos.Line != c.errs[j].Pos.Line {
				return c.errs[i].Pos.Line < c.errs[j].Pos.Line
			}
			return c.errs[i].Pos.Col < c.errs[j].Pos.Col
		})
		return nil, c.errs
	}
	return c.pkg, nil
}

type checker struct {
	component string
	file      *st.File
	pkg       *ast.Package

	errs ErrorList

	// scope is the innermost open scope while checking a body.
	scope *scope

	// loopDepth is how many loops enclose the statement being checked, so
	// EXIT and CONTINUE can be refused outside one.
	loopDepth int

	// result names the implicit result variable of the FUNCTION being
	// checked, empty elsewhere.  Assigning to it is how ST returns a value.
	result string

	// instanceNames are the names already taken in the component's single
	// instance struct, across every PROGRAM and the globals.
	instanceNames map[string]ast.Pos
}

func (c *checker) errorf(pos ast.Pos, format string, args ...interface{}) {
	c.errs = append(c.errs, Error{Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

func (c *checker) warnf(format string, args ...interface{}) {
	c.pkg.Warnings = append(c.pkg.Warnings, fmt.Sprintf(format, args...))
}

// push opens a nested scope; pop closes it.
func (c *checker) push() { c.scope = newScope(c.scope) }
func (c *checker) pop()  { c.scope = c.scope.parent }

// ---------------------------------------------------------------------------
// Top level
// ---------------------------------------------------------------------------

func (c *checker) run() {
	c.pkg.Component.Name = c.component
	c.checkFileAttributes()

	// Types live in a scope outside the program's, so a program-level name
	// may shadow an enumeration constant without a redeclaration error, and
	// so the type scope survives for every program in phase 6.
	c.push()
	defer c.pop()
	c.checkTypeDecls(c.file.Types)
	c.checkPOUs()

	// Globals sit in a scope of their own, between the types and each
	// program.  That is what lets several programs share state while keeping
	// each program's own declarations private to it.
	c.push()
	defer c.pop()
	c.declareGlobals()

	if len(c.file.Programs) == 0 {
		c.errorf(c.file.Pos, "no PROGRAM declared; a component needs at least one, "+
			"and each PROGRAM becomes one HAL function")
		return
	}

	seen := make(map[string]ast.Pos)
	for _, p := range c.file.Programs {
		if prev, dup := seen[key(p.Name)]; dup {
			c.errorf(p.Pos, "PROGRAM %s is already declared at %s", p.Name, prev)
			continue
		}
		seen[key(p.Name)] = p.Pos
		c.checkProgram(p)
	}
}

// declareGlobals resolves VAR_GLOBAL, which becomes per-instance state shared
// by every program in the file.  It lands in inst_t like any other variable;
// what makes it global is only that it is visible from more than one program.
func (c *checker) declareGlobals() {
	for _, b := range c.file.Globals {
		if b.Retain || b.NonRetain {
			c.warnf("%s: RETAIN and NON_RETAIN have no effect; stratuMAK has no retentive memory", b.Pos)
		}
		for _, d := range b.Decls {
			if d.Located != "" {
				c.errorf(d.Pos, "located variables (AT %s) are not supported; "+
					"stratuMAK maps I/O through VAR_INPUT and VAR_OUTPUT onto HAL pins", d.Located)
				continue
			}
			typ, ok := c.resolveType(d.Type)
			if !ok {
				continue
			}
			for _, name := range d.Names {
				c.declareLocal(d, name, typ, ast.StorageGlobal, b.Constant)
			}
		}
	}
}

// rejectUnsupported reports the declarations that parse but that phase 2 does
// not lower, each naming the phase that brings it.  Saying which feature is
// missing beats a resolution failure at its first use, which is what a reader
// would otherwise see.
// componentAttributes maps a file-level attribute to the documentation field
// it fills.  docgen turns these into the component's man page.
var componentAttributes = map[string]func(*ast.Component, string){
	"summary":     func(c *ast.Component, v string) { c.Summary = v },
	"description": func(c *ast.Component, v string) { c.Description = v },
	"license":     func(c *ast.Component, v string) { c.License = v },
	"author":      func(c *ast.Component, v string) { c.Author = v },
	"see_also":    func(c *ast.Component, v string) { c.SeeAlso = v },
	"notes":       func(c *ast.Component, v string) { c.Notes = v },
	"examples":    func(c *ast.Component, v string) { c.Examples = v },
}

func (c *checker) checkFileAttributes() {
	seen := make(map[string]ast.Pos)
	for _, a := range c.file.Attrs {
		name := strings.ToLower(a.Name)
		if prev, dup := seen[name]; dup {
			c.errorf(a.Pos, "attribute %q is already set at %s", a.Name, prev)
			continue
		}
		seen[name] = a.Pos

		set, ok := componentAttributes[name]
		if !ok {
			// An unknown attribute is a warning, not an error: a pragma
			// meant for another toolchain should not stop the build, but
			// silently ignoring one that was meant for us would hide a typo.
			c.warnf("%s: unknown component attribute %q, ignored", a.Pos, a.Name)
			continue
		}
		if a.Value == "" {
			c.errorf(a.Pos, "attribute %q needs a value, as in {attribute '%s' := 'text'}", a.Name, a.Name)
			continue
		}
		set(&c.pkg.Component, a.Value)
	}

	if c.pkg.Component.Summary == "" {
		c.warnf("component %s has no {attribute 'summary'}, so its man page will have no description",
			c.component)
	}
}

// ---------------------------------------------------------------------------
// PROGRAM
// ---------------------------------------------------------------------------

func (c *checker) checkProgram(p *st.ProgramDecl) {
	c.pkg.Component.Pos = p.Pos

	for _, a := range p.Attrs {
		c.warnf("%s: attribute %q on a PROGRAM has no meaning; "+
			"component metadata goes before the first declaration", a.Pos, a.Name)
	}

	c.push()
	defer c.pop()

	// fperiod is the HAL function's period in seconds.  It exists only in a
	// PROGRAM: a function block has no period of its own, and one that needs
	// the cycle time takes it as an input -- which is what keeps a block
	// reusable from a thread of any rate.
	c.scope.declare(&symbol{
		Name:  "fperiod",
		Kind:  symBuiltin,
		Type:  ast.Type{Kind: ast.TypeLReal},
		Store: ast.StorageLocal,
		Pos:   p.Pos,
		Const: true,
	})

	firstPin := len(c.pkg.Component.Pins)
	c.declareVarSections(p.Vars)

	// The pins this program declared, and only those: a function must not pay
	// to copy pins it never touches, and writing back another program's
	// outputs would fight whichever thread that one runs in.
	var pins []string
	for _, pin := range c.pkg.Component.Pins[firstPin:] {
		pins = append(pins, pin.Name)
	}

	body := c.checkStmts(p.Body)

	// One PROGRAM whose name matches the component exports as the component
	// itself; any other name becomes a namespaced function, which is what
	// cgen's emitFunctionExport already does for .comp.
	fnName := p.Name
	if ast.EqualName(p.Name, c.component) {
		fnName = "_"
	}

	c.pkg.Component.Functions = append(c.pkg.Component.Functions, ast.Function{
		Pos:  p.Pos,
		Name: fnName,
		FP:   c.bodyUsesFloat(body),
		Body: body,
		Pins: pins,
	})
}

// bodyUsesFloat reports whether one function's body touches floating point,
// which decides the fp flag it exports with.  A function that touches none can
// run in a thread without FPU save and restore.
//
// The question is asked of the body rather than of what is in scope: a
// component may declare an LREAL that only one of its programs uses, and
// several programs share one scope.  A call reaching a body that uses floating
// point counts too, since that arithmetic happens on this function's stack.
func (c *checker) bodyUsesFloat(body []ast.Stmt) bool {
	seen := make(map[string]bool)

	var check func(body []ast.Stmt) bool
	check = func(body []ast.Stmt) bool {
		found := false
		walkStmtExprs(body, func(e ast.Expr) {
			if e.ExprType().IsFloat() {
				found = true
			}
			if call, ok := e.(*ast.CallExpr); ok && !seen[key(call.Func)] {
				seen[key(call.Func)] = true
				if def := c.pkg.LookupFunction(call.Func); def != nil {
					if def.Result.IsFloat() || check(def.Body) {
						found = true
					}
				}
			}
		})
		walkBody(body, func(s ast.Stmt) {
			inv, ok := s.(*ast.InvokeStmt)
			if !ok {
				return
			}
			ref := inv.Inst.ExprType().Ref
			if seen[key(ref)] {
				return
			}
			seen[key(ref)] = true
			if def := c.pkg.LookupBlock(ref); def != nil && check(def.Body) {
				found = true
			}
		})
		return found
	}
	return check(body)
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (c *checker) declareVarSections(blocks []*st.VarBlock) {
	for _, b := range blocks {
		switch b.Kind {
		case st.VarExternal:
			c.errorf(b.Pos, "VAR_EXTERNAL is not needed; a VAR_GLOBAL is already visible "+
				"from every PROGRAM in the file")
			continue
		case st.VarGlobal:
			c.errorf(b.Pos, "VAR_GLOBAL belongs at file level, outside any PROGRAM, "+
				"since it is what several programs share")
			continue
		}
		if b.Retain || b.NonRetain {
			c.warnf("%s: RETAIN and NON_RETAIN have no effect; stratuMAK has no retentive memory", b.Pos)
		}
		for _, d := range b.Decls {
			c.declareVar(b, d)
		}
	}
}

func (c *checker) declareVar(b *st.VarBlock, d *st.VarDecl) {
	if d.Located != "" {
		c.errorf(d.Pos, "located variables (AT %s) are not supported; "+
			"stratuMAK maps I/O through VAR_INPUT and VAR_OUTPUT onto HAL pins", d.Located)
		return
	}
	if d.Edge != st.EdgeNone {
		c.errorf(d.Pos, "R_EDGE and F_EDGE are not supported; "+
			"use the R_TRIG and F_TRIG function blocks once they land")
		return
	}

	typ, ok := c.resolveType(d.Type)
	if !ok {
		return
	}

	store := ast.StorageVar
	if b.Kind.IsHAL() {
		store = ast.StoragePin
	}

	// One declaration may name several variables; each becomes its own
	// symbol, sharing the type and the initialiser.
	for _, name := range d.Names {
		if b.Kind.IsHAL() {
			c.declarePin(b, d, name, typ)
			continue
		}
		c.declareLocal(d, name, typ, store, b.Constant)
	}
}

func (c *checker) declareLocal(d *st.VarDecl, name string, typ ast.Type, store ast.Storage, constant bool) {
	sym := &symbol{
		Name:  name,
		Kind:  symVar,
		Type:  typ,
		Store: store,
		Pos:   d.Pos,
		Const: constant,
	}
	if !c.bind(sym) {
		return
	}
	if !c.claimInstanceName(name, d.Pos, "variable") {
		return
	}

	init := ""
	if d.Init != nil && (typ.Kind == ast.TypeArray || typ.Kind == ast.TypeStruct) {
		c.errorf(d.Init.Position(),
			"%s cannot be given an initial value in its declaration; "+
				"%s is an aggregate, so initialise its elements in the body", name, typ)
		return
	}
	if d.Init != nil {
		e := c.checkExpr(d.Init, typ)
		if e != nil {
			lit, ok := c.foldToC(e)
			if !ok {
				c.errorf(d.Init.Position(),
					"the initial value of %s must be a compile-time constant", name)
			}
			init = lit
			if v, isInt := foldInt(e); isInt {
				sym.ConstValue, sym.HasConstValue = v, true
			}
		}
	}

	v := ast.Variable{
		Pos:     d.Pos,
		CType:   typ.CType(),
		Name:    name,
		Default: init,
	}
	if typ.Kind == ast.TypeFB || (typ.Kind == ast.TypeArray && typ.Elem != nil && typ.Elem.Kind == ast.TypeFB) {
		// An instance is state; the backend emits its struct and its
		// per-instance initialiser, so nothing more is needed here beyond the
		// member itself.
		v.CType = instCType(typ)
	}
	if typ.Kind == ast.TypeArray {
		// Both fields are set: Array is the total so a reader of either sees
		// something true, and Dims keeps the shape so the generated C reads
		// like the declaration rather than a flattened block.
		v.Array = typ.Elements()
		for _, d := range typ.Dims {
			v.Dims = append(v.Dims, d.Count())
		}
	}
	c.pkg.Component.Variables = append(c.pkg.Component.Variables, v)
}

// declarePin turns one HAL-facing declaration into a pin.
func (c *checker) declarePin(b *st.VarBlock, d *st.VarDecl, name string, typ ast.Type) {
	halType, fit := typ.HALType()
	switch fit {
	case ast.HALFitNone:
		// A multi-dimensional array is worth its own message: it is not that
		// the element type cannot reach HAL, but that a pin name carries one
		// index.
		if typ.Kind == ast.TypeArray && len(typ.Dims) > 1 {
			c.errorf(d.Pos, "%s cannot be a HAL pin: a pin name carries one index, "+
				"so only a one-dimensional array maps to an array pin", name)
			return
		}
		c.errorf(d.Pos, "%s cannot be a HAL pin: %s has no HAL representation. "+
			"Move it to a VAR section, or use one of BOOL, SINT, INT, DINT, USINT, UINT, UDINT, REAL or LREAL",
			name, typ)
		return
	case ast.HALFitLossy:
		// REAL reaches the pin losslessly but not back, because HAL float is
		// a double.  Accepted, but never silently.
		c.warnf("%s: pin %s is REAL; HAL carries it as a double, so a value written by HAL "+
			"loses precision when read back. Use LREAL for an exact pin", d.Pos, name)
	}

	dir := ast.PinIn
	switch b.Kind {
	case st.VarOutput:
		dir = ast.PinOut
	case st.VarInOut:
		dir = ast.PinIO
	}

	if !c.claimInstanceName(name, d.Pos, "pin") {
		return
	}
	sym := &symbol{
		Name:  name,
		Kind:  symVar,
		Type:  typ,
		Store: ast.StoragePin,
		Pos:   d.Pos,
		// An input pin is driven by whatever HAL signal is netted to it.
		// Assigning one in the body would give it a second writer, which is
		// exactly the conflict that kept function block I/O off HAL.
		Const: b.Kind == st.VarInput,
	}
	if !c.bind(sym) {
		return
	}

	def := ""
	if d.Init != nil && typ.Kind == ast.TypeArray {
		c.errorf(d.Init.Position(),
			"array pin %s cannot be given a default in its declaration; "+
				"set its elements in the body", name)
		return
	}
	if d.Init != nil {
		if b.Kind == st.VarOutput {
			c.warnf("%s: initial value on output pin %s is applied once at load; "+
				"an output is normally computed each cycle", d.Pos, name)
		}
		e := c.checkExpr(d.Init, typ)
		if e != nil {
			lit, ok := c.foldToC(e)
			if !ok {
				c.errorf(d.Init.Position(),
					"the default value of pin %s must be a compile-time constant", name)
			}
			def = lit
		}
	}

	pin := ast.Pin{
		Pos:     d.Pos,
		Name:    name,
		Type:    halType,
		Dir:     dir,
		Default: def,
		Doc:     attrValue(d.Attrs, "doc"),
	}
	if typ.Kind == ast.TypeArray {
		pin.ArraySize = typ.Dims[0].Count()
		// HAL names an array pin's elements by index, and cgen builds that
		// name from a run of '#' in the pin name -- one per digit.  An .st
		// declaration carries no such marker, so append one wide enough for
		// the largest index: ARRAY[0..7] becomes comp.channel.0 .. .7, and
		// ARRAY[0..99] becomes comp.channel.00 .. .99, so the names sort.
		pin.Name = name + "." + strings.Repeat("#", digitsFor(pin.ArraySize-1))
		if typ.Dims[0].Lo != 0 {
			// A HAL array pin is named comp.pin.0 .. comp.pin.N-1, so a
			// declared lower bound other than zero would make the ST index and
			// the pin name disagree -- `ch[1]` would be `comp.ch.0`.  Refuse
			// rather than let the two drift apart.
			c.errorf(d.Pos, "array pin %s must start at index 0, not %d: "+
				"HAL names an array pin's elements %s.0 upwards", name, typ.Dims[0].Lo, name)
			return
		}
	}
	c.pkg.Component.Pins = append(c.pkg.Component.Pins, pin)

	for _, a := range d.Attrs {
		if !strings.EqualFold(a.Name, "doc") {
			c.warnf("%s: unknown attribute %q on %s, ignored", a.Pos, a.Name, name)
		}
	}
}

// claimInstanceName reserves a name in the component's single instance struct.
//
// A file's PROGRAMs are separate scopes but share one inst_t and one HAL
// component, so two of them cannot both declare `n` or both export a pin
// called `ready`.  Strict IEC would make a program's VAR private to it; here
// the collision is refused instead, because a name that silently means
// different storage in different programs is worse than a rename -- and
// VAR_GLOBAL already exists for what is genuinely shared.
func (c *checker) claimInstanceName(name string, pos ast.Pos, what string) bool {
	if c.instanceNames == nil {
		c.instanceNames = make(map[string]ast.Pos)
	}
	if prev, taken := c.instanceNames[key(name)]; taken {
		c.errorf(pos, "%s %s is already declared at %s; the PROGRAMs of one file share a single "+
			"instance, so give it another name or move it to VAR_GLOBAL", what, name, prev)
		return false
	}
	c.instanceNames[key(name)] = pos
	return true
}

// bind declares sym in the current scope, reporting a redeclaration.
func (c *checker) bind(sym *symbol) bool {
	if prev := c.scope.lookupLocal(sym.Name); prev != nil {
		c.errorf(sym.Pos, "%s is already declared at %s", sym.Name, prev.Pos)
		return false
	}

	// Shadowing an enumeration constant is refused rather than allowed.  ST is
	// case-insensitive, so a variable `running` and a constant `RUNNING` are
	// one name: the declaration would silently change what every later
	// mention means, and `state := RUNNING` would assign a BOOL.  A shadowed
	// ordinary variable is fine -- it is only the constant that becomes
	// unreachable and unmentionable.
	if prev := c.scope.lookup(sym.Name); prev != nil && prev.Kind == symEnumConst {
		c.errorf(sym.Pos, "%s would shadow the constant %s of enumeration %s declared at %s; "+
			"ST identifiers are case-insensitive, so the two are one name",
			sym.Name, prev.Name, prev.EnumType, prev.Pos)
		return false
	}

	c.scope.declare(sym)
	return true
}

// digitsFor is how many decimal digits n needs, at least one.
func digitsFor(n int) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

// instCType is the C type of a function block instance, or of an array of
// them.
func instCType(t ast.Type) string {
	for t.Kind == ast.TypeArray && t.Elem != nil {
		t = *t.Elem
	}
	return t.CType()
}

func attrValue(attrs []st.Attribute, name string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Type references
// ---------------------------------------------------------------------------

// resolveType turns a written type reference into a resolved type.
func (c *checker) resolveType(ref *st.TypeRef) (ast.Type, bool) {
	if ref == nil {
		return ast.Type{}, false
	}

	if ref.IsArray() {
		return c.resolveArrayType(ref)
	}
	if ref.Subrange != nil {
		c.errorf(ref.Pos, "subrange types are not supported yet; declare %s without the range", ref.Name)
		return ast.Type{}, false
	}

	upper := strings.ToUpper(ref.Name)

	if upper == "STRING" {
		t := ast.Type{Kind: ast.TypeString}
		if ref.StringLen != nil {
			e := c.checkExpr(ref.StringLen, ast.Type{Kind: ast.TypeDInt})
			n, ok := foldInt(e)
			if !ok || n <= 0 {
				c.errorf(ref.Pos, "STRING length must be a positive compile-time constant")
				return ast.Type{}, false
			}
			t.Len = int(n)
		}
		return t, true
	}
	if ref.StringLen != nil {
		c.errorf(ref.Pos, "%s takes no length; only STRING is written STRING[n]", ref.Name)
		return ast.Type{}, false
	}

	if kind, ok := lookupElementary(ref.Name); ok {
		return ast.Type{Kind: kind}, true
	}
	if why, ok := deferredTypes[upper]; ok {
		c.errorf(ref.Pos, "%s", why)
		return ast.Type{}, false
	}

	// A user-defined type.  An alias resolves to its target, carrying its own
	// name along for diagnostics; an enumeration or struct resolves to itself.
	if sym := c.scope.lookup(ref.Name); sym != nil {
		if sym.Kind == symBlock {
			// A function block used as a type declares an instance of it.
			return ast.Type{Kind: ast.TypeFB, Ref: sym.Name}, true
		}
		if sym.Kind != symType {
			c.errorf(ref.Pos, "%s is a %s, not a type", ref.Name, sym.Kind)
			return ast.Type{}, false
		}
		td := c.pkg.LookupType(ref.Name)
		if td == nil {
			return ast.Type{}, false
		}
		switch td.Kind {
		case ast.DefAlias:
			return td.Alias, true
		case ast.DefEnum:
			return ast.Type{Kind: ast.TypeEnum, Ref: td.Name}, true
		case ast.DefStruct:
			return ast.Type{Kind: ast.TypeStruct, Ref: td.Name}, true
		}
	}

	c.errorf(ref.Pos, "unknown type %s", ref.Name)
	return ast.Type{}, false
}

// arrayDimLimit caps how many elements one array may hold.  It is a
// plausibility limit, not a correctness one: an array of ten million doubles
// inside an instance struct is a mistake worth catching at compile time rather
// than at the allocation that fails on a machine.
const arrayDimLimit = 1 << 20

// resolveArrayType resolves ARRAY[lo..hi, ...] OF elem.
func (c *checker) resolveArrayType(ref *st.TypeRef) (ast.Type, bool) {
	elem, ok := c.resolveType(ref.Elem)
	if !ok {
		return ast.Type{}, false
	}
	if elem.Kind == ast.TypeArray {
		// `ARRAY[..] OF ARRAY[..] OF T` is the same shape as a
		// multi-dimensional array; flattening it here keeps one
		// representation, so indexing and emission have one case to handle.
		dims := make([]ast.ArrayDim, 0, len(ref.Dims)+len(elem.Dims))
		outer, dimsOK := c.resolveDims(ref)
		if !dimsOK {
			return ast.Type{}, false
		}
		dims = append(dims, outer...)
		dims = append(dims, elem.Dims...)
		return c.finishArray(ref, dims, *elem.Elem)
	}

	dims, ok := c.resolveDims(ref)
	if !ok {
		return ast.Type{}, false
	}
	return c.finishArray(ref, dims, elem)
}

func (c *checker) finishArray(ref *st.TypeRef, dims []ast.ArrayDim, elem ast.Type) (ast.Type, bool) {
	t := ast.Type{Kind: ast.TypeArray, Elem: &elem, Dims: dims}
	if n := t.Elements(); n > arrayDimLimit {
		c.errorf(ref.Pos, "array of %d elements is too large (the limit is %d)", n, arrayDimLimit)
		return ast.Type{}, false
	}
	return t, true
}

// resolveDims folds the bounds of each dimension.
func (c *checker) resolveDims(ref *st.TypeRef) ([]ast.ArrayDim, bool) {
	dims := make([]ast.ArrayDim, 0, len(ref.Dims))
	for _, d := range ref.Dims {
		lo, okLo := c.constOf(d.Lo, ast.Type{Kind: ast.TypeDInt})
		hi, okHi := c.constOf(d.Hi, ast.Type{Kind: ast.TypeDInt})
		if !okLo || !okHi {
			c.errorf(d.Pos, "array bounds must be compile-time constants")
			return nil, false
		}
		if hi < lo {
			c.errorf(d.Pos, "array bound %d..%d is empty; the bounds are the wrong way round", lo, hi)
			return nil, false
		}
		dims = append(dims, ast.ArrayDim{Lo: int(lo), Hi: int(hi)})
	}
	return dims, true
}
