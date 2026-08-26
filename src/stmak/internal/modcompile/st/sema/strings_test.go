// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// strDecls are the variables the string-function tests work on.
const strDecls = "VAR\n a : STRING[8];\n b : STRING[4];\n" +
	" big : STRING[200];\n out : STRING[64];\n n : INT;\nEND_VAR"

// findStrFunc returns the first string-function call in the program's body.
func findStrFunc(t *testing.T, body string) *ast.StrFuncExpr {
	t.Helper()
	pkg := mustCheck(t, "p", wrap(strDecls, body))
	var found *ast.StrFuncExpr
	for _, s := range pkg.Component.Functions[0].Body {
		as, ok := s.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if e, ok := as.Value.(*ast.StrFuncExpr); ok {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("no string function in the lowered body of:\n%s", body)
	}
	return found
}

// TestStrFuncResultLength pins the size of the buffer each call needs.
//
// It is not a detail: the backend declares a local of exactly this size at
// every call site, so a length that is too small silently truncates a correct
// program and one that is too large is real-time stack nobody asked for.
func TestStrFuncResultLength(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want int
	}{
		// As long as both halves together.
		{"concat", "out := CONCAT(a, b);", 12},
		// A literal knows its own length, which is why building a message out
		// of one costs the message and not the default eighty.
		{"concat with a literal", "out := CONCAT('drive=', b);", 10},
		// No longer than the string it comes from...
		{"left", "out := LEFT(a, n);", 8},
		{"right", "out := RIGHT(a, n);", 8},
		{"delete", "out := DELETE(a, n, n);", 8},
		// ...and no longer than a written-out count, when there is one.
		{"left of a written-out count", "out := LEFT(a, 3);", 3},
		{"left of more than there is", "out := LEFT(a, 99);", 8},
		// Both halves again: the second goes inside the first.
		{"insert", "out := INSERT(a, b, n);", 12},
		{"replace", "out := REPLACE(a, b, n, n);", 12},
		// Capped where CODESYS caps it, so a ported source truncates in the
		// same place rather than growing a buffer this one would not have.
		{"past the cap", "out := CONCAT(big, big);", ast.MaxStringLen},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := findStrFunc(t, tt.body)
			if got := e.Typ.StringLen(); got != tt.want {
				t.Errorf("%s: result is STRING[%d], want STRING[%d]", tt.body, got, tt.want)
			}
		})
	}
}

// TestConcatFoldsToPairs covers the variadic form.
//
// CONCAT takes any number of arguments in IEC and two here, because two is
// what a C helper with a destination buffer can take. The extra ones fold into
// nested calls, left to right, which is what the operation means -- and each
// nested call is then an ordinary call site with a buffer of its own.
func TestConcatFoldsToPairs(t *testing.T) {
	e := findStrFunc(t, "out := CONCAT('a', 'bb', 'ccc', 'dddd');")

	// The outermost appends the last argument to everything before it.
	if len(e.Args) != 2 {
		t.Fatalf("the outermost CONCAT has %d arguments, want 2", len(e.Args))
	}
	if lit, ok := e.Args[1].(*ast.StringLit); !ok || lit.Value != "dddd" {
		t.Errorf("the outermost CONCAT appends %v, want the last argument", e.Args[1])
	}
	if got := e.Typ.StringLen(); got != 10 {
		t.Errorf("CONCAT of 1+2+3+4 characters is STRING[%d], want STRING[10]", got)
	}

	// Unwinding it gives the arguments back in order.
	var order []string
	var walk func(x ast.Expr)
	walk = func(x ast.Expr) {
		if c, ok := x.(*ast.StrFuncExpr); ok && c.Func == ast.StrConcat {
			walk(c.Args[0])
			walk(c.Args[1])
			return
		}
		if lit, ok := x.(*ast.StringLit); ok {
			order = append(order, lit.Value)
		}
	}
	walk(e)
	if got := strings.Join(order, ","); got != "a,bb,ccc,dddd" {
		t.Errorf("the nested calls read %q, want the arguments in the order written", got)
	}
}

// TestStrFuncAccepts covers the shapes that must compile.
func TestStrFuncAccepts(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"nested, which is the shape they appear in",
			"out := CONCAT(CONCAT('drive=', LEFT(a, 2)), '!');"},
		{"an index that is an expression", "out := MID(a, n + 1, n);"},
		{"IEC's own parameter names", "out := LEFT(IN := a, L := 3);"},
		{"a count of characters", "n := LEN(a) + FIND(a, b);"},
		{"a comparison of two results", "q := LEFT(a, 2) = RIGHT(b, 2);"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mustCheck(t, "p", wrap(strDecls, tt.body))
		})
	}
}

