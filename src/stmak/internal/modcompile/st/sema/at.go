// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// This file handles the explicit pin mapping of
// docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1: `name AT %Q.pin : TYPE`.
//
// The clause resembles IEC's located-variable syntax and deliberately is not
// it.  `%QX0.0` in CODESYS is a physical address -- memory area, size code,
// offset -- and stratuMAK has no address space to locate a variable in, since
// HAL signals are the addressing scheme.  So `%I`, `%Q` and `%IO` name a
// direction and nothing else, and an address-shaped clause is refused rather
// than quietly reinterpreted.

// atArea is what an AT clause's area letters name.
type atArea struct {
	dir   ast.PinDir
	param bool
	pdir  ast.ParamDir
}

// atAreas maps the area letters to what they map onto.
//
// A HAL parameter is the same shared memory as a pin with no signal able to
// connect to it, so IEC's %M -- memory that is not physical I/O -- is the area
// letter, followed by HAL's own r/rw.  The equivalent pin direction is kept in
// step with it: rw is HAL's to write and the component's to read, exactly like
// an input, and r is the reverse.  See §3.5.
var atAreas = map[string]atArea{
	"I":   {dir: ast.PinIn},
	"Q":   {dir: ast.PinOut},
	"IO":  {dir: ast.PinIO},
	"MRW": {dir: ast.PinIn, param: true, pdir: ast.ParamRW},
	"MR":  {dir: ast.PinOut, param: true, pdir: ast.ParamR},
}

// parseAT reads an AT clause.  The returned name is empty when the clause gave
// none, meaning the pin takes the variable's own name.
func (c *checker) parseAT(text string, pos ast.Pos, varName string) (atArea, string, bool) {
	raw := strings.TrimSpace(text)
	if !strings.HasPrefix(raw, "%") {
		c.errorf(pos, "AT needs a HAL direction, as in `%s AT %%Q : ...`", varName)
		return atArea{}, "", false
	}
	body := raw[1:]

	area, rest := body, ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		area, rest = body[:i], body[i+1:]
	}

	spec, ok := atAreas[strings.ToUpper(area)]
	if !ok {
		// An address-shaped area is the likely mistake, and worth naming: a
		// PLC programmer reaches for %QX0.0 by reflex.
		if looksLikeAddress(area) {
			c.errorf(pos, "AT %%%s is a physical address; stratuMAK has no address space, "+
				"so a variable is mapped to HAL by direction instead. "+
				"Write AT %%I, AT %%Q or AT %%IO for a pin, or AT %%MRW or AT %%MR for a "+
				"parameter, optionally with a name after a dot",
				area)
			return atArea{}, "", false
		}
		c.errorf(pos, "unknown HAL area %%%s; write AT %%I, AT %%Q or AT %%IO for a pin, "+
			"or AT %%MRW or AT %%MR for a parameter", area)
		return atArea{}, "", false
	}

	if rest == "" {
		return spec, varName, true
	}
	if !isPinName(rest) {
		c.errorf(pos, "%q is not a usable HAL name; use letters, digits and underscores, "+
			"separated by dots", rest)
		return atArea{}, "", false
	}
	return spec, rest, true
}

// looksLikeAddress reports whether an area reads like IEC's located syntax --
// a direction letter followed by a size code and digits, as in QX0 or IW12.
func looksLikeAddress(area string) bool {
	up := strings.ToUpper(area)
	if len(up) < 2 {
		return false
	}
	switch up[0] {
	case 'I', 'Q', 'M':
	default:
		return false
	}
	switch up[1] {
	case 'X', 'B', 'W', 'D', 'L':
		return true
	}
	return up[1] >= '0' && up[1] <= '9'
}

// isPinName reports whether s is usable as a HAL pin name.
//
// A hyphen is allowed as well as an underscore.  Every pin name goes through
// the same underscore-to-hyphen transform on the way to HAL, whether it came
// from a VAR_INPUT or from an AT clause, so `at_home` and `at-home` name the
// same pin -- and accepting both means the HAL spelling can be written out
// directly rather than only reached through the transform.
func isPinName(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			ch := part[i]
			switch {
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_':
			case ch == '-' && i > 0:
			case ch >= '0' && ch <= '9' && i > 0:
			default:
				return false
			}
		}
	}
	return true
}

