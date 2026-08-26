// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"strings"
	"testing"
)

// scan returns every token of src up to end of file.
func scan(src string) []Token {
	sc := NewScanner("t.st", src)
	var out []Token
	for {
		t := sc.Next()
		if t.Kind == EOF {
			return out
		}
		out = append(out, t)
		if len(out) > 10000 {
			panic("scanner did not terminate")
		}
	}
}

func kinds(src string) []Kind {
	var out []Kind
	for _, t := range scan(src) {
		out = append(out, t.Kind)
	}
	return out
}

func TestScanKeywordsAreCaseInsensitive(t *testing.T) {
	// ST is case-insensitive for keywords and identifiers alike.
	for _, src := range []string{"IF", "if", "If", "iF"} {
		got := kinds(src)
		if len(got) != 1 || got[0] != KwIf {
			t.Errorf("%q scanned as %v, want IF", src, got)
		}
	}
	// END_FUNCTION_BLOCK must win over FUNCTION_BLOCK: they are separate
	// words, not a prefix relationship, but a hand-written matcher gets this
	// wrong often enough to be worth pinning.
	if got := kinds("END_FUNCTION_BLOCK"); len(got) != 1 || got[0] != KwEndFunctionBlock {
		t.Errorf("END_FUNCTION_BLOCK scanned as %v", got)
	}
}

func TestScanIdentifiers(t *testing.T) {
	toks := scan("max_rate _hidden a1 LREAL")
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4", len(toks))
	}
	for _, tok := range toks {
		if tok.Kind != TokIdent {
			t.Errorf("%q scanned as %v, want an identifier", tok.Text, tok.Kind)
		}
	}
	// Elementary type names are identifiers, not keywords: sema decides what a
	// name means, which keeps `TYPE C : LREAL` and `TYPE M : SomeFB` on one
	// path in the parser.
	if toks[3].Text != "LREAL" {
		t.Errorf("last token = %q", toks[3].Text)
	}
}

func TestScanNumbers(t *testing.T) {
	tests := []struct {
		src  string
		kind Kind
		i    int64
		f    float64
		base int
	}{
		{"42", TokIntLit, 42, 0, 10},
		{"16#FF", TokIntLit, 255, 0, 16},
		{"16#ff", TokIntLit, 255, 0, 16},
		{"2#1010", TokIntLit, 10, 0, 2},
		{"8#777", TokIntLit, 511, 0, 8},
		{"1_000_000", TokIntLit, 1000000, 0, 10},
		{"1.5", TokRealLit, 0, 1.5, 0},
		{"10.0", TokRealLit, 0, 10.0, 0},
		{"1.0e3", TokRealLit, 0, 1000, 0},
		{"1.0E-3", TokRealLit, 0, 0.001, 0},
		{"5e2", TokRealLit, 0, 500, 0},
	}
	for _, tt := range tests {
		toks := scan(tt.src)
		if len(toks) != 1 {
			t.Errorf("%q scanned to %d tokens, want 1: %v", tt.src, len(toks), toks)
			continue
		}
		got := toks[0]
		if got.Kind != tt.kind {
			t.Errorf("%q: kind = %v, want %v", tt.src, got.Kind, tt.kind)
			continue
		}
		if tt.kind == TokIntLit && (got.Int != tt.i || got.Base != tt.base) {
			t.Errorf("%q: value = %d base %d, want %d base %d", tt.src, got.Int, got.Base, tt.i, tt.base)
		}
		if tt.kind == TokRealLit && got.Real != tt.f {
			t.Errorf("%q: value = %v, want %v", tt.src, got.Real, tt.f)
		}
	}
}

// TestScanRangeIsNotAReal is the reason the number scanner peeks at the second
// dot: in ARRAY[1..8] the number ends before '..', and a scanner that took the
// first dot as a decimal point would read "1." and then choke on the second.
func TestScanRangeIsNotAReal(t *testing.T) {
	got := kinds("1..8")
	want := []Kind{TokIntLit, DotDot, TokIntLit}
	if len(got) != len(want) {
		t.Fatalf("1..8 scanned as %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("1..8 scanned as %v, want %v", got, want)
		}
	}
}

// TestScanExponentBacktracks pins the other lookahead in the number scanner:
// `1e` is the number 1 followed by an identifier, not a malformed real.
func TestScanExponentBacktracks(t *testing.T) {
	toks := scan("1e")
	if len(toks) != 2 || toks[0].Kind != TokIntLit || toks[1].Kind != TokIdent {
		t.Fatalf("1e scanned as %v, want an integer then an identifier", toks)
	}
	if toks[1].Text != "e" {
		t.Errorf("second token = %q, want %q", toks[1].Text, "e")
	}
}

