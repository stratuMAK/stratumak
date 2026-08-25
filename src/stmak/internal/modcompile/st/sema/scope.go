// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package sema resolves an ST syntax tree into the shared IR of
// internal/modcompile/ast: it builds the symbol table, infers and checks
// types, folds constants, enforces the real-time rules, and lowers statements.
//
// See docs/dev/MODCOMPILE_ST_FRONTEND.md §5.2.
package sema

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// symKind says what sort of thing a name stands for.  Keeping these apart is
// what lets a diagnostic say "Ramp is a function block, not a variable"
// instead of "undefined".
type symKind int

const (
	symVar symKind = iota
	symEnumConst
	symType
	symBlock
	symFunc
	symAction
	symBuiltin
)

var symKindNames = map[symKind]string{
	symVar:       "variable",
	symEnumConst: "enumeration constant",
	symType:      "type",
	symBlock:     "function block",
	symFunc:      "function",
	symAction:    "action",
	symBuiltin:   "built-in",
}

func (k symKind) String() string { return symKindNames[k] }

// symbol is one declared name.
type symbol struct {
	Name  string // spelling as declared, for diagnostics and C mangling
	Kind  symKind
	Type  ast.Type
	Store ast.Storage
	Pos   ast.Pos

	// Const marks a name that may be read but not assigned: a CONSTANT
	// declaration, a VAR_INPUT pin (HAL drives it), or an enumeration
	// constant.
	Const bool

	// ConstValue and HasConstValue carry a folded compile-time value, which is
	// what makes a CONSTANT usable as an array bound or a CASE label.
	ConstValue    int64
	HasConstValue bool

	// ConstExpr is the folded initialiser of a CONSTANT.  A reference to one
	// resolves to a copy of this rather than to a variable read, which is what
	// lets `FOR i := 0 TO LAST_IDX` and `ARRAY[0..SIZE-1]` work -- both need a
	// value the compiler can see, and real ST names its bounds far more often
	// than it writes them out.
	ConstExpr ast.Expr

	// EnumType names the enumeration a symEnumConst belongs to.
	EnumType string

	// Ambiguous lists the other enumerations that declare a constant of this
	// name.  Two enumerations sharing OFF is ordinary, so the declarations are
	// accepted and only an unqualified use is refused -- with every candidate
	// named, so the fix is obvious.
	Ambiguous []string
}

// scope is one lexical level of the symbol table.
//
// Names are keyed upper-cased because ST is case-insensitive for identifiers
// and not only for keywords: `maxRate` and `MAXRATE` are one name.  The
// symbol keeps the original spelling, so diagnostics and generated C use what
// the author wrote.
type scope struct {
	parent *scope
	names  map[string]*symbol
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, names: make(map[string]*symbol)}
}

func key(name string) string { return strings.ToUpper(name) }

// declare adds sym to this scope.  It returns the existing symbol, and false,
// when the name is already declared at this level -- shadowing an outer scope
// is allowed, redeclaring within one is not.
func (s *scope) declare(sym *symbol) (*symbol, bool) {
	k := key(sym.Name)
	if prev, ok := s.names[k]; ok {
		return prev, false
	}
	s.names[k] = sym
	return sym, true
}

// lookup finds a name in this scope or any enclosing one.
func (s *scope) lookup(name string) *symbol {
	k := key(name)
	for sc := s; sc != nil; sc = sc.parent {
		if sym, ok := sc.names[k]; ok {
			return sym
		}
	}
	return nil
}

// lookupLocal finds a name in this scope only.
func (s *scope) lookupLocal(name string) *symbol {
	if sym, ok := s.names[key(name)]; ok {
		return sym
	}
	return nil
}

// each calls f for every symbol in this scope, in no particular order.
func (s *scope) each(f func(*symbol)) {
	for _, sym := range s.names {
		f(sym)
	}
}
