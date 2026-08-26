// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// IEC's string functions, which are the reason a component can build a message
// at all: CONCAT and the rest.
//
// They are built in rather than written as a library, because each one
// produces a STRING and neither a library nor anything else in this frontend
// can. C returns no arrays, so a result has to be built in a buffer the caller
// owns -- and only the backend knows how to give each call site one of its
// own, which the nesting these appear in requires. See
// docs/dev/MODCOMPILE_ST_STDLIB.md §5.
//
// The positions are IEC's, so they are one-based: MID(s, 3, 1) is the first
// three characters. A position or a length outside the string is clamped at
// runtime and counted, on the same grounds as an array subscript -- a
// component bug must not become a daemon bug.

// strFuncSig describes one function's interface.
//
// kinds is one letter per argument: S for a STRING, I for an integer. params
// are IEC's names for them, so a call written with names is checked against
// what the standard calls them rather than quietly ignored.
type strFuncSig struct {
	fn     ast.StrFunc
	kinds  string
	params []string
	// variadic marks CONCAT, which takes two arguments or more.
	variadic bool
}

var strFuncs = map[string]strFuncSig{
	"CONCAT":  {ast.StrConcat, "SS", []string{"IN1", "IN2"}, true},
	"LEFT":    {ast.StrLeft, "SI", []string{"IN", "L"}, false},
	"RIGHT":   {ast.StrRight, "SI", []string{"IN", "L"}, false},
	"MID":     {ast.StrMid, "SII", []string{"IN", "L", "P"}, false},
	"INSERT":  {ast.StrInsert, "SSI", []string{"IN1", "IN2", "P"}, false},
	"DELETE":  {ast.StrDelete, "SII", []string{"IN", "L", "P"}, false},
	"REPLACE": {ast.StrReplace, "SSII", []string{"IN1", "IN2", "L", "P"}, false},
	"LEN":     {ast.StrLen, "S", []string{"IN"}, false},
	"FIND":    {ast.StrFind, "SS", []string{"IN1", "IN2"}, false},
}

// checkStrFunc resolves a call to one of them, or reports that this is not
// one. Like the duration conversions, it runs before the scope lookup, so a
// file declaring its own FUNCTION named CONCAT is refused as a redeclaration
// rather than silently shadowing the builtin.
func (c *checker) checkStrFunc(v *st.CallExpr, name string) (ast.Expr, bool) {
	upper := strings.ToUpper(name)
	sig, ok := strFuncs[upper]
	if !ok {
		return nil, false
	}

	if !c.checkStrArity(v, upper, sig) {
		return nil, true
	}

	out := &ast.StrFuncExpr{Pos: v.Pos, Func: sig.fn}
	for i, a := range v.Args {
		kind, param := sig.kindAt(i)
		if !c.checkStrArgName(a, upper, i, param) {
			return nil, true
		}

		var e ast.Expr
		if kind == 'S' {
			e = c.checkExpr(a.Value, ast.Type{Kind: ast.TypeString})
			if e == nil {
				return nil, true
			}
			if e.ExprType().Kind != ast.TypeString {
				c.errorf(a.Pos, "%s: %s is a STRING, got %s",
					upper, param, e.ExprType())
				return nil, true
			}
		} else {
			// An index widens into the parameter as it would for any
			// function, so LEFT(s, 3) needs no INT# on the 3.
			e = c.checkExpr(a.Value, ast.Type{Kind: ast.TypeDInt})
			if e == nil {
				return nil, true
			}
			if !e.ExprType().IsInteger() {
				c.errorf(a.Pos, "%s: %s is a number of characters, got %s",
					upper, param, e.ExprType())
				return nil, true
			}
			if n, isConst := foldInt(e); isConst && n < 0 {
				// A written-out negative is a mistake, not something the
				// runtime clamp exists for -- the same line the division
				// guard draws.
				c.errorf(a.Pos, "%s: %s is %d; a position or a length cannot be negative",
					upper, param, n)
				return nil, true
			}
		}
		out.Args = append(out.Args, e)
	}

	if sig.fn == ast.StrConcat && len(out.Args) > 2 {
		// CONCAT is variadic in IEC and binary here, so the extra arguments
		// fold into nested calls: CONCAT(a, b, c) is CONCAT(CONCAT(a, b), c),
		// which is what it means and what people write by hand anyway. Each
		// nested call gets its own buffer from the backend, so the shape costs
		// nothing beyond the intermediate it would have needed regardless.
		acc := &ast.StrFuncExpr{Pos: v.Pos, Func: ast.StrConcat, Args: out.Args[:2]}
		acc.Typ = strResultType(ast.StrConcat, acc.Args)
		for _, next := range out.Args[2:] {
			// Positioned at the argument it appends, so the three faults a
			// three-way CONCAT can raise are told apart by where they are.
			acc = &ast.StrFuncExpr{
				Pos: next.Position(), Func: ast.StrConcat, Args: []ast.Expr{acc, next},
			}
			acc.Typ = strResultType(ast.StrConcat, acc.Args)
		}
		return acc, true
	}

	out.Typ = strResultType(sig.fn, out.Args)
	return out, true
}

