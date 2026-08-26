// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"fmt"
	"strings"

	gmiast "github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// GMI in Structured Text.  See docs/dev/MODCOMPILE_ST_STDLIB.md §7a.
//
// A consumed API reads as an instance with methods, because that is what it
// is: a table of functions and a context, which is a function block by another
// name.  `home.do_homing()` is the same spelling as a call on any other
// instance, and it resolves through the same path.
//
// A provided API is a FUNCTION_BLOCK that IMPLEMENTS it, whose methods are the
// API's functions.  Every GMI callback already takes `void *ctx` first and the
// provider side sets that ctx to the component instance, so a method on a
// block is the shape the callback already has, minus the plumbing.
//
// RT only.  A body running in a HAL thread may call a function the IDL marks
// `@rt_safe "true"` and no other.  Nothing enforced that before: the generated
// header carries STMAK_API_NONBLOCKING, which needs clang 20 to mean anything,
// so a gcc build had no check at all.

// gmiType maps a GMI type reference onto an ST type.
//
// Primitives only.  A string is a `const char *` in the callback and ST has no
// pointer; a `ptr` is opaque by construction; a struct would have to be
// declared on both sides and kept in step by hand.  Each is refused by name
// rather than mapped onto something that would compile and mean something
// else.
func gmiType(api *gmiast.API, t gmiast.TypeRef) (ast.Type, string, error) {
	if t.Kind == gmiast.TypeNamed {
		// An enumeration is an integer with names on it, and reaches C as a
		// typedef.  ST sees a DINT -- it has enumerations of its own, but one
		// declared here would be a second copy of the interface's, kept in
		// step by hand -- while the generated thunk keeps the C spelling, so
		// the callback's function pointer type still matches.
		for i := range api.Enums {
			if api.Enums[i].Name != t.Name {
				continue
			}
			return ast.Type{Kind: ast.TypeDInt},
				fmt.Sprintf("%s_%s_t", api.Prefix, toSnake(t.Name)), nil
		}
		return ast.Type{}, "", fmt.Errorf("%s is a structure, and Structured Text can only "+
			"reach the scalar part of a GMI interface so far", t.Name)
	}
	if t.Kind != gmiast.TypePrimitive {
		return ast.Type{}, "", fmt.Errorf("%s is not a scalar type, and Structured Text "+
			"can only reach the scalar part of a GMI interface so far", t.Name)
	}
	switch t.Name {
	case "bool":
		return ast.Type{Kind: ast.TypeBool}, "", nil
	case "i8":
		return ast.Type{Kind: ast.TypeSInt}, "", nil
	case "i16":
		return ast.Type{Kind: ast.TypeInt}, "", nil
	case "i32":
		return ast.Type{Kind: ast.TypeDInt}, "", nil
	case "i64":
		return ast.Type{Kind: ast.TypeLInt}, "", nil
	case "u8":
		return ast.Type{Kind: ast.TypeUSInt}, "", nil
	case "u16":
		return ast.Type{Kind: ast.TypeUInt}, "", nil
	case "u32":
		return ast.Type{Kind: ast.TypeUDInt}, "", nil
	case "u64":
		return ast.Type{Kind: ast.TypeULInt}, "", nil
	case "f32":
		return ast.Type{Kind: ast.TypeReal}, "", nil
	case "f64":
		return ast.Type{Kind: ast.TypeLReal}, "", nil
	}
	return ast.Type{}, "", fmt.Errorf("%s has no Structured Text equivalent; "+
		"a string is a pointer in the callback and a ptr is opaque, neither of "+
		"which ST has", t.Name)
}

// toSnake mirrors the GMI code generator's naming, so a type's C spelling here
// is the one its header actually declares.
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// declareConsumedAPIs binds a name for each consumed API, so the body can call
// through it.
func (c *checker) declareConsumedAPIs(pos ast.Pos) {
	for _, e := range c.pkg.Component.GMIConsume {
		api := c.apis[strings.ToLower(e.API)]
		if api == nil {
			continue
		}
		c.scope.declare(&symbol{
			Name:  api.Name,
			Kind:  symAPI,
			Pos:   pos,
			Const: true,
		})
	}
}

