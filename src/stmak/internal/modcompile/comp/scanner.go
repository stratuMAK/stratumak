// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package comp implements the .comp file frontend for modcompile.
//
// This file contains the lexical scanner that tokenises the declaration
// section of a .comp file (everything before the ";;" separator).
package comp

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// ---------------------------------------------------------------------------
// Token kinds
// ---------------------------------------------------------------------------

// TokenKind classifies a lexical token.
type TokenKind int

const (
	TokEOF      TokenKind = iota
	TokIdent              // [#a-zA-Z_][-#a-zA-Z0-9_.]*
	TokNumber             // 0x[0-9a-fA-F]+ | [0-9]+
	TokFPNum              // floating point literal
	TokString             // "..."
	TokTString            // """...""" or r"""..."""
	TokSemi               // ;
	TokLBrack             // [
	TokRBrack             // ]
	TokEq                 // =
	TokColon              // :
	TokLParen             // (
	TokRParen             // )
	TokPlus               // +
	TokMinus              // -
	TokStar               // *
	TokSlash              // /
	TokQuestion           // ?
	TokAmpAmp             // &&
	TokPipePipe           // ||
	TokEqEq               // ==
	TokAmp                // &
	TokBangEq             // !=
	TokLShift             // <<
	TokLT                 // <
	TokLTE                // <=
	TokRShift             // >>
	TokGT                 // >
	TokGTE                // >=
	TokPipe               // |

	// Only produced inside an fsm block (Scanner.fsm), so the rest of the
	// header keeps its historical tokenisation byte for byte.
	TokLBrace // {
	TokRBrace // }
	TokComma  // ,
	TokArrow  // ->
)

func (k TokenKind) String() string {
	switch k {
	case TokEOF:
		return "EOF"
	case TokIdent:
		return "ident"
	case TokNumber:
		return "number"
	case TokFPNum:
		return "float"
	case TokString:
		return "string"
	case TokTString:
		return "tstring"
	case TokSemi:
		return ";"
	case TokLBrack:
		return "["
	case TokRBrack:
		return "]"
	case TokEq:
		return "="
	case TokColon:
		return ":"
	case TokLParen:
		return "("
	case TokRParen:
		return ")"
	case TokPlus:
		return "+"
	case TokMinus:
		return "-"
	case TokStar:
		return "*"
	case TokSlash:
		return "/"
	case TokQuestion:
		return "?"
	case TokAmpAmp:
		return "&&"
	case TokPipePipe:
		return "||"
	case TokEqEq:
		return "=="
	case TokAmp:
		return "&"
	case TokBangEq:
		return "!="
	case TokLShift:
		return "<<"
	case TokLT:
		return "<"
	case TokLTE:
		return "<="
	case TokRShift:
		return ">>"
	case TokGT:
		return ">"
	case TokGTE:
		return ">="
	case TokPipe:
		return "|"
	case TokLBrace:
		return "{"
	case TokRBrace:
		return "}"
	case TokComma:
		return ","
	case TokArrow:
		return "->"
	default:
		return "???"
	}
}

// ---------------------------------------------------------------------------
// Token
// ---------------------------------------------------------------------------

// Token is a lexical token from the .comp declaration section.
type Token struct {
	Kind TokenKind
	Val  string
	Pos  ast.Pos
}

func (t Token) String() string {
	if t.Kind == TokEOF {
		return "EOF"
	}
	return fmt.Sprintf("%s(%q)", t.Kind, t.Val)
}

// ---------------------------------------------------------------------------
// Scanner
// ---------------------------------------------------------------------------

// Scanner tokenises the declaration section of a .comp file.
type Scanner struct {
	src  string
	pos  int
	line int
	col  int
	file string

	// fsm switches to the tokenisation used inside an fsm block: C
	// identifiers (no '-', '.' or '#', so `timeout->X` is three tokens) and
	// the punctuation the block grammar needs.  The parser toggles it while
	// the lookahead token sits on the block's first and last token, so no
	// token is ever scanned in the wrong mode.
	fsm bool
}

// NewScanner creates a scanner for the given source text.
func NewScanner(filename, src string) *Scanner {
	return &Scanner{src: src, pos: 0, line: 1, col: 1, file: filename}
}