// kindAt is the letter and IEC name of argument i, extending CONCAT's IN1, IN2
// with IN3 and onwards.
func (s strFuncSig) kindAt(i int) (byte, string) {
	if i < len(s.params) {
		return s.kinds[i], s.params[i]
	}
	return s.kinds[len(s.kinds)-1], "IN" + strconv.Itoa(i+1)
}

// checkStrArity reports whether the call has a usable number of arguments.
func (c *checker) checkStrArity(v *st.CallExpr, upper string, sig strFuncSig) bool {
	if sig.variadic {
		if len(v.Args) < len(sig.params) {
			c.errorf(v.Pos, "%s takes %d arguments or more, got %d",
				upper, len(sig.params), len(v.Args))
			return false
		}
		return true
	}
	if len(v.Args) != len(sig.params) {
		c.errorf(v.Pos, "%s takes %d %s (%s), got %d",
			upper, len(sig.params),
			plural(len(sig.params), "argument", "arguments"),
			strings.Join(sig.params, ", "), len(v.Args))
		return false
	}
	return true
}

// checkStrArgName reports whether a named argument names the parameter it
// stands in front of.
//
// Matched by position like a function's, because reordering would need a
// temporary each to evaluate safely. A name is still checked rather than
// ignored: LEFT(L := 3, IN := s) reads as if it worked.
func (c *checker) checkStrArgName(a st.Arg, upper string, i int, param string) bool {
	if a.Form == st.ArgOutput {
		c.errorf(a.Pos, "%s has no output parameters; it returns a result", upper)
		return false
	}
	if a.Form == st.ArgInput && !ast.EqualName(a.Name, param) {
		c.errorf(a.Pos, "%s: argument %d is %s, not %s; "+
			"a string function's arguments are matched by position",
			upper, i+1, param, a.Name)
		return false
	}
	return true
}

// strResultType is what the call produces.
//
// For the ones that produce a STRING it is the buffer the backend has to
// allocate, so the length is the longest result the arguments could give --
// capped at ast.MaxStringLen, which is where CODESYS caps it too.
func strResultType(fn ast.StrFunc, args []ast.Expr) ast.Type {
	switch fn {
	case ast.StrLen, ast.StrFind:
		// A count of characters, not characters: INT is what IEC gives, and
		// MaxStringLen fits it many times over.
		return ast.Type{Kind: ast.TypeInt}
	}

	n := 0
	switch fn {
	case ast.StrConcat:
		for _, a := range args {
			n += a.ExprType().StringLen()
		}
	case ast.StrLeft, ast.StrRight, ast.StrMid:
		// A part of the string is no longer than the string, and no longer
		// than the count asked for when that is written out.
		n = args[0].ExprType().StringLen()
		if len(args) > 1 {
			if l, ok := foldInt(args[1]); ok && int(l) < n {
				n = int(l)
			}
		}
	case ast.StrDelete:
		n = args[0].ExprType().StringLen()
	case ast.StrInsert, ast.StrReplace:
		n = args[0].ExprType().StringLen() + args[1].ExprType().StringLen()
	}

	if n > ast.MaxStringLen {
		n = ast.MaxStringLen
	}
	if n < 1 {
		// LEFT(s, 0) is the empty string, which still needs its terminator.
		n = 1
	}
	return ast.Type{Kind: ast.TypeString, Len: n}
}

