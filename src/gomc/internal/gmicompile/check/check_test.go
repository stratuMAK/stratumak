// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package check

import (
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/parser"
)

// validate parses src and runs the constraint checker, returning the errors as
// a single joined string for substring assertions.
func validate(t *testing.T, src string) string {
	t.Helper()
	api, perrs := parser.Parse("test.gmi", src)
	if len(perrs) > 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	var msgs []string
	for _, e := range Validate(api) {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "\n")
}

func TestValidateAcceptsWellTypedConstraints(t *testing.T) {
	src := `@api test
@version 1

enum Mode {
    A = 0
    B = 2
}

type Rec {
    n:     i32     @min(1) @max(10)
    ratio: f64     @min(0.0) @max(1.0)
    name:  string  @minlen(1) @maxlen(64) @regex("^[a-z]+$")
    tags:  []i32   @notempty
    fixed: [4]f64  @minlen(1)
    mode:  Mode    @enum_open
    opt:   i32?    @notnull
}

func f(n: i32 @min(0)) -> i32
`
	if got := validate(t, src); got != "" {
		t.Fatalf("expected no errors, got:\n%s", got)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name string
		body string // field/func lines inside the API
		want string
	}{
		{"min on string", `type T { s: string @min(0) }`, "@min applies to numeric"},
		{"maxlen on int", `type T { n: i32 @maxlen(5) }`, "@maxlen applies to string/slice/array"},
		{"regex on int", `type T { n: i32 @regex("x") }`, "@regex applies to string"},
		{"int overflow", `type T { n: i32 @max(9999999999) }`, "out of range for i32"},
		{"float on int", `type T { n: i32 @min(0.5) }`, "out of range for i32"},
		{"negative unsigned", `type T { n: u32 @min(-1) }`, "negative on unsigned"},
		{"min gt max", `type T { n: i32 @min(10) @max(1) }`, "@min(10) > @max(1)"},
		{"redundant notnull", `type T { s: string @notnull }`, "redundant on non-nullable"},
		{"notnull on nullable string", `type T { s: string? @notnull }`, "not expressible"},
		{"unsatisfiable len", `type T { xs: [4]f64 @minlen(9) }`, "exceeds fixed array length 4"},
		{"bad regex", `type T { s: string @regex("(") }`, "does not compile"},
		{"duplicate", `type T { n: i32 @min(0) @min(1) }`, "duplicate constraint @min"},
		{"constraint on out param", `func f(x: i32 out @min(0)) -> i32`, "out (output) parameter"},
		{"enum_open on non-enum", `type T { n: i32 @enum_open }`, "@enum_open applies to enum"},
		{"unknown type in field", `type T { x: Bogus }`, `unknown type "Bogus"`},
		{"unknown type in param", `func f(x: Bogus) -> i32`, `unknown type "Bogus"`},
		{"unknown type in return", `func f() -> Bogus`, `unknown type "Bogus"`},
		{"unknown type in slice", `type T { xs: []Bogus }`, `unknown type "Bogus"`},
		{"unknown type in array", `type T { xs: [4]Bogus }`, `unknown type "Bogus"`},
		{"misspelled primitive", `type T { n: i32x }`, `unknown type "i32x"`},
		{"duplicate type", "type A { x: i32 }\ntype A { y: i32 }", `duplicate name "A" (already declared as type`},
		{"type vs enum collision", "type A { x: i32 }\nenum A { X = 0 }", `duplicate name "A" (already declared as type`},
		{"duplicate func", "func f() -> i32\nfunc f() -> i32", `duplicate func "f"`},
		{"duplicate field", `type T { x: i32  x: f64 }`, `duplicate field of type T "x"`},
		{"duplicate param", `func f(a: i32, a: f64) -> i32`, `duplicate param of func f "a"`},
		{"duplicate enum member name", "enum E { A = 1  A = 2 }", `duplicate member of enum E "A"`},
		// @rc_error: the status/payload contract has to be declarable, or the
		// two sides of the bridge disagree about which value is the answer.
		{"rc_error without out param", "@rc_error\nfunc f(x: i32) -> i32", "requires at least one out parameter"},
		{"rc_error without i32 return", "type T { x: i32 }\n@rc_error\nfunc f(t: T out)", "requires an i32 return"},
		{"rc_error with returns_value", "type T { x: i32 }\n@rc_error\n@returns_value\nfunc f(t: T out) -> i32", "mutually exclusive"},
		{"rc_error REST with two outs", "type T { x: i32 }\n@method GET\n@path /\n@rc_error\nfunc f(a: T out, b: T out) -> i32", "exactly one out parameter"},
		{"slice out without rc_error", "type T { x: i32 }\nfunc f(xs: []T out) -> i32", "only supported on an @rc_error func"},
		{"two slice outs", "type T { x: i32 }\n@rc_error\nfunc f(a: []T out, b: []T out) -> i32", "only one slice out parameter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "@api test\n@version 1\n\n" + tc.body + "\n"
			got := validate(t, src)
			if !strings.Contains(got, tc.want) {
				t.Errorf("errors = %q, want substring %q", got, tc.want)
			}
		})
	}
}

// Duplicate enum VALUES (aliases) are intentionally allowed — only duplicate
// member NAMES are rejected (F3). ast.DistinctMembers dedups by value for the
// emitters, so two names sharing a value is legal.
func TestValidateAllowsAliasedEnumValues(t *testing.T) {
	src := `@api test
@version 1

enum E {
    OK = 0
    DEFAULT = 0
    ERR = 1
}
`
	if got := validate(t, src); got != "" {
		t.Fatalf("expected aliased enum values to be accepted, got:\n%s", got)
	}
}

func TestValidateEnumMinMaxRedundant(t *testing.T) {
	src := `@api test
@version 1

enum Mode {
    A = 0
    B = 1
}

type T {
    m: Mode @max(1)
}
`
	if got := validate(t, src); !strings.Contains(got, "redundant with automatic enum validation") {
		t.Errorf("errors = %q, want enum redundancy message", got)
	}
}
