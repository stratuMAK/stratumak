// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// check parses and checks src, returning the package and any diagnostics.
func check(t *testing.T, component, src string) (*ast.Package, error) {
	t.Helper()
	file, perr := st.Parse(component+".st", src)
	if perr != nil {
		t.Fatalf("source does not parse, so the sema test is meaningless:\n%v\n--- source ---\n%s", perr, src)
	}
	return Check(component, file)
}

// mustCheck fails on any diagnostic.
func mustCheck(t *testing.T, component, src string) *ast.Package {
	t.Helper()
	pkg, err := check(t, component, src)
	if err != nil {
		t.Fatalf("unexpected diagnostics:\n%v\n--- source ---\n%s", err, src)
	}
	return pkg
}

// wrap builds a minimal component around a body, with the declarations a test
// needs in front of it.
func wrap(decls, body string) string {
	return "{attribute 'summary' := 'test'}\nPROGRAM p\n" +
		"VAR_OUTPUT\n  q : BOOL;\nEND_VAR\n" + decls + "\n" + body + "\nEND_PROGRAM"
}

// ---------------------------------------------------------------------------
// Accepted lowering
// ---------------------------------------------------------------------------

func TestLowersAComponent(t *testing.T) {
	pkg := mustCheck(t, "and2", `
{attribute 'summary' := 'Two-input AND gate'}
{attribute 'license' := 'GPL'}
{attribute 'author'  := 'stratuMAK'}
PROGRAM and2
  VAR_INPUT
    {attribute 'doc' := 'First input.'}
    in0 : BOOL;
    in1 : BOOL;
  END_VAR
  VAR_OUTPUT
    out : BOOL;
  END_VAR
  VAR
    seen : DINT := 0;
  END_VAR

  out := in0 AND in1;
  seen := seen + 1;
END_PROGRAM`)

	if pkg.Component.Name != "and2" {
		t.Errorf("component name = %q", pkg.Component.Name)
	}
	if pkg.Component.Summary != "Two-input AND gate" || pkg.Component.License != "GPL" {
		t.Errorf("metadata not lowered: %+v", pkg.Component)
	}

	if len(pkg.Component.Pins) != 3 {
		t.Fatalf("got %d pins, want 3", len(pkg.Component.Pins))
	}
	wantPins := []struct {
		name string
		typ  ast.HALType
		dir  ast.PinDir
	}{
		{"in0", ast.HALBit, ast.PinIn},
		{"in1", ast.HALBit, ast.PinIn},
		{"out", ast.HALBit, ast.PinOut},
	}
	for i, w := range wantPins {
		got := pkg.Component.Pins[i]
		if got.Name != w.name || got.Type != w.typ || got.Dir != w.dir {
			t.Errorf("pin %d = %s %v %v, want %s %v %v", i, got.Name, got.Type, got.Dir, w.name, w.typ, w.dir)
		}
	}
	if pkg.Component.Pins[0].Doc != "First input." {
		t.Errorf("pin doc = %q", pkg.Component.Pins[0].Doc)
	}

	// A VAR becomes per-instance state in inst_t, exactly as a .comp
	// `variable` declaration does.
	if len(pkg.Component.Variables) != 1 {
		t.Fatalf("got %d variables, want 1", len(pkg.Component.Variables))
	}
	if v := pkg.Component.Variables[0]; v.Name != "seen" || v.CType != "int32_t" || v.Default != "0" {
		t.Errorf("variable = %+v", v)
	}

	// A PROGRAM named after its component exports as the component itself.
	if len(pkg.Component.Functions) != 1 || pkg.Component.Functions[0].Name != "_" {
		t.Errorf("functions = %+v", pkg.Component.Functions)
	}
	// No floating point anywhere, so the function exports with fp = 0.
	if pkg.Component.Functions[0].FP {
		t.Error("a component with no float should export with fp = 0")
	}
	if pkg.Component.Functions[0].Body == nil {
		t.Fatal("function has no lowered body")
	}
	if err := pkg.Validate(); err != nil {
		t.Errorf("lowered package fails its own invariant: %v", err)
	}
}

