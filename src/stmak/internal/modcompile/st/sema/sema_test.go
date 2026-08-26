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

// fbSrc is a small function block with one of each kind of member, used by the
// call-site diagnostics below.
const fbSrc = "{attribute 'summary' := 'x'}\n" +
	"FUNCTION_BLOCK Counter\n" +
	"VAR_INPUT\n  step : DINT;\n  reset : BOOL;\nEND_VAR\n" +
	"VAR_OUTPUT\n  total : DINT;\nEND_VAR\n" +
	"VAR\n  scratch : DINT;\nEND_VAR\n" +
	"IF reset THEN\n  total := 0;\nELSE\n  total := total + step;\nEND_IF;\n" +
	"END_FUNCTION_BLOCK\n"

// progWith builds just the PROGRAM, for a test that supplies its own leading
// TYPE block and summary attribute.
func progWith(decls, body string) string {
	return "PROGRAM p\nVAR_OUTPUT\n  q : BOOL;\nEND_VAR\n" + decls + "\n" + body + "\nEND_PROGRAM"
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
		// --- explicit pin mapping with AT ---
		{
			// A PLC programmer reaches for %QX0.0 by reflex; say plainly why
			// it cannot mean anything here rather than "unknown direction".
			name: "an IEC physical address",
			src:  wrap("VAR\n b AT %IX1 : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "is a physical address; stratuMAK has no address space",
		},
		{
			name: "an unknown direction",
			src:  wrap("VAR\n b AT %Z : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "write AT %I, AT %Q or AT %IO",
		},
		{
			name: "AT in a section that already maps",
			src:  wrap("VAR_INPUT\n b AT %I : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "AT is redundant in VAR_INPUT",
		},
		{
			name: "assigning an AT input",
			src:  wrap("VAR\n b AT %I : BOOL;\nEND_VAR", "b := TRUE;"),
			want: "it is an input pin",
		},
		{
			name: "a type that cannot reach a pin",
			src:  wrap("VAR\n b AT %Q : LINT;\nEND_VAR", "q := TRUE;"),
			want: "has no HAL representation",
		},
		{
			name: "an AT name colliding with a pin",
			src:  wrap("VAR\n b AT %Q.q : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "pin q is already declared",
		},
		{
			name: "a publishing instance in VAR_GLOBAL",
			src: "{attribute 'summary' := 'x'}\n" +
				"FUNCTION_BLOCK P\nVAR\n v AT %Q.value : LREAL;\nEND_VAR\nv := 1.0;\nEND_FUNCTION_BLOCK\n" +
				"VAR_GLOBAL\n shared : P;\nEND_VAR\n" +
				progWith("", "q := TRUE;"),
			want: "cannot be a VAR_GLOBAL",
		},
		{
			name: "a publishing instance in a multi-dimensional array",
			src: "{attribute 'summary' := 'x'}\n" +
				"FUNCTION_BLOCK P\nVAR\n v AT %Q.value : LREAL;\nEND_VAR\nv := 1.0;\nEND_FUNCTION_BLOCK\n" +
				progWith("VAR\n grid : ARRAY[0..1, 0..1] OF P;\nEND_VAR", "q := TRUE;"),
			want: "a pin name carries one index",
		},
		{
			name: "a pin path over HAL's name limit",
			src: "{attribute 'summary' := 'x'}\n" +
				"FUNCTION_BLOCK P\nVAR\n " +
				strings.Repeat("v", 130) + " AT %Q." + strings.Repeat("n", 130) + " : LREAL;\nEND_VAR\n" +
				strings.Repeat("v", 130) + " := 1.0;\nEND_FUNCTION_BLOCK\n" +
				progWith("VAR\n instance : P;\nEND_VAR", "q := TRUE;"),
			want: "over HAL's limit",
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
		// --- user-defined types ---
		{
			name: "unknown enumeration constant",
			src:  "{attribute 'summary' := 'x'}\nTYPE L : (A, B); END_TYPE\n" + progWith("VAR\n v : L;\nEND_VAR", "v := C;"),
			want: "C is not declared",
		},
		{
			name: "enumerations do not mix",
			src: "{attribute 'summary' := 'x'}\nTYPE L : (A, B); M : (X, Y); END_TYPE\n" +
				progWith("VAR\n v : L;\nEND_VAR", "v := X;"),
			want: "cannot assign M to v (L)",
		},
		{
			name: "an enumeration is not an integer",
			src:  "{attribute 'summary' := 'x'}\nTYPE L : (A, B); END_TYPE\n" + progWith("VAR\n v : L;\n n : DINT;\nEND_VAR", "n := v;"),
			want: "cannot assign L to n (DINT)",
		},
		{
			// Two enumerations sharing a constant name is ordinary in real
			// code, so the declarations stand and only an unqualified use is
			// refused -- naming every candidate, so the fix is obvious.
			name: "an ambiguous unqualified constant",
			src: "{attribute 'summary' := 'x'}\nTYPE L : (A, OFF); M : (OFF, C); END_TYPE\n" +
				progWith("VAR\n v : L;\nEND_VAR", "v := OFF;"),
			want: "write it qualified, as L.OFF",
		},
		{
			name: "a qualified constant of the wrong enumeration",
			src: "{attribute 'summary' := 'x'}\nTYPE L : (A, B); M : (C, D); END_TYPE\n" +
				progWith("VAR\n v : L;\nEND_VAR", "v := M.C;"),
			want: "cannot assign M to v (L)",
		},
		{
			name: "a qualified constant that does not exist",
			src: "{attribute 'summary' := 'x'}\nTYPE L : (A, B); END_TYPE\n" +
				progWith("VAR\n v : L;\nEND_VAR", "v := L.Z;"),
			want: "enumeration L has no constant Z",
		},
		{
			name: "qualifying something that is not an enumeration",
			src: "{attribute 'summary' := 'x'}\nTYPE P : STRUCT x : LREAL; END_STRUCT; END_TYPE\n" +
				progWith("", "q := P.x = 1.0;"),
			want: "not an enumeration",
		},
		{
			name: "a variable may not shadow an enumeration constant",
			src:  "{attribute 'summary' := 'x'}\nTYPE L : (RUNNING, STOPPED); END_TYPE\n" + progWith("VAR\n running : BOOL;\nEND_VAR", "q := TRUE;"),
			want: "would shadow the constant RUNNING",
		},
		{
			name: "a type that contains itself",
			src: "{attribute 'summary' := 'x'}\nTYPE P : STRUCT a : P; END_STRUCT; END_TYPE\n" +
				progWith("", "q := TRUE;"),
			want: "contains itself, so it has no size",
		},
		{
			name: "unknown struct field",
			src: "{attribute 'summary' := 'x'}\nTYPE P : STRUCT x : LREAL; END_STRUCT; END_TYPE\n" +
				progWith("VAR\n p : P;\nEND_VAR", "p.y := 1.0;"),
			want: "P has no field y",
		},
		{
			name: "field of a non-structure",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "n.x := 1;"),
			want: "is not a structure",
		},
		{
			name: "duplicate struct field",
			src: "{attribute 'summary' := 'x'}\nTYPE P : STRUCT x : LREAL; x : DINT; END_STRUCT; END_TYPE\n" +
				progWith("", "q := TRUE;"),
			want: "field x is already declared",
		},

		// --- arrays ---
		{
			name: "index outside the declared bounds",
			src:  progWith("VAR\n a : ARRAY[1..4] OF DINT;\nEND_VAR", "a[5] := 1;"),
			want: "index 5 is outside 1..4",
		},
		{
			name: "too few subscripts",
			src:  progWith("VAR\n a : ARRAY[0..1, 0..2] OF DINT;\nEND_VAR", "a[0] := 1;"),
			want: "needs 2 subscripts, got 1",
		},
		{
			name: "too many subscripts",
			src:  progWith("VAR\n a : ARRAY[0..3] OF DINT;\nEND_VAR", "a[0, 1] := 1;"),
			want: "too many subscripts",
		},
		{
			name: "indexing something that is not an array",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "n[0] := 1;"),
			want: "n is not an array",
		},
		{
			name: "a non-integer index",
			src:  progWith("VAR\n a : ARRAY[0..3] OF DINT;\n f : LREAL;\nEND_VAR", "a[f] := 1;"),
			want: "array index must be an integer",
		},
		{
			name: "backwards array bounds",
			src:  progWith("VAR\n a : ARRAY[4..1] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "bounds are the wrong way round",
		},
		{
			name: "non-constant array bound",
			src:  progWith("VAR_INPUT\n n : DINT;\nEND_VAR\nVAR\n a : ARRAY[0..n] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "array bounds must be compile-time constants",
		},
		{
			name: "an array is too large to live in an instance",
			src:  progWith("VAR\n a : ARRAY[0..99999999] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "too large",
		},
		{
			name: "a multi-dimensional array pin",
			src:  progWith("VAR_INPUT\n a : ARRAY[0..1, 0..2] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "a pin name carries one index",
		},
		{
			name: "an array pin must start at zero",
			src:  progWith("VAR_INPUT\n a : ARRAY[1..4] OF DINT;\nEND_VAR", "q := TRUE;"),
			want: "must start at index 0",
		},
		{
			name: "an aggregate cannot be initialised in its declaration",
			src:  progWith("VAR\n a : ARRAY[0..3] OF DINT := 0;\nEND_VAR", "q := TRUE;"),
			want: "is an aggregate",
		},

		// --- function blocks ---
		{
			name: "unknown input at a call site",
			src:  fbSrc + progWith("VAR\n r : Counter;\nEND_VAR", "r(nosuch := 1);"),
			want: "Counter has no input nosuch",
		},
		{
			name: "binding an output as an input",
			src:  fbSrc + progWith("VAR\n r : Counter;\nEND_VAR", "r(total := 1);"),
			want: "total is an output of Counter",
		},
		{
			name: "an input bound twice",
			src:  fbSrc + progWith("VAR\n r : Counter;\nEND_VAR", "r(step := 1, step := 2);"),
			want: "input step is already bound",
		},
		{
			name: "too many positional arguments",
			src:  fbSrc + progWith("VAR\n r : Counter;\nEND_VAR", "r(1, 2, 3);"),
			want: "one too many",
		},
		{
			name: "reading an internal VAR of an instance",
			src:  fbSrc + progWith("VAR\n r : Counter;\n n : DINT;\nEND_VAR", "n := r.scratch;"),
			want: "internal to Counter",
		},
		{
			name: "reading a member that does not exist",
			src:  fbSrc + progWith("VAR\n r : Counter;\n n : DINT;\nEND_VAR", "n := r.nosuch;"),
			want: "has no member nosuch",
		},
		{
			name: "invoking something that is not an instance",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "n(1);"),
			want: "not a function block instance",
		},
		{
			name: "calling a function block in an expression",
			src:  fbSrc + progWith("VAR\n n : DINT;\nEND_VAR", "n := Counter(1);"),
			want: "invoke an instance of it",
		},
		{
			name: "a function block may not declare VAR_GLOBAL",
			src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR_GLOBAL\n g : DINT;\nEND_VAR\n" +
				"g := 1;\nEND_FUNCTION_BLOCK\n" + progWith("", "q := TRUE;"),
			want: "may not declare VAR_GLOBAL",
		},

		// --- recursion, in both its forms ---
		{
			name: "a function block that contains itself",
			src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR\n inner : B;\nEND_VAR\n" +
				"inner(  );\nEND_FUNCTION_BLOCK\n" + progWith("", "q := TRUE;"),
			want: "can reach itself",
		},
		{
			// The form that reaches the passes below: a PROGRAM declaring an
			// instance gives them an instance tree to walk, and the tree of a
			// block containing itself is infinite.  The containment check has
			// to come before any of that, or the walk does not terminate.
			name: "a program holding a function block that contains itself",
			src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR\n inner : B;\nEND_VAR\n" +
				"inner();\nEND_FUNCTION_BLOCK\n" + progWith("VAR\n n : B;\nEND_VAR", "n();"),
			want: "can reach itself",
		},
		{
			name: "two function blocks that contain each other",
			src: "{attribute 'summary' := 'x'}\n" +
				"FUNCTION_BLOCK A\nVAR\n b : B;\nEND_VAR\nb();\nEND_FUNCTION_BLOCK\n" +
				"FUNCTION_BLOCK B\nVAR\n a : A;\nEND_VAR\na();\nEND_FUNCTION_BLOCK\n" +
				progWith("", "q := TRUE;"),
			want: "can reach itself",
		},
		{
			// A directly self-recursive function cannot be written at all:
			// inside the body the function's own name is the result variable,
			// per IEC.  Mutual recursion is the form that has to be caught.
			name: "two functions that call each other",
			src: "{attribute 'summary' := 'x'}\n" +
				"FUNCTION F : DINT\nVAR_INPUT\n n : DINT;\nEND_VAR\nF := G(n);\nEND_FUNCTION\n" +
				"FUNCTION G : DINT\nVAR_INPUT\n n : DINT;\nEND_VAR\nG := F(n);\nEND_FUNCTION\n" +
				progWith("", "q := TRUE;"),
			want: "can reach itself",
		},

		// --- functions ---
		{
			name: "wrong argument count",
			src: "{attribute 'summary' := 'x'}\nFUNCTION Add : DINT\nVAR_INPUT\n a, b : DINT;\nEND_VAR\n" +
				"Add := a + b;\nEND_FUNCTION\n" + progWith("VAR\n n : DINT;\nEND_VAR", "n := Add(1);"),
			want: "takes 2 arguments, got 1",
		},
		{
			name: "a function may not keep state",
			src: "{attribute 'summary' := 'x'}\nFUNCTION F : DINT\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
				"F := 1;\nEND_FUNCTION\n" + progWith("", "q := TRUE;"),
			want: "keeps no state between calls",
		},
		{
			name: "a function may not hold an instance",
			src: fbSrc + "FUNCTION F : DINT\nVAR\n c : Counter;\nEND_VAR\nF := 1;\nEND_FUNCTION\n" +
				progWith("", "q := TRUE;"),
			want: "may not hold a function block instance",
		},
		{
			name: "discarding a function result",
			src: "{attribute 'summary' := 'x'}\nFUNCTION F : DINT\nVAR_INPUT\n n : DINT;\nEND_VAR\n" +
				"F := n;\nEND_FUNCTION\n" + progWith("", "F(1);"),
			want: "discards it",
		},
		{
			// A stateless function that depended on the rate of whatever
			// thread called it would not be stateless.  A FUNCTION_BLOCK is a
			// different matter -- it has an instance, and so a thread.
			name: "fperiod inside a FUNCTION",
			src: "{attribute 'summary' := 'x'}\nFUNCTION F : LREAL\nVAR_INPUT\n v : LREAL;\nEND_VAR\n" +
				"F := v * fperiod;\nEND_FUNCTION\n" + progWith("", "q := TRUE;"),
			want: "fperiod is not available in a FUNCTION",
		},

		// --- several programs, and what they share ---
		{
			name: "two programs of the same name",
			src: "{attribute 'summary' := 'x'}\n" +
				"PROGRAM a\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM\n" +
				"PROGRAM a\nVAR_OUTPUT\n r : BOOL;\nEND_VAR\nr := TRUE;\nEND_PROGRAM",
			want: "PROGRAM a is already declared",
		},
		{
			name: "VAR_GLOBAL inside a PROGRAM",
			src:  progWith("VAR_GLOBAL\n g : DINT;\nEND_VAR", "q := TRUE;"),
			want: "belongs at file level",
		},
		{
			name: "VAR_EXTERNAL is unnecessary",
			src:  progWith("VAR_EXTERNAL\n g : DINT;\nEND_VAR", "q := TRUE;"),
			want: "already visible from every PROGRAM",
		},
		{
			name: "a pin name used by two programs",
			src: "{attribute 'summary' := 'x'}\n" +
				"PROGRAM a\nVAR_OUTPUT\n shared : BOOL;\nEND_VAR\nshared := TRUE;\nEND_PROGRAM\n" +
				"PROGRAM b\nVAR_OUTPUT\n shared : BOOL;\nEND_VAR\nshared := TRUE;\nEND_PROGRAM",
			want: "already declared",
		},

		{
			// A day and a time of day are each half of a point in time, and
			// what they measure differs -- so the arithmetic between them is
			// checked rather than assumed.  See TestDayAndClockArithmetic.
			name: "a day and a time of day do not mix",
			src:  wrap("VAR\n d : DATE;\n c : TIME_OF_DAY;\nEND_VAR", "d := c;"),
			want: "cannot assign",
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

// TestActionDiagnostics covers what an ACTION may and may not be.  An action
// is a named piece of a POU's body sharing its variables, so most of the rules
// follow from having no interface and no state of its own.
func TestActionDiagnostics(t *testing.T) {
	const fb = "{attribute 'summary' := 'x'}\n" +
		"FUNCTION_BLOCK B\nVAR\n n : DINT;\nEND_VAR\n" +
		"go();\n" +
		"ACTION go:\n n := n + 1;\nEND_ACTION\n" +
		"END_FUNCTION_BLOCK\n"

	tests := []struct{ name, src, want string }{
		{
			name: "an action taking a positional argument",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "go(1);\nACTION go:\n n := 1;\nEND_ACTION"),
			want: "an action has no parameters",
		},
		{
			name: "an action with an output binding",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "go(n => n);\nACTION go:\n n := 1;\nEND_ACTION"),
			want: "an action takes no output binding",
		},
		{
			name: "presetting something that is not assignable",
			src:  progWith("VAR_INPUT\n p : DINT;\nEND_VAR", "go(p := 1);\nACTION go:\n q := TRUE;\nEND_ACTION"),
			want: "cannot assign to p",
		},
		{
			name: "presetting a name that does not exist",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "go(nosuch := 1);\nACTION go:\n n := 1;\nEND_ACTION"),
			want: "nosuch is not declared",
		},
		{
			name: "two actions of one POU sharing a name",
			src: progWith("VAR\n n : DINT;\nEND_VAR",
				"q := TRUE;\nACTION go:\n n := 1;\nEND_ACTION\nACTION go:\n n := 2;\nEND_ACTION"),
			want: "go is already declared",
		},
		{
			name: "an action colliding with a variable",
			src:  progWith("VAR\n go : DINT;\nEND_VAR", "q := TRUE;\nACTION go:\n go := 1;\nEND_ACTION"),
			want: "go is already declared",
		},
		{
			// The stack depth cannot be bounded, and an action calling itself
			// is the cheapest way to write an unbounded one.
			name: "an action that calls itself",
			src:  progWith("VAR\n n : DINT;\nEND_VAR", "go();\nACTION go:\n n := n + 1;\n go();\nEND_ACTION"),
			want: "can reach itself",
		},
		{
			name: "two actions that call each other",
			src: progWith("VAR\n n : DINT;\nEND_VAR",
				"a();\nACTION a:\n b();\nEND_ACTION\nACTION b:\n a();\nEND_ACTION"),
			want: "can reach itself",
		},
		{
			name: "an action of a function block that calls itself",
			src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR\n n : DINT;\nEND_VAR\n" +
				"go();\nACTION go:\n go();\nEND_ACTION\nEND_FUNCTION_BLOCK\n" +
				progWith("", "q := TRUE;"),
			want: "can reach itself",
		},
		{
			name: "an action reached from outside its POU",
			src:  fb + progWith("VAR\n b : B;\nEND_VAR", "go();"),
			want: "go is not declared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.src)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant a message containing %q", err, tt.want)
			}
		})
	}
}

// TestParamDiagnostics covers the rules an AT parameter carries.
//
// A parameter is a pin with no signal able to connect to it, so the direction
// rule is the same one pins have: rw is HAL's to set and the component's to
// read, r the reverse.  What differs is that there is no array form -- a HAL
// parameter is a single value.
func TestParamDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  string
		want string
	}{{
		name: "assigning an rw parameter",
		src: wrap("VAR\n gain AT %MRW : LREAL := 1.0;\nEND_VAR",
			"gain := 2.0;"),
		want: "it is an rw parameter, which HAL sets and the component reads",
	}, {
		name: "an unknown area",
		src:  wrap("VAR\n gain AT %MX : LREAL;\nEND_VAR", "q := TRUE;"),
		want: "is a physical address",
	}, {
		name: "an area that is neither pin nor parameter",
		src:  wrap("VAR\n gain AT %Z : LREAL;\nEND_VAR", "q := TRUE;"),
		want: "unknown HAL area %Z",
	}, {
		name: "an array parameter",
		src:  wrap("VAR\n gains AT %MRW : ARRAY [0..3] OF LREAL;\nEND_VAR", "q := TRUE;"),
		want: "a HAL parameter is a single value",
	}, {
		name: "a type with no HAL representation",
		src: "{attribute 'summary' := 'x'}\nTYPE P : STRUCT\n a : DINT;\nEND_STRUCT\nEND_TYPE\n" +
			progWith("VAR\n p AT %MRW : P;\nEND_VAR", "q := TRUE;"),
		want: "cannot be mapped to a HAL parameter",
	}, {
		name: "a non-constant default",
		src:  wrap("VAR\n n : DINT;\n gain AT %MRW : LREAL := n;\nEND_VAR", "q := TRUE;"),
		want: "default value of parameter gain must be a compile-time constant",
	}, {
		name: "a parameter colliding with a pin",
		src: "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n ready : BOOL;\nEND_VAR\n" +
			"VAR\n flag AT %MR.ready : BOOL;\nEND_VAR\nready := TRUE;\nEND_PROGRAM",
		want: "parameter ready is already declared",
	}, {
		name: "assigning an rw parameter from an action",
		src: "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\n" +
			"VAR\n gain AT %MRW : LREAL := 1.0;\nEND_VAR\n" +
			"go(gain := 2.0);\nACTION go:\n q := TRUE;\nEND_ACTION\nEND_PROGRAM",
		want: "it is an rw parameter",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.src)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant a message containing %q", err, tt.want)
			}
		})
	}
}

