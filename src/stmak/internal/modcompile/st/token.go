// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package st implements the IEC 61131-3 Structured Text frontend for
// modcompile: scanner, parser and (later) semantic analysis.  The parser
// produces the syntax tree in syntax.go, which records what the source wrote;
// sema resolves that into the shared IR in internal/modcompile/ast.
//
// See docs/dev/MODCOMPILE_ST_FRONTEND.md.
package st

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Kind classifies a token.
type Kind int

const (
	EOF Kind = iota
	Invalid

	TokIdent
	TokIntLit    // 42, 16#FF, 2#1010 -- Base carries the radix
	TokRealLit   // 1.5, 1.0e3
	TokTypedLit  // BYTE#32, LREAL#11.0, T#100ms, DATE#2024-01-20
	TokStringLit // 'text'
	TokWStringLit
	Pragma        // {attribute 'name' := 'value'}
	TokDirectAddr // %IX1, %QW2 -- a located-variable address

	// Punctuation and operators.
	Assign      // :=
	OutAssign   // =>
	DotDot      // ..
	Plus        // +
	Minus       // -
	Star        // *
	Slash       // /
	Power       // **
	Less        // <
	Greater     // >
	LessEqual   // <=
	GreaterEq   // >=
	Equal       // =
	NotEqual    // <>
	Amp         // & -- a synonym for AND
	LParen      // (
	RParen      // )
	LBracket    // [
	RBracket    // ]
	Comma       // ,
	Semicolon   // ;
	Colon       // :
	Dot         // .
	Caret       // ^
	keywordBase // marker: every constant past here is a keyword

	// Declarations.
	KwProgram
	KwEndProgram
	KwFunction
	KwEndFunction
	KwFunctionBlock
	KwEndFunctionBlock
	KwType
	KwEndType
	KwStruct
	KwEndStruct
	KwVar
	KwVarInput
	KwVarOutput
	KwVarInOut
	KwVarGlobal
	KwVarTemp
	KwVarExternal
	KwVarAccess
	KwEndVar
	KwArray
	KwOf
	KwAt
	KwConstant
	KwRetain
	KwNonRetain
	KwREdge
	KwFEdge

	// Statements.
	KwIf
	KwThen
	KwElsif
	KwElse
	KwEndIf
	KwCase
	KwEndCase
	KwFor
	KwTo
	KwBy
	KwDo
	KwEndFor
	KwWhile
	KwEndWhile
	KwRepeat
	KwUntil
	KwEndRepeat
	KwExit
	KwContinue
	KwReturn
	KwAction
	KwEndAction

	// Word operators and literals.
	KwAnd
	KwOr
	KwXor
	KwNot
	KwMod
	KwTrue
	KwFalse
)

var kindNames = map[Kind]string{
	EOF:           "end of file",
	Invalid:       "invalid token",
	TokIdent:      "identifier",
	TokIntLit:     "integer literal",
	TokRealLit:    "real literal",
	TokTypedLit:   "typed literal",
	TokStringLit:  "string literal",
	TokWStringLit: "wide string literal",
	Pragma:        "attribute pragma",
	TokDirectAddr: "direct address",
	Assign:        "':='",
	OutAssign:     "'=>'",
	DotDot:        "'..'",
	Plus:          "'+'",
	Minus:         "'-'",
	Star:          "'*'",
	Slash:         "'/'",
	Power:         "'**'",
	Less:          "'<'",
	Greater:       "'>'",
	LessEqual:     "'<='",
	GreaterEq:     "'>='",
	Equal:         "'='",
	NotEqual:      "'<>'",
	Amp:           "'&'",
	LParen:        "'('",
	RParen:        "')'",
	LBracket:      "'['",
	RBracket:      "']'",
	Comma:         "','",
	Semicolon:     "';'",
	Colon:         "':'",
	Dot:           "'.'",
	Caret:         "'^'",
}