// bindAT resolves an AT clause into a binding, checking that the type can
// reach a pin at all.
func (c *checker) bindAT(text string, pos ast.Pos, varName string, typ ast.Type) *ast.HALBinding {
	spec, halName, ok := c.parseAT(text, pos, varName)
	if !ok {
		return nil
	}
	what := "pin"
	if spec.param {
		what = "parameter"
	}
	if typ.Kind == ast.TypeArray {
		if spec.param {
			c.errorf(pos, "%s cannot be mapped with AT: a HAL parameter is a single value, "+
				"so an array cannot be one", varName)
			return nil
		}
		c.errorf(pos, "%s cannot be mapped with AT: an array maps to an array pin only "+
			"from a PROGRAM's VAR_INPUT or VAR_OUTPUT", varName)
		return nil
	}
	halType, fit := typ.HALType()
	if fit == ast.HALFitNone {
		if typ.Kind == ast.TypeTime {
			c.errorf(pos, "%s cannot be mapped to a HAL %s: HAL has no duration type. "+
				"Map an LREAL carrying seconds and convert with SECONDS_TO_TIME, "+
				"or a DINT carrying milliseconds and DINT_TO_TIME", varName, what)
			return nil
		}
		c.errorf(pos, "%s cannot be mapped to a HAL %s: %s has no HAL representation",
			varName, what, typ)
		return nil
	}
	if fit == ast.HALFitLossy {
		c.warnf("%s: %s %s is REAL; HAL carries it as a double, so a value written by HAL "+
			"loses precision when read back. Use LREAL for an exact %s", pos, what, halName, what)
	}
	return &ast.HALBinding{
		Pos: pos, Name: halName, Dir: spec.dir, Type: halType,
		Param: spec.param, ParamDir: spec.pdir,
	}
}

// ---------------------------------------------------------------------------
// Materialising a block's pins along the instance path
// ---------------------------------------------------------------------------

// publishInstancePins walks the function block instances a program declared and
// creates a pin for every AT variable it finds, named by the path that reaches
// it.  The bindings are recorded on the function, which loads and stores them
// around its body.
func (c *checker) publishInstancePins(fn *ast.Function, vars []instanceVar) {
	for _, v := range vars {
		c.walkInstance(fn, v.name, v.typ, v.pos, false)
	}
}

// instanceVar is one declared name that holds a function block instance.
type instanceVar struct {
	name string
	typ  ast.Type
	pos  ast.Pos
}

// walkInstance descends one instance, creating pins for its AT members and
// recursing into instances it holds.  arrayed records that a segment already
// crossed an array, since a HAL pin name carries only one index.
func (c *checker) walkInstance(fn *ast.Function, path string, typ ast.Type, pos ast.Pos, arrayed bool) {
	count := 0
	elem := typ
	if typ.Kind == ast.TypeArray {
		if typ.Elem == nil || typ.Elem.Kind != ast.TypeFB {
			return
		}
		if len(typ.Dims) != 1 {
			// Only reported once the block turns out to publish something.
			if c.blockPublishes(typ.Elem.Ref) {
				c.errorf(pos, "%s publishes HAL pins, so it may not be held in a "+
					"multi-dimensional array: a pin name carries one index", typ.Elem.Ref)
			}
			return
		}
		count = typ.Dims[0].Count()
		elem = *typ.Elem
		arrayed = true
	}
	if elem.Kind != ast.TypeFB {
		return
	}

	def := c.pkg.LookupBlock(elem.Ref)
	if def == nil {
		return
	}

	for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts, def.Vars} {
		for _, v := range set {
			if v.HAL != nil {
				c.publishOne(fn, path, count, v, pos)
				continue
			}
			// Recurse into a nested instance.
			inner := v.Typ
			if inner.Kind == ast.TypeFB ||
				(inner.Kind == ast.TypeArray && inner.Elem != nil && inner.Elem.Kind == ast.TypeFB) {
				if arrayed && c.subtreePublishes(inner) {
					c.errorf(pos, "%s.%s publishes HAL pins through an array of instances; "+
						"only one segment of a pin's path may be an index",
						path, v.Name)
					continue
				}
				c.walkInstance(fn, pathJoin(path, v.Name, count > 0), inner, pos, arrayed)
			}
		}
	}
}