func (s *Scanner) cur() byte {
	if s.pos >= len(s.src) {
		return 0
	}
	return s.src[s.pos]
}

func (s *Scanner) peek(offset int) byte {
	p := s.pos + offset
	if p >= len(s.src) {
		return 0
	}
	return s.src[p]
}

func (s *Scanner) advance() {
	if s.pos < len(s.src) {
		if s.src[s.pos] == '\n' {
			s.line++
			s.col = 1
		} else {
			s.col++
		}
		s.pos++
	}
}

func (s *Scanner) here() ast.Pos {
	return ast.Pos{File: s.file, Line: s.line, Col: s.col}
}

func (s *Scanner) skipWhitespaceAndComments() {
	for s.pos < len(s.src) {
		c := s.cur()
		// Whitespace.
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			s.advance()
			continue
		}
		// Line comment.
		if c == '/' && s.peek(1) == '/' {
			for s.pos < len(s.src) && s.cur() != '\n' {
				s.advance()
			}
			continue
		}
		// Block comment.
		if c == '/' && s.peek(1) == '*' {
			s.advance() // skip /
			s.advance() // skip *
			for s.pos < len(s.src) {
				if s.cur() == '*' && s.peek(1) == '/' {
					s.advance() // skip *
					s.advance() // skip /
					break
				}
				s.advance()
			}
			continue
		}
		break
	}
}

// Next returns the next token.
func (s *Scanner) Next() Token {
	s.skipWhitespaceAndComments()
	if s.pos >= len(s.src) {
		return Token{Kind: TokEOF, Pos: s.here()}
	}

	pos := s.here()
	c := s.cur()

	// Triple-quoted string: """...""" or r"""..."""
	if c == '"' && s.peek(1) == '"' && s.peek(2) == '"' {
		return s.scanTripleString(pos, false)
	}
	if c == 'r' && s.peek(1) == '"' && s.peek(2) == '"' && s.peek(3) == '"' {
		return s.scanTripleString(pos, true)
	}

	// Regular string: "..."
	if c == '"' {
		return s.scanString(pos)
	}

	if s.fsm {
		if tok, ok := s.scanFSMToken(pos, c); ok {
			return tok
		}
	}

	// Identifier (or HAL name): starts with letter, _, or #
	if isIdentStart(c) {
		return s.scanIdent(pos)
	}

	// Number: starts with digit, or . followed by digit (float)
	if isDigit(c) {
		return s.scanNumber(pos)
	}
	if c == '.' && isDigit(s.peek(1)) {
		return s.scanNumber(pos)
	}

	// Two-character operators (check before single-char).
	if s.pos+1 < len(s.src) {
		two := string([]byte{c, s.peek(1)})
		switch two {
		case "&&":
			s.advance()
			s.advance()
			return Token{TokAmpAmp, two, pos}
		case "||":
			s.advance()
			s.advance()
			return Token{TokPipePipe, two, pos}
		case "==":
			s.advance()
			s.advance()
			return Token{TokEqEq, two, pos}
		case "!=":
			s.advance()
			s.advance()
			return Token{TokBangEq, two, pos}
		case "<<":
			s.advance()
			s.advance()
			return Token{TokLShift, two, pos}
		case "<=":
			s.advance()
			s.advance()
			return Token{TokLTE, two, pos}
		case ">>":
			s.advance()
			s.advance()
			return Token{TokRShift, two, pos}
		case ">=":
			s.advance()
			s.advance()
			return Token{TokGTE, two, pos}
		}
	}

	// Single-character tokens.
	s.advance()
	switch c {
	case ';':
		return Token{TokSemi, ";", pos}
	case '[':
		return Token{TokLBrack, "[", pos}
	case ']':
		return Token{TokRBrack, "]", pos}
	case '=':
		return Token{TokEq, "=", pos}
	case ':':
		return Token{TokColon, ":", pos}
	case '(':
		return Token{TokLParen, "(", pos}
	case ')':
		return Token{TokRParen, ")", pos}
	case '+':
		return Token{TokPlus, "+", pos}
	case '-':
		return Token{TokMinus, "-", pos}
	case '*':
		return Token{TokStar, "*", pos}
	case '/':
		return Token{TokSlash, "/", pos}
	case '?':
		return Token{TokQuestion, "?", pos}
	case '&':
		return Token{TokAmp, "&", pos}
	case '<':
		return Token{TokLT, "<", pos}
	case '>':
		return Token{TokGT, ">", pos}
	case '|':
		return Token{TokPipe, "|", pos}
	default:
		return Token{TokEOF, string(c), pos}
	}
}

