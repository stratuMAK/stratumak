// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// mustParse parses src and fails the test on any diagnostic.
func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse("t.st", src)
	if err != nil {
		t.Fatalf("parse failed:\n%v\n--- source ---\n%s", err, src)
	}
	return f
}

// parseErrors parses src and returns the diagnostics, failing if there are none.
func parseErrors(t *testing.T, src string) ErrorList {
	t.Helper()
	_, err := Parse("t.st", src)
	if err == nil {
		t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", src)
	}
	list, ok := err.(ErrorList)
	if !ok {
		t.Fatalf("error is %T, want ErrorList", err)
	}
	return list
}

// wrapFB puts body inside a minimal FUNCTION_BLOCK so a statement can be
// parsed on its own.
func wrapFB(body string) string {
	return "FUNCTION_BLOCK B\n" + body + "\nEND_FUNCTION_BLOCK"
}

// firstStmt parses body inside a function block and returns its first statement.
func firstStmt(t *testing.T, body string) Stmt {
	t.Helper()
	f := mustParse(t, wrapFB(body))
	if len(f.Blocks) != 1 {
		t.Fatalf("got %d function blocks, want 1", len(f.Blocks))
	}
	if len(f.Blocks[0].Body) == 0 {
		t.Fatal("function block body is empty")
	}
	return f.Blocks[0].Body[0]
}

// exprString renders an expression as a fully parenthesised string, which is
// how the precedence tests read the shape the parser built.
func exprString(e Expr) string {
	switch v := e.(type) {
	case nil:
		return "<nil>"
	case *Ident:
		return v.Name
	case *IntLit:
		return fmt.Sprintf("%d", v.Value)
	case *RealLit:
		return fmt.Sprintf("%g", v.Value)
	case *BoolLit:
		if v.Value {
			return "TRUE"
		}
		return "FALSE"
	case *StringLit:
		return fmt.Sprintf("'%s'", v.Value)
	case *TypedLit:
		return v.Prefix + "#" + v.Body
	case *BinaryExpr:
		return fmt.Sprintf("(%s %s %s)", exprString(v.X), v.Op, exprString(v.Y))
	case *UnaryExpr:
		return fmt.Sprintf("(%s %s)", v.Op, exprString(v.X))
	case *IndexExpr:
		parts := make([]string, len(v.Indices))
		for i, ix := range v.Indices {
			parts[i] = exprString(ix)
		}
		return fmt.Sprintf("%s[%s]", exprString(v.Base), strings.Join(parts, ", "))
	case *FieldExpr:
		return exprString(v.Base) + "." + v.Name
	case *CallExpr:
		parts := make([]string, len(v.Args))
		for i, a := range v.Args {
			switch a.Form {
			case ArgInput:
				parts[i] = a.Name + " := " + exprString(a.Value)
			case ArgOutput:
				parts[i] = a.Name + " => " + exprString(a.Value)
			default:
				parts[i] = exprString(a.Value)
			}
		}
		return fmt.Sprintf("%s(%s)", exprString(v.Func), strings.Join(parts, ", "))
	case *DerefExpr:
		return exprString(v.Base) + "^"
	}
	return fmt.Sprintf("<%T>", e)
}

// assignValue parses `x := <expr>;` and renders the right-hand side.
func assignValue(t *testing.T, expr string) string {
	t.Helper()
	s := firstStmt(t, "x := "+expr+";")
	as, ok := s.(*AssignStmt)
	if !ok {
		t.Fatalf("got %T, want an assignment", s)
	}
	return exprString(as.Value)
}

// TestExprPrecedence is the reason expressions.st was imported: a hand-written
// recursive-descent parser gets precedence wrong quietly, and the result still
// compiles.
func TestExprPrecedence(t *testing.T) {
	tests := []struct{ src, want string }{
		// Arithmetic.
		{"1 + 2 * 3", "(1 + (2 * 3))"},
		{"1 * 2 + 3", "((1 * 2) + 3)"},
		{"1 - 2 - 3", "((1 - 2) - 3)"}, // left-associative
		{"1 + 2 MOD 3", "(1 + (2 MOD 3))"},

		// ** binds tighter than * and /, and is right-associative.
		{"2 ** 3 ** 2", "(2 ** (3 ** 2))"},
		{"2 * 3 ** 2", "(2 * (3 ** 2))"},

		// Unary minus binds looser than ** on the left operand: -2**2 is
		// -(2**2), not (-2)**2.
		{"- 2 ** 2", "(- (2 ** 2))"},
		{"NOT a AND b", "((NOT a) AND b)"},

		// Comparison is looser than arithmetic, equality looser than
		// comparison, then AND, XOR, OR.
		{"a + 1 < b", "((a + 1) < b)"},
		{"a < b = c", "((a < b) = c)"},
		{"a AND b OR c", "((a AND b) OR c)"},
		{"a OR b AND c", "(a OR (b AND c))"},
		{"a AND b XOR c", "((a AND b) XOR c)"},
		{"a XOR b OR c", "((a XOR b) OR c)"},
		{"a & b", "(a AND b)"}, // & is a synonym for AND

		// Parentheses override.
		{"(1 + 2) * 3", "((1 + 2) * 3)"},
		{"NOT (a OR b)", "(NOT (a OR b))"},

		// The two lines from expressions.st that motivated all of the above.
		{"- 1 + 2 * 3 - 4 / 6 MOD 7 ** 8",
			"(((- 1) + (2 * 3)) - ((4 / 6) MOD (7 ** 8)))"},
		{"1 >= 2 <= 3 <> 4 XOR 5",
			"((((1 >= 2) <= 3) <> 4) XOR 5)"},
	}
	for _, tt := range tests {
		if got := assignValue(t, tt.src); got != tt.want {
			t.Errorf("%s\n  got  %s\n  want %s", tt.src, got, tt.want)
		}
	}
}