func TestScanTypedLiterals(t *testing.T) {
	tests := []struct {
		src    string
		prefix string
		body   string
	}{
		{"BYTE#32", "BYTE", "32"},
		{"LREAL#11.0", "LREAL", "11.0"},
		{"BOOL#TRUE", "BOOL", "TRUE"},
		{"WORD#16#FF", "WORD", "16#FF"},
		{"T#100ms", "T", "100ms"},
		{"t#1s", "t", "1s"},
		{"DATE#2024-01-20", "DATE", "2024-01-20"},
		{"TIME_OF_DAY#14:30:20", "TIME_OF_DAY", "14:30:20"},
		{"DATE_AND_TIME#2024-01-20-15:30:22", "DATE_AND_TIME", "2024-01-20-15:30:22"},
	}
	for _, tt := range tests {
		toks := scan(tt.src)
		if len(toks) != 1 || toks[0].Kind != TokTypedLit {
			t.Errorf("%q scanned as %v, want one typed literal", tt.src, toks)
			continue
		}
		if toks[0].Prefix != tt.prefix || toks[0].Str != tt.body {
			t.Errorf("%q: prefix/body = %q/%q, want %q/%q",
				tt.src, toks[0].Prefix, toks[0].Str, tt.prefix, tt.body)
		}
	}
}

// TestScanTypedLiteralStopsAtMinus is why the body scan depends on the prefix.
// A date may contain '-', so DATE#2024-01-20 is one literal; a numeric literal
// may not, so INT#5-3 has to be a subtraction rather than one malformed token.
func TestScanTypedLiteralStopsAtMinus(t *testing.T) {
	got := kinds("INT#5-3")
	want := []Kind{TokTypedLit, Minus, TokIntLit}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("INT#5-3 scanned as %v, want %v", got, want)
	}

	if got := kinds("DATE#2024-01-20"); len(got) != 1 {
		t.Fatalf("DATE#2024-01-20 scanned as %v, want a single literal", got)
	}
}

func TestScanComments(t *testing.T) {
	tests := []string{
		"// line comment\n42",
		"(* block *) 42",
		"/* c style */ 42",
		"(* multi\n   line *) 42",
		"42 // trailing",
	}
	for _, src := range tests {
		toks := scan(src)
		if len(toks) != 1 || toks[0].Kind != TokIntLit || toks[0].Int != 42 {
			t.Errorf("%q scanned as %v, want just 42", src, toks)
		}
	}
}

// TestScanNestedBlockComment pins the nesting decision.  IEC 61131-3 3rd
// edition allows nested comments, and nesting is the safer reading either way:
// without it, `(* a (* b *)` would end early and hand the parser the remains of
// a comment as code.
func TestScanNestedBlockComment(t *testing.T) {
	toks := scan("(* outer (* inner *) still outer *) 42")
	if len(toks) != 1 || toks[0].Kind != TokIntLit {
		t.Fatalf("nested comment scanned as %v, want just 42", toks)
	}

	// The other half of the bargain: an unterminated comment is reported
	// rather than silently eating the file.
	toks = scan("(* never closed")
	if len(toks) != 1 || toks[0].Kind != Invalid {
		t.Fatalf("unterminated comment scanned as %v, want an error token", toks)
	}
	if !strings.Contains(toks[0].Text, "unterminated") {
		t.Errorf("diagnostic = %q", toks[0].Text)
	}
}

func TestScanStrings(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{`'hello'`, "hello"},
		{`'  '`, "  "},
		{`''`, ""},
		{`'it$'s'`, "it's"},
		{`'a$$b'`, "a$b"},
		{`'a$0Ab'`, "a\nb"},
		{`'tab$there'`, "tab\there"},
	}
	for _, tt := range tests {
		toks := scan(tt.src)
		if len(toks) != 1 || toks[0].Kind != TokStringLit {
			t.Errorf("%s scanned as %v", tt.src, toks)
			continue
		}
		if toks[0].Str != tt.want {
			t.Errorf("%s: value = %q, want %q", tt.src, toks[0].Str, tt.want)
		}
	}

	if toks := scan(`"wide"`); len(toks) != 1 || toks[0].Kind != TokWStringLit {
		t.Errorf(`"wide" scanned as %v, want a wide string`, toks)
	}

	// A string may not span a line: without that rule a missing closing quote
	// swallows the rest of the file.
	toks := scan("'unterminated\n")
	if len(toks) == 0 || toks[0].Kind != Invalid {
		t.Errorf("unterminated string scanned as %v", toks)
	}
}

