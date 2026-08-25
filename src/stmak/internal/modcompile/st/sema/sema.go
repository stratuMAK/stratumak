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
	c.rejectUnsupported()

	switch len(c.file.Programs) {
	case 0:
		pos := c.file.Pos
		c.errorf(pos, "no PROGRAM declared; a component needs at least one, "+
			"and each PROGRAM becomes one HAL function")
		return
	case 1:
	default:
		// Several PROGRAMs mean several exported HAL functions sharing
		// VAR_GLOBAL state.  That is the design, but the pieces it needs --
		// a shared scope and per-function pin sets -- come with phase 6.
		c.errorf(c.file.Programs[1].Pos,
			"a file with more than one PROGRAM is not supported yet; "+
				"one PROGRAM per exported HAL function arrives with phase 6")
		return
	}

	c.checkProgram(c.file.Programs[0])
}

// rejectUnsupported reports the declarations that parse but that phase 2 does
// not lower, each naming the phase that brings it.  Saying which feature is
// missing beats a resolution failure at its first use, which is what a reader
// would otherwise see.
func (c *checker) rejectUnsupported() {
	for i := range c.file.Types {
		c.errorf(c.file.Types[i].Pos,
			"TYPE declarations are not supported yet; user-defined types arrive with phase 4")
	}
	for _, b := range c.file.Blocks {
		c.errorf(b.Pos,
			"FUNCTION_BLOCK %s: function blocks are not supported yet; they arrive with phase 5", b.Name)
	}
	for _, f := range c.file.Funcs {
		c.errorf(f.Pos,
			"FUNCTION %s: functions are not supported yet; they arrive with phase 5", f.Name)
	}
	for _, g := range c.file.Globals {
		c.errorf(g.Pos,
			"VAR_GLOBAL is not supported yet; state shared between programs arrives with phase 6")
	}
}

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

	c.declareVarSections(p.Vars)

	body := c.checkStmts(p.Body)

	// One PROGRAM whose name matches the component exports as the component
	// itself; any other name becomes a namespaced function, which is what
	// cgen's emitFunctionExport already does for .comp.
	fnName := p.Name
	if ast.EqualName(p.Name, c.component) {
		fnName = "_"
	}

	c.pkg.Component.Functions = []ast.Function{{
		Pos:  p.Pos,
		Name: fnName,
		FP:   c.usesFloat(),
		Body: body,
	}}
}

// usesFloat reports whether any declared name is a floating-point type, which
// decides the fp flag on the exported HAL function.  A component that touches
// no floating point exports with fp = 0 and can run in a thread without FPU
// save and restore.
func (c *checker) usesFloat() bool {
	found := false
	for sc := c.scope; sc != nil; sc = sc.parent {
		sc.each(func(s *symbol) {
			if s.Kind == symVar && s.Type.IsFloat() {
				found = true
			}
		})
	}
	return found
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (c *checker) declareVarSections(blocks []*st.VarBlock) {
	for _, b := range blocks {
		switch b.Kind {
		case st.VarExternal:
			c.errorf(b.Pos, "VAR_EXTERNAL is not supported yet; it arrives with VAR_GLOBAL in phase 6")
			continue
		case st.VarGlobal:
			c.errorf(b.Pos, "VAR_GLOBAL is not supported yet; it arrives with phase 6")
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

	init := ""
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

	c.pkg.Component.Variables = append(c.pkg.Component.Variables, ast.Variable{
		CType:   typ.CType(),
		Name:    name,
		Default: init,
	})
}

// declarePin turns one HAL-facing declaration into a pin.
func (c *checker) declarePin(b *st.VarBlock, d *st.VarDecl, name string, typ ast.Type) {
	halType, fit := typ.HALType()
	switch fit {
	case ast.HALFitNone:
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

	c.pkg.Component.Pins = append(c.pkg.Component.Pins, ast.Pin{
		Pos:     d.Pos,
		Name:    name,
		Type:    halType,
		Dir:     dir,
		Default: def,
		Doc:     attrValue(d.Attrs, "doc"),
	})

	for _, a := range d.Attrs {
		if !strings.EqualFold(a.Name, "doc") {
			c.warnf("%s: unknown attribute %q on %s, ignored", a.Pos, a.Name, name)
		}
	}
}

// bind declares sym in the current scope, reporting a redeclaration.
func (c *checker) bind(sym *symbol) bool {
	if prev := c.scope.lookupLocal(sym.Name); prev != nil {
		c.errorf(sym.Pos, "%s is already declared at %s", sym.Name, prev.Pos)
		return false
	}
	c.scope.declare(sym)
	return true
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
		c.errorf(ref.Pos, "array types are not supported yet; they arrive with phase 4")
		return ast.Type{}, false
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

	// A name that is not elementary would be a user-defined type, which
	// arrives with phase 4.  Saying so beats "unknown type".
	c.errorf(ref.Pos, "unknown type %s; user-defined types arrive with phase 4", ref.Name)
	return ast.Type{}, false
}
