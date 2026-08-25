// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// The syntax tree records what the source wrote, not what it means.  Type
// references are names with decoration and nothing is resolved; expressions
// carry no types; an argument list has not been matched against a signature.
// Sema turns this into the shared IR in internal/modcompile/ast.
//
// Keeping the two apart is what lets the parser accept a file whose names do
// not resolve -- which most of the imported IronPLC corpus is, deliberately --
// and lets sema report a resolution failure with a position rather than the
// parser guessing.

// ---------------------------------------------------------------------------
// File and declarations
// ---------------------------------------------------------------------------

// File is one parsed .st source.
type File struct {
	Pos ast.Pos

	// Attrs are the pragmas that preceded the first declaration, which carry
	// the component's own metadata: summary, licence, author.
	Attrs []Attribute

	Types    []*TypeDecl
	Globals  []*VarBlock
	Blocks   []*FunctionBlockDecl
	Funcs    []*FunctionDecl
	Programs []*ProgramDecl
}

// Attribute is one `{attribute 'name' := 'value'}` pragma.
type Attribute struct {
	Pos   ast.Pos
	Name  string
	Value string
}

// TypeDecl is one entry of a TYPE ... END_TYPE block.  Exactly one of Alias,
// Enum and Struct is set.
type TypeDecl struct {
	Pos   ast.Pos
	Attrs []Attribute
	Name  string

	Alias  *TypeRef     // TYPE Celsius : LREAL; END_TYPE
	Enum   []EnumMember // TYPE Light : (RED, GREEN); END_TYPE
	Struct []*FieldDecl // TYPE Point : STRUCT ... END_STRUCT; END_TYPE

	// Init is the type's default value, from `... := INFO`.
	Init Expr
}

// EnumMember is one enumeration constant, with the explicit value the source
// gave it if any.
type EnumMember struct {
	Pos   ast.Pos
	Name  string
	Value Expr // nil when the source gave no explicit value
}

// FieldDecl is one member of a STRUCT.
type FieldDecl struct {
	Pos   ast.Pos
	Attrs []Attribute
	Name  string
	Type  *TypeRef
	Init  Expr
}

// VarKind is which VAR section a declaration came from.
type VarKind int

const (
	VarLocal VarKind = iota
	VarInput
	VarOutput
	VarInOut
	VarGlobal
	VarTemp
	VarExternal
)

var varKindNames = map[VarKind]string{
	VarLocal:    "VAR",
	VarInput:    "VAR_INPUT",
	VarOutput:   "VAR_OUTPUT",
	VarInOut:    "VAR_IN_OUT",
	VarGlobal:   "VAR_GLOBAL",
	VarTemp:     "VAR_TEMP",
	VarExternal: "VAR_EXTERNAL",
}

func (k VarKind) String() string {
	if n, ok := varKindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("VarKind(%d)", int(k))
}

// IsHAL reports whether this section is the one that becomes HAL pins on a
// PROGRAM.  See docs/dev/MODCOMPILE_ST_FRONTEND.md §2.
func (k VarKind) IsHAL() bool {
	return k == VarInput || k == VarOutput || k == VarInOut
}

// VarBlock is one VAR ... END_VAR section.
type VarBlock struct {
	Pos       ast.Pos
	Kind      VarKind
	Constant  bool
	Retain    bool
	NonRetain bool
	Decls     []*VarDecl
}

// EdgeKind is the R_EDGE / F_EDGE qualifier on a BOOL input.
type EdgeKind int

const (
	EdgeNone EdgeKind = iota
	EdgeRising
	EdgeFalling
)

// VarDecl is one declaration line.  ST allows several names to share a type
// and an initialiser, as in `PI, G : BOOL R_EDGE;`, so Names is a list.
type VarDecl struct {
	Pos   ast.Pos
	Attrs []Attribute
	Names []string
	Type  *TypeRef
	Init  Expr
	Edge  EdgeKind

	// Located is the direct address from an `AT %IX1` clause, kept as written.
	// stratuMAK maps I/O through VAR_INPUT/VAR_OUTPUT onto HAL pins instead,
	// so sema rejects this; the parser accepts it to give that rejection a
	// position and a name.
	Located string
}

