// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"math"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// elementary maps an ST elementary type name to its kind.  These are not
// keywords in the scanner -- to the parser a type name is just an identifier --
// so this table is where a name first becomes a type.
var elementary = map[string]ast.TypeKind{
	"BOOL":  ast.TypeBool,
	"SINT":  ast.TypeSInt,
	"INT":   ast.TypeInt,
	"DINT":  ast.TypeDInt,
	"LINT":  ast.TypeLInt,
	"USINT": ast.TypeUSInt,
	"UINT":  ast.TypeUInt,
	"UDINT": ast.TypeUDInt,
	"ULINT": ast.TypeULInt,
	"REAL":  ast.TypeReal,
	"LREAL": ast.TypeLReal,
	// A duration, held as int64 nanoseconds -- what get_time() and a task's
	// period already are.  LTIME is the same type: IEC leaves TIME's range
	// implementation-defined, and at nanoseconds there is nothing for a
	// "long" one to add.
	"TIME":  ast.TypeTime,
	"LTIME": ast.TypeTime,
	// A point in time, held as nanoseconds since 1970-01-01 UTC.  It is a
	// separate type from TIME on purpose: subtracting two of them gives a
	// duration, and adding two of them gives nothing at all.
	"DATE_AND_TIME":  ast.TypeDateTime,
	"DT":             ast.TypeDateTime,
	"LDATE_AND_TIME": ast.TypeDateTime,
	"LDT":            ast.TypeDateTime,
	// A day, held as that day's midnight in the same unit, and a time of day,
	// held as nanoseconds since midnight.  Separate types because what you can
	// do with each differs: two days subtract to a duration, a day and a time
	// of day combine into a point in time, and neither is the other.
	"DATE":         ast.TypeDate,
	"D":            ast.TypeDate,
	"LDATE":        ast.TypeDate,
	"LD":           ast.TypeDate,
	"TIME_OF_DAY":  ast.TypeTimeOfDay,
	"TOD":          ast.TypeTimeOfDay,
	"LTIME_OF_DAY": ast.TypeTimeOfDay,
	"LTOD":         ast.TypeTimeOfDay,
}

// deferredTypes name types stratuMAK will support but does not yet, mapped to
// the reason.  Naming them explicitly turns "unknown type TIME" into something
// a reader can act on.
var deferredTypes = map[string]string{
	"WSTRING": "WSTRING is not supported; use STRING",
	"BYTE":    "bit-string types (BYTE, WORD, DWORD, LWORD) are not implemented yet; use USINT, UINT, UDINT or ULINT",
	"WORD":    "bit-string types (BYTE, WORD, DWORD, LWORD) are not implemented yet; use USINT, UINT, UDINT or ULINT",
	"DWORD":   "bit-string types (BYTE, WORD, DWORD, LWORD) are not implemented yet; use USINT, UINT, UDINT or ULINT",
	"LWORD":   "bit-string types (BYTE, WORD, DWORD, LWORD) are not implemented yet; use USINT, UINT, UDINT or ULINT",
}

// lookupElementary resolves an elementary type name, case-insensitively.
func lookupElementary(name string) (ast.TypeKind, bool) {
	k, ok := elementary[strings.ToUpper(name)]
	return k, ok
}

// ---------------------------------------------------------------------------
// Assignability and conversion
// ---------------------------------------------------------------------------

// convKind says how a value of one type reaches another.
type convKind int

const (
	// convNone: the conversion is not allowed.
	convNone convKind = iota
	// convIdentical: same type, nothing to do.
	convIdentical
	// convWiden: value-preserving, and sema inserts an ast.ConvExpr.
	convWiden
)