func TestExprPostfix(t *testing.T) {
	tests := []struct{ src, want string }{
		{"a[1]", "a[1]"},
		{"a[i, j]", "a[i, j]"},
		// ST spells a multi-dimensional access both ways; the parser keeps the
		// distinction the source made and lets sema reconcile them.
		{"arr[idx][idx]", "arr[idx][idx]"},
		{"p.x", "p.x"},
		{"rect.topLeft.y", "rect.topLeft.y"},
		{"pts[i].x", "pts[i].x"},
		{"f(1, 2)", "f(1, 2)"},
		{"f()", "f()"},
		{"fb.out", "fb.out"},
		{"INT_TO_REAL(a + b)", "INT_TO_REAL((a + b))"},
	}
	for _, tt := range tests {
		if got := assignValue(t, tt.src); got != tt.want {
			t.Errorf("%s\n  got  %s\n  want %s", tt.src, got, tt.want)
		}
	}
}

func TestExprLiterals(t *testing.T) {
	tests := []struct{ src, want string }{
		{"42", "42"},
		{"16#FF", "255"},
		{"2#1010", "10"},
		{"1.5", "1.5"},
		{"TRUE", "TRUE"},
		{"FALSE", "FALSE"},
		{"'text'", "'text'"},
		{"BYTE#32", "BYTE#32"},
		{"t#1s", "t#1s"},
		{"DATE#2024-01-20", "DATE#2024-01-20"},
	}
	for _, tt := range tests {
		if got := assignValue(t, tt.src); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.src, got, tt.want)
		}
	}
}

func TestParseStatements(t *testing.T) {
	// IF with every arm.
	s := firstStmt(t, `
IF a THEN
  x := 1;
ELSIF b THEN
  x := 2;
ELSIF c THEN
  x := 3;
ELSE
  x := 4;
END_IF;`)
	ifs, ok := s.(*IfStmt)
	if !ok {
		t.Fatalf("got %T, want an IF", s)
	}
	if len(ifs.ElseIfs) != 2 {
		t.Errorf("got %d ELSIF arms, want 2", len(ifs.ElseIfs))
	}
	if !ifs.HasElse || len(ifs.Else) != 1 {
		t.Errorf("ELSE arm not recorded: HasElse=%v len=%d", ifs.HasElse, len(ifs.Else))
	}

	// An IF with no ELSE must be distinguishable from one with an empty ELSE,
	// which is what sema needs for exhaustiveness.
	s = firstStmt(t, "IF a THEN x := 1; END_IF;")
	if ifs := s.(*IfStmt); ifs.HasElse {
		t.Error("IF without ELSE reports HasElse")
	}
	s = firstStmt(t, "IF a THEN x := 1; ELSE END_IF;")
	if ifs := s.(*IfStmt); !ifs.HasElse || len(ifs.Else) != 0 {
		t.Error("IF with an empty ELSE not distinguished from no ELSE")
	}

	// FOR, with and without BY.
	s = firstStmt(t, "FOR i := 1 TO 10 BY 2 DO x := i; END_FOR;")
	fs, ok := s.(*ForStmt)
	if !ok {
		t.Fatalf("got %T, want a FOR", s)
	}
	if fs.By == nil {
		t.Error("BY step not recorded")
	}
	s = firstStmt(t, "FOR i := 1 TO 10 DO x := i; END_FOR;")
	if fs := s.(*ForStmt); fs.By != nil {
		t.Error("absent BY should leave the step nil, meaning one")
	}

	// WHILE and REPEAT.
	if _, ok := firstStmt(t, "WHILE a DO x := 1; END_WHILE;").(*WhileStmt); !ok {
		t.Error("WHILE not parsed")
	}
	rs, ok := firstStmt(t, "REPEAT x := 1; UNTIL a END_REPEAT;").(*RepeatStmt)
	if !ok {
		t.Fatal("REPEAT not parsed")
	}
	if rs.Cond == nil || len(rs.Body) != 1 {
		t.Error("REPEAT body or condition missing")
	}

	// Loop control and early return.
	if _, ok := firstStmt(t, "WHILE a DO EXIT; END_WHILE;").(*WhileStmt); !ok {
		t.Error("EXIT inside WHILE not parsed")
	}
	if _, ok := firstStmt(t, "RETURN;").(*ReturnStmt); !ok {
		t.Error("RETURN not parsed")
	}
}

