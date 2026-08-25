// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Scanner turns ST source into tokens.
//
// It reports a lexical problem by returning a token of kind Invalid whose Text
// is the message, rather than by stopping.  The parser turns that into a
// positioned error.  Scanning on past the problem means one bad literal
// produces one diagnostic instead of truncating the file.
type Scanner struct {
	src  string
	file string

	off  int // byte offset of the next character
	line int
	col  int
}

// NewScanner returns a scanner over src, attributing positions to filename.
func NewScanner(filename, src string) *Scanner {
	return &Scanner{src: src, file: filename, line: 1, col: 1}
}

func (s *Scanner) here() ast.Pos {
	return ast.Pos{File: s.file, Line: s.line, Col: s.col}
}

func (s *Scanner) atEnd() bool { return s.off >= len(s.src) }

// cur is the character at the cursor, or 0 at end of input.
func (s *Scanner) cur() byte {
	if s.atEnd() {
		return 0
	}
	return s.src[s.off]
}

// peek is the character n positions ahead, or 0 past the end.
func (s *Scanner) peek(n int) byte {
	if s.off+n >= len(s.src) {
		return 0
	}
	return s.src[s.off+n]
}

func (s *Scanner) advance() {
	if s.atEnd() {
		return
	}
	if s.src[s.off] == '\n' {
		s.line++
		s.col = 1
	} else {
		s.col++
	}
	s.off++
}

// skipSpaceAndComments consumes whitespace and all three comment forms.  It
// returns an Invalid token, and true, when a block comment runs off the end of
// the file -- silently treating the rest of the source as a comment would turn
// a typo into a file that parses to nothing.
func (s *Scanner) skipSpaceAndComments() (Token, bool) {
	for !s.atEnd() {
		c := s.cur()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			s.advance()

		case c == '/' && s.peek(1) == '/':
			for !s.atEnd() && s.cur() != '\n' {
				s.advance()
			}

		case c == '(' && s.peek(1) == '*':
			if tok, bad := s.skipBlockComment("(*", "*)"); bad {
				return tok, true
			}

		case c == '/' && s.peek(1) == '*':
			if tok, bad := s.skipBlockComment("/*", "*/"); bad {
				return tok, true
			}

		default:
			return Token{}, false
		}
	}
	return Token{}, false
}

// skipBlockComment consumes a block comment, honouring nesting.
//
// IEC 61131-3 3rd edition allows nested comments, and nesting is the safer
// reading either way: with it, an unterminated inner comment is reported at
// end of file; without it, `(* a (* b *)` would silently end early and hand
// the parser the remains of a comment as code.
func (s *Scanner) skipBlockComment(open, close string) (Token, bool) {
	start := s.here()
	depth := 0
	for !s.atEnd() {
		switch {
		case strings.HasPrefix(s.src[s.off:], open):
			depth++
			s.advance()
			s.advance()
		case strings.HasPrefix(s.src[s.off:], close):
			depth--
			s.advance()
			s.advance()
			if depth == 0 {
				return Token{}, false
			}
		default:
			s.advance()
		}
	}
	return Token{
		Kind: Invalid,
		Pos:  start,
		Text: fmt.Sprintf("unterminated %s comment", open),
	}, true
}

// Next returns the next token.
func (s *Scanner) Next() Token {
	if tok, bad := s.skipSpaceAndComments(); bad {
		return tok
	}
	if s.atEnd() {
		return Token{Kind: EOF, Pos: s.here()}
	}

	pos := s.here()
	c := s.cur()

	switch {
	case isIdentStart(c):
		return s.scanIdentOrTypedLit(pos)
	case isDigit(c):
		return s.scanNumber(pos)
	case c == '\'':
		return s.scanString(pos, '\'', TokStringLit)
	case c == '"':
		return s.scanString(pos, '"', TokWStringLit)
	case c == '{':
		return s.scanPragma(pos)
	case c == '%':
		return s.scanDirectAddress(pos)
	}

	// Multi-character operators first, longest match.
	two := ""
	if s.off+1 < len(s.src) {
		two = s.src[s.off : s.off+2]
	}
	switch two {
	case ":=":
		return s.op(pos, Assign, 2)
	case "=>":
		return s.op(pos, OutAssign, 2)
	case "..":
		return s.op(pos, DotDot, 2)
	case "<=":
		return s.op(pos, LessEqual, 2)
	case ">=":
		return s.op(pos, GreaterEq, 2)
	case "<>":
		return s.op(pos, NotEqual, 2)
	case "**":
		return s.op(pos, Power, 2)
	}

	switch c {
	case '+':
		return s.op(pos, Plus, 1)
	case '-':
		return s.op(pos, Minus, 1)
	case '*':
		return s.op(pos, Star, 1)
	case '/':
		return s.op(pos, Slash, 1)
	case '<':
		return s.op(pos, Less, 1)
	case '>':
		return s.op(pos, Greater, 1)
	case '=':
		return s.op(pos, Equal, 1)
	case '&':
		return s.op(pos, Amp, 1)
	case '(':
		return s.op(pos, LParen, 1)
	case ')':
		return s.op(pos, RParen, 1)
	case '[':
		return s.op(pos, LBracket, 1)
	case ']':
		return s.op(pos, RBracket, 1)
	case ',':
		return s.op(pos, Comma, 1)
	case ';':
		return s.op(pos, Semicolon, 1)
	case ':':
		return s.op(pos, Colon, 1)
	case '.':
		return s.op(pos, Dot, 1)
	case '^':
		return s.op(pos, Caret, 1)
	}

	s.advance()
	return Token{
		Kind: Invalid,
		Pos:  pos,
		Text: fmt.Sprintf("unexpected character %q", string(c)),
	}
}