// ActionDecl is a named piece of a POU's body: `ACTION name: ... END_ACTION`.
//
// It shares every variable of the POU that declares it and has none of its
// own, which is what distinguishes it from a FUNCTION_BLOCK -- there is no
// state to lay out and no interface to pass through.  IEC 61131-3 defines it
// for SFC steps; in ST-only code it is how a long cyclic body is split into
// named pieces.
type ActionDecl struct {
	Pos   ast.Pos
	Attrs []Attribute
	Name  string
	Body  []Stmt
}

// ProgramDecl is a PROGRAM, which becomes one exported HAL function.
type ProgramDecl struct {
	Pos     ast.Pos
	Attrs   []Attribute
	Name    string
	Vars    []*VarBlock
	Body    []Stmt
	Actions []*ActionDecl
}

// FunctionBlockDecl is a FUNCTION_BLOCK: a reusable sub-component with state.
type FunctionBlockDecl struct {
	Pos     ast.Pos
	Attrs   []Attribute
	Name    string
	Vars    []*VarBlock
	Body    []Stmt
	Actions []*ActionDecl
}

// FunctionDecl is a FUNCTION: stateless, returns a value, callable from an
// expression.
type FunctionDecl struct {
	Pos    ast.Pos
	Attrs  []Attribute
	Name   string
	Result *TypeRef
	Vars   []*VarBlock
	Body   []Stmt
}

// ---------------------------------------------------------------------------
// Type references
// ---------------------------------------------------------------------------

// TypeRef is a type as written: a name, possibly decorated with a string
// length, a subrange or array dimensions.  Nothing here is resolved -- Name
// may be an elementary type, a TYPE alias, or a function block.
type TypeRef struct {
	Pos ast.Pos

	// Name is the type's spelling, empty for an array (see Dims and Elem).
	Name string

	// StringLen is the `n` of STRING[n], nil when absent.
	StringLen Expr

	// Subrange is the `(lo..hi)` of a subrange type such as `INT (1..2)`.
	Subrange *Range

	// Dims and Elem describe ARRAY[...] OF Elem.  Dims is non-nil exactly when
	// this is an array.
	Dims []Range
	Elem *TypeRef

	// Pointer marks `POINTER TO Elem` or `REFERENCE TO Elem`.  stratuMAK
	// implements neither, so sema refuses it -- but only once it has parsed,
	// which is what lets the refusal name the construct instead of failing on
	// the word TO.
	Pointer bool
}

// IsArray reports whether this reference is an array type.
func (t *TypeRef) IsArray() bool { return t != nil && t.Dims != nil }

// Range is an inclusive `lo..hi` pair, used for array dimensions and
// subranges.  The bounds are expressions because ST permits constants there;
// sema folds them.
type Range struct {
	Pos    ast.Pos
	Lo, Hi Expr
}