// TestParamAccepted covers what an AT parameter is allowed to do: an r
// parameter is written by the body, and an rw one is read by it.
func TestParamAccepted(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(
		"VAR\n gain AT %MRW : LREAL := 2.0;\n hits AT %MR.count : DINT;\nEND_VAR",
		"hits := hits + 1;\nq := gain > 1.0;"))

	if len(pkg.Component.Params) != 2 {
		t.Fatalf("got %d parameters, want 2", len(pkg.Component.Params))
	}
	gain, hits := pkg.Component.Params[0], pkg.Component.Params[1]
	if gain.Name != "gain" || gain.Dir != ast.ParamRW || gain.Default != "2.0" {
		t.Errorf("gain = %+v", gain)
	}
	// The AT clause renamed it, so the parameter is `count` and the variable
	// stays `hits`.
	if hits.Name != "count" || hits.Dir != ast.ParamR {
		t.Errorf("hits = %+v", hits)
	}
	// A parameter is not a pin, and must not be counted as one.
	if len(pkg.Component.Pins) != 1 {
		t.Errorf("got %d pins, want only the VAR_OUTPUT", len(pkg.Component.Pins))
	}
}

// TestPeriodIsReachableEverywhere covers the reversal of frontend §3.3.
//
// fperiod is the period of the thread this body is running in, and the backend
// can reach it from anywhere: from the HAL function's own argument where that
// is in scope, and from the RT task otherwise.  So a FUNCTION_BLOCK and an
// ACTION may both use it, which is what the standard library's timers need.
func TestPeriodIsReachableEverywhere(t *testing.T) {
	pkg := mustCheck(t, "p", "{attribute 'summary' := 'x'}\n"+
		"FUNCTION_BLOCK B\nVAR_OUTPUT\n acc : LREAL;\nEND_VAR\n"+
		"acc := acc + fperiod;\nEND_FUNCTION_BLOCK\n"+
		"PROGRAM p\nVAR_OUTPUT\n q : LREAL;\nEND_VAR\n"+
		"VAR\n b : B;\n own : LREAL;\nEND_VAR\n"+
		"b();\nq := b.acc;\ntick();\n"+
		"ACTION tick:\n own := own + fperiod;\nEND_ACTION\nEND_PROGRAM")

	// Every reference is the period storage class, whichever body it came
	// from: it is one thing, and the backend alone decides how to reach it.
	var seen int
	note := func(body []ast.Stmt) {
		walkStmtExprs(body, func(e ast.Expr) {
			ref, ok := e.(*ast.VarRef)
			if !ok {
				return
			}
			if ref.Name == "fperiod" {
				seen++
				if ref.Store != ast.StoragePeriod {
					t.Errorf("fperiod resolved to storage %v, want period", ref.Store)
				}
			}
		})
	}
	note(pkg.Blocks[0].Body)
	note(pkg.Component.Functions[0].Actions[0].Body)
	if seen != 2 {
		t.Errorf("found %d fperiod references, want 2 (one in the block, one in the action)", seen)
	}
}