func (s *Scanner) op(pos ast.Pos, k Kind, n int) Token {
	text := s.src[s.off : s.off+n]
	for i := 0; i < n; i++ {
		s.advance()
	}
	return Token{Kind: k, Pos: pos, Text: text}
}

// scanIdentOrTypedLit scans an identifier, a keyword, or the prefix of a typed
// literal such as BYTE#32 or T#100ms.
func (s *Scanner) scanIdentOrTypedLit(pos ast.Pos) Token {
	start := s.off
	for !s.atEnd() && isIdentCont(s.cur()) {
		s.advance()
	}
	text := s.src[start:s.off]

	// A '#' immediately after the name makes this a typed literal rather than
	// an identifier: BOOL#TRUE, LREAL#11.0, DATE#2024-01-20.
	if s.cur() == '#' {
		return s.scanTypedLit(pos, text)
	}

	if kw, ok := keywords[strings.ToUpper(text)]; ok {
		return Token{Kind: kw, Pos: pos, Text: text}
	}
	return Token{Kind: TokIdent, Pos: pos, Text: text}
}

// scanTypedLit scans the body of a typed literal, having consumed its prefix.
// What counts as the body depends on the prefix: a date or duration may
// contain '-' and ':', while a numeric or boolean literal must not, so that
// `INT#5-3` scans as a subtraction rather than one malformed literal.
func (s *Scanner) scanTypedLit(pos ast.Pos, prefix string) Token {
	s.advance() // '#'
	start := s.off

	dateLike := isDateTimePrefix(prefix)
	for !s.atEnd() {
		c := s.cur()
		switch {
		case isIdentCont(c) || c == '.' || c == '#':
			s.advance()
		case dateLike && (c == '-' || c == ':'):
			s.advance()
		default:
			goto done
		}
	}
done:
	body := s.src[start:s.off]
	if body == "" {
		return Token{
			Kind: Invalid,
			Pos:  pos,
			Text: fmt.Sprintf("%s# has no value after the '#'", prefix),
		}
	}
	return Token{
		Kind:   TokTypedLit,
		Pos:    pos,
		Text:   prefix + "#" + body,
		Str:    body,
		Prefix: prefix,
	}
}

// scanNumber scans an integer or real literal, including a based literal such
// as 16#FF, where the radix precedes the '#'.
func (s *Scanner) scanNumber(pos ast.Pos) Token {
	start := s.off
	for !s.atEnd() && (isDigit(s.cur()) || s.cur() == '_') {
		s.advance()
	}

	// Based literal: the digits so far were the radix.
	if s.cur() == '#' {
		radixText := stripUnderscores(s.src[start:s.off])
		radix, err := strconv.Atoi(radixText)
		if err != nil || radix < 2 || radix > 36 {
			s.advance()
			return Token{
				Kind: Invalid,
				Pos:  pos,
				Text: fmt.Sprintf("base %s is not a valid radix (want 2 to 36)", radixText),
			}
		}
		s.advance() // '#'
		digitStart := s.off
		for !s.atEnd() && (isAlnum(s.cur()) || s.cur() == '_') {
			s.advance()
		}
		digits := stripUnderscores(s.src[digitStart:s.off])
		if digits == "" {
			return Token{
				Kind: Invalid,
				Pos:  pos,
				Text: fmt.Sprintf("base-%d literal has no digits", radix),
			}
		}
		v, err := strconv.ParseInt(digits, radix, 64)
		if err != nil {
			return Token{
				Kind: Invalid,
				Pos:  pos,
				Text: fmt.Sprintf("%q is not a valid base-%d integer", digits, radix),
			}
		}
		return Token{
			Kind: TokIntLit, Pos: pos, Text: s.src[start:s.off],
			Int: v, Base: radix,
		}
	}

	isReal := false

	// A '.' makes this a real -- unless it is the '..' of a range, where the
	// number ends before it (ARRAY[1..8]).
	if s.cur() == '.' && s.peek(1) != '.' {
		isReal = true
		s.advance()
		for !s.atEnd() && (isDigit(s.cur()) || s.cur() == '_') {
			s.advance()
		}
	}

	// Exponent.
	if c := s.cur(); c == 'e' || c == 'E' {
		save := s.off
		saveLine, saveCol := s.line, s.col
		s.advance()
		if c := s.cur(); c == '+' || c == '-' {
			s.advance()
		}
		if isDigit(s.cur()) {
			isReal = true
			for !s.atEnd() && (isDigit(s.cur()) || s.cur() == '_') {
				s.advance()
			}
		} else {
			// Not an exponent after all: `1e` is the number 1 followed by the
			// identifier e.  Put the cursor back rather than consuming it.
			s.off, s.line, s.col = save, saveLine, saveCol
		}
	}

	text := s.src[start:s.off]
	clean := stripUnderscores(text)

	if isReal {
		v, err := strconv.ParseFloat(clean, 64)
		if err != nil {
			return Token{Kind: Invalid, Pos: pos, Text: fmt.Sprintf("%q is not a valid real literal", text)}
		}
		return Token{Kind: TokRealLit, Pos: pos, Text: text, Real: v}
	}

	v, err := strconv.ParseInt(clean, 10, 64)
	if err != nil {
		return Token{Kind: Invalid, Pos: pos, Text: fmt.Sprintf("integer literal %s does not fit in 64 bits", text)}
	}
	return Token{Kind: TokIntLit, Pos: pos, Text: text, Int: v, Base: 10}
}