// ---------------------------------------------------------------------------
// Scan helpers
// ---------------------------------------------------------------------------

func (s *Scanner) scanIdent(pos ast.Pos) Token {
	start := s.pos
	s.advance() // consume first char
	for s.pos < len(s.src) && isIdentCont(s.cur()) {
		s.advance()
	}
	return Token{TokIdent, s.src[start:s.pos], pos}
}

func (s *Scanner) scanNumber(pos ast.Pos) Token {
	start := s.pos
	isFloat := false

	// Hex: 0x...
	if s.cur() == '0' && (s.peek(1) == 'x' || s.peek(1) == 'X') {
		s.advance() // 0
		s.advance() // x
		for s.pos < len(s.src) && isHexDigit(s.cur()) {
			s.advance()
		}
		return Token{TokNumber, s.src[start:s.pos], pos}
	}

	// Starts with dot: .5, .123 etc.
	if s.cur() == '.' {
		isFloat = true
		s.advance()
	}

	// Integer or float: digits, optional dot, optional exponent.
	for s.pos < len(s.src) && isDigit(s.cur()) {
		s.advance()
	}
	if !isFloat && s.pos < len(s.src) && s.cur() == '.' {
		isFloat = true
		s.advance()
		for s.pos < len(s.src) && isDigit(s.cur()) {
			s.advance()
		}
	}
	// Exponent.
	if s.pos < len(s.src) && (s.cur() == 'e' || s.cur() == 'E') {
		isFloat = true
		s.advance()
		if s.pos < len(s.src) && (s.cur() == '+' || s.cur() == '-') {
			s.advance()
		}
		for s.pos < len(s.src) && isDigit(s.cur()) {
			s.advance()
		}
	}
	// Float suffix.
	if s.pos < len(s.src) && (s.cur() == 'f' || s.cur() == 'F') {
		isFloat = true
		s.advance()
	}

	val := s.src[start:s.pos]
	if isFloat {
		return Token{TokFPNum, val, pos}
	}
	return Token{TokNumber, val, pos}
}

func (s *Scanner) scanString(pos ast.Pos) Token {
	s.advance() // skip opening "
	var b strings.Builder
	for s.pos < len(s.src) && s.cur() != '"' {
		if s.cur() == '\\' && s.pos+1 < len(s.src) {
			s.advance()
			// Backslash-newline is a line continuation (halcompile
			// semantics): both characters vanish and the lines join.
			if s.cur() == '\n' || (s.cur() == '\r' && s.peek(1) == '\n') {
				if s.cur() == '\r' {
					s.advance()
				}
				s.advance()
				continue
			}
			b.WriteByte(unescapeChar(s.cur()))
			s.advance()
			continue
		}
		b.WriteByte(s.cur())
		s.advance()
	}
	if s.pos < len(s.src) {
		s.advance() // skip closing "
	}
	return Token{TokString, b.String(), pos}
}

func (s *Scanner) scanTripleString(pos ast.Pos, raw bool) Token {
	if raw {
		s.advance() // skip 'r'
	}
	s.advance() // skip "
	s.advance() // skip "
	s.advance() // skip "
	var b strings.Builder
	for s.pos < len(s.src) {
		if s.cur() == '"' && s.peek(1) == '"' && s.peek(2) == '"' {
			s.advance() // skip "
			s.advance() // skip "
			s.advance() // skip "
			break
		}
		if !raw && s.cur() == '\\' && s.pos+1 < len(s.src) {
			s.advance()
			// Line continuation, as in the plain-string scanner above.
			if s.cur() == '\n' || (s.cur() == '\r' && s.peek(1) == '\n') {
				if s.cur() == '\r' {
					s.advance()
				}
				s.advance()
				continue
			}
			b.WriteByte(unescapeChar(s.cur()))
			s.advance()
			continue
		}
		b.WriteByte(s.cur())
		s.advance()
	}
	return Token{TokTString, b.String(), pos}
}

