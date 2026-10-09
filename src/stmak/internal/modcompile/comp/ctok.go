// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

// A small C tokenizer for the fsm checks and the timeout unit rewrite.  It
// does not parse C; it finds identifiers and how they are used, which is all
// the heuristics in fsm_check.go need.  Every identifier scan goes through
// it, so strings, character literals and comments are skipped the same way
// everywhere.  The skipping itself is shared with the header scanner
// (skipLineComment, skipBlockComment, skipCLiteral), so the three places
// that step over C comments and literals agree on where they end.

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

type ctokKind int

const (
	ctIdent  ctokKind = iota
	ctNumber          // a preprocessing number, suffixes included
	ctString          // string or character literal
	ctPunct
)

type ctok struct {
	Kind ctokKind
	Text string
	Off  int // byte offset in the tokenized text
}

// cPuncts are the multi-character operators, longest first.
var cPuncts = []string{
	"<<=", ">>=", "...",
	"->", "++", "--", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "##",
}

// skipLineComment returns the offset of the newline that ends the '//'
// comment at i, or len(src).  With splice, a backslash in front of the
// newline continues the comment onto the next line, as C does.  Without it,
// the comment ends at that newline and spliced reports the backslash, for
// a caller that cannot let C splice: captured text is emitted line by line.
func skipLineComment(src string, i int, splice bool) (end int, spliced bool) {
	for i < len(src) && src[i] != '\n' {
		if src[i] == '\\' {
			rest := src[i+1:]
			if strings.HasPrefix(rest, "\n") || strings.HasPrefix(rest, "\r\n") {
				nl := i + 1 + strings.IndexByte(rest, '\n')
				if !splice {
					return nl, true
				}
				i = nl // the i++ below steps onto the next line
			}
		}
		i++
	}
	return i, false
}

// skipBlockComment returns the offset just past the '/* */' comment at i.
// ok is false when the comment is not closed; end is then len(src).
func skipBlockComment(src string, i int) (end int, ok bool) {
	n := strings.Index(src[i+2:], "*/")
	if n < 0 {
		return len(src), false
	}
	return i + 2 + n + 2, true
}

// skipCLiteral returns the offset just past the string or character literal
// at i, whose quote is src[i]; a backslash escapes the byte after it.  ok is
// false when the line or the text ends before the closing quote; end is then
// the offset of that newline, or len(src).
func skipCLiteral(src string, i int) (end int, ok bool) {
	q := src[i]
	i++
	for i < len(src) && src[i] != q && src[i] != '\n' {
		if src[i] == '\\' {
			i++
		}
		i++
	}
	if i < len(src) && src[i] == q {
		return i + 1, true
	}
	return min(i, len(src)), false
}

// cTokenize splits C text into tokens, dropping whitespace and comments.
func cTokenize(src string) []ctok {
	var toks []ctok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v' || c == '\\':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			// The text is compiled as it is, so a backslash at the end of
			// the line continues the comment, as in C.
			i, _ = skipLineComment(src, i, true)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end, ok := skipBlockComment(src, i)
			if !ok {
				return toks
			}
			i = end
		case c == '"' || c == '\'':
			end, _ := skipCLiteral(src, i)
			toks = append(toks, ctok{ctString, src[i:end], i})
			i = end
		case isIdentChar(c) && !isDigit(c):
			start := i
			for i < len(src) && isIdentChar(src[i]) {
				i++
			}
			toks = append(toks, ctok{ctIdent, src[start:i], start})
		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1])):
			start := i
			i++
			for i < len(src) {
				d := src[i]
				if (d == '+' || d == '-') && strings.ContainsRune("eEpP", rune(src[i-1])) {
					i++
					continue
				}
				if !isIdentChar(d) && d != '.' {
					break
				}
				i++
			}
			toks = append(toks, ctok{ctNumber, src[start:i], start})
		default:
			p := string(c)
			for _, op := range cPuncts {
				if strings.HasPrefix(src[i:], op) {
					p = op
					break
				}
			}
			toks = append(toks, ctok{ctPunct, p, i})
			i += len(p)
		}
	}
	return toks
}

