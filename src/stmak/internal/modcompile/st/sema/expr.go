// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// checkExpr resolves and types one expression.  want is the type the context
// asks for; it is used only to give an untyped literal a type and to make a
// diagnostic say what was expected, never to paper over a mismatch.  A nil
// result means the expression was rejected and a diagnostic was reported.
func (c *checker) checkExpr(e st.Expr, want ast.Type) ast.Expr {
	switch v := e.(type) {
	case *st.IntLit:
		return c.checkIntLit(v, want)
	case *st.RealLit:
		return c.checkRealLit(v, want)
	case *st.BoolLit:
		return &ast.BoolLit{Pos: v.Pos, Value: v.Value}
	case *st.StringLit:
		return c.checkStringLit(v)
	case *st.TypedLit:
		return c.checkTypedLit(v)
	case *st.Ident:
		return c.checkIdent(v)
	case *st.BinaryExpr:
		return c.checkBinary(v, want)
	case *st.UnaryExpr:
		return c.checkUnary(v, want)
	case *st.IndexExpr:
		return c.checkIndex(v)
	case *st.FieldExpr:
		return c.checkField(v)
	case *st.CallExpr:
		return c.checkCall(v)
	case *st.DerefExpr:
		c.errorf(v.Pos, "pointer dereference is not supported; stratuMAK ST has no pointers. "+
			"Pass the value through a VAR_IN_OUT, which is the IEC way to reach a caller's data")
		return nil
	case *st.BitExpr:
		c.errorf(v.Pos, "partial access (.%d) needs the bit-string types BYTE, WORD, DWORD "+
			"and LWORD, which are not implemented yet", v.Bit)
		return nil
	case *st.StructLit:
		c.errorf(v.Pos, "a structured initialiser is not supported; "+
			"assign the members separately in the body")
		return nil
	}
	c.errorf(e.Position(), "unsupported expression")
	return nil
}

// checkIntLit types an integer literal.  A literal has no type of its own: it
// adapts to its context when it fits, so `x : SINT := 1` needs no ceremony and
// `x : SINT := 300` is refused with the range that made it impossible.
func (c *checker) checkIntLit(v *st.IntLit, want ast.Type) ast.Expr {
	if want.IsInteger() {
		if !fitsIn(v.Value, want) {
			lo, hi, _ := intRange(want.Kind)
			c.errorf(v.Pos, "%s is out of range for %s (%d to %d)", v.Text, want, lo, hi)
			return nil
		}
		return &ast.IntLit{Pos: v.Pos, Value: v.Value, Typ: want}
	}
	if want.IsFloat() {
		return &ast.RealLit{Pos: v.Pos, Value: float64(v.Value), Typ: want}
	}
	// No usable context: DINT is the default integer, as in most ST dialects.
	return &ast.IntLit{Pos: v.Pos, Value: v.Value, Typ: ast.Type{Kind: ast.TypeDInt}}
}

func (c *checker) checkRealLit(v *st.RealLit, want ast.Type) ast.Expr {
	typ := ast.Type{Kind: ast.TypeLReal}
	if want.IsFloat() {
		typ = want
	} else if want.IsInteger() {
		c.errorf(v.Pos, "%s is a real literal but %s is an integer type; "+
			"a fractional value cannot be assigned without rounding it explicitly", v.Text, want)
		return nil
	}
	return &ast.RealLit{Pos: v.Pos, Value: v.Value, Typ: typ}
}

func (c *checker) checkStringLit(v *st.StringLit) ast.Expr {
	if v.Wide {
		c.errorf(v.Pos, "wide string literals are not supported; WSTRING is not implemented")
		return nil
	}
	return &ast.StringLit{
		Pos:   v.Pos,
		Value: v.Value,
		Typ:   ast.Type{Kind: ast.TypeString, Len: len(v.Value)},
	}
}