// TestParseCaseClauseBoundaries covers the lookahead a CASE needs: an arm ends
// where the next label begins, and `VAL2: x := 1;` and `x := 1;` agree on
// their first token, so the parser has to speculate and rewind.
func TestParseCaseClauseBoundaries(t *testing.T) {
	s := firstStmt(t, `
CASE sel OF
  1: x := 1;
  2, 3: x := 2;
  4..6: x := 3;
  7: x := 4;
       y := 5;
ELSE
  x := 0;
END_CASE;`)
	cs, ok := s.(*CaseStmt)
	if !ok {
		t.Fatalf("got %T, want a CASE", s)
	}
	if len(cs.Clauses) != 4 {
		t.Fatalf("got %d clauses, want 4", len(cs.Clauses))
	}
	if n := len(cs.Clauses[1].Labels); n != 2 {
		t.Errorf("clause 2 has %d labels, want 2", n)
	}
	if cs.Clauses[2].Labels[0].High == nil {
		t.Error("range label 4..6 lost its upper bound")
	}
	// The last arm has two statements: the arm must not stop at the first
	// semicolon, only at the next label.
	if n := len(cs.Clauses[3].Body); n != 2 {
		t.Errorf("last clause has %d statements, want 2", n)
	}
	if !cs.HasElse || len(cs.Else) != 1 {
		t.Errorf("ELSE arm not recorded")
	}

	// A CASE without ELSE.
	cs = firstStmt(t, "CASE sel OF\n 1: x := 1;\nEND_CASE;").(*CaseStmt)
	if cs.HasElse {
		t.Error("CASE without ELSE reports HasElse")
	}

	// An empty arm, as textual.st writes it.
	cs = firstStmt(t, "CASE sel OF\n 1: (* nothing *);\n 2: x := 1;\nEND_CASE;").(*CaseStmt)
	if len(cs.Clauses) != 2 {
		t.Errorf("got %d clauses, want 2", len(cs.Clauses))
	}
}

// TestSemicolonAfterCompoundIsOptional pins the other thing the corpus taught:
// real ST omits the separator after END_IF, and insisting on it would reject
// files every other toolchain accepts.
func TestSemicolonAfterCompoundIsOptional(t *testing.T) {
	f := mustParse(t, wrapFB(`
IF a THEN
  x := 1;
END_IF
IF b THEN
  x := 2;
END_IF`))
	if n := len(f.Blocks[0].Body); n != 2 {
		t.Errorf("got %d statements, want 2", n)
	}

	// A simple statement still needs its semicolon.
	errs := parseErrors(t, wrapFB("x := 1\ny := 2;"))
	if !strings.Contains(errs[0].Msg, "';'") {
		t.Errorf("diagnostic = %q, want it to mention the missing ';'", errs[0].Msg)
	}
}

func TestParseDeclarations(t *testing.T) {
	f := mustParse(t, `
TYPE
  Light : (RED, YELLOW, GREEN);
  Level : (LOW := 1, HIGH := 10);
  Celsius : LREAL;
  Point : STRUCT
    x : LREAL;
    y : LREAL := 1.0;
  END_STRUCT;
  Grid : ARRAY[0..2, 0..3] OF LREAL;
END_TYPE

VAR_GLOBAL
  shared : DINT;
END_VAR

FUNCTION_BLOCK Ramp
  VAR_INPUT
    target : LREAL;
  END_VAR
  VAR_OUTPUT
    out : LREAL;
  END_VAR
  VAR
    step : LREAL := 0.0;
  END_VAR
  out := target;
END_FUNCTION_BLOCK

FUNCTION Avg : LREAL
  VAR_INPUT
    a, b : LREAL;
  END_VAR
  Avg := (a + b) / 2.0;
END_FUNCTION

PROGRAM main
  VAR_INPUT
    enable : BOOL;
  END_VAR
  VAR
    r : Ramp;
  END_VAR
  r(target := 1.0);
  out := r.out;
END_PROGRAM`)

	if len(f.Types) != 5 {
		t.Errorf("got %d type declarations, want 5", len(f.Types))
	}
	if len(f.Types[0].Enum) != 3 {
		t.Errorf("Light has %d constants, want 3", len(f.Types[0].Enum))
	}
	if f.Types[1].Enum[0].Value == nil {
		t.Error("explicit enumeration value not recorded")
	}
	if f.Types[2].Alias == nil || f.Types[2].Alias.Name != "LREAL" {
		t.Errorf("Celsius alias = %v", f.Types[2].Alias)
	}
	if len(f.Types[3].Struct) != 2 {
		t.Errorf("Point has %d fields, want 2", len(f.Types[3].Struct))
	}
	if !f.Types[4].Alias.IsArray() || len(f.Types[4].Alias.Dims) != 2 {
		t.Errorf("Grid is not a two-dimensional array: %v", f.Types[4].Alias)
	}

	if len(f.Globals) != 1 || f.Globals[0].Kind != VarGlobal {
		t.Errorf("VAR_GLOBAL not recorded")
	}
	if len(f.Blocks) != 1 || len(f.Blocks[0].Vars) != 3 {
		t.Errorf("function block sections = %d, want 3", len(f.Blocks[0].Vars))
	}
	if len(f.Funcs) != 1 || f.Funcs[0].Result == nil || f.Funcs[0].Result.Name != "LREAL" {
		t.Errorf("function result type not recorded")
	}
	// One declaration, two names: `a, b : LREAL;`.
	if n := len(f.Funcs[0].Vars[0].Decls[0].Names); n != 2 {
		t.Errorf("multi-name declaration recorded %d names, want 2", n)
	}
	if len(f.Programs) != 1 || len(f.Programs[0].Body) != 2 {
		t.Errorf("program body = %d statements, want 2", len(f.Programs[0].Body))
	}
}