// checkAPICall resolves `api.func(args)` against a consumed GMI interface.
//
// Returns nil when name is not that of a consumed API, so the caller falls
// through to the ordinary instance paths.
func (c *checker) checkAPICall(base *st.Ident, pos ast.Pos, fn string, args []st.Arg) (ast.Expr, bool) {
	sym := c.scope.lookup(base.Name)
	if sym == nil || sym.Kind != symAPI {
		return nil, false
	}
	api := c.apis[strings.ToLower(sym.Name)]
	if api == nil {
		return nil, false
	}

	var f *gmiast.Func
	for i := range api.Funcs {
		if strings.EqualFold(api.Funcs[i].Name, fn) {
			f = &api.Funcs[i]
			break
		}
	}
	if f == nil {
		c.errorf(pos, "%s has no function %s", api.Name, fn)
		return nil, true
	}

	// The check the generated header cannot make on a gcc build.
	if !f.RTSafe {
		c.errorf(pos, "%s.%s is not marked rt_safe in the interface, and this body runs in "+
			"a HAL thread. Calling it could block the thread for as long as the "+
			"provider takes", api.Name, f.Name)
		return nil, true
	}

	out := &ast.APICallExpr{Pos: pos, API: api.Name, Func: f.Name}
	if f.Return != nil {
		typ, _, err := gmiType(api, *f.Return)
		if err != nil {
			c.errorf(pos, "%s.%s returns %s", api.Name, f.Name, err)
			return nil, true
		}
		out.Typ = typ
	}

	if len(args) != len(f.Params) {
		c.errorf(pos, "%s.%s takes %d %s, got %d", api.Name, f.Name,
			len(f.Params), plural(len(f.Params), "argument", "arguments"), len(args))
		return out, true
	}
	for i, a := range args {
		p := f.Params[i]
		if p.ByRef || p.IsOut || p.IsPtr {
			c.errorf(a.Pos, "%s.%s takes %s by reference, which Structured Text cannot pass",
				api.Name, f.Name, p.Name)
			return out, true
		}
		typ, _, err := gmiType(api, p.Type)
		if err != nil {
			c.errorf(a.Pos, "%s.%s parameter %s: %s", api.Name, f.Name, p.Name, err)
			return out, true
		}
		if a.Form == st.ArgOutput {
			c.errorf(a.Pos, "%s.%s has no output parameters", api.Name, f.Name)
			return out, true
		}
		if a.Form == st.ArgInput && !strings.EqualFold(a.Name, p.Name) {
			c.errorf(a.Pos, "%s.%s: argument %d is %s, not %s; matched by position",
				api.Name, f.Name, i+1, p.Name, a.Name)
			return out, true
		}
		e := c.checkExpr(a.Value, typ)
		if e == nil {
			continue
		}
		e = c.assign(a.Pos, api.Name+"."+f.Name+"."+p.Name, typ, e)
		if e == nil {
			continue
		}
		out.Args = append(out.Args, e)
	}
	return out, true
}

// checkImplements resolves `FUNCTION_BLOCK X IMPLEMENTS api`: every rt_safe
// function of the API has to have a method of the same name and shape.
//
// Checked as a whole rather than per method, because a half-implemented
// interface registers callbacks that are null when called -- which is a fault
// in the RT thread of whoever consumes it, a long way from here.
func (c *checker) checkImplements(b *st.FunctionBlockDecl, def *ast.FunctionBlock) {
	for _, name := range b.Implements {
		api := c.apis[strings.ToLower(name)]
		if api == nil {
			c.errorf(b.Pos, "no GMI interface named %s; "+
				"add {attribute 'gmi_provide' := '%s'} so the interface is loaded",
				name, name)
			continue
		}
		def.Implements = append(def.Implements, api.Name)

		for i := range api.Funcs {
			f := &api.Funcs[i]
			if !f.RTSafe {
				// A non-rt-safe function cannot be implemented in ST anyway,
				// since the body it would run in is a HAL function.
				continue
			}
			var m *ast.Method
			for j := range def.Methods {
				if strings.EqualFold(def.Methods[j].Name, f.Name) {
					m = &def.Methods[j]
					break
				}
			}
			if m == nil {
				c.errorf(b.Pos, "%s implements %s but has no METHOD %s; "+
					"an interface with a hole in it registers a callback that is null "+
					"when a consumer calls it", b.Name, api.Name, f.Name)
				continue
			}
			c.checkMethodMatches(b.Pos, api, f, m)
		}
	}
}

// checkMethodMatches reports where a method's shape differs from the interface
// function it implements.
func (c *checker) checkMethodMatches(pos ast.Pos, api *gmiast.API, f *gmiast.Func, m *ast.Method) {
	want := ast.Type{}
	if f.Return != nil {
		t, ctype, err := gmiType(api, *f.Return)
		if err != nil {
			c.errorf(m.Pos, "%s.%s returns %s", api.Name, f.Name, err)
			return
		}
		want = t
		m.ResultCType = ctype
	}
	if m.Result.Kind != want.Kind {
		if want.Kind == ast.TypeInvalid {
			c.errorf(m.Pos, "%s.%s returns nothing, so METHOD %s must not declare a result",
				api.Name, f.Name, m.Name)
		} else {
			c.errorf(m.Pos, "%s.%s returns %s, so METHOD %s must be declared `METHOD %s : %s`",
				api.Name, f.Name, want, m.Name, m.Name, want)
		}
		return
	}

	if len(m.Params) != len(f.Params) {
		c.errorf(m.Pos, "%s.%s takes %d %s, so METHOD %s must declare the same",
			api.Name, f.Name, len(f.Params),
			plural(len(f.Params), "parameter", "parameters"), m.Name)
		return
	}
	for i, p := range f.Params {
		if p.ByRef || p.IsOut || p.IsPtr {
			c.errorf(m.Pos, "%s.%s takes %s by reference, which Structured Text cannot receive",
				api.Name, f.Name, p.Name)
			return
		}
		typ, ctype, err := gmiType(api, p.Type)
		if err != nil {
			c.errorf(m.Pos, "%s.%s parameter %s: %s", api.Name, f.Name, p.Name, err)
			return
		}
		if m.ParamCTypes == nil {
			m.ParamCTypes = make([]string, len(m.Params))
		}
		m.ParamCTypes[i] = ctype
		got := m.Params[i]
		if got.Typ.Kind != typ.Kind {
			c.errorf(got.Pos, "%s.%s parameter %d is %s %s, but METHOD %s declares %s as %s",
				api.Name, f.Name, i+1, typ, p.Name, m.Name, got.Name, got.Typ)
			return
		}
	}
}