// checkTypedLit types a literal written with an explicit prefix: DINT#42,
// LREAL#1.5, BOOL#TRUE.
func (c *checker) checkTypedLit(v *st.TypedLit) ast.Expr {
	upper := strings.ToUpper(v.Prefix)

	if why, ok := deferredTypes[upper]; ok {
		c.errorf(v.Pos, "%s", why)
		return nil
	}
	if isDateTimePrefix(upper) {
		c.errorf(v.Pos, "%s literals are not supported; "+
			"duration and date types arrive with the standard function block library", v.Prefix)
		return nil
	}

	kind, ok := lookupElementary(v.Prefix)
	if !ok {
		c.errorf(v.Pos, "unknown type %s in the literal %s#%s", v.Prefix, v.Prefix, v.Body)
		return nil
	}
	typ := ast.Type{Kind: kind}

	if kind == ast.TypeBool {
		switch strings.ToUpper(v.Body) {
		case "TRUE", "1":
			return &ast.BoolLit{Pos: v.Pos, Value: true}
		case "FALSE", "0":
			return &ast.BoolLit{Pos: v.Pos, Value: false}
		}
		c.errorf(v.Pos, "BOOL#%s is not a boolean value; write BOOL#TRUE or BOOL#FALSE", v.Body)
		return nil
	}

	if typ.IsFloat() {
		f, err := strconv.ParseFloat(strings.ReplaceAll(v.Body, "_", ""), 64)
		if err != nil {
			c.errorf(v.Pos, "%s is not a valid %s value", v.Body, v.Prefix)
			return nil
		}
		return &ast.RealLit{Pos: v.Pos, Value: f, Typ: typ}
	}

	n, err := parseIntBody(v.Body)
	if err != nil {
		c.errorf(v.Pos, "%s is not a valid %s value", v.Body, v.Prefix)
		return nil
	}
	if !fitsIn(n, typ) {
		lo, hi, _ := intRange(kind)
		c.errorf(v.Pos, "%s is out of range for %s (%d to %d)", v.Body, v.Prefix, lo, hi)
		return nil
	}
	return &ast.IntLit{Pos: v.Pos, Value: n, Typ: typ}
}

// parseIntBody reads the numeric body of a typed literal, which may itself be
// written in a base: UDINT#16#DEAD_BEEF.
func parseIntBody(body string) (int64, error) {
	body = strings.ReplaceAll(body, "_", "")
	if i := strings.IndexByte(body, '#'); i >= 0 {
		radix, err := strconv.Atoi(body[:i])
		if err != nil || radix < 2 || radix > 36 {
			return 0, fmt.Errorf("bad radix")
		}
		return strconv.ParseInt(body[i+1:], radix, 64)
	}
	return strconv.ParseInt(body, 10, 64)
}

// isDateTimePrefix mirrors the scanner's table.  Duplicated rather than
// exported because the two ask different questions: the scanner needs to know
// how far the literal runs, sema needs to know whether it can type it.
func isDateTimePrefix(upper string) bool {
	switch upper {
	case "T", "TIME", "LT", "LTIME", "D", "DATE", "LD", "LDATE",
		"TOD", "TIME_OF_DAY", "LTOD", "LTIME_OF_DAY",
		"DT", "DATE_AND_TIME", "LDT", "LDATE_AND_TIME":
		return true
	}
	return false
}