func TestParseVarSectionKinds(t *testing.T) {
	f := mustParse(t, `
FUNCTION_BLOCK B
  VAR_INPUT  a : INT; END_VAR
  VAR_OUTPUT b : INT; END_VAR
  VAR_IN_OUT c : INT; END_VAR
  VAR        d : INT; END_VAR
  VAR_TEMP   e : INT; END_VAR
  VAR CONSTANT f : INT := 1; END_VAR
  a := 1;
END_FUNCTION_BLOCK`)
	want := []VarKind{VarInput, VarOutput, VarInOut, VarLocal, VarTemp, VarLocal}
	got := f.Blocks[0].Vars
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i] {
			t.Errorf("section %d = %v, want %v", i, got[i].Kind, want[i])
		}
	}
	if !got[5].Constant {
		t.Error("CONSTANT qualifier not recorded")
	}
	// Only the three HAL-facing sections become pins.
	for _, k := range []VarKind{VarInput, VarOutput, VarInOut} {
		if !k.IsHAL() {
			t.Errorf("%v should be HAL-facing", k)
		}
	}
	for _, k := range []VarKind{VarLocal, VarTemp, VarGlobal, VarExternal} {
		if k.IsHAL() {
			t.Errorf("%v should not be HAL-facing", k)
		}
	}
}

func TestParseTypeDecorations(t *testing.T) {
	f := mustParse(t, `
FUNCTION_BLOCK B
  VAR
    s   : STRING[10];
    s2  : STRING [1];
    s3  : STRING;
    rng : INT (1..2);
    arr : ARRAY[1..8] OF INT;
    grid : ARRAY[0..2, 0..3] OF ARRAY[1..2] OF LREAL;
    names : ARRAY[1..3] OF STRING[10];
  END_VAR
  x := 1;
END_FUNCTION_BLOCK`)
	d := f.Blocks[0].Vars[0].Decls

	if d[0].Type.StringLen == nil {
		t.Error("STRING[10] length not recorded")
	}
	if d[1].Type.StringLen == nil {
		t.Error("STRING [1] with a space not recorded")
	}
	if d[2].Type.StringLen != nil {
		t.Error("bare STRING should carry no explicit length")
	}
	if d[3].Type.Subrange == nil {
		t.Error("subrange INT (1..2) not recorded")
	}
	if !d[4].Type.IsArray() || len(d[4].Type.Dims) != 1 {
		t.Error("ARRAY[1..8] not recorded")
	}
	if !d[5].Type.IsArray() || len(d[5].Type.Dims) != 2 || !d[5].Type.Elem.IsArray() {
		t.Error("nested array type not recorded")
	}
	if !d[6].Type.IsArray() || d[6].Type.Elem.StringLen == nil {
		t.Error("ARRAY OF STRING[10] not recorded")
	}
}

func TestParseEdgeAndLocated(t *testing.T) {
	f := mustParse(t, `
FUNCTION_BLOCK B
  VAR_INPUT
    PI, G : BOOL R_EDGE;
    FE : BOOL F_EDGE;
  END_VAR
  x := 1;
END_FUNCTION_BLOCK`)
	d := f.Blocks[0].Vars[0].Decls
	if len(d[0].Names) != 2 || d[0].Edge != EdgeRising {
		t.Errorf("`PI, G : BOOL R_EDGE` not recorded: names=%v edge=%v", d[0].Names, d[0].Edge)
	}
	if d[1].Edge != EdgeFalling {
		t.Error("F_EDGE not recorded")
	}

	// A located variable parses so that sema can reject it by name rather than
	// the parser failing with a syntax error on '%'.
	f = mustParse(t, `
PROGRAM main
  VAR
    Button AT %IX1 : BOOL;
  END_VAR
  x := 1;
END_PROGRAM`)
	if got := f.Programs[0].Vars[0].Decls[0].Located; got == "" {
		t.Error("AT address not recorded")
	}
}

