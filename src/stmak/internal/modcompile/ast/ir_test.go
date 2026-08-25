// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package ast

import "testing"

// TestTypeHALMapping pins the ST-to-HAL table of
// docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1.  It is a table rather than a set of
// assertions because the interesting content is the *fit* column: which types
// reach a pin exactly, which are widened, which lose precision on the way back,
// and which cannot appear on a pin at all.  A silent change to any of those is
// a change to the language.
func TestTypeHALMapping(t *testing.T) {
	tests := []struct {
		typ  Type
		want HALType
		fit  HALFit
	}{
		{Type{Kind: TypeBool}, HALBit, HALFitExact},
		{Type{Kind: TypeDInt}, HALS32, HALFitExact},
		{Type{Kind: TypeUDInt}, HALU32, HALFitExact},
		{Type{Kind: TypeLReal}, HALFloat, HALFitExact},

		{Type{Kind: TypeSInt}, HALS32, HALFitWiden},
		{Type{Kind: TypeInt}, HALS32, HALFitWiden},
		{Type{Kind: TypeUSInt}, HALU32, HALFitWiden},
		{Type{Kind: TypeUInt}, HALU32, HALFitWiden},
		{Type{Kind: TypeEnum, Ref: "Light"}, HALS32, HALFitWiden},

		// REAL reaches the pin losslessly but not back: HAL float is a double.
		// Accepted with a warning, never silently.
		{Type{Kind: TypeReal}, HALFloat, HALFitLossy},

		// No 64-bit HAL type exists.
		{Type{Kind: TypeLInt}, 0, HALFitNone},
		{Type{Kind: TypeULInt}, 0, HALFitNone},

		// No HAL representation at all.
		{Type{Kind: TypeTime}, 0, HALFitNone},
		{Type{Kind: TypeString, Len: 10}, 0, HALFitNone},
		{Type{Kind: TypeStruct, Ref: "Point"}, 0, HALFitNone},
		{Type{Kind: TypeFB, Ref: "Ramp"}, 0, HALFitNone},

		// A one-dimensional array maps to an array pin of its element type.
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeBool}, Dims: []ArrayDim{{0, 7}}},
			HALBit, HALFitExact,
		},
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeInt}, Dims: []ArrayDim{{1, 4}}},
			HALS32, HALFitWiden,
		},
		// A HAL pin name carries one index, so a multi-dimensional array has
		// nowhere to go.
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeBool}, Dims: []ArrayDim{{0, 1}, {0, 3}}},
			0, HALFitNone,
		},
		// An array of a type that cannot reach a pin cannot either.
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeLInt}, Dims: []ArrayDim{{0, 3}}},
			0, HALFitNone,
		},
	}

	for _, tt := range tests {
		got, fit := tt.typ.HALType()
		if fit != tt.fit {
			t.Errorf("%s: fit = %v, want %v", tt.typ, fit, tt.fit)
			continue
		}
		if fit != HALFitNone && got != tt.want {
			t.Errorf("%s: HAL type = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestTypeCType(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{Type{Kind: TypeBool}, "bool"},
		{Type{Kind: TypeSInt}, "int8_t"},
		{Type{Kind: TypeInt}, "int16_t"},
		{Type{Kind: TypeDInt}, "int32_t"},
		{Type{Kind: TypeLInt}, "int64_t"},
		{Type{Kind: TypeUSInt}, "uint8_t"},
		{Type{Kind: TypeUInt}, "uint16_t"},
		{Type{Kind: TypeUDInt}, "uint32_t"},
		{Type{Kind: TypeULInt}, "uint64_t"},
		{Type{Kind: TypeReal}, "float"},
		{Type{Kind: TypeLReal}, "double"},
		{Type{Kind: TypeTime}, "int64_t"},
		{Type{Kind: TypeString}, "char"},
		{Type{Kind: TypeEnum, Ref: "Light"}, "int32_t"},
		{Type{Kind: TypeStruct, Ref: "Point"}, "Point_t"},
		{Type{Kind: TypeFB, Ref: "Ramp"}, "Ramp_t"},
		// C puts the extent on the declarator, so an array renders as its
		// element type and cgen appends the brackets.
		{Type{Kind: TypeArray, Elem: &Type{Kind: TypeLReal}, Dims: []ArrayDim{{0, 3}}}, "double"},
		{Type{Kind: TypeInvalid}, "void"},
	}
	for _, tt := range tests {
		if got := tt.typ.CType(); got != tt.want {
			t.Errorf("%s: CType() = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestTypeString(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{Type{Kind: TypeLReal}, "LREAL"},
		{Type{Kind: TypeString}, "STRING"},
		{Type{Kind: TypeString, Len: 10}, "STRING[10]"},
		{Type{Kind: TypeEnum, Ref: "Light"}, "Light"},
		{Type{Kind: TypeFB, Ref: "Ramp"}, "Ramp"},
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeInt}, Dims: []ArrayDim{{1, 10}}},
			"ARRAY[1..10] OF INT",
		},
		{
			Type{Kind: TypeArray, Elem: &Type{Kind: TypeReal}, Dims: []ArrayDim{{0, 2}, {0, 3}}},
			"ARRAY[0..2, 0..3] OF REAL",
		},
		// An alias reports the name the source wrote: a diagnostic about a
		// Temperature should say Temperature, not LREAL.
		{Type{Kind: TypeLReal, Alias: "Temperature"}, "Temperature"},
	}
	for _, tt := range tests {
		if got := tt.typ.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestTypePredicates(t *testing.T) {
	// An enumeration is deliberately not an integer.  Treating it as one is
	// how the standalone prototype lost enum type safety, so the distinction
	// is asserted rather than left to the reader of IsInteger.
	enum := Type{Kind: TypeEnum, Ref: "Light"}
	if enum.IsInteger() {
		t.Error("enumeration reports as an integer type")
	}
	if !enum.IsScalar() {
		t.Error("enumeration should be scalar")
	}
	if enum.IsNumeric() {
		t.Error("enumeration should not be numeric")
	}

	if !(Type{Kind: TypeInt}).IsSigned() {
		t.Error("INT should be signed")
	}
	if (Type{Kind: TypeUInt}).IsSigned() {
		t.Error("UINT should not be signed")
	}
	if !(Type{Kind: TypeReal}).IsFloat() || !(Type{Kind: TypeLReal}).IsFloat() {
		t.Error("REAL and LREAL should both be floats")
	}
	if (Type{Kind: TypeString}).IsScalar() {
		t.Error("STRING should not be scalar")
	}

	for _, tc := range []struct {
		typ  Type
		bits int
	}{
		{Type{Kind: TypeBool}, 1},
		{Type{Kind: TypeSInt}, 8},
		{Type{Kind: TypeInt}, 16},
		{Type{Kind: TypeDInt}, 32},
		{Type{Kind: TypeReal}, 32},
		{Type{Kind: TypeLInt}, 64},
		{Type{Kind: TypeLReal}, 64},
		{Type{Kind: TypeTime}, 64},
		{Type{Kind: TypeString}, 0},
	} {
		if got := tc.typ.Bits(); got != tc.bits {
			t.Errorf("%s: Bits() = %d, want %d", tc.typ, got, tc.bits)
		}
	}
}

func TestArrayShape(t *testing.T) {
	// A non-zero lower bound is kept as written, because it has to be
	// subtracted at every index site and because diagnostics quote it.
	d := ArrayDim{Lo: 1, Hi: 10}
	if d.Count() != 10 {
		t.Errorf("Count() = %d, want 10", d.Count())
	}
	if d.String() != "1..10" {
		t.Errorf("String() = %q, want %q", d.String(), "1..10")
	}

	multi := Type{
		Kind: TypeArray,
		Elem: &Type{Kind: TypeReal},
		Dims: []ArrayDim{{0, 2}, {0, 3}},
	}
	if got := multi.Elements(); got != 12 {
		t.Errorf("Elements() = %d, want 12", got)
	}
	if got := (Type{Kind: TypeInt}).Elements(); got != 0 {
		t.Errorf("Elements() on a non-array = %d, want 0", got)
	}
}

func TestStringLenDefault(t *testing.T) {
	if got := (Type{Kind: TypeString}).StringLen(); got != DefaultStringLen {
		t.Errorf("undeclared STRING length = %d, want %d", got, DefaultStringLen)
	}
	if got := (Type{Kind: TypeString, Len: 10}).StringLen(); got != 10 {
		t.Errorf("STRING[10] length = %d, want 10", got)
	}
	if got := (Type{Kind: TypeInt}).StringLen(); got != 0 {
		t.Errorf("StringLen() on a non-string = %d, want 0", got)
	}
}

func TestOperatorSpelling(t *testing.T) {
	// ** has no C operator; the backend has to build it another way, and an
	// empty C() is how it finds out.
	if BinPow.C() != "" {
		t.Errorf("BinPow.C() = %q, want empty", BinPow.C())
	}
	if BinPow.String() != "**" {
		t.Errorf("BinPow.String() = %q, want %q", BinPow.String(), "**")
	}

	// XOR on booleans is != in C, which is correct only because both operands
	// are already normalised to 0/1.
	if BinXor.C() != "!=" {
		t.Errorf("BinXor.C() = %q, want %q", BinXor.C(), "!=")
	}
	if BinNe.C() != "!=" {
		t.Errorf("BinNe.C() = %q, want %q", BinNe.C(), "!=")
	}

	if !BinLt.IsComparison() || BinAdd.IsComparison() {
		t.Error("IsComparison misclassified")
	}
	if !BinAnd.IsLogical() || BinAdd.IsLogical() {
		t.Error("IsLogical misclassified")
	}

	// Only the two dividing operators need the §4 zero guard.
	for op, want := range map[BinOp]bool{
		BinDiv: true, BinMod: true,
		BinAdd: false, BinMul: false, BinPow: false,
	} {
		if got := op.NeedsZeroGuard(); got != want {
			t.Errorf("%s.NeedsZeroGuard() = %v, want %v", op, got, want)
		}
	}

	if UnNot.C() != "!" || UnNot.String() != "NOT" {
		t.Errorf("UnNot spelling wrong: %q / %q", UnNot.String(), UnNot.C())
	}
}

func TestIsLValue(t *testing.T) {
	v := &VarRef{Name: "x", Store: StorageVar, Typ: Type{Kind: TypeInt}}
	arr := &VarRef{
		Name:  "a",
		Store: StorageVar,
		Typ:   Type{Kind: TypeArray, Elem: &Type{Kind: TypeInt}, Dims: []ArrayDim{{0, 3}}},
	}

	// A reference chain is assignable however deeply it nests: the whole point
	// of the check is that `pts[i].x := 1.0` works, which the standalone
	// prototype could not express at all.
	nested := &FieldExpr{
		Base:  &IndexExpr{Base: arr, Indices: []Expr{v}, Typ: Type{Kind: TypeStruct, Ref: "Point"}},
		Field: "x",
		Typ:   Type{Kind: TypeLReal},
	}

	for _, e := range []Expr{v, arr, nested} {
		if !IsLValue(e) {
			t.Errorf("%T should be an lvalue", e)
		}
	}
	for _, e := range []Expr{
		&IntLit{Value: 1},
		&BinaryExpr{Op: BinAdd, X: v, Y: v},
		&CallExpr{Func: "f"},
		&EnumLit{Enum: "Light", Const: "RED"},
	} {
		if IsLValue(e) {
			t.Errorf("%T should not be an lvalue", e)
		}
	}
}

func TestExprTypeIsCarried(t *testing.T) {
	// Every expression reports its own resolved type rather than deferring to
	// context.  This is the property the whole IR exists for.
	lreal := Type{Kind: TypeLReal}
	boolean := Type{Kind: TypeBool}

	cases := []struct {
		expr Expr
		want Type
	}{
		{&IntLit{Value: 1, Typ: Type{Kind: TypeDInt}}, Type{Kind: TypeDInt}},
		{&RealLit{Value: 1.5, Typ: lreal}, lreal},
		{&BoolLit{Value: true}, boolean},
		{&StringLit{Value: "x", Typ: Type{Kind: TypeString, Len: 4}}, Type{Kind: TypeString, Len: 4}},
		{&EnumLit{Enum: "Light", Const: "RED", Typ: Type{Kind: TypeEnum, Ref: "Light"}}, Type{Kind: TypeEnum, Ref: "Light"}},
		{&VarRef{Name: "x", Typ: lreal}, lreal},
		{&ConvExpr{X: &IntLit{Value: 1}, Typ: lreal}, lreal},
		{&BinaryExpr{Op: BinLt, Typ: boolean}, boolean},
		{&UnaryExpr{Op: UnNeg, Typ: lreal}, lreal},
		{&CallExpr{Func: "AverageVal", Typ: lreal}, lreal},
	}
	for _, c := range cases {
		if got := c.expr.ExprType(); got.Kind != c.want.Kind || got.Ref != c.want.Ref || got.Len != c.want.Len {
			t.Errorf("%T: ExprType() = %s, want %s", c.expr, got, c.want)
		}
	}
}

func TestNodePositionsAreReported(t *testing.T) {
	pos := Pos{File: "ramplimit.st", Line: 12, Col: 3}

	exprs := []Expr{
		&IntLit{Pos: pos}, &RealLit{Pos: pos}, &BoolLit{Pos: pos}, &StringLit{Pos: pos},
		&EnumLit{Pos: pos}, &VarRef{Pos: pos}, &IndexExpr{Pos: pos}, &FieldExpr{Pos: pos},
		&BinaryExpr{Pos: pos}, &UnaryExpr{Pos: pos}, &ConvExpr{Pos: pos}, &CallExpr{Pos: pos},
	}
	for _, e := range exprs {
		if e.Position() != pos {
			t.Errorf("%T: Position() = %v, want %v", e, e.Position(), pos)
		}
	}

	stmts := []Stmt{
		&AssignStmt{Pos: pos}, &IfStmt{Pos: pos}, &CaseStmt{Pos: pos}, &ForStmt{Pos: pos},
		&WhileStmt{Pos: pos}, &RepeatStmt{Pos: pos}, &ExitStmt{Pos: pos},
		&ReturnStmt{Pos: pos}, &InvokeStmt{Pos: pos},
	}
	for _, s := range stmts {
		if s.Position() != pos {
			t.Errorf("%T: Position() = %v, want %v", s, s.Position(), pos)
		}
	}

	if pos.String() != "ramplimit.st:12:3" {
		t.Errorf("Pos.String() = %q", pos.String())
	}
}

func TestCaseLabelRange(t *testing.T) {
	if (CaseLabel{Lo: 3, Hi: 3}).IsRange() {
		t.Error("a single-value label reports as a range")
	}
	if !(CaseLabel{Lo: 1, Hi: 5}).IsRange() {
		t.Error("a range label reports as single-valued")
	}
}

func TestEqualNameIsCaseInsensitive(t *testing.T) {
	// ST is case-insensitive for identifiers, not only for keywords.  Every
	// name comparison in the frontend goes through EqualName for that reason.
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"maxRate", "MAXRATE", true},
		{"Ramp", "ramp", true},
		{"out", "out", true},
		{"out", "output", false},
		{"", "", true},
	} {
		if got := EqualName(tc.a, tc.b); got != tc.want {
			t.Errorf("EqualName(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStorageString(t *testing.T) {
	for s, want := range map[Storage]string{
		StorageLocal:  "local",
		StoragePin:    "pin",
		StorageParam:  "param",
		StorageVar:    "var",
		StorageGlobal: "global",
		StorageField:  "field",
		StoragePeriod: "period",
	} {
		if got := s.String(); got != want {
			t.Errorf("Storage(%d).String() = %q, want %q", int(s), got, want)
		}
	}
	if got := Storage(99).String(); got != "Storage(99)" {
		t.Errorf("unknown storage renders as %q", got)
	}
}