// TestTimeDiagnostics covers what a duration may and may not take part in.
//
// A duration is not a number.  Letting it be one would accept `PT + 1` -- one
// what? -- so every combination that has an unambiguous meaning is written out
// and the rest are refused by name.
func TestTimeDiagnostics(t *testing.T) {
	decls := "VAR\n pt : TIME := T#1s;\n n : DINT;\n x : LREAL;\nEND_VAR"

	for _, tt := range []struct {
		name string
		src  string
		want string
	}{{
		name: "adding a number to a duration",
		src:  wrap(decls, "pt := pt + 1;"),
		want: "needs two durations",
	}, {
		name: "comparing a duration with a number",
		src:  wrap(decls, "q := pt > 5;"),
		want: "only be compared with another duration",
	}, {
		name: "multiplying two durations",
		src:  wrap(decls, "pt := pt * pt;"),
		want: "two durations cannot be multiplied",
	}, {
		name: "dividing a number by a duration",
		src:  wrap(decls, "n := n / pt;"),
		want: "cannot be divided by a duration",
	}, {
		name: "MOD on a duration",
		src:  wrap(decls, "pt := pt MOD 2;"),
		want: "cannot be applied to a duration",
	}, {
		name: "assigning a number to a duration",
		src:  wrap(decls, "pt := n;"),
		want: "cannot",
	}, {
		name: "a malformed literal",
		src:  wrap(decls, "pt := T#30s1m;"),
		want: "largest to smallest",
	}, {
		name: "a conversion given the wrong type",
		src:  wrap(decls, "n := TIME_TO_DINT(n);"),
		want: "TIME_TO_DINT takes TIME",
	}, {
		// A divisor written out as zero is a mistake, not something the RT
		// guard exists for.  A duration would otherwise slip past that rule.
		name: "a duration divided by a written-out zero",
		src:  wrap(decls, "pt := pt / 0;"),
		want: "division by zero",
	}, {
		// Every prefix in the family now parses, so what is left to get wrong
		// is mixing them: a time of day is a position within some day, and a
		// point in time says which day.  Comparing them compares nothing.
		name: "a time of day compared with a point in time",
		src:  wrap(decls, "q := TOD#08:00:00 < DT#2026-01-01-09:00:00;"),
		want: "they measure different things",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.src)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant a message containing %q", err, tt.want)
			}
		})
	}
}