func TestParseCallArgumentForms(t *testing.T) {
	s := firstStmt(t, "fb(a := 1, b => y, 3);")
	cs, ok := s.(*CallStmt)
	if !ok {
		t.Fatalf("got %T, want a call statement", s)
	}
	if len(cs.Args) != 3 {
		t.Fatalf("got %d arguments, want 3", len(cs.Args))
	}
	if cs.Args[0].Form != ArgInput || cs.Args[0].Name != "a" {
		t.Errorf("argument 0 = %+v, want input binding a", cs.Args[0])
	}
	if cs.Args[1].Form != ArgOutput || cs.Args[1].Name != "b" {
		t.Errorf("argument 1 = %+v, want output binding b", cs.Args[1])
	}
	if cs.Args[2].Form != ArgPositional {
		t.Errorf("argument 2 = %+v, want positional", cs.Args[2])
	}

	// A bare identifier argument must stay positional: the parser looks past
	// it for := or => and rewinds when neither follows.
	s = firstStmt(t, "f(x, y);")
	cs = s.(*CallStmt)
	for i, a := range cs.Args {
		if a.Form != ArgPositional {
			t.Errorf("argument %d = %+v, want positional", i, a)
		}
	}
}

func TestParseAttributes(t *testing.T) {
	f := mustParse(t, `
{attribute 'summary' := 'Rate-limited follower'}
{attribute 'license' := 'GPL'}
PROGRAM main
  VAR_INPUT
    {attribute 'doc' := 'Commanded position.'}
    target : LREAL;
  END_VAR
  x := 1;
END_PROGRAM`)

	if len(f.Programs) != 1 {
		t.Fatal("program not parsed")
	}
	// Pragmas before the first declaration are the component's metadata, not
	// the first declaration's.  That is what makes a summary at the top of a
	// file mean the component even when a FUNCTION_BLOCK is declared next.
	if len(f.Attrs) != 2 {
		t.Fatalf("got %d file attributes, want 2", len(f.Attrs))
	}
	if f.Attrs[0].Name != "summary" || f.Attrs[0].Value != "Rate-limited follower" {
		t.Errorf("attribute 0 = %+v", f.Attrs[0])
	}
	if len(f.Programs[0].Attrs) != 0 {
		t.Errorf("file metadata leaked onto the program: %+v", f.Programs[0].Attrs)
	}

	d := f.Programs[0].Vars[0].Decls[0]
	if len(d.Attrs) != 1 || d.Attrs[0].Name != "doc" {
		t.Errorf("declaration attribute not attached: %+v", d.Attrs)
	}

	// A valueless attribute is a flag.
	f = mustParse(t, "{attribute 'hal_publish'}\nPROGRAM p\nx := 1;\nEND_PROGRAM")
	if a := f.Attrs; len(a) != 1 || a[0].Name != "hal_publish" || a[0].Value != "" {
		t.Errorf("flag attribute = %+v", f.Attrs)
	}

	// A doubled quote inside a value is the ST escape.
	f = mustParse(t, "{attribute 'summary' := 'it''s here'}\nPROGRAM p\nx := 1;\nEND_PROGRAM")
	if got := f.Attrs[0].Value; got != "it's here" {
		t.Errorf("escaped quote = %q, want %q", got, "it's here")
	}
}

// TestAttributesOnLaterDeclarations checks the other half of the split: once a
// declaration has been seen, a pragma attaches to the one it precedes.
func TestAttributesOnLaterDeclarations(t *testing.T) {
	f := mustParse(t, `
{attribute 'summary' := 'component metadata'}
FUNCTION_BLOCK First
  x := 1;
END_FUNCTION_BLOCK

{attribute 'doc' := 'the second block'}
FUNCTION_BLOCK Second
  y := 1;
END_FUNCTION_BLOCK`)

	if len(f.Attrs) != 1 || f.Attrs[0].Name != "summary" {
		t.Errorf("file attributes = %+v", f.Attrs)
	}
	if len(f.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(f.Blocks))
	}
	if len(f.Blocks[0].Attrs) != 0 {
		t.Errorf("first block picked up the file metadata: %+v", f.Blocks[0].Attrs)
	}
	if a := f.Blocks[1].Attrs; len(a) != 1 || a[0].Name != "doc" {
		t.Errorf("second block attributes = %+v", a)
	}
}

// TestTrailingAttributeIsReported: a pragma with nothing after it attaches to
// nothing, and saying so beats dropping it silently.
func TestTrailingAttributeIsReported(t *testing.T) {
	errs := parseErrors(t, "PROGRAM p\nx := 1;\nEND_PROGRAM\n{attribute 'doc' := 'orphan'}")
	if !strings.Contains(errs.Error(), "does not precede a declaration") {
		t.Errorf("diagnostics: %v", errs)
	}
}