// scanString scans a quoted literal.  ST escapes with '$': $' is a quote, $$ a
// dollar, $N a newline, and $xx a byte in hexadecimal.
func (s *Scanner) scanString(pos ast.Pos, quote byte, kind Kind) Token {
	s.advance() // opening quote
	var b strings.Builder
	for {
		if s.atEnd() || s.cur() == '\n' {
			return Token{Kind: Invalid, Pos: pos, Text: "unterminated string literal"}
		}
		c := s.cur()
		if c == quote {
			s.advance()
			return Token{Kind: kind, Pos: pos, Str: b.String(), Text: b.String()}
		}
		if c != '$' {
			b.WriteByte(c)
			s.advance()
			continue
		}

		// Escape sequence.
		s.advance()
		if s.atEnd() {
			return Token{Kind: Invalid, Pos: pos, Text: "unterminated string literal"}
		}
		e := s.cur()
		switch e {
		case '$', '\'', '"':
			b.WriteByte(e)
			s.advance()
		case 'L', 'l':
			b.WriteByte('\n')
			s.advance()
		case 'N', 'n':
			b.WriteByte('\n')
			s.advance()
		case 'P', 'p':
			b.WriteByte('\f')
			s.advance()
		case 'R', 'r':
			b.WriteByte('\r')
			s.advance()
		case 'T', 't':
			b.WriteByte('\t')
			s.advance()
		default:
			if isHexDigit(e) && isHexDigit(s.peek(1)) {
				v, err := strconv.ParseUint(s.src[s.off:s.off+2], 16, 8)
				if err == nil {
					b.WriteByte(byte(v))
					s.advance()
					s.advance()
					continue
				}
			}
			return Token{
				Kind: Invalid, Pos: pos,
				Text: fmt.Sprintf("unknown escape $%s in string literal", string(e)),
			}
		}
	}
}

// scanDirectAddress scans a located-variable address such as %IX1 or %MD3.4.
//
// stratuMAK does not implement located variables -- I/O reaches HAL through
// VAR_INPUT and VAR_OUTPUT instead -- but scanning them keeps the rejection in
// sema, where it can name the construct, rather than surfacing as a mystifying
// "unexpected character '%'" from the scanner.
func (s *Scanner) scanDirectAddress(pos ast.Pos) Token {
	start := s.off
	s.advance() // '%'
	for !s.atEnd() && (isIdentCont(s.cur()) || s.cur() == '.') {
		s.advance()
	}
	text := s.src[start:s.off]
	if len(text) == 1 {
		return Token{Kind: Invalid, Pos: pos, Text: "'%' must be followed by a direct address such as %IX1"}
	}
	return Token{Kind: TokDirectAddr, Pos: pos, Text: text}
}

// scanPragma scans a brace-delimited attribute pragma, returning its inner
// text unparsed.  The content is a small language of its own and the parser
// pulls it apart; the scanner's job is only to find the matching brace.
func (s *Scanner) scanPragma(pos ast.Pos) Token {
	s.advance() // '{'
	start := s.off
	depth := 1
	for !s.atEnd() {
		switch s.cur() {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				body := s.src[start:s.off]
				s.advance()
				return Token{Kind: Pragma, Pos: pos, Str: body, Text: "{" + body + "}"}
			}
		}
		s.advance()
	}
	return Token{Kind: Invalid, Pos: pos, Text: "unterminated attribute pragma: no closing '}'"}
}

// ---------------------------------------------------------------------------
// Character classes
// ---------------------------------------------------------------------------

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool { return isIdentStart(c) || isDigit(c) }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// stripUnderscores removes the digit separators IEC allows inside numbers.
func stripUnderscores(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	return strings.ReplaceAll(s, "_", "")
}