// TestTimeAccepted covers what a duration may do, and that the literal reached
// the value it should.
func TestTimeAccepted(t *testing.T) {
	pkg := mustCheck(t, "p", wrap(
		"VAR\n pt : TIME := T#1m30s;\n half : TIME;\n n : DINT;\n ratio : LREAL;\nEND_VAR",
		"half := pt / 2;\nratio := pt / half;\nn := TIME_TO_DINT(pt);\nq := pt > half;"))

	var pt *ast.Variable
	for i := range pkg.Component.Variables {
		if pkg.Component.Variables[i].Name == "pt" {
			pt = &pkg.Component.Variables[i]
		}
	}
	if pt == nil {
		t.Fatal("pt was not declared")
	}
	// A duration lands in inst_t as the int64 count of nanoseconds it is.
	if pt.CType != "int64_t" {
		t.Errorf("pt is declared %q, want int64_t", pt.CType)
	}
	// 90 seconds in nanoseconds: the representation is the point.
	if pt.Default != "90000000000LL" {
		t.Errorf("T#1m30s initialised to %q, want 90000000000LL", pt.Default)
	}
}

// TestCNameDiagnostics covers the names the generated C could not spell.
//
// Every ST variable, pin, parameter and struct field reaches the C as a bare
// identifier, so one that is a C keyword produces code gcc rejects -- an error
// against generated source nobody keeps, which is what this frontend exists to
// prevent.  Names that reach the C decorated are not at risk and are not
// refused.
func TestCNameDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  string
		want string
	}{{
		name: "a variable",
		src:  wrap("VAR\n int : DINT;\nEND_VAR", "q := TRUE;"),
		want: "a variable cannot be named int",
	}, {
		// ST is case-insensitive, so Int and int are one name; which spelling
		// happened to be declared must not decide whether it builds.
		name: "a variable, differently spelled",
		src:  wrap("VAR\n Long : DINT;\nEND_VAR", "q := TRUE;"),
		want: "cannot be named Long",
	}, {
		name: "an output pin",
		src: "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n static : BOOL;\nEND_VAR\n" +
			"static := TRUE;\nEND_PROGRAM",
		want: "cannot be named static",
	}, {
		// The pin's own name, which an AT clause may make differ from the
		// variable's -- and it is the pin's that becomes the member.
		name: "a pin renamed by AT",
		src:  wrap("VAR\n count AT %Q.int : DINT;\nEND_VAR", "count := 1;"),
		want: "a pin cannot be named int",
	}, {
		name: "a parameter renamed by AT",
		src:  wrap("VAR\n gain AT %MRW.double : LREAL;\nEND_VAR", "q := gain > 1.0;"),
		want: "a parameter cannot be named double",
	}, {
		name: "a struct field",
		src: "{attribute 'summary' := 'x'}\nTYPE S : STRUCT\n union : DINT;\nEND_STRUCT\nEND_TYPE\n" +
			progWith("VAR\n s : S;\nEND_VAR", "q := TRUE;"),
		want: "a struct field cannot be named union",
	}, {
		name: "a function block member",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR\n signed : DINT;\nEND_VAR\n" +
			"signed := 1;\nEND_FUNCTION_BLOCK\n" + progWith("VAR\n b : B;\nEND_VAR", "b();"),
		want: "cannot be named signed",
	}, {
		name: "a function local",
		src: "{attribute 'summary' := 'x'}\nFUNCTION F : DINT\nVAR\n register : DINT;\nEND_VAR\n" +
			"register := 1;\nF := register;\nEND_FUNCTION\n" + progWith("", "q := F() > 0;"),
		want: "cannot be named register",
	}, {
		// The generator's own names all begin with a double underscore, and C
		// reserves the prefix besides.
		name: "a leading double underscore",
		src:  wrap("VAR\n __state : DINT;\nEND_VAR", "q := TRUE;"),
		want: "leading double underscore is reserved",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.src)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant a message containing %q", err, tt.want)
			}
		})
	}
}