func TestScanPragma(t *testing.T) {
	toks := scan("{attribute 'summary' := 'Two-input AND gate'}")
	if len(toks) != 1 || toks[0].Kind != Pragma {
		t.Fatalf("pragma scanned as %v", toks)
	}
	if toks[0].Str != "attribute 'summary' := 'Two-input AND gate'" {
		t.Errorf("pragma body = %q", toks[0].Str)
	}

	if toks := scan("{unterminated"); len(toks) != 1 || toks[0].Kind != Invalid {
		t.Errorf("unterminated pragma scanned as %v", toks)
	}
}

func TestScanOperators(t *testing.T) {
	// Longest match matters: ':=' must not scan as ':' then '=', and '**' must
	// not scan as two '*'.
	got := kinds(":= => .. <= >= <> ** + - * / < > = & ( ) [ ] , ; : . ^")
	want := []Kind{
		Assign, OutAssign, DotDot, LessEqual, GreaterEq, NotEqual, Power,
		Plus, Minus, Star, Slash, Less, Greater, Equal, Amp,
		LParen, RParen, LBracket, RBracket, Comma, Semicolon, Colon, Dot, Caret,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestScanPositions(t *testing.T) {
	toks := scan("a\n  b\n")
	if len(toks) != 2 {
		t.Fatalf("got %d tokens", len(toks))
	}
	if toks[0].Pos.Line != 1 || toks[0].Pos.Col != 1 {
		t.Errorf("first token at %v, want 1:1", toks[0].Pos)
	}
	if toks[1].Pos.Line != 2 || toks[1].Pos.Col != 3 {
		t.Errorf("second token at %v, want 2:3", toks[1].Pos)
	}
}

func TestScanInvalidCharacter(t *testing.T) {
	toks := scan("a @ b")
	if len(toks) != 3 || toks[1].Kind != Invalid {
		t.Fatalf("scanned as %v", toks)
	}
	if !strings.Contains(toks[1].Text, "@") {
		t.Errorf("diagnostic %q does not quote the character", toks[1].Text)
	}
	// Scanning continues past the bad character, so one stray byte yields one
	// diagnostic rather than truncating the file.
	if toks[2].Kind != TokIdent || toks[2].Text != "b" {
		t.Errorf("scanning did not resume after the invalid character: %v", toks)
	}
}

func TestScanBadRadix(t *testing.T) {
	for _, src := range []string{"99#12", "16#"} {
		toks := scan(src)
		if len(toks) == 0 || toks[0].Kind != Invalid {
			t.Errorf("%q scanned as %v, want an error token", src, toks)
		}
	}
}

// TestScanIntegerMagnitude covers the range a literal is scanned over.
//
// A literal carries no sign -- the minus in `-1` is an operator -- so what is
// scanned is a magnitude, and it runs to 2^64-1 because a ULINT does. Reading
// it as a signed int64 refused ULINT's largest value outright, and refused
// LINT's lowest for the magnitude it never has.
func TestScanIntegerMagnitude(t *testing.T) {
	for _, tt := range []struct {
		src  string
		bits int64
		big  bool
	}{
		{"0", 0, false},
		{"42", 42, false},
		{"9223372036854775807", 9223372036854775807, false},
		// LINT's lowest, as the magnitude the unary minus is applied to.
		{"9223372036854775808", -9223372036854775808, true},
		{"18446744073709551615", -1, true},
		// And in a base, which parsed signed as well.
		{"16#FFFFFFFFFFFFFFFF", -1, true},
		{"16#7FFFFFFFFFFFFFFF", 9223372036854775807, false},
	} {
		t.Run(tt.src, func(t *testing.T) {
			got := scan(tt.src)
			if len(got) != 1 || got[0].Kind != TokIntLit {
				t.Fatalf("%s scanned as %v, want one integer literal", tt.src, kinds(tt.src))
			}
			if got[0].Int != tt.bits {
				t.Errorf("%s has pattern %d, want %d", tt.src, got[0].Int, tt.bits)
			}
			if got[0].Big != tt.big {
				t.Errorf("%s: Big is %v, want %v -- it says whether the pattern reads "+
					"as the value or has still to be given a type", tt.src, got[0].Big, tt.big)
			}
		})
	}

	// One past what a 64-bit word holds is still refused, and says so.
	got := scan("18446744073709551616")
	if len(got) != 1 || got[0].Kind != Invalid ||
		!strings.Contains(got[0].Text, "does not fit in 64 bits") {
		t.Errorf("2^64 scanned as %v, want a refusal naming the width", got)
	}
}