// isAssignOp reports whether t assigns to the operand on its left.
func isAssignOp(t ctok) bool {
	if t.Kind != ctPunct {
		return false
	}
	switch t.Text {
	case "=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "++", "--":
		return true
	}
	return false
}

// cUse is one use of an identifier: the name, how it is indexed, and
// whether the use writes it.
type cUse struct {
	Name string
	Off  int
	// Kind is refScalar for a bare name, refPinElem for name(...) and
	// refVarElem for name[...].  Index is the literal index, or -1 when the
	// index is computed (or the parentheses are a call with other args).
	Kind  refKind
	Index int
	Write bool
	// Call is set when the name is followed by '(' -- a function, a
	// function-like macro, or a pin/param array accessor.
	Call bool
}

// matching returns the index of the token closing the bracket at toks[i].
func matching(toks []ctok, i int) int {
	open := toks[i].Text
	close := map[string]string{"(": ")", "[": "]", "{": "}"}[open]
	depth := 0
	for j := i; j < len(toks); j++ {
		if toks[j].Kind != ctPunct {
			continue
		}
		switch toks[j].Text {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(toks) - 1
}

// cUses lists every identifier use in src.  Names after '.' or '->' are
// members, not identifiers of the component, and are left out, and so is
// the operand of sizeof, which is not evaluated.
func cUses(src string) []cUse { return usesOf(cTokenize(src)) }

// usesOf is cUses on tokenized text.
func usesOf(toks []ctok) []cUse {
	var uses []cUse
	skip := -1 // last token of a sizeof operand
	for i, t := range toks {
		if i <= skip || t.Kind != ctIdent {
			continue
		}
		if t.Text == "sizeof" {
			skip = sizeofEnd(toks, i)
			continue
		}
		if i > 0 && toks[i-1].Kind == ctPunct && (toks[i-1].Text == "." || toks[i-1].Text == "->") {
			continue
		}
		u := cUse{Name: t.Text, Off: t.Off, Kind: refScalar, Index: -1}
		end := i + 1 // first token after the use
		if end < len(toks) && toks[end].Kind == ctPunct && (toks[end].Text == "(" || toks[end].Text == "[") {
			if toks[end].Text == "(" {
				u.Kind, u.Call = refPinElem, true
			} else {
				u.Kind = refVarElem
			}
			close := matching(toks, end)
			if close == end+2 && toks[end+1].Kind == ctNumber {
				if n, ok := parseIndex(toks[end+1].Text); ok {
					u.Index = n
				}
			}
			end = close + 1
		}
		if end < len(toks) && isAssignOp(toks[end]) {
			u.Write = true
		}
		if i > 0 && toks[i-1].Kind == ctPunct && (toks[i-1].Text == "++" || toks[i-1].Text == "--") {
			u.Write = true
		}
		uses = append(uses, u)
	}
	return uses
}

// sizeofEnd returns the index of the last token of the operand of the
// sizeof at toks[i]: a parenthesized expression or type, or a name with its
// subscripts and calls (sizeof buf[0]).
func sizeofEnd(toks []ctok, i int) int {
	j := i + 1
	if j >= len(toks) {
		return i
	}
	if toks[j].Kind == ctPunct && toks[j].Text == "(" {
		return matching(toks, j)
	}
	end := j
	for end+1 < len(toks) && toks[end+1].Kind == ctPunct && (toks[end+1].Text == "[" || toks[end+1].Text == "(") {
		end = matching(toks, end+1)
	}
	return end
}

// offsetPos returns the source position of byte off in text, which starts
// at pos.
func offsetPos(pos ast.Pos, text string, off int) ast.Pos {
	before := text[:off]
	if n := strings.Count(before, "\n"); n > 0 {
		pos.Line += n
		pos.Col = off - strings.LastIndexByte(before, '\n')
	} else {
		pos.Col += off
	}
	return pos
}