// TestStrFuncRefusals covers what must not compile, each for a stated reason.
func TestStrFuncRefusals(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{{
		name: "too few arguments",
		body: "out := MID(a, 2);",
		want: "MID takes 3 arguments (IN, L, P), got 2",
	}, {
		name: "CONCAT of one",
		body: "out := CONCAT(a);",
		want: "CONCAT takes 2 arguments or more, got 1",
	}, {
		name: "a number where a string goes",
		body: "out := LEFT(n, 2);",
		want: "IN is a STRING, got INT",
	}, {
		name: "a string where a count goes",
		body: "out := LEFT(a, b);",
		want: "L is a number of characters, got STRING[4]",
	}, {
		// A runtime count is clamped; one written out as negative is a
		// mistake, and the clamp is not what that is for.
		name: "a written-out negative count",
		body: "out := LEFT(a, -1);",
		want: "a position or a length cannot be negative",
	}, {
		name: "arguments named out of order",
		body: "out := LEFT(L := 3, IN := a);",
		want: "argument 1 is IN, not L",
	}, {
		// LEN counts characters, so it is a number and not a string.
		name: "a count assigned to a string",
		body: "out := LEN(a);",
		want: "cannot assign",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", wrap(strDecls, tt.body))
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.body)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}
}

// TestStrFromResultLength pins the buffer each conversion needs.
//
// These are exact rather than generous, and that is the whole safety argument
// for the family: the buffer holds the widest result the type can give, so no
// conversion in it can truncate and none of them needs a fault path.
func TestStrFromResultLength(t *testing.T) {
	decls := "VAR\n out : STRING[64];\n" +
		" s8 : SINT;\n u8 : USINT;\n i16 : INT;\n u16 : UINT;\n" +
		" i32 : DINT;\n u32 : UDINT;\n i64 : LINT;\n u64 : ULINT;\n" +
		" r : REAL;\n lr : LREAL;\n pt : TIME;\n dt : DATE_AND_TIME;\n" +
		" day : DATE;\n clock : TIME_OF_DAY;\n b : BOOL;\nEND_VAR"

	for _, tt := range []struct {
		body string
		want int
	}{
		{"out := BOOL_TO_STRING(b);", 5},   // FALSE
		{"out := SINT_TO_STRING(s8);", 4},  // -128
		{"out := USINT_TO_STRING(u8);", 3}, // 255
		{"out := INT_TO_STRING(i16);", 6},  // -32768
		{"out := UINT_TO_STRING(u16);", 5}, // 65535
		{"out := DINT_TO_STRING(i32);", 11},
		{"out := UDINT_TO_STRING(u32);", 10},
		{"out := LINT_TO_STRING(i64);", 20},
		{"out := ULINT_TO_STRING(u64);", 20},
		{"out := REAL_TO_STRING(r);", 32},
		{"out := LREAL_TO_STRING(lr);", 32},
		{"out := TIME_TO_STRING(pt);", 40},
		{"out := DT_TO_STRING(dt);", 40},
		{"out := DATE_TO_STRING(day);", 16},
		{"out := TOD_TO_STRING(clock);", 24},
	} {
		t.Run(tt.body, func(t *testing.T) {
			pkg := mustCheck(t, "p", wrap(decls, tt.body))
			as := pkg.Component.Functions[0].Body[0].(*ast.AssignStmt)
			e, ok := as.Value.(*ast.StrFuncExpr)
			if !ok || e.Func != ast.StrFrom {
				t.Fatalf("%s did not lower to a conversion, got %T", tt.body, as.Value)
			}
			if got := e.Typ.StringLen(); got != tt.want {
				t.Errorf("%s: buffer is %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

// TestStrFromRefusals covers what the family will not take.
func TestStrFromRefusals(t *testing.T) {
	decls := "VAR\n out : STRING[64];\n pt : TIME;\n day : DATE;\n" +
		" n : DINT;\n s : STRING[8];\nEND_VAR"

	for _, tt := range []struct{ name, body, want string }{{
		// All four are int64s underneath, so nothing but the declared type
		// would stop a duration being written out as a date fifty years off.
		name: "a duration where a point in time goes",
		body: "out := DT_TO_STRING(pt);",
		want: "DT_TO_STRING takes DATE_AND_TIME, got TIME",
	}, {
		name: "a day where a duration goes",
		body: "out := TIME_TO_STRING(day);",
		want: "TIME_TO_STRING takes TIME, got DATE",
	}, {
		name: "a number where a duration goes",
		body: "out := TIME_TO_STRING(n);",
		want: "TIME_TO_STRING takes TIME, got DINT",
	}, {
		name: "a string, which is one already",
		body: "out := INT_TO_STRING(s);",
		want: "cannot assign",
	}, {
		name: "two arguments",
		body: "out := INT_TO_STRING(1, 2);",
		want: "INT_TO_STRING takes one argument, got 2",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := check(t, "p", wrap(decls, tt.body))
			if err == nil {
				t.Fatalf("expected a diagnostic, got none\n--- source ---\n%s", tt.body)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("diagnostics:\n%v\nwant one containing %q", err, tt.want)
			}
		})
	}
}

// TestStrFromWidensANumber covers the one implicit conversion the family
// allows: a narrower integer reaching a wider parameter, as it would for any
// function. DINT_TO_STRING(anInt) should not need a cast written out.
func TestStrFromWidensANumber(t *testing.T) {
	decls := "VAR\n out : STRING[64];\n small : INT;\nEND_VAR"
	mustCheck(t, "p", wrap(decls, "out := DINT_TO_STRING(small);"))
}