func TestProgramNameDiffersFromComponent(t *testing.T) {
	// A PROGRAM whose name is not the component's becomes a namespaced HAL
	// function, which is what cgen already does for a named .comp function.
	pkg := mustCheck(t, "mycomp", `
{attribute 'summary' := 'test'}
PROGRAM read
  VAR_INPUT
    a : DINT;
  END_VAR
  VAR_OUTPUT
    b : DINT;
  END_VAR
  b := a;
END_PROGRAM`)
	if pkg.Component.Functions[0].Name != "read" {
		t.Errorf("function name = %q, want %q", pkg.Component.Functions[0].Name, "read")
	}
}

func TestFloatingPointFlag(t *testing.T) {
	pkg := mustCheck(t, "p", `
{attribute 'summary' := 'test'}
PROGRAM p
  VAR_INPUT
    a : LREAL;
  END_VAR
  VAR_OUTPUT
    b : LREAL;
  END_VAR
  b := a * 2.0;
END_PROGRAM`)
	if !pkg.Component.Functions[0].FP {
		t.Error("a component using LREAL must export with fp = 1")
	}
}

func TestPinTypeMapping(t *testing.T) {
	pkg := mustCheck(t, "p", `
{attribute 'summary' := 'test'}
PROGRAM p
  VAR_INPUT
    b  : BOOL;
    s  : SINT;
    i  : INT;
    d  : DINT;
    us : USINT;
    ui : UINT;
    ud : UDINT;
    l  : LREAL;
  END_VAR
  VAR_OUTPUT
    o : BOOL;
  END_VAR
  o := b;
END_PROGRAM`)

	want := []ast.HALType{
		ast.HALBit, ast.HALS32, ast.HALS32, ast.HALS32,
		ast.HALU32, ast.HALU32, ast.HALU32, ast.HALFloat,
		ast.HALBit,
	}
	if len(pkg.Component.Pins) != len(want) {
		t.Fatalf("got %d pins, want %d", len(pkg.Component.Pins), len(want))
	}
	for i, w := range want {
		if pkg.Component.Pins[i].Type != w {
			t.Errorf("pin %s = %v, want %v", pkg.Component.Pins[i].Name, pkg.Component.Pins[i].Type, w)
		}
	}
}

// TestRealPinWarnsRatherThanRejects pins the decision of §3.1: REAL reaches a
// pin losslessly but not back, because HAL float is a double.  Accepted with a
// warning naming the pin, never silently and never refused.
func TestRealPinWarnsRatherThanRejects(t *testing.T) {
	pkg := mustCheck(t, "p", `
{attribute 'summary' := 'test'}
PROGRAM p
  VAR_INPUT
    approx : REAL;
  END_VAR
  VAR_OUTPUT
    o : REAL;
  END_VAR
  o := approx;
END_PROGRAM`)

	var found bool
	for _, w := range pkg.Warnings {
		if strings.Contains(w, "approx") && strings.Contains(w, "LREAL") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning naming the REAL pin and suggesting LREAL: %v", pkg.Warnings)
	}
}

// TestWideningIsInserted checks that every conversion becomes an explicit node,
// so the backend never has to decide whether one is needed.
func TestWideningIsInserted(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(`
VAR
  small : INT;
  big   : DINT;
  f     : LREAL;
END_VAR`, `
big := small;
f := big;
q := TRUE;`))

	body := pkg.Component.Functions[0].Body
	if len(body) != 3 {
		t.Fatalf("got %d statements, want 3", len(body))
	}
	for i := 0; i < 2; i++ {
		as, ok := body[i].(*ast.AssignStmt)
		if !ok {
			t.Fatalf("statement %d is %T", i, body[i])
		}
		if _, isConv := as.Value.(*ast.ConvExpr); !isConv {
			t.Errorf("statement %d: widening was not made explicit, value is %T", i, as.Value)
		}
	}
}