// checkIdent resolves a name.
func (c *checker) checkIdent(v *st.Ident) ast.Expr {
	sym := c.scope.lookup(v.Name)
	if sym == nil {
		// fperiod exists in a PROGRAM and a FUNCTION_BLOCK but not here, and
		// "not declared" would send the reader looking for a typo.
		if strings.EqualFold(v.Name, "fperiod") {
			c.errorf(v.Pos, "fperiod is not available in a FUNCTION: a stateless function that "+
				"depended on the rate of whatever thread called it would not be stateless. "+
				"Take the cycle time as an input, as IEC's own DERIVATIVE and INTEGRAL do")
			return nil
		}
		c.errorf(v.Pos, "%s is not declared", v.Name)
		return nil
	}

	switch sym.Kind {
	case symVar, symBuiltin:
		// A CONSTANT resolves to its value rather than to a read.  That is
		// what lets it bound a FOR, size an array or label a CASE arm -- all
		// of which need a value the compiler can see, and real ST names its
		// bounds far more often than it writes them out.
		if sym.Const && sym.ConstExpr != nil {
			if lit := cloneConst(sym.ConstExpr, v.Pos); lit != nil {
				return lit
			}
		}
		return &ast.VarRef{Pos: v.Pos, Name: sym.Name, Store: sym.Store, Typ: sym.Type}

	case symEnumConst:
		if len(sym.Ambiguous) > 0 {
			c.errorf(v.Pos, "%s is a constant of %s and of %s; write it qualified, as %s.%s",
				v.Name, sym.EnumType, strings.Join(sym.Ambiguous, " and of "),
				sym.EnumType, sym.Name)
			return nil
		}
		// Carrying the value here is what lets the backend emit the generated
		// constant rather than an undefined identifier -- the defect that made
		// the standalone prototype's own enum example uncompilable.
		return &ast.EnumLit{
			Pos:   v.Pos,
			Enum:  sym.EnumType,
			Const: sym.Name,
			Value: sym.ConstValue,
			Typ:   sym.Type,
		}
	}

	c.errorf(v.Pos, "%s is a %s, not a value", v.Name, sym.Kind)
	return nil
}

// foldToConst reduces a constant's initialiser to a single literal.
//
// A constant is remembered as its value, not as the expression that produced
// it: `MINUTES_PER_DAY : UINT := (24 * 60)` has to be usable as an array bound
// and a loop limit, and only a literal folds at the use site.
func foldToConst(e ast.Expr, typ ast.Type) ast.Expr {
	if e == nil {
		return nil
	}
	switch v := e.(type) {
	case *ast.BoolLit, *ast.EnumLit:
		return v.(ast.Expr)
	case *ast.StringLit:
		return v
	}
	if typ.IsFloat() {
		if f, ok := foldFloat(e); ok {
			return &ast.RealLit{Pos: e.Position(), Value: f, Typ: typ}
		}
		return nil
	}
	if n, ok := foldInt(e); ok {
		return &ast.IntLit{Pos: e.Position(), Value: n, Typ: typ}
	}
	if f, ok := foldFloat(e); ok {
		return &ast.RealLit{Pos: e.Position(), Value: f, Typ: typ}
	}
	return nil
}

// cloneConst copies a folded constant for one use site.
//
// Copying matters: coerce retypes a literal in place when it widens, so
// handing out the same node twice would let one use change another's type.
// A constant that is not one of these forms is not folded, and the reference
// stays a read.
func cloneConst(e ast.Expr, pos ast.Pos) ast.Expr {
	switch v := e.(type) {
	case *ast.IntLit:
		return &ast.IntLit{Pos: pos, Value: v.Value, Typ: v.Typ}
	case *ast.RealLit:
		return &ast.RealLit{Pos: pos, Value: v.Value, Typ: v.Typ}
	case *ast.BoolLit:
		return &ast.BoolLit{Pos: pos, Value: v.Value}
	case *ast.EnumLit:
		return &ast.EnumLit{Pos: pos, Enum: v.Enum, Const: v.Const, Value: v.Value, Typ: v.Typ}
	}
	return nil
}

// qualifiedEnum resolves `EnumName.CONSTANT`.
func (c *checker) qualifiedEnum(v *st.FieldExpr, typeName string) ast.Expr {
	td := c.pkg.LookupType(typeName)
	if td == nil {
		return nil
	}
	if td.Kind != ast.DefEnum {
		c.errorf(v.Pos, "%s is a %s, not an enumeration, so %s.%s names nothing",
			typeName, td.Kind, typeName, v.Name)
		return nil
	}
	k, ok := td.LookupConst(v.Name)
	if !ok {
		c.errorf(v.Pos, "enumeration %s has no constant %s", typeName, v.Name)
		return nil
	}
	return &ast.EnumLit{
		Pos:   v.Pos,
		Enum:  td.Name,
		Const: k.Name,
		Value: k.Value,
		Typ:   ast.Type{Kind: ast.TypeEnum, Ref: td.Name},
	}
}