// TestLoopAttributeIsAttached checks the carrier for the §4 iteration bound.
// The parser only records it; requiring it is sema's job.
func TestLoopAttributeIsAttached(t *testing.T) {
	s := firstStmt(t, "{attribute 'max_iterations' := '1000'}\nWHILE a DO x := 1; END_WHILE;")
	ws, ok := s.(*WhileStmt)
	if !ok {
		t.Fatalf("got %T, want a WHILE", s)
	}
	if len(ws.Attrs) != 1 || ws.Attrs[0].Name != "max_iterations" || ws.Attrs[0].Value != "1000" {
		t.Errorf("loop attribute = %+v", ws.Attrs)
	}

	s = firstStmt(t, "{attribute 'max_iterations' := '10'}\nREPEAT x := 1; UNTIL a END_REPEAT;")
	if rs := s.(*RepeatStmt); len(rs.Attrs) != 1 {
		t.Error("REPEAT did not receive its attribute")
	}

	// An attribute in front of anything else here is a mistake worth naming.
	errs := parseErrors(t, wrapFB("{attribute 'max_iterations' := '10'}\nx := 1;"))
	if !strings.Contains(errs[0].Msg, "WHILE or REPEAT") {
		t.Errorf("diagnostic = %q", errs[0].Msg)
	}
}

// TestDiagnostics covers each error the parser can produce.  A parser is
// judged as much by what it says when the source is wrong as by what it
// accepts, so each message is asserted rather than merely the failure.
func TestDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // substring
		line int    // 0 means do not check
	}{
		{
			name: "assignment written with =",
			src:  wrapFB("x = 1;"),
			want: "ST assigns with ':=' and compares with '='",
			line: 2,
		},
		{
			name: "bare expression",
			src:  wrapFB("x + 1;"),
			want: "is not a statement",
		},
		{
			name: "missing THEN",
			src:  wrapFB("IF a x := 1; END_IF;"),
			want: "expected 'THEN'",
		},
		{
			name: "missing END_IF",
			src:  wrapFB("IF a THEN x := 1;"),
			want: "expected 'END_IF'",
		},
		{
			name: "reserved word as a name",
			src:  "PROGRAM IF\nx := 1;\nEND_PROGRAM",
			want: "reserved word",
		},
		{
			name: "function without a result type",
			src:  "FUNCTION f\nf := 1;\nEND_FUNCTION",
			want: "needs a result type",
		},
		{
			name: "unterminated block comment",
			src:  "PROGRAM p\n(* never closed\n",
			want: "unterminated",
		},
		{
			name: "unterminated string",
			src:  wrapFB("x := 'oops;"),
			want: "unterminated string",
		},
		{
			name: "bad radix",
			src:  wrapFB("x := 99#12;"),
			want: "not a valid radix",
		},
		{
			name: "stray character",
			src:  wrapFB("x := @;"),
			want: "unexpected character",
		},
		{
			name: "junk at top level",
			src:  "x := 1;",
			want: "expected a TYPE, VAR_GLOBAL, PROGRAM, FUNCTION or FUNCTION_BLOCK",
		},
		{
			name: "VAR_ACCESS is refused by name",
			src:  "PROGRAM p\nVAR_ACCESS\n a : b : BOOL READ_ONLY;\nEND_VAR\nx := 1;\nEND_PROGRAM",
			want: "VAR_ACCESS is not supported",
		},
		{
			name: "pragma that is not an attribute",
			src:  "{warning disable}\nPROGRAM p\nx := 1;\nEND_PROGRAM",
			want: "must start with 'attribute'",
		},
		{
			name: "attribute without a quoted name",
			src:  "{attribute summary := 'x'}\nPROGRAM p\nx := 1;\nEND_PROGRAM",
			want: "needs a quoted name",
		},
		{
			name: "missing END_VAR",
			src:  "PROGRAM p\nVAR\n a : INT;\nx := 1;\nEND_PROGRAM",
			want: "expected 'END_VAR'",
		},
		{
			name: "missing type after colon",
			src:  "PROGRAM p\nVAR\n a : ;\nEND_VAR\nx := 1;\nEND_PROGRAM",
			want: "expected a type name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := parseErrors(t, tt.src)
			joined := errs.Error()
			if !strings.Contains(joined, tt.want) {
				t.Errorf("diagnostics:\n%s\nwant a message containing %q", joined, tt.want)
			}
			if tt.line != 0 && errs[0].Pos.Line != tt.line {
				t.Errorf("first diagnostic at line %d, want %d", errs[0].Pos.Line, tt.line)
			}
			for _, e := range errs {
				if e.Pos.File != "t.st" || e.Pos.Line == 0 {
					t.Errorf("diagnostic has no usable position: %v", e)
				}
			}
		})
	}
}

// TestParseRecovers checks that one mistake does not hide the rest of the
// file: the parser resynchronises and keeps reporting.
func TestParseRecovers(t *testing.T) {
	errs := parseErrors(t, `
PROGRAM one
  x = 1;
END_PROGRAM

PROGRAM two
  y = 2;
END_PROGRAM`)
	if len(errs) < 2 {
		t.Fatalf("got %d diagnostics, want at least 2 (one per program):\n%v", len(errs), errs)
	}
	if errs[0].Pos.Line >= errs[1].Pos.Line {
		t.Errorf("diagnostics are not in source order: %v", errs)
	}
}