// ---------------------------------------------------------------------------
// The *_TO_STRING family
// ---------------------------------------------------------------------------

// Writing a value out is the other half of building a message, and it is a
// family rather than one function because IEC names the source type: a
// UINT_TO_STRING says what it takes, and passing it something else is a
// mistake caught here rather than a number that reads wrong in a log.
//
// The lengths below are the longest each type can produce. They are exact, not
// generous: the buffer holds the worst case, so no conversion in this family
// can truncate and none of them reports a fault. The one exception is a
// floating value too large to place, which says so -- see the backend.

// strFrom is one member of the family: what it takes, and the longest result
// it can give.
type strFrom struct {
	from ast.TypeKind
	// max is the result length, excluding the terminator.
	max int
}

var strFroms = map[string]strFrom{
	// "FALSE".
	"BOOL_TO_STRING": {ast.TypeBool, 5},

	// The widest each integer can be written, sign included: -128, -32768,
	// -2147483648, -9223372036854775808, and 18446744073709551615 unsigned.
	"SINT_TO_STRING":  {ast.TypeSInt, 4},
	"USINT_TO_STRING": {ast.TypeUSInt, 3},
	"INT_TO_STRING":   {ast.TypeInt, 6},
	"UINT_TO_STRING":  {ast.TypeUInt, 5},
	"DINT_TO_STRING":  {ast.TypeDInt, 11},
	"UDINT_TO_STRING": {ast.TypeUDInt, 10},
	"LINT_TO_STRING":  {ast.TypeLInt, 20},
	"ULINT_TO_STRING": {ast.TypeULInt, 20},

	// Sign, up to nineteen digits, a point and six decimals.
	"REAL_TO_STRING":  {ast.TypeReal, 32},
	"LREAL_TO_STRING": {ast.TypeLReal, 32},

	// The literal each one is written as, which is what makes a value read
	// back as the thing it came from: T#-106751d23h47m16s854ms775us808ns is
	// the longest duration an int64 of nanoseconds holds.
	"TIME_TO_STRING": {ast.TypeTime, 40},
	"DT_TO_STRING":   {ast.TypeDateTime, 40},
	"DATE_TO_STRING": {ast.TypeDate, 16},
	"TOD_TO_STRING":  {ast.TypeTimeOfDay, 24},
}

// checkStrFrom resolves one of them, or reports that this is not one.
func (c *checker) checkStrFrom(v *st.CallExpr, name string) (ast.Expr, bool) {
	upper := strings.ToUpper(name)
	sig, ok := strFroms[upper]
	if !ok {
		return nil, false
	}
	if len(v.Args) != 1 {
		c.errorf(v.Pos, "%s takes one argument, got %d", upper, len(v.Args))
		return nil, true
	}

	from := ast.Type{Kind: sig.from}
	arg := c.checkExpr(v.Args[0].Value, from)
	if arg == nil {
		return nil, true
	}
	if at := arg.ExprType(); at.Kind != sig.from {
		if at.IsTimeLike() || sig.from == ast.TypeBool || from.IsTimeLike() {
			// These would convert into each other as plain int64s and read as
			// something else entirely, so the type has to be the named one.
			c.errorf(v.Args[0].Pos, "%s takes %s, got %s", upper, from, at)
			return nil, true
		}
		// A number widens into the parameter as it would for any function, so
		// DINT_TO_STRING(anInt) needs no cast written out.
		arg = c.assign(v.Args[0].Pos, upper, from, arg)
		if arg == nil {
			return nil, true
		}
	}

	return &ast.StrFuncExpr{
		Pos: v.Pos, Func: ast.StrFrom, Args: []ast.Expr{arg},
		Typ: ast.Type{Kind: ast.TypeString, Len: sig.max},
	}, true
}