// checkIndex resolves an array subscript.
//
// ST spells a multi-dimensional access two ways -- `a[i, j]` and `a[i][j]` --
// and the parser keeps whichever the source wrote.  Both mean one access, so
// the whole chain is collected before anything is checked and the backend sees
// a single node either way.
func (c *checker) checkIndex(v *st.IndexExpr) ast.Expr {
	// Walk down to the innermost base, collecting subscript groups.
	var groups [][]st.Expr
	base := st.Expr(v)
	for {
		ix, ok := base.(*st.IndexExpr)
		if !ok {
			break
		}
		groups = append([][]st.Expr{ix.Indices}, groups...)
		base = ix.Base
	}

	arr := c.checkExpr(base, ast.Type{})
	if arr == nil {
		return nil
	}
	arrType := arr.ExprType()
	if arrType.Kind != ast.TypeArray {
		c.errorf(v.Pos, "%s is not an array", exprName(arr, arrType))
		return nil
	}

	var indices []ast.Expr
	for _, group := range groups {
		for _, ix := range group {
			dim := len(indices)
			if dim >= len(arrType.Dims) {
				c.errorf(ix.Position(), "too many subscripts for %s: it has %d %s",
					arrType, len(arrType.Dims),
					plural(len(arrType.Dims), "dimension", "dimensions"))
				return nil
			}
			e := c.checkExpr(ix, ast.Type{Kind: ast.TypeDInt})
			if e == nil {
				return nil
			}
			if !e.ExprType().IsInteger() {
				c.errorf(ix.Position(), "an array index must be an integer, got %s", e.ExprType())
				return nil
			}
			// An index the compiler can see is settled now: an out-of-range
			// constant is a mistake, not something to clamp at run time.
			if n, ok := foldInt(e); ok {
				d := arrType.Dims[dim]
				if n < int64(d.Lo) || n > int64(d.Hi) {
					c.errorf(ix.Position(), "index %d is outside %s of %s", n, d, arrType)
					return nil
				}
			}
			indices = append(indices, e)
		}
	}

	// A partial subscript would name a sub-array, which has nowhere to go:
	// stratuMAK ST has no array-valued expressions.
	if len(indices) != len(arrType.Dims) {
		c.errorf(v.Pos, "%s needs %d %s, got %d",
			arrType, len(arrType.Dims),
			plural(len(arrType.Dims), "subscript", "subscripts"), len(indices))
		return nil
	}

	return &ast.IndexExpr{Pos: v.Pos, Base: arr, Indices: indices, Typ: *arrType.Elem}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// exprName describes an expression for a diagnostic, preferring its name.
func exprName(e ast.Expr, t ast.Type) string {
	if ref, ok := e.(*ast.VarRef); ok {
		return ref.Name
	}
	return "this expression (" + t.String() + ")"
}

// checkField resolves a member access.
//
// A base naming an enumeration is a qualified constant -- EN_STATE.OFF -- which
// is how IEC 61131-3 3rd edition and CODESYS disambiguate two enumerations that
// share a constant name.  It has to be tried before the base is resolved as a
// value, because a type is not one.
func (c *checker) checkField(v *st.FieldExpr) ast.Expr {
	if ref, ok := v.Base.(*st.Ident); ok {
		sym := c.scope.lookup(ref.Name)
		if sym != nil && sym.Kind == symType {
			return c.qualifiedEnum(v, sym.Name)
		}
		// A variable named after its own enumeration is idiomatic -- `mode :
		// Mode` -- and ST is case-insensitive, so the variable shadows the
		// type.  `Mode.OFF` still means the constant: an enumeration has no
		// instance members, so there is no other reading to lose.  A struct is
		// deliberately not treated this way, since `p.x` on a `p : Point` does
		// mean the field.
		if td := c.pkg.LookupType(ref.Name); td != nil && td.Kind == ast.DefEnum {
			if _, isConst := td.LookupConst(v.Name); isConst {
				return c.qualifiedEnum(v, td.Name)
			}
		}
	}

	base := c.checkExpr(v.Base, ast.Type{})
	if base == nil {
		return nil
	}
	bt := base.ExprType()

	if bt.Kind == ast.TypeFB {
		def := c.pkg.LookupBlock(bt.Ref)
		if def == nil {
			return nil
		}
		decl, found, public := c.blockFieldType(def, v.Name)
		if !found {
			c.errorf(v.Pos, "%s has no member %s; its interface is: %s",
				bt.Ref, v.Name, blockNames(def))
			return nil
		}
		if !public {
			// A block's internal VARs are private: exposing them would make
			// every instance's implementation part of its interface.
			c.errorf(v.Pos, "%s is internal to %s; only its VAR_INPUT, VAR_OUTPUT "+
				"and VAR_IN_OUT are readable from outside", v.Name, bt.Ref)
			return nil
		}
		return &ast.FieldExpr{Pos: v.Pos, Base: base, Field: decl.Name, Typ: decl.Typ}
	}
	if bt.Kind != ast.TypeStruct {
		c.errorf(v.Pos, "%s is not a structure, so it has no field %s", exprName(base, bt), v.Name)
		return nil
	}

	td := c.pkg.LookupType(bt.Ref)
	if td == nil {
		c.errorf(v.Pos, "unknown structure %s", bt.Ref)
		return nil
	}
	f, ok := td.LookupField(v.Name)
	if !ok {
		c.errorf(v.Pos, "%s has no field %s", bt.Ref, v.Name)
		return nil
	}
	// The declared spelling, not what the source wrote: ST is case-insensitive
	// but the generated C is not.
	return &ast.FieldExpr{Pos: v.Pos, Base: base, Field: f.Name, Typ: f.Typ}
}

// checkBinary types a binary operator, inserting the conversions that make its
// operands agree.
func (c *checker) checkBinary(v *st.BinaryExpr, want ast.Type) ast.Expr {
	// Only a comparison or a connective imposes its own operand type; for
	// arithmetic the context type is a useful hint for untyped literals.
	hint := want
	if v.Op.IsComparison() {
		hint = ast.Type{}
	} else if v.Op.IsLogical() {
		hint = ast.Type{Kind: ast.TypeBool}
	}

	x := c.checkExpr(v.X, hint)
	if x == nil {
		return nil
	}
	// The left operand's type is the better hint for the right one: it is
	// what makes `total := total + 1` give the literal total's type.
	rhint := hint
	if !v.Op.IsComparison() && !v.Op.IsLogical() {
		rhint = x.ExprType()
	} else if v.Op.IsComparison() {
		rhint = x.ExprType()
	}
	y := c.checkExpr(v.Y, rhint)
	if y == nil {
		return nil
	}

	xt, yt := x.ExprType(), y.ExprType()

	if v.Op.IsLogical() {
		if xt.Kind != ast.TypeBool || yt.Kind != ast.TypeBool {
			c.errorf(v.Pos, "%s needs BOOL operands, got %s and %s. "+
				"Bitwise %s on integers arrives with the bit-string types in phase 4",
				v.Op, xt, yt, v.Op)
			return nil
		}
		return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: ast.Type{Kind: ast.TypeBool}}
	}

	common, ok := unify(xt, yt)
	if !ok {
		c.errorf(v.Pos, "cannot apply %s to %s and %s", v.Op, xt, yt)
		return nil
	}
	x = c.coerce(x, common)
	y = c.coerce(y, common)

	if v.Op.IsComparison() {
		if common.Kind == ast.TypeString {
			c.errorf(v.Pos, "strings cannot be compared with %s yet", v.Op)
			return nil
		}
		return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: ast.Type{Kind: ast.TypeBool}}
	}

	// Arithmetic.
	if !common.IsNumeric() {
		c.errorf(v.Pos, "%s cannot be applied to %s", v.Op, common)
		return nil
	}
	if v.Op == ast.BinMod && !common.IsInteger() {
		c.errorf(v.Pos, "MOD needs integer operands, got %s", common)
		return nil
	}

	// A constant zero divisor is a compile error rather than a fault at
	// runtime: the guard of §4 exists for divisors that are not knowable
	// here, not for one written out.
	if v.Op.NeedsZeroGuard() {
		if n, ok := foldInt(y); ok && n == 0 {
			c.errorf(v.Y.Position(), "division by zero")
			return nil
		}
		if f, ok := foldFloat(y); ok && f == 0 && common.IsFloat() && v.Op == ast.BinDiv {
			c.warnf("%s: division by the constant 0.0 yields infinity", v.Y.Position())
		}
	}

	typ := common
	if v.Op == ast.BinPow {
		// Exponentiation goes through the C library, which works in doubles;
		// an integer power would silently truncate.
		typ = ast.Type{Kind: ast.TypeLReal}
		x = c.coerce(x, typ)
		y = c.coerce(y, typ)
	}
	return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: typ}
}