// ---------------------------------------------------------------------------
// Character classification
// ---------------------------------------------------------------------------

func isIdentStart(c byte) bool {
	return c == '_' || c == '#' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isIdentCont returns true for characters that can continue a HAL-style
// identifier: letters, digits, underscore, hash, hyphen, dot.
func isIdentCont(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '-' || c == '.'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// IsName reports whether s is a valid NAME token (C identifier).
func IsName(s string) bool {
	if len(s) == 0 {
		return false
	}
	r := rune(s[0])
	if r != '_' && !unicode.IsLetter(r) {
		return false
	}
	for _, r := range s[1:] {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func unescapeChar(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case 'v':
		return '\v'
	case '\\':
		return '\\'
	case '"':
		return '"'
	default:
		return c
	}
}

// ---------------------------------------------------------------------------
// fsm block support
// ---------------------------------------------------------------------------

// scanFSMToken scans the tokens that differ inside an fsm block.  ok is false
// for everything else, which the regular scanner handles.
func (s *Scanner) scanFSMToken(pos ast.Pos, c byte) (Token, bool) {
	switch {
	case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		start := s.pos
		for s.pos < len(s.src) && isIdentChar(s.cur()) {
			s.advance()
		}
		return Token{TokIdent, s.src[start:s.pos], pos}, true
	case c == '-' && s.peek(1) == '>':
		s.advance()
		s.advance()
		return Token{TokArrow, "->", pos}, true
	case c == '{':
		s.advance()
		return Token{TokLBrace, "{", pos}, true
	case c == '}':
		s.advance()
		return Token{TokRBrace, "}", pos}, true
	case c == ',':
		s.advance()
		return Token{TokComma, ",", pos}, true
	}
	return Token{}, false
}

// CaptureC reads verbatim C from the current position up to, but not
// including, the first byte of stop that appears outside any bracket pair.
// (), [] and {} are balanced; string and character literals and comments are
// skipped, so a bracket or stop byte inside them does not count.  It returns
// the text and the position of its first byte.  A closing bracket that does
// not match, or the end of the header, is an error.
func (s *Scanner) CaptureC(stop string) (string, ast.Pos, error) {
	start, pos := s.pos, s.here()
	var stack []byte
	for s.pos < len(s.src) {
		c := s.cur()
		if len(stack) == 0 && strings.IndexByte(stop, c) >= 0 {
			return s.src[start:s.pos], pos, nil
		}
		switch {
		case c == '/' && s.peek(1) == '/':
			for s.pos < len(s.src) && s.cur() != '\n' {
				s.advance()
			}
			continue
		case c == '/' && s.peek(1) == '*':
			at := s.here()
			s.advance()
			s.advance()
			for s.pos < len(s.src) && !(s.cur() == '*' && s.peek(1) == '/') {
				s.advance()
			}
			if s.pos >= len(s.src) {
				return "", pos, fmt.Errorf("%s: unterminated comment", at)
			}
			s.advance()
			s.advance()
			continue
		case c == '"' || c == '\'':
			at := s.here()
			s.advance()
			for s.pos < len(s.src) && s.cur() != c && s.cur() != '\n' {
				if s.cur() == '\\' {
					s.advance()
				}
				s.advance()
			}
			if s.pos >= len(s.src) || s.cur() != c {
				return "", pos, fmt.Errorf("%s: unterminated %s literal", at,
					map[byte]string{'"': "string", '\'': "character"}[c])
			}
		case c == '(':
			stack = append(stack, ')')
		case c == '[':
			stack = append(stack, ']')
		case c == '{':
			stack = append(stack, '}')
		case c == ')' || c == ']' || c == '}':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return "", pos, fmt.Errorf("%s: unbalanced %q", s.here(), string(c))
			}
			stack = stack[:len(stack)-1]
		}
		s.advance()
	}
	if len(stack) > 0 {
		return "", pos, fmt.Errorf("%s: missing %q before end of header", pos, string(stack[len(stack)-1]))
	}
	return "", pos, fmt.Errorf("%s: expected one of %q before end of header", pos, stop)
}
