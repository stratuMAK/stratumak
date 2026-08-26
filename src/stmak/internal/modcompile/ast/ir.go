// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package ast

import (
	"fmt"
	"strings"
)

// This file holds the intermediate representation the ST frontend lowers into
// and the C backend emits from.  See docs/dev/MODCOMPILE_ST_FRONTEND.md §5.1.
//
// Two properties matter more than the node list:
//
//   - Every expression carries a resolved type.  Not a hint threaded down from
//     the assignment target -- the actual inferred type, decided by sema.  A
//     backend that has to re-derive types ends up re-implementing the frontend
//     badly, which is exactly how the standalone prototype came to emit
//     uncompilable C for its own enum feature.
//
//   - Every reference carries a storage class.  The backend then knows whether
//     a name means a HAL pin, an inst_t member or a block-local without
//     looking anything up.
//
// The IR is a tree, not a graph: a Type names an enum, struct or function
// block by Ref rather than pointing at it.  That keeps `modcompile --parse`
// output finite and readable, and keeps a shared definition from being
// duplicated or mutated through one of several aliases.

// ---------------------------------------------------------------------------
// Nodes and positions
// ---------------------------------------------------------------------------

// Node is anything in the IR that can be blamed in a diagnostic.
type Node interface {
	Position() Pos
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// TypeKind classifies a resolved ST type.  Aliases never appear here: sema
// resolves them away, keeping the spelling only for diagnostics.
type TypeKind int

const (
	TypeInvalid TypeKind = iota

	TypeBool

	TypeSInt // 8-bit signed
	TypeInt  // 16-bit signed
	TypeDInt // 32-bit signed
	TypeLInt // 64-bit signed

	TypeUSInt // 8-bit unsigned
	TypeUInt  // 16-bit unsigned
	TypeUDInt // 32-bit unsigned
	TypeULInt // 64-bit unsigned

	TypeReal  // 32-bit float
	TypeLReal // 64-bit float

	TypeTime     // duration, nanoseconds
	TypeDateTime // a point in time, nanoseconds since 1970-01-01 UTC
	TypeString   // STRING[n]

	TypeEnum   // named enumeration; Ref is the TypeDef name
	TypeStruct // named struct; Ref is the TypeDef name
	TypeArray  // Elem and Dims describe the shape
	TypeFB     // function block instance; Ref is the FunctionBlock name
)

var typeKindNames = map[TypeKind]string{
	TypeInvalid:  "<invalid>",
	TypeBool:     "BOOL",
	TypeSInt:     "SINT",
	TypeInt:      "INT",
	TypeDInt:     "DINT",
	TypeLInt:     "LINT",
	TypeUSInt:    "USINT",
	TypeUInt:     "UINT",
	TypeUDInt:    "UDINT",
	TypeULInt:    "ULINT",
	TypeReal:     "REAL",
	TypeLReal:    "LREAL",
	TypeTime:     "TIME",
	TypeDateTime: "DATE_AND_TIME",
	TypeString:   "STRING",
	TypeEnum:     "<enum>",
	TypeStruct:   "<struct>",
	TypeArray:    "<array>",
	TypeFB:       "<function block>",
}

func (k TypeKind) String() string {
	if s, ok := typeKindNames[k]; ok {
		return s
	}
	return fmt.Sprintf("TypeKind(%d)", int(k))
}

// DefaultStringLen is the STRING length used when a declaration gives none.
const DefaultStringLen = 80

// ArrayDim is one array dimension, with the inclusive ST bounds as written.
// The bounds are kept rather than only the count because a non-zero Lo has to
// be subtracted at every index site, and because a diagnostic that says
// "index 12 outside 1..10" is worth more than one naming an internal offset.
type ArrayDim struct {
	Lo, Hi int
}

// Count is the number of elements in this dimension.
func (d ArrayDim) Count() int { return d.Hi - d.Lo + 1 }

func (d ArrayDim) String() string { return fmt.Sprintf("%d..%d", d.Lo, d.Hi) }

// Type is a resolved ST type.
type Type struct {
	Kind TypeKind

	// Ref names the TypeDef (enum, struct) or FunctionBlock this type refers
	// to.  Empty for every other kind.  Resolve it with Package.LookupType or
	// Package.LookupBlock rather than storing a pointer -- see the file
	// comment on why the IR stays a tree.
	Ref string

	// Elem and Dims describe TypeArray.  Dims is outermost first, so
	// ARRAY[0..1, 0..3] OF INT has Dims[0] = 0..1.
	Elem *Type
	Dims []ArrayDim

	// Len is the declared STRING length, excluding the terminator.  Zero means
	// the declaration gave none and DefaultStringLen applies.
	Len int

	// Alias is the name the source actually wrote, when the type reached here
	// through a TYPE alias.  Diagnostics use it so an error about a
	// Temperature reads as Temperature rather than LREAL.  Never load-bearing.
	Alias string

	// CName overrides the C spelling this type would otherwise be given.
	//
	// It is how a GMI structure reaches Structured Text: the type ST sees is
	// named as the interface names it, PktUartConfig, while the C it compiles
	// to is the one the generated header already declares.  There is no second
	// declaration to keep in step, because there is no second declaration.
	CName string
}

func (t Type) String() string {
	if t.Alias != "" {
		return t.Alias
	}
	switch t.Kind {
	case TypeEnum, TypeStruct, TypeFB:
		return t.Ref
	case TypeString:
		if t.Len > 0 {
			return fmt.Sprintf("STRING[%d]", t.Len)
		}
		return "STRING"
	case TypeArray:
		dims := make([]string, len(t.Dims))
		for i, d := range t.Dims {
			dims[i] = d.String()
		}
		elem := "<nil>"
		if t.Elem != nil {
			elem = t.Elem.String()
		}
		return fmt.Sprintf("ARRAY[%s] OF %s", strings.Join(dims, ", "), elem)
	default:
		return t.Kind.String()
	}
}

// StringLen is the effective STRING length, applying the default.
func (t Type) StringLen() int {
	if t.Kind != TypeString {
		return 0
	}
	if t.Len > 0 {
		return t.Len
	}
	return DefaultStringLen
}

// Elements is the total element count of an array across all dimensions.
func (t Type) Elements() int {
	if t.Kind != TypeArray {
		return 0
	}
	n := 1
	for _, d := range t.Dims {
		n *= d.Count()
	}
	return n
}

// IsInteger reports whether t is one of the integer types.  An enumeration is
// not an integer: it is assignment-compatible with its own constants only, and
// treating it as one is how the standalone prototype lost enum type safety.
func (t Type) IsInteger() bool {
	switch t.Kind {
	case TypeSInt, TypeInt, TypeDInt, TypeLInt,
		TypeUSInt, TypeUInt, TypeUDInt, TypeULInt:
		return true
	}
	return false
}

// IsSigned reports whether t is a signed integer.
func (t Type) IsSigned() bool {
	switch t.Kind {
	case TypeSInt, TypeInt, TypeDInt, TypeLInt:
		return true
	}
	return false
}

// IsFloat reports whether t is REAL or LREAL.
func (t Type) IsFloat() bool { return t.Kind == TypeReal || t.Kind == TypeLReal }

// IsNumeric reports whether arithmetic applies to t.
func (t Type) IsNumeric() bool { return t.IsInteger() || t.IsFloat() }

// IsScalar reports whether t occupies a single value slot -- the types that
// can appear on a HAL pin, before the HAL representability question.
func (t Type) IsScalar() bool {
	switch t.Kind {
	case TypeBool, TypeEnum, TypeTime, TypeDateTime:
		return true
	}
	return t.IsNumeric()
}

// Bits is the width of an elementary type, or 0 if the question is meaningless.
func (t Type) Bits() int {
	switch t.Kind {
	case TypeBool:
		return 1
	case TypeSInt, TypeUSInt:
		return 8
	case TypeInt, TypeUInt:
		return 16
	case TypeDInt, TypeUDInt, TypeReal, TypeEnum:
		return 32
	case TypeLInt, TypeULInt, TypeLReal, TypeTime, TypeDateTime:
		return 64
	}
	return 0
}

// CType is the C spelling used for this type in an inst_t member or a function
// block state struct.  Composite types render as their generated typedef name;
// arrays render as the element type, since C puts the extent on the declarator
// rather than the type.
func (t Type) CType() string {
	switch t.Kind {
	case TypeBool:
		return "bool"
	case TypeSInt:
		return "int8_t"
	case TypeInt:
		return "int16_t"
	case TypeDInt:
		return "int32_t"
	case TypeLInt:
		return "int64_t"
	case TypeUSInt:
		return "uint8_t"
	case TypeUInt:
		return "uint16_t"
	case TypeUDInt:
		return "uint32_t"
	case TypeULInt:
		return "uint64_t"
	case TypeReal:
		return "float"
	case TypeLReal:
		return "double"
	case TypeTime, TypeDateTime:
		return "int64_t"
	case TypeString:
		return "char"
	case TypeEnum:
		return "int32_t"
	case TypeStruct, TypeFB:
		if t.CName != "" {
			return t.CName
		}
		return t.Ref + "_t"
	case TypeArray:
		if t.Elem != nil {
			return t.Elem.CType()
		}
	}
	return "void"
}

// HALFit describes how well an ST type fits a HAL pin.  HAL has five scalar
// types and its float is a double, so several ST types reach a pin only by
// widening and two do not reach one at all.  See
// docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1.
type HALFit int

const (
	// HALFitNone: the type cannot appear on a pin at all.
	HALFitNone HALFit = iota
	// HALFitExact: round-trips through HAL without loss.
	HALFitExact
	// HALFitWiden: widened on the way to the pin, which loses nothing, and
	// narrowed back within its own range.
	HALFitWiden
	// HALFitLossy: reaches the pin losslessly but loses precision on the way
	// back, because HAL float is a double and REAL is not.  Accepted with a
	// warning naming the pin, never silently.
	HALFitLossy
)

var halFitNames = map[HALFit]string{
	HALFitNone:  "none",
	HALFitExact: "exact",
	HALFitWiden: "widened",
	HALFitLossy: "lossy",
}

func (f HALFit) String() string {
	if n, ok := halFitNames[f]; ok {
		return n
	}
	return fmt.Sprintf("HALFit(%d)", int(f))
}

// HALType maps an ST type onto a HAL pin type.  The second result says how
// exact the mapping is; on HALFitNone the first result is meaningless.
//
// An array maps to an array pin of its element's HAL type, and only in one
// dimension -- HAL pin names carry a single index.
func (t Type) HALType() (HALType, HALFit) {
	switch t.Kind {
	case TypeBool:
		return HALBit, HALFitExact
	case TypeDInt:
		return HALS32, HALFitExact
	case TypeUDInt:
		return HALU32, HALFitExact
	case TypeLReal:
		return HALFloat, HALFitExact
	case TypeSInt, TypeInt:
		return HALS32, HALFitWiden
	case TypeUSInt, TypeUInt:
		return HALU32, HALFitWiden
	case TypeEnum:
		return HALS32, HALFitWiden
	case TypeReal:
		return HALFloat, HALFitLossy
	case TypeTime:
		// HAL has no duration type.  A TIME pin is therefore an s32 of
		// milliseconds -- what a CODESYS TIME is, so a number set from outside
		// means there what it means here -- and the body still works in
		// nanoseconds, with the conversion at the boundary.  Lossy, and named
		// so: a duration finer than a millisecond does not survive the trip.
		return HALS32, HALFitLossy
	case TypeArray:
		if t.Elem == nil || len(t.Dims) != 1 {
			return 0, HALFitNone
		}
		return t.Elem.HALType()
	}
	// LINT, ULINT: no 64-bit HAL type.
	// DATE_AND_TIME, STRING, STRUCT, FB: no HAL representation at all.
	return 0, HALFitNone
}

// ---------------------------------------------------------------------------
// Storage classes
// ---------------------------------------------------------------------------

// Storage says where a referenced name lives, so the backend can emit an
// access without resolving anything itself.
type Storage int

const (
	// StorageLocal: a block-local or compiler temporary, emitted as a plain C
	// local.  Does not survive the cycle.
	StorageLocal Storage = iota
	// StoragePin: a HAL pin in inst_hal_t, reached through the convenience
	// macro cgen already emits for .comp.
	StoragePin
	// StorageParam: a HAL parameter in inst_hal_t.
	StorageParam
	// StorageVar: per-instance state in inst_t, the same place a .comp
	// `variable` declaration lands.
	StorageVar
	// StorageGlobal: a VAR_GLOBAL shared between the programs of one file.
	// Also inst_t, kept distinct so diagnostics can say which it was.
	StorageGlobal
	// StorageField: a member of the enclosing function block instance,
	// reached through that instance's own pointer rather than inst_t.
	StorageField
	// StoragePeriod: fperiod, the period of the thread this body is running
	// in.  Not storage at all, but the backend has to decide how to reach it
	// -- from the HAL function's own argument where that is in scope, and
	// from the RT task otherwise -- and this is what tells it which.
	StoragePeriod
	// StorageClock: MONOTONIC, the RT monotonic clock.  Like StoragePeriod it
	// is not storage; it tells the backend to read the clock rather than to
	// look a name up anywhere.
	StorageClock
)

var storageNames = map[Storage]string{
	StorageLocal:  "local",
	StoragePin:    "pin",
	StorageParam:  "param",
	StorageVar:    "var",
	StorageGlobal: "global",
	StorageField:  "field",
	StoragePeriod: "period",
	StorageClock:  "clock",
}

func (s Storage) String() string {
	if n, ok := storageNames[s]; ok {
		return n
	}
	return fmt.Sprintf("Storage(%d)", int(s))
}

// IsWritable reports whether a name in this storage class may be assigned.
// Every class currently may; the method exists so that the read-only cases
// coming with parameters and CONSTANT have one place to live rather than being
// spelled out at each assignment site.
func (s Storage) IsWritable() bool { return true }

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// BinOp is a binary operator.
type BinOp int

const (
	BinAdd BinOp = iota
	BinSub
	BinMul
	BinDiv
	BinMod
	BinPow
	BinEq
	BinNe
	BinLt
	BinLe
	BinGt
	BinGe
	BinAnd
	BinOr
	BinXor
)

type opSpelling struct{ st, c string }

// A C spelling of "" marks an operator with no C equivalent, which the backend
// must build another way: ** has no C operator at all.
var binOpSpelling = map[BinOp]opSpelling{
	BinAdd: {"+", "+"},
	BinSub: {"-", "-"},
	BinMul: {"*", "*"},
	BinDiv: {"/", "/"},
	BinMod: {"MOD", "%"},
	BinPow: {"**", ""},
	BinEq:  {"=", "=="},
	BinNe:  {"<>", "!="},
	BinLt:  {"<", "<"},
	BinLe:  {"<=", "<="},
	BinGt:  {">", ">"},
	BinGe:  {">=", ">="},
	BinAnd: {"AND", "&&"},
	BinOr:  {"OR", "||"},
	BinXor: {"XOR", "!="},
}

func (o BinOp) String() string {
	if s, ok := binOpSpelling[o]; ok {
		return s.st
	}
	return fmt.Sprintf("BinOp(%d)", int(o))
}

// C is the C operator, or "" when there is none and the backend must build the
// operation another way.
func (o BinOp) C() string { return binOpSpelling[o].c }

// IsComparison reports whether o yields a BOOL regardless of operand type.
func (o BinOp) IsComparison() bool {
	switch o {
	case BinEq, BinNe, BinLt, BinLe, BinGt, BinGe:
		return true
	}
	return false
}

// IsLogical reports whether o is a boolean connective.
func (o BinOp) IsLogical() bool {
	switch o {
	case BinAnd, BinOr, BinXor:
		return true
	}
	return false
}

// NeedsZeroGuard reports whether o divides by its right operand, and so needs
// the zero-divisor guard of docs/dev/MODCOMPILE_ST_FRONTEND.md §4 when the
// operands are integers.  Floating-point division is exempt: IEEE inf and NaN
// are defined results, not a fault.
func (o BinOp) NeedsZeroGuard() bool { return o == BinDiv || o == BinMod }

// UnOp is a unary operator.
type UnOp int

const (
	UnPlus UnOp = iota
	UnNeg
	UnNot
)

var unOpSpelling = map[UnOp]opSpelling{
	UnPlus: {"+", "+"},
	UnNeg:  {"-", "-"},
	UnNot:  {"NOT", "!"},
}

func (o UnOp) String() string {
	if s, ok := unOpSpelling[o]; ok {
		return s.st
	}
	return fmt.Sprintf("UnOp(%d)", int(o))
}

// C is the C operator for o.
func (o UnOp) C() string { return unOpSpelling[o].c }

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Expr is a typed ST expression.  ExprType never returns TypeInvalid on an
// expression sema accepted.
type Expr interface {
	Node
	ExprType() Type
	exprNode()
}

// IntLit is an integer literal, including one written in a base (16#FF).
type IntLit struct {
	Pos   Pos
	Value int64
	Typ   Type
}

// RealLit is a floating-point literal.
type RealLit struct {
	Pos   Pos
	Value float64
	Typ   Type
}

// BoolLit is TRUE or FALSE.
type BoolLit struct {
	Pos   Pos
	Value bool
}

// StringLit is a single-quoted string literal, already unescaped.
type StringLit struct {
	Pos   Pos
	Value string
	Typ   Type
}

// EnumLit is an enumeration constant resolved to its declaration.  Carrying
// the numeric value here is what lets the backend emit the generated macro
// rather than an undefined identifier.
type EnumLit struct {
	Pos   Pos
	Enum  string // TypeDef name
	Const string // constant name as declared
	Value int64
	Typ   Type
}

// VarRef names a declared variable, pin, parameter or function block instance.
type VarRef struct {
	Pos   Pos
	Name  string // name as declared, for diagnostics and C mangling
	Store Storage
	Typ   Type
}

// IndexExpr is an array subscript.  Indices has one entry per dimension; sema
// rejects a partial subscript rather than lowering it to pointer arithmetic.
type IndexExpr struct {
	Pos     Pos
	Base    Expr
	Indices []Expr
	Typ     Type
}

// FieldExpr is a struct field or a function block output read.
type FieldExpr struct {
	Pos   Pos
	Base  Expr
	Field string
	Typ   Type
}

// BinaryExpr applies a binary operator to two operands of the same type; sema
// inserts a ConvExpr where the source relied on implicit widening.
type BinaryExpr struct {
	Pos  Pos
	Op   BinOp
	X, Y Expr
	Typ  Type
}

// UnaryExpr applies a unary operator.
type UnaryExpr struct {
	Pos Pos
	Op  UnOp
	X   Expr
	Typ Type
}

// ConvExpr is a type conversion made explicit by sema, whether it came from an
// implicit widening rule or from a written conversion such as INT_TO_REAL.
// Making every conversion a node means the backend never has to decide whether
// one is needed, and a reader of --parse output can see where they happen.
type ConvExpr struct {
	Pos Pos
	X   Expr
	Typ Type
}

// TimeConvExpr converts between a duration and a number.
//
// It is not a ConvExpr because it is not a C cast: a duration is nanoseconds
// and the number is milliseconds or seconds, so a scale factor comes with it.
// Unit is how many nanoseconds one of the number's units is worth, and which
// way it applies follows from whether Typ is TIME.
type TimeConvExpr struct {
	Pos  Pos
	X    Expr
	Unit float64
	Typ  Type
}

// APICallExpr calls one function of a consumed GMI interface.
//
// The callbacks table and its context both live in inst_t, put there by the
// existing gmi_consume machinery, so the call site needs only the API's name
// and the function's.
type APICallExpr struct {
	Pos  Pos
	API  string
	Func string
	Args []Expr
	Typ  Type
}

// APICallStmt is such a call whose result is not used.
type APICallStmt struct {
	Pos  Pos
	Call *APICallExpr
}

// MethodCallExpr calls a METHOD on a function block instance.
//
// It is an expression because a method may return a value; one that returns
// nothing is wrapped in a MethodCallStmt instead, so the backend never has to
// decide whether to discard a result.
type MethodCallExpr struct {
	Pos Pos

	// Inst is the instance the method runs on.
	Inst Expr
	// Owner is the block Inst is an instance of, which names the C function.
	Owner  string
	Method string

	Args []Expr
	Typ  Type
}

// MethodCallStmt is a method call whose result is not used, which is the only
// form a method returning nothing may take.
type MethodCallStmt struct {
	Pos  Pos
	Call *MethodCallExpr
}

// CallExpr calls a stateless FUNCTION.  A function block invocation is a
// statement (InvokeStmt), not an expression, because it mutates instance state.
type CallExpr struct {
	Pos  Pos
	Func string
	Args []Expr
	Typ  Type
}

func (e *IntLit) Position() Pos         { return e.Pos }
func (e *RealLit) Position() Pos        { return e.Pos }
func (e *BoolLit) Position() Pos        { return e.Pos }
func (e *StringLit) Position() Pos      { return e.Pos }
func (e *EnumLit) Position() Pos        { return e.Pos }
func (e *VarRef) Position() Pos         { return e.Pos }
func (e *IndexExpr) Position() Pos      { return e.Pos }
func (e *FieldExpr) Position() Pos      { return e.Pos }
func (e *BinaryExpr) Position() Pos     { return e.Pos }
func (e *UnaryExpr) Position() Pos      { return e.Pos }
func (e *ConvExpr) Position() Pos       { return e.Pos }
func (e *TimeConvExpr) Position() Pos   { return e.Pos }
func (e *MethodCallExpr) Position() Pos { return e.Pos }
func (e *APICallExpr) Position() Pos    { return e.Pos }
func (e *CallExpr) Position() Pos       { return e.Pos }

func (e *IntLit) ExprType() Type         { return e.Typ }
func (e *RealLit) ExprType() Type        { return e.Typ }
func (e *BoolLit) ExprType() Type        { return Type{Kind: TypeBool} }
func (e *StringLit) ExprType() Type      { return e.Typ }
func (e *EnumLit) ExprType() Type        { return e.Typ }
func (e *VarRef) ExprType() Type         { return e.Typ }
func (e *IndexExpr) ExprType() Type      { return e.Typ }
func (e *FieldExpr) ExprType() Type      { return e.Typ }
func (e *BinaryExpr) ExprType() Type     { return e.Typ }
func (e *UnaryExpr) ExprType() Type      { return e.Typ }
func (e *ConvExpr) ExprType() Type       { return e.Typ }
func (e *TimeConvExpr) ExprType() Type   { return e.Typ }
func (e *MethodCallExpr) ExprType() Type { return e.Typ }
func (e *APICallExpr) ExprType() Type    { return e.Typ }
func (e *CallExpr) ExprType() Type       { return e.Typ }

func (*IntLit) exprNode()         {}
func (*RealLit) exprNode()        {}
func (*BoolLit) exprNode()        {}
func (*StringLit) exprNode()      {}
func (*EnumLit) exprNode()        {}
func (*VarRef) exprNode()         {}
func (*IndexExpr) exprNode()      {}
func (*FieldExpr) exprNode()      {}
func (*BinaryExpr) exprNode()     {}
func (*UnaryExpr) exprNode()      {}
func (*TimeConvExpr) exprNode()   {}
func (*MethodCallExpr) exprNode() {}
func (*APICallExpr) exprNode()    {}
func (*ConvExpr) exprNode()       {}
func (*CallExpr) exprNode()       {}

// IsLValue reports whether e may appear on the left of an assignment.  Only a
// reference chain qualifies: a name, a subscript of one, or a field of one.
func IsLValue(e Expr) bool {
	switch v := e.(type) {
	case *VarRef:
		return v.Store.IsWritable()
	case *IndexExpr:
		return IsLValue(v.Base)
	case *FieldExpr:
		return IsLValue(v.Base)
	}
	return false
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// Stmt is an ST statement.
type Stmt interface {
	Node
	stmtNode()
}

// AssignStmt is `Target := Value`.  Target satisfies IsLValue and Value's type
// equals Target's -- sema inserts any conversion rather than leaving it here.
type AssignStmt struct {
	Pos    Pos
	Target Expr
	Value  Expr
}

// ElseIf is one ELSIF arm.
type ElseIf struct {
	Pos  Pos
	Cond Expr
	Body []Stmt
}

// IfStmt is IF/ELSIF/ELSE/END_IF.  Else is nil when the source wrote none,
// which is distinct from an ELSE with an empty body.
type IfStmt struct {
	Pos    Pos
	Cond   Expr
	Then   []Stmt
	ElseIf []ElseIf
	Else   []Stmt
}

// CaseLabel is one CASE label: a single value when Lo equals Hi, otherwise the
// inclusive range `Lo..Hi`.
type CaseLabel struct {
	Pos    Pos
	Lo, Hi int64
}

// IsRange reports whether the label covers more than one value.
func (l CaseLabel) IsRange() bool { return l.Hi != l.Lo }

// CaseClause is one labelled arm of a CASE.
type CaseClause struct {
	Pos    Pos
	Labels []CaseLabel
	Body   []Stmt
}

// CaseStmt is CASE/OF/ELSE/END_CASE.  Labels are folded constants and sema has
// already rejected overlaps, so the backend can emit a switch directly.
type CaseStmt struct {
	Pos      Pos
	Selector Expr
	Clauses  []CaseClause
	Else     []Stmt
}

// ForStmt is a counting loop.  By is nil when the source wrote no BY, meaning a
// step of one.  Bounded by construction, which is why it carries no iteration
// cap.
type ForStmt struct {
	Pos  Pos
	Var  Expr
	From Expr
	To   Expr
	By   Expr
	Body []Stmt
}

// WhileStmt is a pre-tested loop.  MaxIter is the cap from the mandatory
// max_iterations attribute (§4): a HAL function body may not contain a loop
// whose termination the compiler cannot bound, so sema rejects the statement
// outright rather than lowering one with MaxIter of zero.
type WhileStmt struct {
	Pos     Pos
	Cond    Expr
	Body    []Stmt
	MaxIter int
}

// RepeatStmt is a post-tested loop.  MaxIter carries the same obligation as
// WhileStmt.
type RepeatStmt struct {
	Pos     Pos
	Body    []Stmt
	Cond    Expr
	MaxIter int
}

// ExitStmt leaves the innermost loop.
type ExitStmt struct {
	Pos Pos
}

// ReturnStmt leaves the enclosing program, function block or function.
type ReturnStmt struct {
	Pos Pos
}

// InvokeArg is one named argument of a function block invocation.  ST allows
// positional arguments too; sema resolves those to names here so the backend
// sees one form.
type InvokeArg struct {
	Pos   Pos
	Name  string
	Value Expr

	// Out marks the `name => target` form, which copies one of the instance's
	// outputs back into Value after the call rather than supplying an input.
	// Value is then an lvalue.
	Out bool
}

// Action is a named piece of a POU's body, sharing every variable of the POU
// that declares it and having none of its own.  See
// docs/dev/MODCOMPILE_ST_FRONTEND.md §3.4.
type Action struct {
	Pos  Pos
	Name string
	Doc  string
	Body []Stmt

	// TouchesPins records that the body reaches one of the POU's pins.  A
	// PROGRAM snapshots its pins into locals, so an action that reads one
	// needs them passed in; one that does not can take the instance alone.
	TouchesPins bool
}

// ActionCall runs one action of the enclosing POU.
//
// `Act(v := x)` is shorthand for `v := x; Act();` where v is a variable of the
// POU, so the assignments are lowered here rather than left as a second kind
// of argument binding.
type ActionCall struct {
	Pos     Pos
	Action  string
	Presets []*AssignStmt

	// Inst is the function block instance whose action this is, for a call
	// from outside the block: `motor.stop()`.  Nil for a call from within the
	// POU that declares the action, which reaches its own.
	Inst Expr

	// Owner is the block Inst is an instance of, needed to name the C
	// function.  Empty when Inst is nil.
	Owner string
}

func (s *ActionCall) Position() Pos     { return s.Pos }
func (s *MethodCallStmt) Position() Pos { return s.Pos }
func (s *APICallStmt) Position() Pos    { return s.Pos }
func (*ActionCall) stmtNode()           {}
func (*MethodCallStmt) stmtNode()       {}
func (*APICallStmt) stmtNode()          {}

// InvokeStmt calls a function block instance: `inst(a := x, b := y);`.  It is a
// statement rather than an expression because it mutates the instance; reading
// a result is a separate FieldExpr on the same instance.
type InvokeStmt struct {
	Pos  Pos
	Inst Expr
	Args []InvokeArg
}

func (s *AssignStmt) Position() Pos { return s.Pos }
func (s *IfStmt) Position() Pos     { return s.Pos }
func (s *CaseStmt) Position() Pos   { return s.Pos }
func (s *ForStmt) Position() Pos    { return s.Pos }
func (s *WhileStmt) Position() Pos  { return s.Pos }
func (s *RepeatStmt) Position() Pos { return s.Pos }
func (s *ExitStmt) Position() Pos   { return s.Pos }
func (s *ReturnStmt) Position() Pos { return s.Pos }
func (s *InvokeStmt) Position() Pos { return s.Pos }

func (*AssignStmt) stmtNode() {}
func (*IfStmt) stmtNode()     {}
func (*CaseStmt) stmtNode()   {}
func (*ForStmt) stmtNode()    {}
func (*WhileStmt) stmtNode()  {}
func (*RepeatStmt) stmtNode() {}
func (*ExitStmt) stmtNode()   {}
func (*ReturnStmt) stmtNode() {}
func (*InvokeStmt) stmtNode() {}

// ---------------------------------------------------------------------------
// ST declarations
// ---------------------------------------------------------------------------

// HALBinding maps a variable onto a HAL pin, from an `AT %I` / `%Q` / `%IO`
// clause.  Unlike a PROGRAM's VAR_INPUT, an AT variable is not written by any
// call site, so its direction is unambiguous and it may live at any depth --
// inside a function block included.  See
// docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1.
type HALBinding struct {
	Pos Pos

	// Name is the pin's own name, defaulting to the variable's.
	Name string

	Dir  PinDir
	Type HALType

	// Param marks a binding that reaches a HAL parameter rather than a pin,
	// from an `AT %MRW` / `%MR` clause.  It is the same shared memory with no
	// signal able to connect to it, so ParamDir carries HAL's own r/rw while
	// Dir stays set to the pin direction that moves the value the same way --
	// rw like an input, r like an output -- which lets everything that only
	// cares which way the value travels stay as it is.
	Param    bool
	ParamDir ParamDir
}

// FoldsAway reports whether every reference to this declaration was replaced
// by its value, so it needs no storage.  A constant that could not be reduced
// to a literal -- a STRING, say -- still does.
func (v VarDecl) FoldsAway() bool {
	if !v.Const || v.Init == nil {
		return false
	}
	switch v.Init.(type) {
	case *IntLit, *RealLit, *BoolLit, *EnumLit:
		return true
	}
	return false
}

// PinBinding maps one HAL pin onto a member of a function block instance
// somewhere inside the component.  The instance path is what distinguishes two
// instances of the same block, so it is resolved once here rather than being
// rediscovered by the backend.
type PinBinding struct {
	// Pin is the HAL pin's name, as it appears in Component.Pins.  A path
	// crossing an array of instances carries a '#' run there, exactly as an
	// array pin does.
	Pin string

	// Path is the C member path from inst_t to the variable, with an array
	// segment written as "[%d]": "r.pos", or "axes[%d].pos".
	Path string

	Dir PinDir

	// ArrayOf is the element count when the path crosses an array of
	// instances, and zero otherwise.  At most one segment may be an array,
	// because a HAL pin name carries one index.
	ArrayOf int

	// Duration marks a binding onto a TIME: milliseconds on the HAL side,
	// nanoseconds in the instance, converted where it crosses.
	Duration bool

	// Param marks a binding onto a HAL parameter rather than a pin.  The two
	// differ in inst_hal_t: a pin is a pointer into shared memory and a
	// parameter is the storage itself, so the copy is a dereference for one
	// and a plain assignment for the other.
	Param bool
}

// VarDecl is one declared name inside a VAR block of any flavour.  Which
// flavour it came from is recorded in Store, since that is what the backend
// needs; the HAL-facing ones are additionally lowered into Component.Pins so
// that pin creation, documentation and .comp share one path.
type VarDecl struct {
	Pos   Pos
	Name  string
	Typ   Type
	Store Storage
	Init  Expr // nil when the declaration gave no initialiser
	Doc   string

	// Const marks a CONSTANT declaration.  A reference to one resolves to its
	// value rather than to a read, which is what lets it bound a loop or size
	// an array.
	Const bool

	// HAL, when set, maps this variable onto a pin.  Inside a function block
	// the pin's full name is not known until the instance path is, so the
	// binding records only what the declaration said.
	HAL *HALBinding
}

// EnumConst is one enumeration constant with its resolved value.
type EnumConst struct {
	Pos   Pos
	Name  string
	Value int64
	Doc   string
}

// Field is one member of a struct type.
type Field struct {
	Pos  Pos
	Name string
	Typ  Type
	Init Expr
	Doc  string
}

// ---------------------------------------------------------------------------
// Case-insensitive lookup
// ---------------------------------------------------------------------------

// EqualName compares two ST identifiers.  ST is case-insensitive for
// identifiers and not only for keywords, so every name comparison in the
// frontend has to go through here rather than ==.
func EqualName(a, b string) bool { return strings.EqualFold(a, b) }