func (c *checker) checkUnary(v *st.UnaryExpr, want ast.Type) ast.Expr {
	hint := want
	if v.Op == ast.UnNot {
		hint = ast.Type{Kind: ast.TypeBool}
	}
	x := c.checkExpr(v.X, hint)
	if x == nil {
		return nil
	}
	xt := x.ExprType()

	switch v.Op {
	case ast.UnNot:
		if xt.Kind != ast.TypeBool {
			c.errorf(v.Pos, "NOT needs a BOOL operand, got %s", xt)
			return nil
		}
		return &ast.UnaryExpr{Pos: v.Pos, Op: v.Op, X: x, Typ: ast.Type{Kind: ast.TypeBool}}

	case ast.UnNeg, ast.UnPlus:
		if !xt.IsNumeric() {
			c.errorf(v.Pos, "unary %s needs a numeric operand, got %s", v.Op, xt)
			return nil
		}
		if v.Op == ast.UnNeg && xt.IsInteger() && !xt.IsSigned() {
			c.errorf(v.Pos, "cannot negate %s: it is unsigned", xt)
			return nil
		}
		if v.Op == ast.UnPlus {
			// Unary plus is a no-op; drop it rather than carrying a node
			// the backend would have to special-case.
			return x
		}
		return &ast.UnaryExpr{Pos: v.Pos, Op: v.Op, X: x, Typ: xt}
	}

	c.errorf(v.Pos, "unsupported unary operator %s", v.Op)
	return nil
}