// TestCNameDecoratedAreFine is the other half: a name that reaches the C
// decorated is not at risk, and refusing it would reject a legal program for
// no reason.
func TestCNameDecoratedAreFine(t *testing.T) {
	// A type becomes `Mode_t`, an enumeration constant `MODE_AUTO`, a function
	// block `Switch_t` and `Switch_body`, an action `p_go`.  None is bare.
	mustCheck(t, "p", "{attribute 'summary' := 'x'}\n"+
		"TYPE Mode : (AUTO, MANUAL); END_TYPE\n"+
		"FUNCTION_BLOCK Switch\nVAR_OUTPUT\n m : Mode;\nEND_VAR\n"+
		"m := AUTO;\nEND_FUNCTION_BLOCK\n"+
		"PROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\n"+
		"VAR\n sw : Switch;\nEND_VAR\n"+
		"sw();\nIF sw.m = AUTO THEN go(); END_IF;\n"+
		"ACTION go:\n q := TRUE;\nEND_ACTION\nEND_PROGRAM")
}

// TestExternalActionCall covers `instance.action()`, and the rule that keeps
// it out of the way of methods.
//
// Actions and methods are different constructs -- an action has no interface,
// a method has one -- and they share this call syntax, which is what CODESYS
// does. A name is one or the other, so resolution is unambiguous; what would
// not be is an argument list, which is why an external action call takes none.
func TestExternalActionCall(t *testing.T) {
	const motor = "{attribute 'summary' := 'x'}\n" +
		"FUNCTION_BLOCK Motor\nVAR_OUTPUT\n speed : DINT;\nEND_VAR\n" +
		"speed := speed;\nACTION stop:\n speed := 0;\nEND_ACTION\nEND_FUNCTION_BLOCK\n"

	t.Run("accepted", func(t *testing.T) {
		pkg := mustCheck(t, "p", motor+
			progWith("VAR\n m : Motor;\nEND_VAR", "m();\nm.stop();\nq := m.speed > 0;"))
		var found *ast.ActionCall
		walkStmtsFor(pkg.Component.Functions[0].Body, func(s ast.Stmt) {
			if call, ok := s.(*ast.ActionCall); ok && call.Inst != nil {
				found = call
			}
		})
		if found == nil {
			t.Fatal("m.stop() did not lower to an action call on an instance")
		}
		if found.Owner != "Motor" || found.Action != "stop" {
			t.Errorf("call is %s.%s, want Motor.stop", found.Owner, found.Action)
		}
	})

	for _, tt := range []struct{ name, src, want string }{{
		// The one that matters for methods: an argument list here would be
		// the same spelling a method call uses, and its meaning would depend
		// on what the name turned out to be.
		name: "arguments",
		src:  motor + progWith("VAR\n m : Motor;\nEND_VAR", "m.stop(speed := 5);"),
		want: "takes no arguments: an action has no interface",
	}, {
		name: "a name that is neither an action nor a member",
		src:  motor + progWith("VAR\n m : Motor;\nEND_VAR", "m.nosuch();"),
		want: "has no member nosuch",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatalf("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}
}

// TestBlocksOrderedByContainment is a regression.
//
// A block's struct names the types of the blocks it holds, so C needs those
// complete first.  ST declaration order carries no such obligation -- bodies
// are forward-declared so blocks may call each other in any order -- and a
// block declared before the one it contains used to emit `Inner_t inner;`
// before Inner_t existed, with the error landing in generated C.
func TestBlocksOrderedByContainment(t *testing.T) {
	pkg := mustCheck(t, "p", "{attribute 'summary' := 'x'}\n"+
		"FUNCTION_BLOCK Outer\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n"+
		"VAR\n inner : Inner;\nEND_VAR\ninner();\no := inner.o;\nEND_FUNCTION_BLOCK\n"+
		"FUNCTION_BLOCK Inner\nVAR_OUTPUT\n o : DINT;\nEND_VAR\no := 2;\nEND_FUNCTION_BLOCK\n"+
		progWith("VAR\n x : Outer;\nEND_VAR", "x();\nq := x.o > 0;"))

	at := map[string]int{}
	for i := range pkg.Blocks {
		at[pkg.Blocks[i].Name] = i
	}
	if len(at) != 2 {
		t.Fatalf("got blocks %v, want Outer and Inner", at)
	}
	if at["Inner"] > at["Outer"] {
		t.Errorf("Inner is emitted at %d, after Outer at %d; the contained block has to come first",
			at["Inner"], at["Outer"])
	}
}

// walkStmtsFor visits every statement of a body, including nested ones.
func walkStmtsFor(body []ast.Stmt, f func(ast.Stmt)) { walkBody(body, f) }

// TestHALNameLength covers the limit HAL stores a name in.
//
// Truncation is the failure worth preventing: two pins differing only past the
// limit become one on the machine, and nothing says so. The check used to
// exist only for a pin published along an instance path -- the case that grows
// without anyone noticing -- and not for a POU's own, which is the one an
// author is more likely to write.
func TestHALNameLength(t *testing.T) {
	long := func(n int) string { return strings.Repeat("z", n) }

	for _, tt := range []struct {
		name string
		src  string
		want string
	}{{
		name: "a pin",
		src: "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n " +
			long(130) + " : DINT;\nEND_VAR\n" + long(130) + " := 1;\nEND_PROGRAM",
		want: "over HAL's limit of 127",
	}, {
		// An array pin only reaches its full width once the index is on it:
		// what has to fit is comp.name.99, not comp.name.
		name: "an array pin, measured at its widest element",
		src: "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n " +
			long(124) + " : ARRAY [0..99] OF DINT;\nEND_VAR\n" +
			long(124) + "[0] := 1;\nEND_PROGRAM",
		want: ".## is 129 characters",
	}, {
		name: "a parameter",
		src:  wrap("VAR\n "+long(130)+" AT %MRW : LREAL;\nEND_VAR", "q := TRUE;"),
		want: "the parameter name",
	}, {
		name: "a pin published along an instance path",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR\n " +
			long(130) + " AT %Q : DINT;\nEND_VAR\n" + long(130) + " := 1;\nEND_FUNCTION_BLOCK\n" +
			progWith("VAR\n inst : B;\nEND_VAR", "inst();"),
		want: "over HAL's limit of 127",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatal("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}

	// And the boundary is not off by one: exactly 127 fits.
	fits := "p." + strings.Repeat("z", 125)
	if len(fits) != 127 {
		t.Fatalf("test is wrong: %d", len(fits))
	}
	mustCheck(t, "p", "{attribute 'summary' := 'x'}\nPROGRAM p\nVAR_OUTPUT\n "+
		strings.Repeat("z", 125)+" : DINT;\nEND_VAR\n"+
		strings.Repeat("z", 125)+" := 1;\nEND_PROGRAM")
}

// TestInstanceMemberNames covers the names inst_t already carries.
//
// Every component variable, pin and parameter becomes a member beside `base`,
// `env`, `comp_id`, `name` and `hal`, so one of those names is a duplicate
// member -- reported by the C compiler against generated code rather than
// against the declaration.
//
// The other half matters as much: these are only refused where the name lands
// in inst_t. A method parameter is a C local and collides with nothing, and
// refusing it would reject a provider implementing an interface whose function
// takes a `comp_id`, which the `home` interface does.
func TestInstanceMemberNames(t *testing.T) {
	for _, name := range []string{"hal", "name", "comp_id", "env", "base"} {
		t.Run("variable "+name, func(t *testing.T) {
			_, err := check(t, "p", wrap("VAR\n "+name+" : DINT;\nEND_VAR", "q := TRUE;"))
			if err == nil || !strings.Contains(err.Error(), "part of every instance") {
				t.Errorf("a variable named %s gave %v, want a refusal", name, err)
			}
		})
	}
	t.Run("an output pin", func(t *testing.T) {
		_, err := check(t, "p", "{attribute 'summary' := 'x'}\nPROGRAM p\n"+
			"VAR_OUTPUT\n hal : BOOL;\nEND_VAR\nhal := TRUE;\nEND_PROGRAM")
		if err == nil || !strings.Contains(err.Error(), "part of every instance") {
			t.Errorf("a pin named hal gave %v, want a refusal", err)
		}
	})

	// Not an instance member: a method's parameter, a function's local, and a
	// function block's own member all live somewhere else.
	mustCheck(t, "p", "{attribute 'summary' := 'x'}\n"+
		"FUNCTION_BLOCK B\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n"+
		"VAR\n hal : DINT;\nEND_VAR\n"+
		"o := hal;\n"+
		"METHOD go : DINT\nVAR_INPUT\n comp_id : DINT;\nEND_VAR\n"+
		"go := comp_id;\nEND_METHOD\n"+
		"END_FUNCTION_BLOCK\n"+
		progWith("VAR\n b : B;\nEND_VAR", "b();\nq := b.o > 0;"))
}

// TestStringVariableIsSized is a regression.
//
// A STRING is a char array, and neither path that emits a variable knew it:
// `label : STRING[32]` became `char label;`, one byte where the declaration
// asked for thirty-three, and every use of it wrote past the end.  Valid C,
// and memory corruption in the real-time process.
func TestStringVariableIsSized(t *testing.T) {
	pkg := mustCheck(t, "p", wrap("VAR\n label : STRING[32] := 'hi';\nEND_VAR", "q := TRUE;"))

	var v *ast.Variable
	for i := range pkg.Component.Variables {
		if pkg.Component.Variables[i].Name == "label" {
			v = &pkg.Component.Variables[i]
		}
	}
	if v == nil {
		t.Fatal("label was not declared")
	}
	if !v.String {
		t.Error("label is not marked as a STRING, so its initialiser would be filled rather than copied")
	}
	if v.Array != 33 {
		t.Errorf("label has room for %d characters, want 33 -- the declared length plus the terminator", v.Array)
	}
}

// TestOwnMethodCall covers calling a method on the instance already in scope.
//
// A method needs an instance, and inside a block's own body, action or method
// there is one -- __self in the generated function -- so no name for it is
// needed. IEC spells that THIS^.name(); the bare form is what CODESYS accepts.
// Both reach the same call.
func TestOwnMethodCall(t *testing.T) {
	const block = "{attribute 'summary' := 'x'}\n" +
		"FUNCTION_BLOCK B\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
		"VAR\n n : DINT;\nEND_VAR\n" +
		"o := twice();\nTHIS^.bump();\n" +
		"METHOD twice : DINT\n twice := n * 2;\nEND_METHOD\n" +
		"METHOD bump\n n := n + 1;\nEND_METHOD\n" +
		"END_FUNCTION_BLOCK\n"

	pkg := mustCheck(t, "p", block+progWith("VAR\n b : B;\nEND_VAR", "b();\nq := b.o > 0;"))

	var bare, viaThis *ast.MethodCallExpr
	walkStmtsFor(pkg.Blocks[0].Body, func(s ast.Stmt) {
		if c, ok := s.(*ast.MethodCallStmt); ok {
			viaThis = c.Call
		}
	})
	walkStmtExprs(pkg.Blocks[0].Body, func(e ast.Expr) {
		if c, ok := e.(*ast.MethodCallExpr); ok && c.Method == "twice" {
			bare = c
		}
	})
	if bare == nil {
		t.Fatal("the bare sibling call did not resolve")
	}
	if viaThis == nil {
		t.Fatal("THIS^.bump() did not resolve")
	}
	// Both name no instance: the one in scope needs no expression to reach.
	if bare.Inst != nil || viaThis.Inst != nil {
		t.Error("a call on the instance in scope should carry no instance expression")
	}
	if viaThis.Method != "bump" || viaThis.Owner != "B" {
		t.Errorf("THIS^ resolved to %s.%s", viaThis.Owner, viaThis.Method)
	}
}

// TestMethodRecursion covers what allowing sibling calls made possible.
//
// Until a method could reach a sibling, methods could not recurse at all:
// reaching one needed THIS, and reaching another block's needed containment,
// which is refused. Both routes now exist, so the cycle check has to see them.
func TestMethodRecursion(t *testing.T) {
	for _, tt := range []struct{ name, src, want string }{{
		name: "two methods calling each other",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK R\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
			"o := a();\nMETHOD a : DINT\n a := b();\nEND_METHOD\n" +
			"METHOD b : DINT\n b := a();\nEND_METHOD\nEND_FUNCTION_BLOCK\n" +
			progWith("VAR\n r : R;\nEND_VAR", "r();\nq := r.o > 0;"),
		// Qualified, so the reader knows which block to look in.
		want: "R.a can reach itself",
	}, {
		name: "a method calling itself",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK R\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
			"o := a();\nMETHOD a : DINT\n a := a() + 1;\nEND_METHOD\nEND_FUNCTION_BLOCK\n" +
			progWith("VAR\n r : R;\nEND_VAR", "r();\nq := r.o > 0;"),
		want: "R.a can reach itself",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatal("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}

	// A block calling its own method is not recursion, and used to be reported
	// as such: the call added an edge to the block as well as to the method,
	// so every block that used one reached itself.
	mustCheck(t, "p", "{attribute 'summary' := 'x'}\n"+
		"FUNCTION_BLOCK R\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n"+
		"o := a();\nMETHOD a : DINT\n a := 1;\nEND_METHOD\nEND_FUNCTION_BLOCK\n"+
		progWith("VAR\n r : R;\nEND_VAR", "r();\nq := r.o > 0;"))
}

// TestThisIsNotAValue covers the half of THIS that stays refused.
//
// In IEC it is a pointer and THIS^ dereferences it. stratuMAK has no pointers,
// so letting THIS be a value would put one back; as the base of a call it need
// not be one, since the instance is already a parameter of the function.
func TestThisIsNotAValue(t *testing.T) {
	for _, tt := range []struct{ name, src, want string }{{
		name: "used as a value",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
			"o := THIS;\nEND_FUNCTION_BLOCK\n" + progWith("VAR\n b : B;\nEND_VAR", "b();"),
		want: "THIS is not a value",
	}, {
		name: "outside any function block",
		src:  wrap("", "q := THIS^.nosuch();"),
		want: "there is none here",
	}, {
		name: "naming a method that does not exist",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
			"THIS^.nosuch();\nEND_FUNCTION_BLOCK\n" + progWith("VAR\n b : B;\nEND_VAR", "b();"),
		want: "B has no method nosuch",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatal("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}
}

// TestStringPlacements covers where a STRING may and may not appear.
//
// It is a char array, and C's rules about arrays decide all of these: an array
// cannot be returned, an array parameter is a pointer, and an array of them
// needs both dimensions. Each was broken and each failed loudly in generated
// C rather than against the declaration.
func TestStringPlacements(t *testing.T) {
	for _, tt := range []struct{ name, src, want string }{{
		// C cannot return an array. A static buffer would be shared between
		// callers, and a FUNCTION has no output parameter to fill instead.
		name: "a function returning one",
		src: "{attribute 'summary' := 'x'}\nFUNCTION f : STRING[8]\n f := 'hi';\nEND_FUNCTION\n" +
			progWith("", "q := TRUE;"),
		want: "may not return a STRING",
	}, {
		name: "a method returning one",
		src: "{attribute 'summary' := 'x'}\nFUNCTION_BLOCK B\nVAR_OUTPUT\n o : DINT;\nEND_VAR\n" +
			"o := 1;\nMETHOD m : STRING[8]\n m := 'hi';\nEND_METHOD\nEND_FUNCTION_BLOCK\n" +
			progWith("VAR\n b : B;\nEND_VAR", "b();"),
		want: "may not return a STRING",
	}, {
		// Passed as a pointer to the caller's characters, so writing through
		// it would change the caller's string.
		name: "assigning to a string parameter",
		src: "{attribute 'summary' := 'x'}\nFUNCTION f : DINT\nVAR_INPUT\n s : STRING[8];\nEND_VAR\n" +
			"s := 'no';\nf := 1;\nEND_FUNCTION\n" + progWith("", "q := f('x') > 0;"),
		want: "a view of the caller's characters",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", tt.src)
			if err == nil {
				t.Fatal("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}

	// An array of STRING carries both dimensions: the table's, and each
	// string's own. With only the first, every entry was one character.
	pkg := mustCheck(t, "p", wrap("VAR\n names : ARRAY [0..2] OF STRING[8];\nEND_VAR",
		"names[0] := 'hi';\nq := TRUE;"))
	var v *ast.Variable
	for i := range pkg.Component.Variables {
		if pkg.Component.Variables[i].Name == "names" {
			v = &pkg.Component.Variables[i]
		}
	}
	if v == nil {
		t.Fatal("names was not declared")
	}
	if len(v.Dims) != 2 || v.Dims[0] != 3 || v.Dims[1] != 9 {
		t.Errorf("names has dimensions %v, want [3 9] -- three strings of eight plus a terminator",
			v.Dims)
	}
}

// TestDurationOnAPin covers a TIME crossing the HAL boundary.
//
// HAL has no duration type, so a TIME pin is an s32 of milliseconds -- which
// is what a CODESYS TIME is, so a value set from outside means there what it
// means here.  The body still works in nanoseconds; the scale is applied where
// the pin is read and written, and a duration finer than a millisecond does
// not survive, which is warned about at the declaration.
func TestDurationOnAPin(t *testing.T) {
	pkg := mustCheck(t, "p", "{attribute 'summary' := 'x'}\nPROGRAM p\n"+
		"VAR_INPUT\n pt : TIME := T#500ms;\nEND_VAR\n"+
		"VAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := pt > T#0s;\nEND_PROGRAM")

	var pin *ast.Pin
	for i := range pkg.Component.Pins {
		if pkg.Component.Pins[i].Name == "pt" {
			pin = &pkg.Component.Pins[i]
		}
	}
	if pin == nil {
		t.Fatal("pt was not declared")
	}
	if pin.Type != ast.HALS32 {
		t.Errorf("a TIME pin is %v, want s32", pin.Type)
	}
	if !pin.Duration {
		t.Error("the pin is not marked a duration, so nothing would convert at the boundary")
	}
	// The default is what HAL stores, which is milliseconds: emitting the
	// nanoseconds would put 500000000 where setp reads 500.
	if pin.Default != "500" {
		t.Errorf("T#500ms defaults to %q on the pin, want 500 -- milliseconds, not nanoseconds",
			pin.Default)
	}
	if len(pkg.Warnings) == 0 || !strings.Contains(strings.Join(pkg.Warnings, "\n"), "milliseconds") {
		t.Errorf("no warning about the rounding: %v", pkg.Warnings)
	}
}

// TestDayAndClockArithmetic covers what the two halves of a point in time can
// and cannot do.
//
// They are separate types because what each can do differs, and the cases are
// written out rather than inferred: two days subtract to the time between
// them, a day and a time of day combine into a point in time, and adding two
// days is nothing at all.
func TestDayAndClockArithmetic(t *testing.T) {
	decls := "VAR\n d : DATE := D#2026-01-01;\n e : DATE := D#2026-02-01;\n" +
		" c : TIME_OF_DAY := TOD#08:00:00;\n f : TIME_OF_DAY := TOD#17:00:00;\n" +
		" stamp : DATE_AND_TIME;\n span : TIME;\nEND_VAR"

	mustCheck(t, "p", wrap(decls,
		"span := e - d;\nspan := f - c;\nstamp := d + c;\nstamp := c + d;\nq := c < f;"))

	for _, tt := range []struct{ name, body, want string }{{
		// The one people reach for. 23:00 plus two hours is one in the
		// morning of the next day, and a TIME_OF_DAY has no next day.
		name: "a duration added to a time of day",
		body: "c := c + T#2h;",
		want: "the result would leave the day",
	}, {
		name: "two days added",
		body: "d := d + e;",
		want: "cannot be applied to DATE and DATE",
	}, {
		name: "a day compared with a time of day",
		body: "q := d > c;",
		want: "they measure different things",
	}, {
		name: "a day assigned to a time of day",
		body: "c := d;",
		want: "cannot assign",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", wrap(decls, tt.body))
			if err == nil {
				t.Fatal("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}
}