// TestLiteralsAdaptToContext: a literal has no type of its own, so `x : SINT
// := 1` needs no ceremony while `x : SINT := 300` is refused with the range.
func TestLiteralsAdaptToContext(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(`
VAR
  a : SINT := 100;
  b : UDINT := 4000000000;
  c : LREAL := 1;
  d : DINT := 16#FF;
END_VAR`, "q := TRUE;"))

	want := map[string]string{"a": "100", "b": "4000000000", "c": "1.0", "d": "255"}
	for _, v := range pkg.Component.Variables {
		if w, ok := want[v.Name]; ok && v.Default != w {
			t.Errorf("%s default = %q, want %q", v.Name, v.Default, w)
		}
	}
}

func TestTypedLiterals(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(`
VAR
  a : UDINT := UDINT#16#DEAD_BEEF;
  b : BOOL := BOOL#TRUE;
  c : LREAL := LREAL#11.0;
  d : DINT := DINT#42;
END_VAR`, "q := TRUE;"))

	want := map[string]string{"a": "3735928559", "b": "true", "c": "11.0", "d": "42"}
	for _, v := range pkg.Component.Variables {
		if w, ok := want[v.Name]; ok && v.Default != w {
			t.Errorf("%s default = %q, want %q", v.Name, v.Default, w)
		}
	}
}

func TestStatementsLower(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(`
VAR
  i : DINT;
  n : DINT;
  f : LREAL;
END_VAR`, `
IF n > 0 THEN
  n := 1;
ELSIF n < 0 THEN
  n := 2;
ELSE
  n := 3;
END_IF;

CASE n OF
  0: n := 10;
  1, 2: n := 20;
  3..5: n := 30;
ELSE
  n := 40;
END_CASE;

FOR i := 0 TO 9 BY 2 DO
  n := n + i;
  IF n > 100 THEN
    EXIT;
  END_IF;
END_FOR;

{attribute 'max_iterations' := '50'}
WHILE n < 10 DO
  n := n + 1;
END_WHILE;

{attribute 'max_iterations' := '10'}
REPEAT
  n := n - 1;
UNTIL n < 1
END_REPEAT;

q := TRUE;`))

	body := pkg.Component.Functions[0].Body
	if len(body) != 6 {
		t.Fatalf("got %d statements, want 6", len(body))
	}

	ifs, ok := body[0].(*ast.IfStmt)
	if !ok || len(ifs.ElseIf) != 1 || len(ifs.Else) != 1 {
		t.Errorf("IF did not lower with its arms: %+v", body[0])
	}

	cs, ok := body[1].(*ast.CaseStmt)
	if !ok || len(cs.Clauses) != 3 || len(cs.Else) != 1 {
		t.Fatalf("CASE did not lower: %+v", body[1])
	}
	if len(cs.Clauses[1].Labels) != 2 {
		t.Errorf("multi-label arm has %d labels", len(cs.Clauses[1].Labels))
	}
	if l := cs.Clauses[2].Labels[0]; l.Lo != 3 || l.Hi != 5 || !l.IsRange() {
		t.Errorf("range label = %+v, want 3..5", l)
	}

	if _, ok := body[2].(*ast.ForStmt); !ok {
		t.Errorf("FOR did not lower: %T", body[2])
	}
	ws, ok := body[3].(*ast.WhileStmt)
	if !ok || ws.MaxIter != 50 {
		t.Errorf("WHILE bound = %+v, want MaxIter 50", body[3])
	}
	rs, ok := body[4].(*ast.RepeatStmt)
	if !ok || rs.MaxIter != 10 {
		t.Errorf("REPEAT bound = %+v, want MaxIter 10", body[4])
	}
}