// coerce wraps e in a conversion when its type is not already t.  Every
// conversion is an explicit node, so the backend never has to decide whether
// one is needed and a reader of --parse output can see where they happen.
func (c *checker) coerce(e ast.Expr, t ast.Type) ast.Expr {
	if e == nil || sameType(e.ExprType(), t) {
		return e
	}
	// Retype a literal in place rather than wrapping it: `1.0 + x` should
	// emit the literal at the right type, not a cast of a double.
	switch lit := e.(type) {
	case *ast.IntLit:
		if t.IsInteger() && fitsIn(lit.Value, t) {
			lit.Typ = t
			return lit
		}
		if t.IsFloat() {
			return &ast.RealLit{Pos: lit.Pos, Value: float64(lit.Value), Typ: t}
		}
	case *ast.RealLit:
		if t.IsFloat() {
			lit.Typ = t
			return lit
		}
	}
	return &ast.ConvExpr{Pos: e.Position(), X: e, Typ: t}
}

// assign checks that value may be assigned to a target of type want, inserting
// any widening conversion.  It is the one place narrowing is refused.
func (c *checker) assign(pos ast.Pos, what string, want ast.Type, value ast.Expr) ast.Expr {
	if value == nil {
		return nil
	}
	got := value.ExprType()
	switch convert(got, want) {
	case convIdentical:
		return value
	case convWiden:
		return c.coerce(value, want)
	}

	// Say why, because the reason decides the fix.
	switch {
	case got.IsInteger() && want.IsInteger() && got.IsSigned() != want.IsSigned():
		c.errorf(pos, "cannot assign %s to %s (%s): signed and unsigned do not convert implicitly",
			got, what, want)
	case got.IsNumeric() && want.IsNumeric() && got.Bits() > want.Bits():
		c.errorf(pos, "cannot assign %s to %s (%s): the value would be truncated", got, what, want)
	case got.Kind == ast.TypeBool || want.Kind == ast.TypeBool:
		c.errorf(pos, "cannot assign %s to %s (%s): BOOL is a truth value, not a number", got, what, want)
	default:
		c.errorf(pos, "cannot assign %s to %s (%s)", got, what, want)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Constant folding
// ---------------------------------------------------------------------------

// foldInt evaluates a constant integer expression.
func foldInt(e ast.Expr) (int64, bool) {
	switch v := e.(type) {
	case *ast.IntLit:
		return v.Value, true
	case *ast.EnumLit:
		// An enumeration constant is a compile-time value, which is what lets
		// it initialise a variable and label a CASE arm.
		return v.Value, true
	case *ast.ConvExpr:
		return foldInt(v.X)
	case *ast.UnaryExpr:
		x, ok := foldInt(v.X)
		if !ok {
			return 0, false
		}
		if v.Op == ast.UnNeg {
			return -x, true
		}
		return x, true
	case *ast.BinaryExpr:
		x, okx := foldInt(v.X)
		y, oky := foldInt(v.Y)
		if !okx || !oky {
			return 0, false
		}
		switch v.Op {
		case ast.BinAdd:
			return x + y, true
		case ast.BinSub:
			return x - y, true
		case ast.BinMul:
			return x * y, true
		case ast.BinDiv:
			if y == 0 {
				return 0, false
			}
			return x / y, true
		case ast.BinMod:
			if y == 0 {
				return 0, false
			}
			return x % y, true
		}
	}
	return 0, false
}

// foldFloat evaluates a constant floating-point expression.
func foldFloat(e ast.Expr) (float64, bool) {
	switch v := e.(type) {
	case *ast.RealLit:
		return v.Value, true
	case *ast.IntLit:
		return float64(v.Value), true
	case *ast.ConvExpr:
		return foldFloat(v.X)
	case *ast.UnaryExpr:
		x, ok := foldFloat(v.X)
		if !ok {
			return 0, false
		}
		if v.Op == ast.UnNeg {
			return -x, true
		}
		return x, true
	case *ast.BinaryExpr:
		x, okx := foldFloat(v.X)
		y, oky := foldFloat(v.Y)
		if !okx || !oky {
			return 0, false
		}
		switch v.Op {
		case ast.BinAdd:
			return x + y, true
		case ast.BinSub:
			return x - y, true
		case ast.BinMul:
			return x * y, true
		case ast.BinDiv:
			if y == 0 {
				return 0, false
			}
			return x / y, true
		}
	}
	return 0, false
}

// foldToC renders a constant expression as a C initialiser, which is the form
// ast.Pin.Default and ast.Variable.Default take.
func (c *checker) foldToC(e ast.Expr) (string, bool) {
	if e == nil {
		return "", false
	}
	switch v := e.(type) {
	case *ast.BoolLit:
		if v.Value {
			return "true", true
		}
		return "false", true
	case *ast.StringLit:
		return strconv.Quote(v.Value), true
	}
	if n, ok := foldInt(e); ok {
		return strconv.FormatInt(n, 10), true
	}
	if f, ok := foldFloat(e); ok {
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		if e.ExprType().Kind == ast.TypeReal {
			s += "f"
		}
		return s, true
	}
	return "", false
}