// TestParseTerminates guards the loop-guard in parseStmtList: a statement that
// consumes no tokens would otherwise spin forever on malformed input.
func TestParseTerminates(t *testing.T) {
	for _, src := range []string{
		"PROGRAM p\n)))\nEND_PROGRAM",
		"FUNCTION_BLOCK B\n,,,,\nEND_FUNCTION_BLOCK",
		"TYPE\n:::\nEND_TYPE",
		"PROGRAM p\nVAR\n,,,\nEND_VAR\nEND_PROGRAM",
		"CASE",
		"REPEAT",
		"{",
		"(*",
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = Parse("t.st", src)
		}()
		select {
		case <-done:
		case <-timeoutAfterSeconds(5):
			t.Fatalf("parsing %q did not terminate", src)
		}
	}
}

// TestErrorListFormatting checks the diagnostic rendering the CLI prints.
func TestErrorListFormatting(t *testing.T) {
	var empty ErrorList
	if empty.Error() != "no errors" {
		t.Errorf("empty list renders as %q", empty.Error())
	}
	one := ErrorList{{Pos: ast.Pos{File: "a.st", Line: 3, Col: 5}, Msg: "boom"}}
	if got := one.Error(); got != "a.st:3:5: boom" {
		t.Errorf("single error renders as %q", got)
	}
	two := append(one, Error{Pos: ast.Pos{File: "a.st", Line: 9, Col: 1}, Msg: "bang"})
	if n := strings.Count(two.Error(), "\n"); n != 1 {
		t.Errorf("two errors render on %d lines", n+1)
	}
}

// timeoutAfterSeconds is a small helper so TestParseTerminates does not need a
// dependency on the time package spelled out at every call.
func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}

// TestParseRealWorldSpellings covers constructs found by parsing three
// existing CODESYS projects.  Each was a parse failure before; each is either
// accepted now or refused by name, because a construct that reaches sema can
// be explained and one that dies in the parser cannot.
func TestParseRealWorldSpellings(t *testing.T) {
	// STRING(11) is the older spelling of STRING[11].  It shares its opening
	// paren with a subrange, and only the '..' tells them apart.
	f := mustParse(t, wrapFB("VAR\n s : STRING(11); r : INT (1..2); END_VAR\nx := 1;"))
	d := f.Blocks[0].Vars[0].Decls
	if d[0].Type.StringLen == nil {
		t.Error("STRING(11) length not recorded")
	}
	if d[1].Type.Subrange == nil {
		t.Error("INT (1..2) subrange not recorded")
	}

	// POINTER TO and REFERENCE TO parse so sema can refuse them by name.
	f = mustParse(t, wrapFB("VAR\n p : POINTER TO BYTE; r : REFERENCE TO INT; END_VAR\nx := 1;"))
	for i, d := range f.Blocks[0].Vars[0].Decls {
		if !d.Type.Pointer || d.Type.Elem == nil {
			t.Errorf("declaration %d did not record a pointer type: %+v", i, d.Type)
		}
	}

	// A namespaced type is one name here; whether the library exists is
	// sema's question.
	f = mustParse(t, wrapFB("VAR\n d : visuElems.IDialogManager; END_VAR\nx := 1;"))
	if got := f.Blocks[0].Vars[0].Decls[0].Type.Name; got != "visuElems.IDialogManager" {
		t.Errorf("namespaced type name = %q", got)
	}

	// The separator after END_STRUCT is optional, as CODESYS writes it.
	mustParse(t, "TYPE T : STRUCT a : INT; END_STRUCT END_TYPE\nPROGRAM p\nx := 1;\nEND_PROGRAM")

	// Partial access: the Nth bit of an integer.  129 uses in one project.
	s := firstStmt(t, "flag := word.0;")
	as, ok := s.(*AssignStmt)
	if !ok {
		t.Fatalf("got %T", s)
	}
	bit, ok := as.Value.(*BitExpr)
	if !ok || bit.Bit != 0 {
		t.Errorf("word.0 did not parse as partial access: %T", as.Value)
	}

	// A structured initialiser, and an array of them written as a
	// comma-separated list with no enclosing brackets.
	f = mustParse(t, wrapFB(
		"VAR\n one : T := (a := 1, b := 2);\n many : ARRAY[1..2] OF T := (a := 1), (a := 2);\nEND_VAR\nx := 1;"))
	d = f.Blocks[0].Vars[0].Decls
	lit, ok := d[0].Init.(*StructLit)
	if !ok || len(lit.Fields) != 2 {
		t.Errorf("structured initialiser not parsed: %#v", d[0].Init)
	}
	arr, ok := d[1].Init.(*StructLit)
	if !ok || len(arr.Elems) != 2 {
		t.Errorf("array of structured values not parsed: %#v", d[1].Init)
	}

	// VAR_CONFIG is refused by name, at file level and inside a POU.
	for _, src := range []string{
		"VAR_CONFIG\n p.x AT %IX1 : BOOL;\nEND_VAR\nPROGRAM p\nx := 1;\nEND_PROGRAM",
		"PROGRAM p\nVAR_CONFIG\n a : INT;\nEND_VAR\nx := 1;\nEND_PROGRAM",
	} {
		errs := parseErrors(t, src)
		if !strings.Contains(errs.Error(), "VAR_CONFIG is not supported") {
			t.Errorf("VAR_CONFIG not refused by name:\n%v", errs)
		}
	}
}