// publishOne creates the pin for one AT member of an instance.
func (c *checker) publishOne(fn *ast.Function, path string, count int, v ast.VarDecl, pos ast.Pos) {
	halName := path + "." + v.HAL.Name
	cPath := path + "." + v.Name

	what := "pin"
	if v.HAL.Param {
		what = "parameter"
	}

	binding := ast.PinBinding{Pin: halName, Path: cPath, Dir: v.HAL.Dir, Param: v.HAL.Param}
	arraySize := 0

	if count > 0 {
		// The array segment becomes the index, exactly as an array pin's does:
		// axes.0.position .. axes.3.position.
		halName = path + "." + strings.Repeat("#", digitsFor(count-1)) + "." + v.HAL.Name
		arraySize = count
		binding.Pin = halName
		binding.Path = fmt.Sprintf("%s[%%d].%s", path, v.Name)
		binding.ArrayOf = count
	}

	if !c.claimInstanceName(halName, pos, what) {
		return
	}
	if n := len(c.component) + 1 + len(halName); n > halNameLimit {
		c.errorf(pos, "the %s name %s.%s is %d characters, over HAL's limit of %d; "+
			"shorten the instance or member name", what, c.component, halName, n, halNameLimit)
		return
	}

	// An initialiser on a published member is the HAL default, not just the
	// instance's: the member is loaded from shared memory at entry, so a value
	// set only on the instance would be overwritten before the body saw it.
	def := ""
	if v.Init != nil {
		lit, ok := c.foldToC(v.Init)
		if !ok {
			c.errorf(v.Pos, "the default value of %s %s must be a compile-time constant",
				what, halName)
		}
		def = lit
	}

	if v.HAL.Param {
		c.pkg.Component.Params = append(c.pkg.Component.Params, ast.Param{
			Pos:       v.HAL.Pos,
			Name:      halName,
			Type:      v.HAL.Type,
			Dir:       v.HAL.ParamDir,
			ArraySize: arraySize,
			Default:   def,
			Doc:       v.Doc,
		})
		fn.PinBindings = append(fn.PinBindings, binding)
		return
	}

	c.pkg.Component.Pins = append(c.pkg.Component.Pins, ast.Pin{
		Pos:       v.HAL.Pos,
		Name:      halName,
		Type:      v.HAL.Type,
		Dir:       v.HAL.Dir,
		ArraySize: arraySize,
		Default:   def,
		Doc:       v.Doc,
	})
	fn.PinBindings = append(fn.PinBindings, binding)
}

// halNameLimit is STMAK_HAL_NAME_LEN.  A pin whose name does not fit is
// refused here rather than being truncated into a collision at load time.
const halNameLimit = 127

func pathJoin(path, name string, indexed bool) string {
	if indexed {
		return path + ".#." + name
	}
	return path + "." + name
}

// blockPublishes reports whether a block declares any AT variable.
func (c *checker) blockPublishes(name string) bool {
	def := c.pkg.LookupBlock(name)
	if def == nil {
		return false
	}
	for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts, def.Vars} {
		for _, v := range set {
			if v.HAL != nil {
				return true
			}
		}
	}
	return false
}

// subtreePublishes reports whether a type, or anything it holds, declares an AT
// variable.
func (c *checker) subtreePublishes(t ast.Type) bool {
	for t.Kind == ast.TypeArray && t.Elem != nil {
		t = *t.Elem
	}
	if t.Kind != ast.TypeFB {
		return false
	}
	if c.blockPublishes(t.Ref) {
		return true
	}
	def := c.pkg.LookupBlock(t.Ref)
	if def == nil {
		return false
	}
	for _, set := range [][]ast.VarDecl{def.Inputs, def.Outputs, def.InOuts, def.Vars} {
		for _, v := range set {
			if v.Typ.Kind == ast.TypeFB ||
				(v.Typ.Kind == ast.TypeArray && v.Typ.Elem != nil && v.Typ.Elem.Kind == ast.TypeFB) {
				if c.subtreePublishes(v.Typ) {
					return true
				}
			}
		}
	}
	return false
}
