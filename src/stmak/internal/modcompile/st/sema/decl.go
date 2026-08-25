// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// checkTypeDecls resolves a TYPE ... END_TYPE block.
//
// Names are registered before any body is resolved, so a struct may refer to
// another type declared further down the same block -- which is what a reader
// expects, and refusing it would make declaration order load-bearing for no
// reason.  Recursion is caught afterwards, since a type containing itself has
// no size.
func (c *checker) checkTypeDecls(decls []*st.TypeDecl) {
	// Pass one: every name exists before any body is resolved.
	for _, d := range decls {
		if d.Name == "" {
			continue
		}
		sym := &symbol{Name: d.Name, Kind: symType, Pos: d.Pos}
		if prev := c.scope.lookupLocal(d.Name); prev != nil {
			c.errorf(d.Pos, "%s is already declared at %s", d.Name, prev.Pos)
			continue
		}
		c.scope.declare(sym)
		c.pkg.Types = append(c.pkg.Types, ast.TypeDef{Pos: d.Pos, Name: d.Name})
	}

	// Pass two: resolve each body.
	for _, d := range decls {
		if d.Name == "" {
			continue
		}
		td := c.pkg.LookupType(d.Name)
		if td == nil {
			continue
		}
		switch {
		case d.Enum != nil:
			c.checkEnumDecl(d, td)
		case d.Struct != nil:
			c.checkStructDecl(d, td)
		default:
			c.checkAliasDecl(d, td)
		}
		td.Doc = attrValue(d.Attrs, "doc")
		for _, a := range d.Attrs {
			if !strings.EqualFold(a.Name, "doc") {
				c.warnf("%s: unknown attribute %q on type %s, ignored", a.Pos, a.Name, d.Name)
			}
		}
	}

	c.checkTypeCycles()
	c.orderTypes()
}

func (c *checker) checkAliasDecl(d *st.TypeDecl, td *ast.TypeDef) {
	td.Kind = ast.DefAlias
	t, ok := c.resolveType(d.Alias)
	if !ok {
		return
	}
	// The alias name travels with the type so a diagnostic about a Celsius
	// says Celsius rather than LREAL.  It carries no meaning beyond that.
	t.Alias = d.Name
	td.Alias = t

	if d.Init != nil {
		if _, ok := c.constOf(d.Init, t); !ok {
			c.errorf(d.Init.Position(), "the default value of type %s must be a compile-time constant", d.Name)
		}
	}
}

// checkEnumDecl resolves an enumeration and declares its constants.
//
// The constants go into the same scope as the type, which is what makes
// `state := RUNNING` work.  Resolving a constant to its value is the whole
// point: the standalone prototype left them as bare identifiers and emitted
// `inst->RED`, which does not compile.
func (c *checker) checkEnumDecl(d *st.TypeDecl, td *ast.TypeDef) {
	td.Kind = ast.DefEnum
	enumType := ast.Type{Kind: ast.TypeEnum, Ref: d.Name}

	next := int64(0)
	for _, m := range d.Enum {
		value := next
		if m.Value != nil {
			v, ok := c.constOf(m.Value, ast.Type{Kind: ast.TypeDInt})
			if !ok {
				c.errorf(m.Value.Position(),
					"the value of enumeration constant %s must be a compile-time integer", m.Name)
				continue
			}
			value = v
		}
		next = value + 1

		// The constant always belongs to its enumeration, and is always
		// reachable as `Light.RED`.  What a collision costs is the
		// unqualified spelling: two enumerations sharing OFF is ordinary in
		// real code, so the declaration is accepted and only a bare `OFF`
		// becomes an error -- reported where it is written, naming both
		// candidates.
		td.Consts = append(td.Consts, ast.EnumConst{Pos: m.Pos, Name: m.Name, Value: value})

		if prev := c.scope.lookup(m.Name); prev != nil {
			if prev.Kind == symEnumConst {
				prev.Ambiguous = append(prev.Ambiguous, d.Name)
				continue
			}
			c.errorf(m.Pos, "enumeration constant %s collides with the %s declared at %s",
				m.Name, prev.Kind, prev.Pos)
			continue
		}

		c.scope.declare(&symbol{
			Name:          m.Name,
			Kind:          symEnumConst,
			Type:          enumType,
			Pos:           m.Pos,
			Const:         true,
			ConstValue:    value,
			HasConstValue: true,
			EnumType:      d.Name,
		})
	}

	if d.Init != nil {
		if _, ok := c.constOf(d.Init, enumType); !ok {
			c.errorf(d.Init.Position(),
				"the default value of enumeration %s must be one of its constants", d.Name)
		}
	}
}