// TestParseActions covers the ACTION form: a named piece of a POU's body,
// written after the statements it belongs with.
func TestParseActions(t *testing.T) {
	f := mustParse(t, `
PROGRAM p
  VAR
    n : DINT;
  END_VAR

  first();
  second(n := 1);

ACTION first:
  n := n + 1;
END_ACTION

{attribute 'doc' := 'The second one.'}
ACTION second:
  n := n * 2;
END_ACTION

END_PROGRAM`)

	prog := f.Programs[0]
	if len(prog.Body) != 2 {
		t.Errorf("body has %d statements, want 2 -- an ACTION must end the body, not join it", len(prog.Body))
	}
	if len(prog.Actions) != 2 {
		t.Fatalf("got %d actions, want 2", len(prog.Actions))
	}
	if prog.Actions[0].Name != "first" || prog.Actions[1].Name != "second" {
		t.Errorf("action names = %q, %q", prog.Actions[0].Name, prog.Actions[1].Name)
	}
	if len(prog.Actions[1].Attrs) != 1 || prog.Actions[1].Attrs[0].Name != "doc" {
		t.Errorf("an action's doc attribute did not attach: %+v", prog.Actions[1].Attrs)
	}

	// The preset form: `Act(v := x)` sets a variable of the POU first.
	call, ok := prog.Body[1].(*CallStmt)
	if !ok {
		t.Fatalf("second statement is %T, want a call", prog.Body[1])
	}
	if len(call.Args) != 1 || call.Args[0].Form != ArgInput || call.Args[0].Name != "n" {
		t.Errorf("preset argument = %+v", call.Args)
	}

	// A function block may have them too.
	f = mustParse(t, "FUNCTION_BLOCK B\nVAR\n n : DINT;\nEND_VAR\ngo();\nACTION go:\n n := 1;\nEND_ACTION\nEND_FUNCTION_BLOCK")
	if len(f.Blocks[0].Actions) != 1 {
		t.Errorf("function block action not parsed")
	}

	// A FUNCTION may not: it has no state for an action to share.
	errs := parseErrors(t, "FUNCTION F : DINT\nF := 1;\nACTION a:\n F := 2;\nEND_ACTION\nEND_FUNCTION")
	if !strings.Contains(errs.Error(), "a FUNCTION may not have an ACTION") {
		t.Errorf("diagnostics: %v", errs)
	}
}

// TestParseActionsAfterPOU covers the placement CODESYS 2.3 exports use: the
// actions follow the POU's END_ keyword rather than sitting inside it.  Both
// spellings mean the same thing, so both must parse to the same tree.
func TestParseActionsAfterPOU(t *testing.T) {
	f := mustParse(t, `
FUNCTION_BLOCK b
  VAR
    m : DINT;
  END_VAR

  bump();
END_FUNCTION_BLOCK

ACTION	bump:
  m := m + 1;
END_ACTION

PROGRAM p
  VAR
    n : DINT;
  END_VAR

  first();
END_PROGRAM

ACTION first:
  n := n + 1;
END_ACTION`)

	if len(f.Blocks) != 1 || len(f.Blocks[0].Actions) != 1 {
		t.Fatalf("block got %d actions, want 1", len(f.Blocks[0].Actions))
	}
	if f.Blocks[0].Actions[0].Name != "bump" {
		t.Errorf("block action = %q, want bump", f.Blocks[0].Actions[0].Name)
	}
	if len(f.Programs) != 1 || len(f.Programs[0].Actions) != 1 {
		t.Fatalf("program got %d actions, want 1", len(f.Programs[0].Actions))
	}
	if f.Programs[0].Actions[0].Name != "first" {
		t.Errorf("program action = %q, want first", f.Programs[0].Actions[0].Name)
	}
	// The action must not have been swept into the block that came first.
	if len(f.Blocks[0].Actions) != 1 {
		t.Errorf("a trailing action attached to the wrong POU")
	}
}

// TestParseActionsWithoutPOU covers an ACTION that follows nothing it could be
// part of.  A FUNCTION is refused for the same reason it is refused inside:
// there is no instance for the action to share.
func TestParseActionsWithoutPOU(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  string
		want string
	}{{
		name: "no POU at all",
		src: `
ACTION orphan:
  n := 1;
END_ACTION`,
		want: "follows no PROGRAM or FUNCTION_BLOCK",
	}, {
		name: "after a FUNCTION",
		src: `
FUNCTION f : DINT
  f := 1;
END_FUNCTION

ACTION extra:
  f := 2;
END_ACTION`,
		want: "a FUNCTION may not have an ACTION",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			errs := parseErrors(t, tt.src)
			if !strings.Contains(errs.Error(), tt.want) {
				t.Errorf("errors =\n%v\nwant one containing %q", errs, tt.want)
			}
		})
	}
}