// TestExpressionsCarryTypes is the property the whole IR exists for.
func TestExpressionsCarryTypes(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(`
VAR
  a : DINT;
  f : LREAL;
END_VAR`, `
q := a > 1;
f := a + 1;
q := NOT q;`))

	body := pkg.Component.Functions[0].Body
	types := []ast.TypeKind{ast.TypeBool, ast.TypeLReal, ast.TypeBool}
	for i, want := range types {
		as := body[i].(*ast.AssignStmt)
		if got := as.Value.ExprType().Kind; got != want {
			t.Errorf("statement %d value type = %v, want %v", i, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

// TestDiagnostics covers every rejection class phase 2 can produce.  A checker
// is judged by what it says when the source is wrong at least as much as by
// what it accepts, so each message is asserted rather than merely the failure.
func TestDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		// --- resolution ---
		{
			name: "unknown identifier",
			src:  wrap("", "q := nosuch;"),
			want: "nosuch is not declared",
		},
		{
			name: "redeclaration",
			src:  wrap("VAR\n a : INT;\n a : DINT;\nEND_VAR", "q := TRUE;"),
			want: "a is already declared",
		},

		// --- typing ---
		{
			name: "narrowing assignment",
			src:  wrap("VAR\n small : INT;\n big : DINT;\nEND_VAR", "small := big;"),
			want: "would be truncated",
		},
		{
			name: "signed to unsigned",
			src:  wrap("VAR\n s : DINT;\n u : UDINT;\nEND_VAR", "u := s;"),
			want: "signed and unsigned do not convert implicitly",
		},
		{
			name: "bool is not a number",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "n := q;"),
			want: "BOOL is a truth value, not a number",
		},
		{
			name: "string assigned to int -- the prototype accepted this",
			src:  wrap("VAR\n n : DINT;\n s : STRING[4];\nEND_VAR", "n := s;"),
			want: "cannot assign STRING[4]",
		},
		{
			name: "literal out of range",
			src:  wrap("VAR\n a : SINT := 300;\nEND_VAR", "q := TRUE;"),
			want: "out of range for SINT (-128 to 127)",
		},
		{
			name: "real literal into an integer",
			src:  wrap("VAR\n a : DINT := 1.5;\nEND_VAR", "q := TRUE;"),
			want: "cannot be assigned without rounding",
		},
		{
			name: "arithmetic on BOOL",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "n := q + 1;"),
			want: "cannot apply",
		},
		{
			name: "AND on integers",
			src:  wrap("VAR\n a : DINT;\n b : DINT;\nEND_VAR", "q := a AND b;"),
			want: "needs BOOL operands",
		},
		{
			name: "no truthiness in a condition",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "IF n THEN q := TRUE; END_IF;"),
			want: "ST has no truthiness",
		},
		{
			name: "MOD on floats",
			src:  wrap("VAR\n f : LREAL;\nEND_VAR", "f := f MOD 2.0;"),
			want: "MOD needs integer operands",
		},
		{
			name: "negating an unsigned",
			src:  wrap("VAR\n u : UDINT;\nEND_VAR", "u := -u;"),
			want: "it is unsigned",
		},
		{
			name: "division by a literal zero",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "n := n / 0;"),
			want: "division by zero",
		},

		// --- assignability ---
		{
			name: "assigning an input pin",
			src:  wrap("VAR_INPUT\n a : DINT;\nEND_VAR", "a := 1;"),
			want: "it is an input pin",
		},
		{
			name: "assigning a constant",
			src:  wrap("VAR CONSTANT\n k : DINT := 1;\nEND_VAR", "k := 2;"),
			want: "it is a constant",
		},
		{
			name: "assigning a literal",
			src:  wrap("", "1 := 2;"),
			want: "cannot assign to this expression",
		},

		// --- HAL surface ---
		{
			name: "LINT cannot be a pin",
			src:  wrap("VAR_INPUT\n big : LINT;\nEND_VAR", "q := TRUE;"),
			want: "has no HAL representation",
		},
		{
			name: "STRING cannot be a pin",
			src:  wrap("VAR_INPUT\n s : STRING[8];\nEND_VAR", "q := TRUE;"),
			want: "has no HAL representation",
		},
		{
			name: "located variables are refused by name",
			src:  wrap("VAR\n b AT %IX1 : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "stratuMAK maps I/O through VAR_INPUT and VAR_OUTPUT",
		},

		// --- real-time rules, §4 ---
		{
			name: "unbounded WHILE",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "WHILE n < 10 DO n := n + 1; END_WHILE;"),
			want: "needs an iteration bound the compiler can see",
		},
		{
			name: "unbounded REPEAT",
			src:  wrap("VAR\n n : DINT;\nEND_VAR", "REPEAT n := n + 1; UNTIL n > 3 END_REPEAT;"),
			want: "needs an iteration bound the compiler can see",
		},
		{
			name: "FOR with a non-constant limit needs a bound too",
			src: wrap("VAR_INPUT\n lim : DINT;\nEND_VAR\nVAR\n i : DINT;\n n : DINT;\nEND_VAR",
				"FOR i := 0 TO lim DO n := n + 1; END_FOR;"),
			want: "needs an iteration bound the compiler can see",
		},
		{
			name: "max_iterations must be a number",
			src: wrap("VAR\n n : DINT;\nEND_VAR",
				"{attribute 'max_iterations' := 'lots'}\nWHILE n < 3 DO n := n + 1; END_WHILE;"),
			want: "must be a whole number",
		},
		{
			name: "max_iterations must be positive",
			src: wrap("VAR\n n : DINT;\nEND_VAR",
				"{attribute 'max_iterations' := '0'}\nWHILE n < 3 DO n := n + 1; END_WHILE;"),
			want: "must be positive",
		},
		{
			name: "max_iterations has a ceiling",
			src: wrap("VAR\n n : DINT;\nEND_VAR",
				"{attribute 'max_iterations' := '99999999'}\nWHILE n < 3 DO n := n + 1; END_WHILE;"),
			want: "too large for a real-time thread",
		},
		{
			name: "FOR step of zero",
			src: wrap("VAR\n i : DINT;\n n : DINT;\nEND_VAR",
				"FOR i := 0 TO 9 BY 0 DO n := n + 1; END_FOR;"),
			want: "never terminates",
		},
		{
			name: "EXIT outside a loop",
			src:  wrap("", "EXIT;"),
			want: "only allowed inside a loop",
		},

		// --- CASE ---
		{
			name: "overlapping CASE labels",
			src: wrap("VAR\n n : DINT;\nEND_VAR",
				"CASE n OF\n 1..5: n := 1;\n 3: n := 2;\nEND_CASE;"),
			want: "overlaps the one at",
		},
		{
			name: "backwards CASE range",
			src: wrap("VAR\n n : DINT;\nEND_VAR",
				"CASE n OF\n 5..1: n := 1;\nEND_CASE;"),
			want: "bounds are the wrong way round",
		},
		{
			name: "non-constant CASE label",
			src: wrap("VAR\n n : DINT;\n m : DINT;\nEND_VAR",
				"CASE n OF\n m: n := 1;\nEND_CASE;"),
			want: "must be a compile-time constant",
		},
		{
			name: "CASE on a float",
			src: wrap("VAR\n f : LREAL;\nEND_VAR",
				"CASE f OF\n 1: q := TRUE;\nEND_CASE;"),
			want: "needs an integer or enumeration selector",
		},

		// --- structure ---
		{
			name: "no PROGRAM",
			src:  "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\n x := 1;\nEND_FUNCTION_BLOCK",
			want: "no PROGRAM declared",
		},
		{
			name: "attribute needs a value",
			src:  "{attribute 'summary'}\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM",
			want: "needs a value",
		},
		{
			name: "duplicate attribute",
			src: "{attribute 'summary' := 'a'}\n{attribute 'summary' := 'b'}\n" +
				"PROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM",
			want: "is already set at",
		},

		// --- staged features, each naming the phase that brings it ---
		{
			name: "TYPE declarations",
			src:  "{attribute 'summary' := 'x'}\nTYPE L : (A, B); END_TYPE\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM",
			want: "user-defined types arrive with phase 4",
		},
		{
			name: "function blocks",
			src:  "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK R\n x := 1;\nEND_FUNCTION_BLOCK\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM",
			want: "function blocks are not supported yet",
		},
		{
			name: "VAR_GLOBAL",
			src:  "{attribute 'summary' := 'x'}\nVAR_GLOBAL\n g : DINT;\nEND_VAR\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM",
			want: "arrives with phase 6",
		},
		{
			name: "two programs",
			src: "{attribute 'summary' := 'x'}\n" +
				"PROGRAM a\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM\n" +
				"PROGRAM b\nVAR_OUTPUT\n r : BOOL;\nEND_VAR\nr := TRUE;\nEND_PROGRAM",
			want: "more than one PROGRAM is not supported yet",
		},
		{
			name: "arrays",
			src:  wrap("VAR\n a : ARRAY[0..3] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "array types are not supported yet; they arrive with phase 4",
		},
		{
			name: "TIME is named, not merely unknown",
			src:  wrap("VAR\n t : TIME;\nEND_VAR", "q := TRUE;"),
			want: "TIME type is not implemented yet",
		},
		{
			name: "bit-string types are named",
			src:  wrap("VAR\n w : WORD;\nEND_VAR", "q := TRUE;"),
			want: "bit-string types",
		},
		{
			name: "unknown type points at phase 4",
			src:  wrap("VAR\n x : Widget;\nEND_VAR", "q := TRUE;"),
			want: "unknown type Widget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.src)
			}
			list, ok := err.(ErrorList)
			if !ok {
				t.Fatalf("error is %T, want ErrorList", err)
			}
			if !strings.Contains(list.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant a message containing %q", list, tt.want)
			}
			for _, e := range list {
				if e.Pos.Line == 0 || e.Pos.File == "" {
					t.Errorf("diagnostic has no usable position: %v", e)
				}
			}
		})
	}
}