func (t *TypeRef) String() string {
	if t == nil {
		return "<nil>"
	}
	if t.IsArray() {
		return fmt.Sprintf("ARRAY[%d dims] OF %s", len(t.Dims), t.Elem)
	}
	if t.StringLen != nil {
		return t.Name + "[...]"
	}
	return t.Name
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// Stmt is a statement in the syntax tree.
type Stmt interface {
	ast.Node
	stmtNode()
}

// AssignStmt is `Target := Value`.  Whether Target is assignable is sema's
// question, not the parser's.
type AssignStmt struct {
	Pos    ast.Pos
	Target Expr
	Value  Expr
}

// CallStmt is a call used as a statement: `fb(a := x);` or `f(x);`.  At this
// level a function block invocation and a discarded function call look the
// same, so both land here and sema tells them apart.
type CallStmt struct {
	Pos    ast.Pos
	Target Expr
	Args   []Arg
}

// IfStmt is IF/ELSIF/ELSE/END_IF.  HasElse distinguishes a written but empty
// ELSE from no ELSE at all, which sema needs for exhaustiveness.
type IfStmt struct {
	Pos     ast.Pos
	Cond    Expr
	Then    []Stmt
	ElseIfs []ElseIf
	Else    []Stmt
	HasElse bool
}

// ElseIf is one ELSIF arm.
type ElseIf struct {
	Pos  ast.Pos
	Cond Expr
	Body []Stmt
}

// CaseLabel is one label of a CASE arm: a single value, an inclusive range, or
// an enumeration constant named as an identifier.
type CaseLabel struct {
	Pos    ast.Pos
	Value  Expr
	High   Expr // non-nil for `lo..hi`
	IsElse bool
}

// CaseClause is one labelled arm.
type CaseClause struct {
	Pos    ast.Pos
	Labels []CaseLabel
	Body   []Stmt
}

// CaseStmt is CASE/OF/ELSE/END_CASE.
type CaseStmt struct {
	Pos      ast.Pos
	Selector Expr
	Clauses  []CaseClause
	Else     []Stmt
	HasElse  bool
}

// ForStmt is a counting loop.  By is nil when the source wrote no BY.
type ForStmt struct {
	Pos  ast.Pos
	Var  Expr
	From Expr
	To   Expr
	By   Expr
	Body []Stmt
}

// WhileStmt is a pre-tested loop.  Attrs carries any pragma that preceded it,
// which is where the mandatory max_iterations bound of
// docs/dev/MODCOMPILE_ST_FRONTEND.md §4 arrives; enforcing that it is present
// is sema's job, not the parser's.
type WhileStmt struct {
	Pos   ast.Pos
	Attrs []Attribute
	Cond  Expr
	Body  []Stmt
}

// RepeatStmt is a post-tested loop, with the same bound obligation as
// WhileStmt.
type RepeatStmt struct {
	Pos   ast.Pos
	Attrs []Attribute
	Body  []Stmt
	Cond  Expr
}

// ExitStmt leaves the innermost loop.
type ExitStmt struct{ Pos ast.Pos }

// ContinueStmt starts the next iteration of the innermost loop.
type ContinueStmt struct{ Pos ast.Pos }

// ReturnStmt leaves the enclosing body.
type ReturnStmt struct{ Pos ast.Pos }

// EmptyStmt is a stray semicolon.  It is kept rather than dropped so that
// `VAL2: ;` in a CASE arm round-trips and so a doubled `;;` has somewhere to
// hang a diagnostic later.
type EmptyStmt struct{ Pos ast.Pos }

func (s *AssignStmt) Position() ast.Pos   { return s.Pos }
func (s *CallStmt) Position() ast.Pos     { return s.Pos }
func (s *IfStmt) Position() ast.Pos       { return s.Pos }
func (s *CaseStmt) Position() ast.Pos     { return s.Pos }
func (s *ForStmt) Position() ast.Pos      { return s.Pos }
func (s *WhileStmt) Position() ast.Pos    { return s.Pos }
func (s *RepeatStmt) Position() ast.Pos   { return s.Pos }
func (s *ExitStmt) Position() ast.Pos     { return s.Pos }
func (s *ContinueStmt) Position() ast.Pos { return s.Pos }
func (s *ReturnStmt) Position() ast.Pos   { return s.Pos }
func (s *EmptyStmt) Position() ast.Pos    { return s.Pos }

func (*AssignStmt) stmtNode()   {}
func (*CallStmt) stmtNode()     {}
func (*IfStmt) stmtNode()       {}
func (*CaseStmt) stmtNode()     {}
func (*ForStmt) stmtNode()      {}
func (*WhileStmt) stmtNode()    {}
func (*RepeatStmt) stmtNode()   {}
func (*ExitStmt) stmtNode()     {}
func (*ContinueStmt) stmtNode() {}
func (*ReturnStmt) stmtNode()   {}
func (*EmptyStmt) stmtNode()    {}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Expr is an expression in the syntax tree.  It carries no type: that is
// exactly what sema adds when it lowers into the shared IR.
type Expr interface {
	ast.Node
	exprNode()
}

// Ident is a bare name.  It may turn out to be a variable, an enumeration
// constant, a function or a function block instance; the parser does not know
// and does not guess.
type Ident struct {
	Pos  ast.Pos
	Name string
}

// IntLit is an integer literal.  Base records the radix as written so a
// diagnostic can quote the source spelling.
type IntLit struct {
	Pos   ast.Pos
	Text  string
	Value int64
	Base  int
}

// RealLit is a floating-point literal.
type RealLit struct {
	Pos   ast.Pos
	Text  string
	Value float64
}

// BoolLit is TRUE or FALSE.
type BoolLit struct {
	Pos   ast.Pos
	Value bool
}

// StringLit is a quoted literal, already unescaped.  Wide marks a
// double-quoted WSTRING.
type StringLit struct {
	Pos   ast.Pos
	Value string
	Wide  bool
}

// TypedLit is a literal written with an explicit type prefix: BYTE#32,
// LREAL#11.0, T#100ms, DATE#2024-01-20.  The body is kept as text because what
// it means depends on the prefix, and several of those prefixes name types
// stratuMAK defers; sema interprets it and reports the ones it will not take.
type TypedLit struct {
	Pos    ast.Pos
	Prefix string
	Body   string
}

// BinaryExpr applies a binary operator.
type BinaryExpr struct {
	Pos  ast.Pos
	Op   ast.BinOp
	X, Y Expr
}

// UnaryExpr applies a unary operator.
type UnaryExpr struct {
	Pos ast.Pos
	Op  ast.UnOp
	X   Expr
}

// IndexExpr is a subscript.  ST allows both `a[i, j]` and `a[i][j]`; the
// parser normalises the chained form into successive IndexExpr nodes and
// leaves the comma form as one node with several indices, so sema sees the
// distinction the source made.
type IndexExpr struct {
	Pos     ast.Pos
	Base    Expr
	Indices []Expr
}

// FieldExpr is `Base.Name`: a struct member or a function block output.
type FieldExpr struct {
	Pos  ast.Pos
	Base Expr
	Name string
}

// CallExpr is `Func(args)`.
type CallExpr struct {
	Pos  ast.Pos
	Func Expr
	Args []Arg
}

// BitExpr is `Base.N`, the Nth bit of an integer.  IEC 61131-3 spells partial
// access this way, and real code uses it heavily -- so it parses, and sema
// refuses it by name until the bit-string types exist.
type BitExpr struct {
	Pos  ast.Pos
	Base Expr
	Bit  int64
}

// FieldInit is one `name := value` of a structured initialiser.
type FieldInit struct {
	Pos   ast.Pos
	Name  string
	Value Expr
}

// StructLit is a structured initialiser: `(a := 1, b := 2)`.  It appears where
// a value is expected, so the parser has to tell it from a parenthesised
// expression, which it does by looking for `name :=` after the paren.
//
// An array of them is written `( (a := 1), (a := 2) )`, and lands in Elems
// rather than Fields.  Exactly one of the two is set.
type StructLit struct {
	Pos    ast.Pos
	Fields []FieldInit
	Elems  []*StructLit
}

// DerefExpr is `Base^`, a pointer or reference dereference.  stratuMAK
// implements neither, so sema rejects it; the parser accepts it so that the
// rejection names the construct instead of being a syntax error on '^'.
type DerefExpr struct {
	Pos  ast.Pos
	Base Expr
}

// ArgForm is how an argument was bound at a call site.
type ArgForm int

const (
	// ArgPositional: passed by position, `f(x)`.
	ArgPositional ArgForm = iota
	// ArgInput: bound to a named input, `fb(a := x)`.
	ArgInput
	// ArgOutput: bound to a named output, `fb(q => y)`.
	ArgOutput
)

// Arg is one argument at a call site.
type Arg struct {
	Pos   ast.Pos
	Form  ArgForm
	Name  string // empty when Form is ArgPositional
	Value Expr
}

func (e *Ident) Position() ast.Pos      { return e.Pos }
func (e *IntLit) Position() ast.Pos     { return e.Pos }
func (e *RealLit) Position() ast.Pos    { return e.Pos }
func (e *BoolLit) Position() ast.Pos    { return e.Pos }
func (e *StringLit) Position() ast.Pos  { return e.Pos }
func (e *TypedLit) Position() ast.Pos   { return e.Pos }
func (e *BinaryExpr) Position() ast.Pos { return e.Pos }
func (e *UnaryExpr) Position() ast.Pos  { return e.Pos }
func (e *IndexExpr) Position() ast.Pos  { return e.Pos }
func (e *FieldExpr) Position() ast.Pos  { return e.Pos }
func (e *CallExpr) Position() ast.Pos   { return e.Pos }
func (e *DerefExpr) Position() ast.Pos  { return e.Pos }
func (e *BitExpr) Position() ast.Pos    { return e.Pos }
func (e *StructLit) Position() ast.Pos  { return e.Pos }

func (*Ident) exprNode()      {}
func (*IntLit) exprNode()     {}
func (*RealLit) exprNode()    {}
func (*BoolLit) exprNode()    {}
func (*StringLit) exprNode()  {}
func (*TypedLit) exprNode()   {}
func (*BinaryExpr) exprNode() {}
func (*UnaryExpr) exprNode()  {}
func (*IndexExpr) exprNode()  {}
func (*FieldExpr) exprNode()  {}
func (*CallExpr) exprNode()   {}
func (*DerefExpr) exprNode()  {}
func (*BitExpr) exprNode()    {}
func (*StructLit) exprNode()  {}