// convert reports how a value of type from reaches type to.
//
// The rule is deliberately narrower than what C would accept silently.
// Widening within a signedness class is allowed, and any integer reaches a
// float; everything else -- narrowing, crossing between signed and unsigned,
// and anything involving BOOL -- has to be written out with a conversion
// function, because each of those is a place where a value changes quietly.
//
// Literals are handled before this: an untyped integer literal adapts to its
// context if it fits, so `x : SINT := 1` needs no ceremony.
func convert(from, to ast.Type) convKind {
	if sameType(from, to) {
		return convIdentical
	}

	// BOOL converts to and from nothing.  It is a truth value, not a
	// one-bit number, and letting it decay into arithmetic is how a
	// comparison silently becomes an addition.
	if from.Kind == ast.TypeBool || to.Kind == ast.TypeBool {
		return convNone
	}

	// An enumeration is compatible only with itself, checked by sameType
	// above.  Widening it to an integer would lose the type that makes a
	// state machine checkable.
	if from.Kind == ast.TypeEnum || to.Kind == ast.TypeEnum {
		return convNone
	}

	switch {
	case from.IsInteger() && to.IsInteger():
		if from.IsSigned() != to.IsSigned() {
			return convNone
		}
		if from.Bits() <= to.Bits() {
			return convWiden
		}
		return convNone

	case from.IsInteger() && to.IsFloat():
		// Every integer reaches LREAL exactly.  Reaching REAL loses
		// precision above 2^24, but refusing DINT to REAL would make REAL
		// unusable; the narrower target is the author's choice.
		return convWiden

	case from.Kind == ast.TypeReal && to.Kind == ast.TypeLReal:
		return convWiden
	}

	return convNone
}

// sameType reports whether two resolved types are the same.  Alias spelling is
// ignored: it exists for diagnostics and carries no meaning.
func sameType(a, b ast.Type) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ast.TypeEnum, ast.TypeStruct, ast.TypeFB:
		return ast.EqualName(a.Ref, b.Ref)
	case ast.TypeString:
		return a.StringLen() == b.StringLen()
	case ast.TypeArray:
		if len(a.Dims) != len(b.Dims) || a.Elem == nil || b.Elem == nil {
			return false
		}
		for i := range a.Dims {
			if a.Dims[i] != b.Dims[i] {
				return false
			}
		}
		return sameType(*a.Elem, *b.Elem)
	}
	return true
}

// unify picks the common type of two operands of a binary operator, following
// the same widening rule as convert.  The second result is false when there is
// none, which is a type error at the operator.
func unify(a, b ast.Type) (ast.Type, bool) {
	if sameType(a, b) {
		return a, true
	}
	if convert(a, b) == convWiden {
		return b, true
	}
	if convert(b, a) == convWiden {
		return a, true
	}
	return ast.Type{}, false
}

// intRange is the inclusive range of an integer type, used to decide whether
// an untyped literal fits its context.
// The bounds are split by sign because no single Go type holds both ends of
// every ST integer: LINT's lowest needs an int64 and ULINT's highest a uint64.
// Every lower bound is negative or zero and every upper bound is positive, so
// one of each is exact.
func intRange(k ast.TypeKind) (lo int64, hi uint64, ok bool) {
	switch k {
	case ast.TypeSInt:
		return -128, 127, true
	case ast.TypeInt:
		return -32768, 32767, true
	case ast.TypeDInt:
		return -2147483648, 2147483647, true
	case ast.TypeLInt:
		return -9223372036854775808, 9223372036854775807, true
	case ast.TypeUSInt:
		return 0, 255, true
	case ast.TypeUInt:
		return 0, 65535, true
	case ast.TypeUDInt:
		return 0, 4294967295, true
	case ast.TypeULInt:
		return 0, 18446744073709551615, true
	}
	return 0, 0, false
}

// fitsIn reports whether an integer value is representable in t.
//
// big says v is a magnitude past what an int64 holds, so v is its
// two's-complement pattern and reads as negative -- only a type whose own
// upper bound is that large can take it, which is ULINT and nothing else.
func fitsIn(v int64, big bool, t ast.Type) bool {
	lo, hi, ok := intRange(t.Kind)
	if !ok {
		return false
	}
	if big {
		return hi > math.MaxInt64 && uint64(v) <= hi
	}
	if v < 0 {
		return v >= lo
	}
	return uint64(v) <= hi
}

// litIsBig reports whether a typed literal already holds such a value.
//
// Once a type has been chosen the type says it: only ULINT accepts a
// magnitude past int64, so an unsigned literal whose pattern reads as
// negative is one of those and nothing else can be.
func litIsBig(t ast.Type, v int64) bool {
	return v < 0 && t.IsInteger() && !t.IsSigned()
}