// TestDiagnosticsAreOrdered checks that a component with several problems
// reports them in source order, so one compile finds them all.
func TestDiagnosticsAreOrdered(t *testing.T) {
	_, err := check(t, "p", wrap(`
VAR
  a : DINT;
  b : INT;
END_VAR`, `
b := a;
q := nosuch;
c := 1;`))
	list, ok := err.(ErrorList)
	if !ok || len(list) < 3 {
		t.Fatalf("got %v, want at least three diagnostics", err)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Pos.Line > list[i].Pos.Line {
			t.Errorf("diagnostics out of order: %v", list)
			break
		}
	}
}

// TestUnknownAttributeWarns: a pragma meant for another toolchain must not
// stop the build, but silently ignoring one meant for us would hide a typo.
func TestUnknownAttributeWarns(t *testing.T) {
	pkg := mustCheck(t, "p", "{attribute 'summary' := 'x'}\n{attribute 'vendor_thing' := 'y'}\n"+
		"PROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM")
	var found bool
	for _, w := range pkg.Warnings {
		if strings.Contains(w, "vendor_thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("unknown attribute produced no warning: %v", pkg.Warnings)
	}
}

func TestMissingSummaryWarns(t *testing.T) {
	pkg := mustCheck(t, "p", "PROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM")
	var found bool
	for _, w := range pkg.Warnings {
		if strings.Contains(w, "summary") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing summary produced no warning: %v", pkg.Warnings)
	}
}