// keywords maps the upper-cased spelling to its token kind.  ST is
// case-insensitive, so the scanner upper-cases before looking up here.
//
// Elementary type names -- BOOL, INT, LREAL, STRING and the rest -- are
// deliberately absent.  They are ordinary identifiers to the parser, which
// keeps `TYPE Celsius : LREAL` and `TYPE Motor : SomeFB` on one path and
// leaves sema to decide what a name means.
var keywords = map[string]Kind{
	"PROGRAM":            KwProgram,
	"END_PROGRAM":        KwEndProgram,
	"FUNCTION":           KwFunction,
	"END_FUNCTION":       KwEndFunction,
	"FUNCTION_BLOCK":     KwFunctionBlock,
	"END_FUNCTION_BLOCK": KwEndFunctionBlock,
	"TYPE":               KwType,
	"END_TYPE":           KwEndType,
	"STRUCT":             KwStruct,
	"END_STRUCT":         KwEndStruct,
	"VAR":                KwVar,
	"VAR_INPUT":          KwVarInput,
	"VAR_OUTPUT":         KwVarOutput,
	"VAR_IN_OUT":         KwVarInOut,
	"VAR_GLOBAL":         KwVarGlobal,
	"VAR_TEMP":           KwVarTemp,
	"VAR_EXTERNAL":       KwVarExternal,
	"VAR_ACCESS":         KwVarAccess,
	"END_VAR":            KwEndVar,
	"ARRAY":              KwArray,
	"OF":                 KwOf,
	"AT":                 KwAt,
	"CONSTANT":           KwConstant,
	"RETAIN":             KwRetain,
	"NON_RETAIN":         KwNonRetain,
	"R_EDGE":             KwREdge,
	"F_EDGE":             KwFEdge,

	"IF":         KwIf,
	"THEN":       KwThen,
	"ELSIF":      KwElsif,
	"ELSE":       KwElse,
	"END_IF":     KwEndIf,
	"CASE":       KwCase,
	"END_CASE":   KwEndCase,
	"FOR":        KwFor,
	"TO":         KwTo,
	"BY":         KwBy,
	"DO":         KwDo,
	"END_FOR":    KwEndFor,
	"WHILE":      KwWhile,
	"END_WHILE":  KwEndWhile,
	"REPEAT":     KwRepeat,
	"UNTIL":      KwUntil,
	"END_REPEAT": KwEndRepeat,
	"EXIT":       KwExit,
	"CONTINUE":   KwContinue,
	"RETURN":     KwReturn,
	"ACTION":     KwAction,
	"END_ACTION": KwEndAction,

	"AND":   KwAnd,
	"OR":    KwOr,
	"XOR":   KwXor,
	"NOT":   KwNot,
	"MOD":   KwMod,
	"TRUE":  KwTrue,
	"FALSE": KwFalse,
}

// keywordText is the reverse of keywords, built once so a diagnostic can name
// the keyword it expected.
var keywordText = func() map[Kind]string {
	m := make(map[Kind]string, len(keywords))
	for text, kind := range keywords {
		m[kind] = text
	}
	return m
}()

// IsKeyword reports whether k is a reserved word.
func (k Kind) IsKeyword() bool { return k > keywordBase }

func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	if n, ok := keywordText[k]; ok {
		return "'" + n + "'"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// dateTimePrefixes are the typed-literal prefixes whose body may contain '-'
// and ':' -- a date, a time of day, or a duration.  Every other prefix takes a
// plain number or TRUE/FALSE, and must stop at '-' so that `INT#5-3` is a
// subtraction rather than one malformed literal.
var dateTimePrefixes = map[string]bool{
	"T": true, "TIME": true, "LT": true, "LTIME": true,
	"D": true, "DATE": true, "LD": true, "LDATE": true,
	"TOD": true, "TIME_OF_DAY": true, "LTOD": true, "LTIME_OF_DAY": true,
	"DT": true, "DATE_AND_TIME": true, "LDT": true, "LDATE_AND_TIME": true,
}

// isDateTimePrefix reports whether a typed literal with this prefix carries a
// date, time-of-day or duration body.  The comparison is case-insensitive
// because `t#1s` and `T#1s` are the same literal.
func isDateTimePrefix(prefix string) bool {
	return dateTimePrefixes[strings.ToUpper(prefix)]
}

// Token is one lexical item.
type Token struct {
	Kind Kind
	Pos  ast.Pos

	// Text is the source spelling, kept for diagnostics and for the parser to
	// re-read a literal it wants to interpret itself.
	Text string

	// Str is the decoded value of a string literal, or the body of a pragma.
	Str string

	// Int, Real and Base carry a scanned numeric literal.  Base is 10 unless
	// the literal was written in another radix (16#FF).
	Int  int64
	Real float64
	Base int

	// Prefix is the type name of a typed literal, without the '#'.
	Prefix string
}

func (t Token) String() string {
	switch t.Kind {
	case TokIdent, TokIntLit, TokRealLit, TokTypedLit:
		return fmt.Sprintf("%s(%s)", t.Kind, t.Text)
	case TokStringLit, TokWStringLit:
		return fmt.Sprintf("%s(%q)", t.Kind, t.Str)
	case Pragma:
		return fmt.Sprintf("%s({%s})", t.Kind, t.Str)
	default:
		return t.Kind.String()
	}
}