func (c *checker) checkStructDecl(d *st.TypeDecl, td *ast.TypeDef) {
	td.Kind = ast.DefStruct

	seen := make(map[string]ast.Pos)
	for _, f := range d.Struct {
		if prev, dup := seen[key(f.Name)]; dup {
			c.errorf(f.Pos, "field %s is already declared at %s", f.Name, prev)
			continue
		}
		seen[key(f.Name)] = f.Pos

		ft, ok := c.resolveType(f.Type)
		if !ok {
			continue
		}
		if ft.Kind == ast.TypeFB {
			c.errorf(f.Pos, "a struct field may not be a function block instance; "+
				"declare %s in a VAR section instead", f.Name)
			continue
		}

		field := ast.Field{Pos: f.Pos, Name: f.Name, Typ: ft, Doc: attrValue(f.Attrs, "doc")}
		if f.Init != nil {
			// Struct field initialisers would need a per-field init sequence
			// in the generated code; sema accepts the syntax so the message
			// can say that rather than "unexpected :=".
			c.warnf("%s: initial value on struct field %s.%s is ignored; "+
				"initialise it in the body instead", f.Pos, d.Name, f.Name)
		}
		td.Fields = append(td.Fields, field)
	}

	if len(td.Fields) == 0 && len(d.Struct) > 0 {
		return
	}
	if len(d.Struct) == 0 {
		c.errorf(d.Pos, "struct %s has no fields", d.Name)
	}
}

// checkTypeCycles refuses a type that contains itself.  Such a type has no
// size, so the generated C would not compile -- but the message the compiler
// would give names a generated struct, not the TYPE the author wrote.
func (c *checker) checkTypeCycles() {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make(map[string]int, len(c.pkg.Types))

	var visit func(name string, at ast.Pos) bool
	visit = func(name string, at ast.Pos) bool {
		k := key(name)
		switch state[k] {
		case grey:
			c.errorf(at, "type %s contains itself, so it has no size", name)
			return false
		case black:
			return true
		}
		state[k] = grey
		if td := c.pkg.LookupType(name); td != nil {
			for _, ref := range typeRefs(td) {
				if !visit(ref.name, ref.pos) {
					state[k] = black
					return false
				}
			}
		}
		state[k] = black
		return true
	}

	for i := range c.pkg.Types {
		visit(c.pkg.Types[i].Name, c.pkg.Types[i].Pos)
	}
}

type namedRef struct {
	name string
	pos  ast.Pos
}

// typeRefs lists the user-defined types one definition mentions.
func typeRefs(td *ast.TypeDef) []namedRef {
	var out []namedRef
	add := func(t ast.Type, pos ast.Pos) {
		for t.Kind == ast.TypeArray && t.Elem != nil {
			t = *t.Elem
		}
		if t.Kind == ast.TypeStruct || t.Kind == ast.TypeEnum {
			out = append(out, namedRef{t.Ref, pos})
		}
	}
	switch td.Kind {
	case ast.DefAlias:
		add(td.Alias, td.Pos)
	case ast.DefStruct:
		for _, f := range td.Fields {
			add(f.Typ, f.Pos)
		}
	}
	return out
}

// orderTypes sorts the definitions so each is declared before its first use,
// which is what C requires of the generated typedefs.  The order is otherwise
// the source order, so the generated file reads like the .st.
func (c *checker) orderTypes() {
	ordered := make([]ast.TypeDef, 0, len(c.pkg.Types))
	placed := make(map[string]bool, len(c.pkg.Types))
	inProgress := make(map[string]bool, len(c.pkg.Types))

	var place func(td *ast.TypeDef)
	place = func(td *ast.TypeDef) {
		k := key(td.Name)
		if placed[k] || inProgress[k] {
			// inProgress means a cycle, already reported by checkTypeCycles;
			// stop rather than recursing forever.
			return
		}
		inProgress[k] = true
		for _, ref := range typeRefs(td) {
			if dep := c.pkg.LookupType(ref.name); dep != nil {
				place(dep)
			}
		}
		inProgress[k] = false
		placed[k] = true
		ordered = append(ordered, *td)
	}

	for i := range c.pkg.Types {
		place(&c.pkg.Types[i])
	}
	c.pkg.Types = ordered
}

// constOf checks an expression in a type's context and folds it, which is what
// a default value and an enumeration value both need.
func (c *checker) constOf(e st.Expr, want ast.Type) (int64, bool) {
	checked := c.checkExpr(e, want)
	if checked == nil {
		return 0, false
	}
	return foldInt(checked)
}
