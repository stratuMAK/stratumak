// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Parse turns ST source into a syntax tree.  It resolves nothing: a name that
// does not exist, a type that is not declared and an argument list that
// matches no signature all parse without complaint, because deciding those is
// sema's job and doing it here would mean guessing.
//
// Parsing continues past an error so that one run reports several problems.
// The returned error, when non-nil, lists them in source order.
func Parse(filename, src string) (*File, error) {
	p := &parser{sc: NewScanner(filename, src), file: filename}
	p.next()
	f := p.parseFile()
	if len(p.errs) > 0 {
		return nil, p.errs
	}
	return f, nil
}

// ErrorList is the set of diagnostics from one parse, in source order.
type ErrorList []Error

// Error is one positioned diagnostic.
type Error struct {
	Pos ast.Pos
	Msg string
}

func (e Error) Error() string { return e.Pos.String() + ": " + e.Msg }

func (l ErrorList) Error() string {
	switch len(l) {
	case 0:
		return "no errors"
	case 1:
		return l[0].Error()
	}
	var b strings.Builder
	for i, e := range l {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// maxErrors caps how many diagnostics one parse reports.  Past this point the
// parser is almost certainly desynchronised and the remaining messages describe
// its confusion rather than the source.
const maxErrors = 25

type parser struct {
	sc   *Scanner
	file string
	tok  Token
	errs ErrorList

	// panicking suppresses cascading errors until the next synchronisation
	// point, so one misplaced token yields one diagnostic and not a dozen.
	panicking bool
}

// ---------------------------------------------------------------------------
// Token plumbing
// ---------------------------------------------------------------------------

// next advances to the following token, turning any lexical problem into a
// diagnostic as it passes.
func (p *parser) next() {
	for {
		p.tok = p.sc.Next()
		if p.tok.Kind != Invalid {
			return
		}
		p.errorAt(p.tok.Pos, "%s", p.tok.Text)
		if len(p.errs) >= maxErrors {
			p.tok = Token{Kind: EOF, Pos: p.tok.Pos}
			return
		}
	}
}

func (p *parser) at(k Kind) bool { return p.tok.Kind == k }

// accept consumes the current token if it has kind k.
func (p *parser) accept(k Kind) bool {
	if p.tok.Kind == k {
		p.next()
		return true
	}
	return false
}

func (p *parser) errorAt(pos ast.Pos, format string, args ...interface{}) {
	if p.panicking || len(p.errs) >= maxErrors {
		return
	}
	p.errs = append(p.errs, Error{Pos: pos, Msg: fmt.Sprintf(format, args...)})
	p.panicking = true
}

// expect consumes a token of kind k, or reports what it found instead.
func (p *parser) expect(k Kind) Token {
	if p.tok.Kind == k {
		t := p.tok
		p.next()
		p.panicking = false
		return t
	}
	p.errorAt(p.tok.Pos, "expected %s, found %s", k, p.describe())
	return Token{Kind: Invalid, Pos: p.tok.Pos}
}

// describe names the current token for a diagnostic, quoting its spelling
// where that is more useful than its class.
func (p *parser) describe() string {
	switch p.tok.Kind {
	case EOF:
		return "end of file"
	case TokIdent:
		return fmt.Sprintf("identifier %q", p.tok.Text)
	case TokIntLit, TokRealLit, TokTypedLit:
		return fmt.Sprintf("literal %s", p.tok.Text)
	case TokStringLit, TokWStringLit:
		return fmt.Sprintf("string literal %q", p.tok.Str)
	}
	return p.tok.Kind.String()
}

// expectName consumes an identifier and returns its spelling.
func (p *parser) expectName(what string) string {
	if p.tok.Kind == TokIdent {
		name := p.tok.Text
		p.next()
		p.panicking = false
		return name
	}
	// A keyword where a name belongs is worth saying plainly: it is almost
	// always a name that collides with a reserved word.
	if p.tok.Kind.IsKeyword() {
		p.errorAt(p.tok.Pos, "expected %s, found the reserved word %q", what, p.tok.Text)
	} else {
		p.errorAt(p.tok.Pos, "expected %s, found %s", what, p.describe())
	}
	return ""
}

// checkpoint captures enough parser state to speculatively parse ahead and
// then take it back.  Errors are included: a speculative attempt that fails
// must leave no diagnostic behind, or a successful reinterpretation would
// still report the failed guess.
type checkpoint struct {
	sc        Scanner
	tok       Token
	nerrs     int
	panicking bool
}

func (p *parser) mark() checkpoint {
	return checkpoint{sc: *p.sc, tok: p.tok, nerrs: len(p.errs), panicking: p.panicking}
}

func (p *parser) reset(c checkpoint) {
	*p.sc = c.sc
	p.tok = c.tok
	p.errs = p.errs[:c.nerrs]
	p.panicking = c.panicking
}

// syncTo skips tokens until one of kinds (or EOF) is current.  It is how the
// parser recovers: after an error inside a declaration, resume at the next
// declaration rather than reinterpreting the remains of this one.
func (p *parser) syncTo(kinds ...Kind) {
	for !p.at(EOF) {
		for _, k := range kinds {
			if p.at(k) {
				p.panicking = false
				return
			}
		}
		p.next()
	}
	p.panicking = false
}

// ---------------------------------------------------------------------------
// File
// ---------------------------------------------------------------------------

var declStarters = []Kind{
	KwType, KwProgram, KwFunction, KwFunctionBlock, KwVarGlobal, KwAction,
}

func (p *parser) parseFile() *File {
	f := &File{Pos: p.tok.Pos}

	// Pragmas before the first declaration are the file's, and carry the
	// component's own metadata -- summary, licence, author -- which docgen
	// turns into a man page.  Everything after that attaches to the
	// declaration it precedes.
	//
	// Splitting on the first declaration rather than on adjacency is what
	// makes `{attribute 'summary'}` at the top of a file mean the component
	// even when a FUNCTION_BLOCK happens to be declared next.  A pin's `doc`
	// is unaffected: it sits inside a VAR section, always past this point.
	//
	// `doc` is the one exception, and it is why the parser knows that name at
	// all: it documents a thing rather than the file, so it always belongs to
	// whatever follows.  Without the exception the first declaration in a file
	// could not be documented -- its `doc` would land among the component
	// metadata and be reported as unknown there.
	// The POU an ACTION at file level belongs to: the one declared last.
	lastPOU := -1
	lastKind := EOF

	var attrs []Attribute
	for _, a := range p.parseAttributes() {
		if strings.EqualFold(a.Name, "doc") {
			attrs = append(attrs, a)
			continue
		}
		f.Attrs = append(f.Attrs, a)
	}

	for !p.at(EOF) {
		if p.at(Pragma) {
			attrs = append(attrs, p.parseAttributes()...)
			continue
		}

		switch p.tok.Kind {
		case KwType:
			p.parseTypeBlock(f, attrs)
			attrs = nil
		case KwVarGlobal:
			if b := p.parseVarBlock(); b != nil {
				f.Globals = append(f.Globals, b)
			}
			attrs = nil
		case KwProgram:
			if d := p.parseProgram(attrs); d != nil {
				f.Programs = append(f.Programs, d)
				lastPOU, lastKind = len(f.Programs)-1, KwProgram
			}
			attrs = nil
		case KwFunctionBlock:
			if d := p.parseFunctionBlock(attrs); d != nil {
				f.Blocks = append(f.Blocks, d)
				lastPOU, lastKind = len(f.Blocks)-1, KwFunctionBlock
			}
			attrs = nil
		case KwFunction:
			if d := p.parseFunction(attrs); d != nil {
				f.Funcs = append(f.Funcs, d)
				lastKind = KwFunction
			}
			attrs = nil
		case KwAction:
			// CODESYS 2.3 exports write a POU's actions after its END_
			// keyword rather than inside it, so an ACTION here belongs to
			// whatever was declared last.  Both placements mean the same
			// thing; refusing the exported one would refuse real files for
			// no reason.
			acts := p.parseActions()
			switch lastKind {
			case KwProgram:
				f.Programs[lastPOU].Actions = append(f.Programs[lastPOU].Actions, acts...)
			case KwFunctionBlock:
				f.Blocks[lastPOU].Actions = append(f.Blocks[lastPOU].Actions, acts...)
			case KwFunction:
				for _, a := range acts {
					p.errorAt(a.Pos, "a FUNCTION may not have an ACTION; "+
						"actions belong to a PROGRAM or a FUNCTION_BLOCK, whose variables they share")
				}
			default:
				for _, a := range acts {
					p.errorAt(a.Pos, "ACTION %s follows no PROGRAM or FUNCTION_BLOCK, "+
						"so there is nothing for it to be part of", a.Name)
				}
			}
			attrs = nil
		case Semicolon:
			// A stray separator between declarations is harmless.
			p.next()
		case TokIdent:
			if strings.EqualFold(p.tok.Text, "VAR_CONFIG") {
				p.errorAt(p.tok.Pos,
					"VAR_CONFIG is not supported; stratuMAK has no located variables to configure")
				p.syncTo(KwEndVar)
				p.accept(KwEndVar)
				attrs = nil
				continue
			}
			p.errorAt(p.tok.Pos,
				"expected a TYPE, VAR_GLOBAL, PROGRAM, FUNCTION or FUNCTION_BLOCK declaration, found %s",
				p.describe())
			p.syncTo(declStarters...)
			if p.at(EOF) {
				return f
			}
		default:
			p.errorAt(p.tok.Pos,
				"expected a TYPE, VAR_GLOBAL, PROGRAM, FUNCTION or FUNCTION_BLOCK declaration, found %s",
				p.describe())
			p.syncTo(declStarters...)
			if p.at(EOF) {
				return f
			}
		}
	}

	// Pragmas with no declaration after them attach to nothing.  Say so
	// rather than dropping them: a misplaced attribute is otherwise invisible.
	for _, a := range attrs {
		p.errorAt(a.Pos, "attribute %q does not precede a declaration", a.Name)
	}
	return f
}

// parseAttributes consumes a run of pragmas.
func (p *parser) parseAttributes() []Attribute {
	var out []Attribute
	for p.at(Pragma) {
		if a, ok := p.parseAttribute(p.tok); ok {
			out = append(out, a)
		}
		p.next()
	}
	return out
}

// parseAttribute pulls `attribute 'name' := 'value'` out of a pragma body.
//
// The vocabulary is not checked here.  Which attribute names are meaningful is
// sema's business, and an unknown one becomes a warning rather than an error
// so that a pragma meant for another toolchain does not stop the build.
func (p *parser) parseAttribute(tok Token) (Attribute, bool) {
	body := strings.TrimSpace(tok.Str)

	const kw = "attribute"
	if len(body) < len(kw) || !strings.EqualFold(body[:len(kw)], kw) {
		p.errorAt(tok.Pos, "pragma must start with 'attribute', found %q", firstWord(body))
		return Attribute{}, false
	}
	rest := strings.TrimSpace(body[len(kw):])

	name, rest, ok := cutQuoted(rest)
	if !ok {
		p.errorAt(tok.Pos, "attribute needs a quoted name, as in {attribute 'summary' := 'text'}")
		return Attribute{}, false
	}

	rest = strings.TrimSpace(rest)
	if rest == "" {
		// A valueless attribute is a flag: {attribute 'hal_publish'}.
		return Attribute{Pos: tok.Pos, Name: name}, true
	}
	if !strings.HasPrefix(rest, ":=") {
		p.errorAt(tok.Pos, "expected ':=' after attribute name %q, found %q", name, firstWord(rest))
		return Attribute{}, false
	}
	rest = strings.TrimSpace(rest[2:])

	value, rest, ok := cutQuoted(rest)
	if !ok {
		p.errorAt(tok.Pos, "attribute %q needs a quoted value", name)
		return Attribute{}, false
	}
	if extra := strings.TrimSpace(rest); extra != "" {
		p.errorAt(tok.Pos, "unexpected %q after the value of attribute %q", firstWord(extra), name)
		return Attribute{}, false
	}
	return Attribute{Pos: tok.Pos, Name: name, Value: value}, true
}

// cutQuoted takes a single-quoted string off the front of s and returns it with
// the remainder.
//
// It honours the same escapes an ST string literal does -- `$N` for a newline,
// `$$` for a dollar, `”` or `$'` for a quote -- because a pragma value is a
// string and a PLC programmer will reach for those.  A man page needs line
// breaks in its EXAMPLES section, and `$N` is how ST spells one.  A literal
// newline inside the quotes works too.
func cutQuoted(s string) (value, rest string, ok bool) {
	if len(s) == 0 || s[0] != '\'' {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), s[i+1:], true

		case '$':
			if i+1 >= len(s) {
				return "", s, false
			}
			i++
			switch s[i] {
			case '$', '\'', '"':
				b.WriteByte(s[i])
			case 'N', 'n', 'L', 'l':
				b.WriteByte('\n')
			case 'R', 'r':
				b.WriteByte('\r')
			case 'T', 't':
				b.WriteByte('\t')
			case 'P', 'p':
				b.WriteByte('\f')
			default:
				// An unknown escape is kept as written rather than dropped:
				// a man page macro such as \fB is not an escape here.
				b.WriteByte('$')
				b.WriteByte(s[i])
			}

		default:
			b.WriteByte(s[i])
		}
	}
	return "", s, false
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "end of pragma"
	}
	return s
}

// ---------------------------------------------------------------------------
// TYPE ... END_TYPE
// ---------------------------------------------------------------------------

func (p *parser) parseTypeBlock(f *File, attrs []Attribute) {
	p.expect(KwType)
	for !p.at(KwEndType) && !p.at(EOF) {
		if p.at(Pragma) {
			attrs = append(attrs, p.parseAttributes()...)
			continue
		}
		d := p.parseTypeDecl(attrs)
		attrs = nil
		if d == nil {
			p.syncTo(KwEndType, Semicolon)
			p.accept(Semicolon)
			continue
		}
		f.Types = append(f.Types, d)
	}
	p.expect(KwEndType)
}

func (p *parser) parseTypeDecl(attrs []Attribute) *TypeDecl {
	d := &TypeDecl{Pos: p.tok.Pos, Attrs: attrs}
	d.Name = p.expectName("a type name")
	if d.Name == "" {
		return nil
	}
	p.expect(Colon)

	switch {
	case p.at(KwStruct):
		d.Struct = p.parseStructBody()
	case p.at(LParen):
		d.Enum = p.parseEnumBody()
	default:
		d.Alias = p.parseTypeRef()
	}

	// A default value applies to an alias or an enumeration.
	if p.accept(Assign) {
		d.Init = p.parseExpr()
	}
	if d.Struct != nil {
		// parseStructBody already consumed any separator.
		p.accept(Semicolon)
		return d
	}
	p.expect(Semicolon)
	return d
}

func (p *parser) parseStructBody() []*FieldDecl {
	p.expect(KwStruct)
	var fields []*FieldDecl
	for !p.at(KwEndStruct) && !p.at(EOF) {
		attrs := p.parseAttributes()
		if p.at(KwEndStruct) {
			break
		}
		fd := &FieldDecl{Pos: p.tok.Pos, Attrs: attrs}
		fd.Name = p.expectName("a field name")
		if fd.Name == "" {
			p.syncTo(Semicolon, KwEndStruct)
			p.accept(Semicolon)
			continue
		}
		p.expect(Colon)
		fd.Type = p.parseTypeRef()
		if p.accept(Assign) {
			fd.Init = p.parseExpr()
		}
		p.expect(Semicolon)
		fields = append(fields, fd)
	}
	p.expect(KwEndStruct)
	// The separator after END_STRUCT is optional: CODESYS omits it, and
	// insisting would reject files every other toolchain accepts -- the same
	// bargain the compound statements get.
	p.accept(Semicolon)
	return fields
}

func (p *parser) parseEnumBody() []EnumMember {
	p.expect(LParen)
	var members []EnumMember
	for {
		if p.at(RParen) {
			break
		}
		m := EnumMember{Pos: p.tok.Pos}
		m.Name = p.expectName("an enumeration constant")
		if m.Name == "" {
			p.syncTo(Comma, RParen)
		} else if p.accept(Assign) {
			m.Value = p.parseExpr()
		}
		if m.Name != "" {
			members = append(members, m)
		}
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	return members
}

// ---------------------------------------------------------------------------
// Type references
// ---------------------------------------------------------------------------

func (p *parser) parseTypeRef() *TypeRef {
	t := &TypeRef{Pos: p.tok.Pos}

	if p.at(KwArray) {
		p.next()
		p.expect(LBracket)
		t.Dims = []Range{}
		for {
			t.Dims = append(t.Dims, p.parseRange())
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(RBracket)
		p.expect(KwOf)
		t.Elem = p.parseTypeRef()
		return t
	}

	t.Name = p.expectName("a type name")
	if t.Name == "" {
		return t
	}

	// `POINTER TO T` and `REFERENCE TO T`.  Neither is implemented, but both
	// parse so sema can refuse them by name rather than the parser failing on
	// the word TO.
	if p.at(KwTo) && (strings.EqualFold(t.Name, "POINTER") || strings.EqualFold(t.Name, "REFERENCE")) {
		p.next()
		t.Pointer = true
		t.Name = ""
		t.Elem = p.parseTypeRef()
		return t
	}

	// A namespaced type: `visuElems.IDialogManager`.  It is one name here;
	// whether the namespace exists is sema's question.
	for p.at(Dot) {
		p.next()
		part := p.expectName("a type name after the namespace")
		if part == "" {
			return t
		}
		t.Name += "." + part
	}

	// STRING[10] -- and `STRING [1]`, since whitespace is not significant.
	if p.at(LBracket) {
		p.next()
		t.StringLen = p.parseExpr()
		p.expect(RBracket)
		return t
	}

	// A parenthesised suffix is either a subrange, `INT (1..2)`, or the older
	// spelling of a string length, `STRING(11)`.  Which one is settled by
	// whether `..` follows the first expression.
	if p.at(LParen) {
		pos := p.tok.Pos
		p.next()
		first := p.parseExpr()
		if p.accept(DotDot) {
			r := Range{Pos: pos, Lo: first, Hi: p.parseExpr()}
			t.Subrange = &r
		} else {
			t.StringLen = first
		}
		p.expect(RParen)
	}
	return t
}

func (p *parser) parseRange() Range {
	r := Range{Pos: p.tok.Pos}
	r.Lo = p.parseExpr()
	p.expect(DotDot)
	r.Hi = p.parseExpr()
	return r
}

// ---------------------------------------------------------------------------
// VAR blocks
// ---------------------------------------------------------------------------

var varBlockKinds = map[Kind]VarKind{
	KwVar:         VarLocal,
	KwVarInput:    VarInput,
	KwVarOutput:   VarOutput,
	KwVarInOut:    VarInOut,
	KwVarGlobal:   VarGlobal,
	KwVarTemp:     VarTemp,
	KwVarExternal: VarExternal,
}

// atVarBlock reports whether a VAR section starts here.
func (p *parser) atVarBlock() bool {
	if _, ok := varBlockKinds[p.tok.Kind]; ok {
		return true
	}
	// VAR_ACCESS and VAR_CONFIG are recognised here only so parseVarBlock can
	// refuse them by name.  VAR_CONFIG is not a keyword -- nothing else needs
	// it to be -- so it arrives as an identifier.
	return p.at(KwVarAccess) || (p.at(TokIdent) && strings.EqualFold(p.tok.Text, "VAR_CONFIG"))
}

func (p *parser) parseVarBlock() *VarBlock {
	// VAR_ACCESS has a shape of its own and names a facility stratuMAK does
	// not have.  Say so here rather than letting it fail as a syntax error on
	// the second colon of `VarPath : VarName.Path : BOOL READ_ONLY;`.
	if p.at(TokIdent) && strings.EqualFold(p.tok.Text, "VAR_CONFIG") {
		p.errorAt(p.tok.Pos,
			"VAR_CONFIG is not supported; stratuMAK has no located variables to configure")
		p.syncTo(KwEndVar)
		p.accept(KwEndVar)
		return nil
	}
	if p.at(KwVarAccess) {
		p.errorAt(p.tok.Pos,
			"VAR_ACCESS is not supported; stratuMAK exposes variables through HAL pins declared in VAR_INPUT/VAR_OUTPUT")
		p.syncTo(KwEndVar)
		p.accept(KwEndVar)
		return nil
	}

	kind, ok := varBlockKinds[p.tok.Kind]
	if !ok {
		p.errorAt(p.tok.Pos, "expected a VAR section, found %s", p.describe())
		return nil
	}
	b := &VarBlock{Pos: p.tok.Pos, Kind: kind}
	p.next()

	// Qualifiers may follow the section keyword in any order.
	for {
		switch {
		case p.accept(KwConstant):
			b.Constant = true
		case p.accept(KwRetain):
			b.Retain = true
		case p.accept(KwNonRetain):
			b.NonRetain = true
		default:
			goto decls
		}
	}

decls:
	for !p.at(KwEndVar) && !p.at(EOF) {
		attrs := p.parseAttributes()
		if p.at(KwEndVar) {
			break
		}
		d := p.parseVarDecl(attrs)
		if d == nil {
			p.syncTo(Semicolon, KwEndVar)
			p.accept(Semicolon)
			continue
		}
		b.Decls = append(b.Decls, d)
	}
	p.expect(KwEndVar)
	return b
}

func (p *parser) parseVarDecl(attrs []Attribute) *VarDecl {
	d := &VarDecl{Pos: p.tok.Pos, Attrs: attrs}

	// One or more names share a type: `PI, G : BOOL R_EDGE;`.
	for {
		name := p.expectName("a variable name")
		if name == "" {
			return nil
		}
		d.Names = append(d.Names, name)
		if !p.accept(Comma) {
			break
		}
	}

	// A located variable: `Button AT %IX1 : BOOL;`.  Accepted so sema can
	// reject it by name; the address runs to the colon.
	if p.accept(KwAt) {
		var b strings.Builder
		for !p.at(Colon) && !p.at(EOF) {
			b.WriteString(p.tok.Text)
			p.next()
		}
		d.Located = strings.TrimSpace(b.String())
		if d.Located == "" {
			p.errorAt(p.tok.Pos, "AT needs a direct address")
		}
	}

	p.expect(Colon)
	d.Type = p.parseTypeRef()

	switch {
	case p.accept(KwREdge):
		d.Edge = EdgeRising
	case p.accept(KwFEdge):
		d.Edge = EdgeFalling
	}

	if p.accept(Assign) {
		d.Init = p.parseInitialiser()
	}
	p.expect(Semicolon)
	return d
}

// parseInitialiser parses the value after `:=` in a declaration.
//
// An array of structured values is written as a comma-separated list of them
// with no enclosing brackets -- `X : ARRAY[1..2] OF T := (a := 1), (a := 2);`
// -- which is a shape that appears nowhere else, so it is recognised here
// rather than in the expression grammar.
func (p *parser) parseInitialiser() Expr {
	first := p.parseExpr()
	lit, ok := first.(*StructLit)
	if !ok || !p.at(Comma) {
		return first
	}

	out := &StructLit{Pos: lit.Pos, Elems: []*StructLit{lit}}
	for p.accept(Comma) {
		next := p.parseExpr()
		elem, ok := next.(*StructLit)
		if !ok {
			p.errorAt(next.Position(),
				"an array of structured values takes one per element, each in parentheses")
			return out
		}
		out.Elems = append(out.Elems, elem)
	}
	return out
}

// parseVarSections consumes the VAR blocks at the head of a body, gathering any
// pragma between them into attrs.
//
// A pragma *outside* a VAR block belongs to the declaration it heads -- the POU
// itself -- while one *inside* belongs to the variable it precedes.  That is
// the same split the file level uses, and it is what lets a PROGRAM carry a
// doc attribute for its HAL function without the attribute silently becoming
// the first pin's instead.
//
// A pragma that turns out to head the body rather than a VAR section is left
// alone, since that is how a loop receives its max_iterations bound.
func (p *parser) parseVarSections(attrs *[]Attribute) []*VarBlock {
	var out []*VarBlock
	for {
		if p.at(Pragma) {
			cp := p.mark()
			found := p.parseAttributes()
			if !p.atVarBlock() {
				p.reset(cp)
				return out
			}
			*attrs = append(*attrs, found...)
			continue
		}
		if !p.atVarBlock() {
			return out
		}
		if b := p.parseVarBlock(); b != nil {
			out = append(out, b)
		}
	}
}

// ---------------------------------------------------------------------------
// POU declarations
// ---------------------------------------------------------------------------

func (p *parser) parseProgram(attrs []Attribute) *ProgramDecl {
	d := &ProgramDecl{Pos: p.tok.Pos, Attrs: attrs}
	p.expect(KwProgram)
	d.Name = p.expectName("a program name")
	if d.Name == "" {
		p.syncTo(KwEndProgram)
		p.accept(KwEndProgram)
		return nil
	}
	d.Vars = p.parseVarSections(&d.Attrs)
	d.Body = p.parseStmtList(KwEndProgram)
	d.Actions = p.parseActions()
	p.expect(KwEndProgram)
	return d
}

// parseActions consumes the ACTION blocks that follow a POU's body.
func (p *parser) parseActions() []*ActionDecl {
	var out []*ActionDecl
	for p.at(KwAction) || p.at(Pragma) {
		attrs := p.parseAttributes()
		if !p.at(KwAction) {
			for _, a := range attrs {
				p.errorAt(a.Pos, "attribute %q does not precede an ACTION", a.Name)
			}
			return out
		}
		a := &ActionDecl{Pos: p.tok.Pos, Attrs: attrs}
		p.next()
		a.Name = p.expectName("an action name")
		if a.Name == "" {
			p.syncTo(KwEndAction)
			p.accept(KwEndAction)
			continue
		}
		p.expect(Colon)
		a.Body = p.parseStmtList(KwEndAction)
		p.expect(KwEndAction)
		p.accept(Semicolon)
		out = append(out, a)
	}
	return out
}

func (p *parser) parseFunctionBlock(attrs []Attribute) *FunctionBlockDecl {
	d := &FunctionBlockDecl{Pos: p.tok.Pos, Attrs: attrs}
	p.expect(KwFunctionBlock)
	d.Name = p.expectName("a function block name")
	if d.Name == "" {
		p.syncTo(KwEndFunctionBlock)
		p.accept(KwEndFunctionBlock)
		return nil
	}
	d.Vars = p.parseVarSections(&d.Attrs)
	d.Body = p.parseStmtList(KwEndFunctionBlock)
	d.Actions = p.parseActions()
	p.expect(KwEndFunctionBlock)
	return d
}

func (p *parser) parseFunction(attrs []Attribute) *FunctionDecl {
	d := &FunctionDecl{Pos: p.tok.Pos, Attrs: attrs}
	p.expect(KwFunction)
	d.Name = p.expectName("a function name")
	if d.Name == "" {
		p.syncTo(KwEndFunction)
		p.accept(KwEndFunction)
		return nil
	}
	// The result type is written `FUNCTION name : TYPE`, with the colon
	// optionally unspaced as in `FUNCTION FUNC:UINT`.
	if p.accept(Colon) {
		d.Result = p.parseTypeRef()
	} else {
		p.errorAt(p.tok.Pos,
			"function %q needs a result type, as in `FUNCTION %s : INT`", d.Name, d.Name)
	}
	d.Vars = p.parseVarSections(&d.Attrs)
	d.Body = p.parseStmtList(KwEndFunction)
	if p.at(KwAction) {
		// IEC gives actions to programs and function blocks, which have state
		// for an action to share.  A function has only its own locals, so an
		// action of one would be a function with no arguments and no result.
		p.errorAt(p.tok.Pos, "a FUNCTION may not have an ACTION; "+
			"actions belong to a PROGRAM or a FUNCTION_BLOCK, whose variables they share")
		for p.at(KwAction) {
			p.syncTo(KwEndAction)
			p.accept(KwEndAction)
			p.accept(Semicolon)
		}
	}
	p.expect(KwEndFunction)
	return d
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// stmtEnd is every token that can close a statement list.  Passing the
// expected terminator to parseStmtList and stopping at any of these lets a
// missing END_IF be reported where it is missing rather than at end of file.
var stmtEnd = []Kind{
	KwEndProgram, KwEndFunction, KwEndFunctionBlock,
	KwEndIf, KwElsif, KwElse, KwEndCase, KwEndFor, KwEndWhile,
	KwEndRepeat, KwUntil,
	// An ACTION ends the body it follows: the actions of a POU come after its
	// statements, not among them.
	KwAction, KwEndAction,
}

func atStmtEnd(k Kind) bool {
	for _, e := range stmtEnd {
		if k == e {
			return true
		}
	}
	return k == EOF
}

func (p *parser) parseStmtList(terminator Kind) []Stmt {
	var out []Stmt
	for !p.at(terminator) && !atStmtEnd(p.tok.Kind) {
		before := p.tok
		s := p.parseStmt()
		if s != nil {
			out = append(out, s)
		}
		// A statement that consumed nothing would spin forever.  Recover to
		// the next separator so the file still yields a useful error list.
		if p.tok == before {
			p.errorAt(p.tok.Pos, "expected a statement, found %s", p.describe())
			p.syncTo(append([]Kind{Semicolon, terminator}, stmtEnd...)...)
			p.accept(Semicolon)
		}
	}
	return out
}

func (p *parser) parseStmt() Stmt {
	// A pragma here belongs to the statement that follows -- which is how a
	// loop receives its max_iterations bound.
	attrs := p.parseAttributes()

	switch p.tok.Kind {
	case KwIf:
		return p.parseIf()
	case KwCase:
		return p.parseCase()
	case KwFor:
		return p.parseFor()
	case KwWhile:
		return p.parseWhile(attrs)
	case KwRepeat:
		return p.parseRepeat(attrs)
	case KwExit:
		s := &ExitStmt{Pos: p.tok.Pos}
		p.next()
		p.acceptStmtSemi()
		return s
	case KwContinue:
		s := &ContinueStmt{Pos: p.tok.Pos}
		p.next()
		p.acceptStmtSemi()
		return s
	case KwReturn:
		s := &ReturnStmt{Pos: p.tok.Pos}
		p.next()
		p.acceptStmtSemi()
		return s
	case Semicolon:
		s := &EmptyStmt{Pos: p.tok.Pos}
		p.next()
		return s
	}

	if len(attrs) > 0 {
		p.errorAt(attrs[0].Pos,
			"attribute %q may only precede a WHILE or REPEAT loop here", attrs[0].Name)
	}
	return p.parseSimpleStmt()
}

// acceptStmtSemi consumes the semicolon that ends a statement.  It is optional
// before a closing keyword, because real ST in the wild leaves it off there and
// insisting would reject files every other toolchain accepts.
func (p *parser) acceptStmtSemi() {
	if p.accept(Semicolon) {
		return
	}
	if atStmtEnd(p.tok.Kind) {
		return
	}
	p.errorAt(p.tok.Pos, "expected ';' after the statement, found %s", p.describe())
	p.syncTo(append([]Kind{Semicolon}, stmtEnd...)...)
	p.accept(Semicolon)
}

// parseSimpleStmt parses an assignment or a call.  Both begin with an
// expression, so parse that first and decide afterwards.
func (p *parser) parseSimpleStmt() Stmt {
	pos := p.tok.Pos
	lhs := p.parseExpr()
	if lhs == nil {
		return nil
	}

	if p.accept(Assign) {
		s := &AssignStmt{Pos: pos, Target: lhs}
		s.Value = p.parseExpr()
		p.acceptStmtSemi()
		return s
	}

	if call, ok := lhs.(*CallExpr); ok {
		s := &CallStmt{Pos: pos, Target: call.Func, Args: call.Args}
		p.acceptStmtSemi()
		return s
	}

	// A bare expression is not a statement in ST.  The overwhelmingly common
	// cause is `=` written where `:=` was meant, so say that.
	if bin, ok := lhs.(*BinaryExpr); ok && bin.Op == ast.BinEq {
		p.errorAt(pos, "expected an assignment; ST assigns with ':=' and compares with '='")
	} else {
		p.errorAt(pos, "expression is not a statement; expected an assignment or a call")
	}
	p.acceptStmtSemi()
	return nil
}

func (p *parser) parseIf() Stmt {
	s := &IfStmt{Pos: p.tok.Pos}
	p.expect(KwIf)
	s.Cond = p.parseExpr()
	p.expect(KwThen)
	s.Then = p.parseStmtList(KwEndIf)

	for p.at(KwElsif) {
		e := ElseIf{Pos: p.tok.Pos}
		p.next()
		e.Cond = p.parseExpr()
		p.expect(KwThen)
		e.Body = p.parseStmtList(KwEndIf)
		s.ElseIfs = append(s.ElseIfs, e)
	}

	if p.accept(KwElse) {
		s.HasElse = true
		s.Else = p.parseStmtList(KwEndIf)
	}

	p.expect(KwEndIf)
	p.accept(Semicolon)
	return s
}

func (p *parser) parseCase() Stmt {
	s := &CaseStmt{Pos: p.tok.Pos}
	p.expect(KwCase)
	s.Selector = p.parseExpr()
	p.expect(KwOf)

	for !p.at(KwEndCase) && !p.at(KwElse) && !p.at(EOF) {
		c := CaseClause{Pos: p.tok.Pos}
		for {
			l := CaseLabel{Pos: p.tok.Pos}
			l.Value = p.parseExpr()
			if p.accept(DotDot) {
				l.High = p.parseExpr()
			}
			c.Labels = append(c.Labels, l)
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(Colon)
		c.Body = p.parseCaseBody()
		s.Clauses = append(s.Clauses, c)
	}

	if p.accept(KwElse) {
		s.HasElse = true
		s.Else = p.parseStmtList(KwEndCase)
	}

	p.expect(KwEndCase)
	p.accept(Semicolon)
	return s
}

// parseCaseBody parses the statements of one CASE arm.  An arm ends where the
// next one begins, and a label is not distinguishable from the start of a
// statement until its colon: `VAL2: NUM1 := 1;` and `NUM1 := 1;` agree on
// their first token.  So before each statement, speculatively parse a label
// list and keep the result only if a colon follows.
func (p *parser) parseCaseBody() []Stmt {
	var out []Stmt
	for !p.at(KwEndCase) && !p.at(KwElse) && !atStmtEnd(p.tok.Kind) {
		if p.atCaseLabel() {
			return out
		}
		before := p.tok
		if st := p.parseStmt(); st != nil {
			out = append(out, st)
		}
		if p.tok == before {
			p.errorAt(p.tok.Pos, "expected a statement, found %s", p.describe())
			p.syncTo(append([]Kind{Semicolon, KwEndCase}, stmtEnd...)...)
			p.accept(Semicolon)
		}
	}
	return out
}

// atCaseLabel reports whether a CASE label list starts at the current token.
// It parses one speculatively and rewinds, so the caller sees no side effect
// either way.
func (p *parser) atCaseLabel() bool {
	// A label is a constant expression, so a statement keyword rules it out at
	// once and saves the speculative parse.
	switch p.tok.Kind {
	case KwIf, KwCase, KwFor, KwWhile, KwRepeat, KwExit, KwContinue, KwReturn, Semicolon, Pragma:
		return false
	}

	cp := p.mark()
	defer p.reset(cp)

	for {
		if p.parseExpr() == nil {
			return false
		}
		if len(p.errs) > cp.nerrs {
			return false
		}
		if p.accept(DotDot) {
			if p.parseExpr() == nil || len(p.errs) > cp.nerrs {
				return false
			}
		}
		if !p.accept(Comma) {
			break
		}
	}
	return p.at(Colon)
}

func (p *parser) parseFor() Stmt {
	s := &ForStmt{Pos: p.tok.Pos}
	p.expect(KwFor)
	s.Var = p.parseExpr()
	p.expect(Assign)
	s.From = p.parseExpr()
	p.expect(KwTo)
	s.To = p.parseExpr()
	if p.accept(KwBy) {
		s.By = p.parseExpr()
	}
	p.expect(KwDo)
	s.Body = p.parseStmtList(KwEndFor)
	p.expect(KwEndFor)
	p.accept(Semicolon)
	return s
}

func (p *parser) parseWhile(attrs []Attribute) Stmt {
	s := &WhileStmt{Pos: p.tok.Pos, Attrs: attrs}
	p.expect(KwWhile)
	s.Cond = p.parseExpr()
	p.expect(KwDo)
	s.Body = p.parseStmtList(KwEndWhile)
	p.expect(KwEndWhile)
	p.accept(Semicolon)
	return s
}

func (p *parser) parseRepeat(attrs []Attribute) Stmt {
	s := &RepeatStmt{Pos: p.tok.Pos, Attrs: attrs}
	p.expect(KwRepeat)
	s.Body = p.parseStmtList(KwUntil)
	p.expect(KwUntil)
	s.Cond = p.parseExpr()
	p.expect(KwEndRepeat)
	p.accept(Semicolon)
	return s
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Precedence, loosest first, per IEC 61131-3.  Parsing is precedence-climbing
// over this table so that adding a level is a table edit rather than a new
// function.
//
// `&` is a synonym for AND and sits at the same level.
var binPrec = map[Kind]struct {
	prec int
	op   ast.BinOp
}{
	KwOr:  {1, ast.BinOr},
	KwXor: {2, ast.BinXor},
	KwAnd: {3, ast.BinAnd},
	Amp:   {3, ast.BinAnd},

	Equal:    {4, ast.BinEq},
	NotEqual: {4, ast.BinNe},

	Less:      {5, ast.BinLt},
	Greater:   {5, ast.BinGt},
	LessEqual: {5, ast.BinLe},
	GreaterEq: {5, ast.BinGe},

	Plus:  {6, ast.BinAdd},
	Minus: {6, ast.BinSub},

	Star:  {7, ast.BinMul},
	Slash: {7, ast.BinDiv},
	KwMod: {7, ast.BinMod},
}

// powerPrec sits above every binary level and below unary, and is
// right-associative: 2 ** 3 ** 2 is 2 ** (3 ** 2).
const powerPrec = 9

func (p *parser) parseExpr() Expr { return p.parseBinary(1) }

func (p *parser) parseBinary(minPrec int) Expr {
	lhs := p.parseUnary()
	if lhs == nil {
		return nil
	}
	for {
		info, ok := binPrec[p.tok.Kind]
		if !ok || info.prec < minPrec {
			return lhs
		}
		pos := p.tok.Pos
		p.next()
		rhs := p.parseBinary(info.prec + 1)
		if rhs == nil {
			return lhs
		}
		lhs = &BinaryExpr{Pos: pos, Op: info.op, X: lhs, Y: rhs}
	}
}

func (p *parser) parseUnary() Expr {
	switch p.tok.Kind {
	case Minus:
		pos := p.tok.Pos
		p.next()
		x := p.parseUnary()
		if x == nil {
			return nil
		}
		return &UnaryExpr{Pos: pos, Op: ast.UnNeg, X: x}
	case Plus:
		pos := p.tok.Pos
		p.next()
		x := p.parseUnary()
		if x == nil {
			return nil
		}
		return &UnaryExpr{Pos: pos, Op: ast.UnPlus, X: x}
	case KwNot:
		pos := p.tok.Pos
		p.next()
		x := p.parseUnary()
		if x == nil {
			return nil
		}
		return &UnaryExpr{Pos: pos, Op: ast.UnNot, X: x}
	}
	return p.parsePower()
}

// parsePower handles `**`, which binds tighter than unary minus on its left
// operand and is right-associative.
func (p *parser) parsePower() Expr {
	base := p.parsePostfix()
	if base == nil {
		return nil
	}
	if p.at(Power) {
		pos := p.tok.Pos
		p.next()
		// The right operand may itself carry a unary sign: 2 ** -1.
		exp := p.parseUnaryForPower()
		if exp == nil {
			return base
		}
		return &BinaryExpr{Pos: pos, Op: ast.BinPow, X: base, Y: exp}
	}
	return base
}

// parseUnaryForPower parses the right operand of `**`, keeping the operator
// right-associative.
func (p *parser) parseUnaryForPower() Expr {
	switch p.tok.Kind {
	case Minus, Plus, KwNot:
		return p.parseUnary()
	}
	return p.parsePower()
}

// parsePostfix parses a primary followed by any run of subscripts, field
// selections, calls and dereferences.
func (p *parser) parsePostfix() Expr {
	e := p.parsePrimary()
	if e == nil {
		return nil
	}
	for {
		switch p.tok.Kind {
		case LBracket:
			pos := p.tok.Pos
			p.next()
			ix := &IndexExpr{Pos: pos, Base: e}
			for {
				idx := p.parseExpr()
				if idx == nil {
					break
				}
				ix.Indices = append(ix.Indices, idx)
				if !p.accept(Comma) {
					break
				}
			}
			p.expect(RBracket)
			e = ix

		case Dot:
			pos := p.tok.Pos
			p.next()
			// `word.0` is partial access: the Nth bit of an integer.  IEC
			// spells it this way and real code uses it constantly, so it
			// parses here and sema refuses it by name.
			if p.at(TokIntLit) {
				bit := p.tok.Int
				p.next()
				e = &BitExpr{Pos: pos, Base: e, Bit: bit}
				continue
			}
			name := p.expectName("a field name")
			if name == "" {
				return e
			}
			e = &FieldExpr{Pos: pos, Base: e, Name: name}

		case LParen:
			pos := p.tok.Pos
			p.next()
			call := &CallExpr{Pos: pos, Func: e}
			call.Args = p.parseArgs()
			p.expect(RParen)
			e = call

		case Caret:
			pos := p.tok.Pos
			p.next()
			e = &DerefExpr{Pos: pos, Base: e}

		default:
			return e
		}
	}
}

// parseArgs parses a call's argument list.  Three forms mix freely:
// positional, `name := value` for an input, and `name => target` for an output.
func (p *parser) parseArgs() []Arg {
	var args []Arg
	if p.at(RParen) {
		return args
	}
	for {
		a := Arg{Pos: p.tok.Pos}

		// A named binding is an identifier followed by := or =>.  Anything
		// else is positional, so parse the expression and look afterwards.
		if p.at(TokIdent) {
			name := p.tok.Text
			save := *p.sc
			saveTok := p.tok
			p.next()
			switch {
			case p.accept(Assign):
				a.Form, a.Name = ArgInput, name
				a.Value = p.parseExpr()
				args = append(args, a)
				if !p.accept(Comma) {
					return args
				}
				continue
			case p.accept(OutAssign):
				a.Form, a.Name = ArgOutput, name
				a.Value = p.parseExpr()
				args = append(args, a)
				if !p.accept(Comma) {
					return args
				}
				continue
			}
			// Not a named binding: rewind and take it as an expression.
			*p.sc = save
			p.tok = saveTok
		}

		a.Form = ArgPositional
		a.Value = p.parseExpr()
		if a.Value == nil {
			return args
		}
		args = append(args, a)
		if !p.accept(Comma) {
			return args
		}
	}
}

// tryStructLit parses `(name := value, ...)` if that is what follows, and
// returns nil having consumed nothing otherwise.
func (p *parser) tryStructLit() Expr {
	cp := p.mark()
	pos := p.tok.Pos
	p.next() // '('

	// An array of structured values: ( (a := 1), (a := 2) ).
	if p.at(LParen) {
		lit := &StructLit{Pos: pos}
		for {
			elem := p.tryStructLit()
			if elem == nil {
				p.reset(cp)
				return nil
			}
			lit.Elems = append(lit.Elems, elem.(*StructLit))
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(RParen)
		return lit
	}

	if !p.at(TokIdent) {
		p.reset(cp)
		return nil
	}
	save := p.mark()
	p.next()
	if !p.at(Assign) {
		p.reset(cp)
		return nil
	}
	p.reset(save)

	lit := &StructLit{Pos: pos}
	for {
		f := FieldInit{Pos: p.tok.Pos}
		f.Name = p.expectName("a field name")
		if f.Name == "" {
			p.reset(cp)
			return nil
		}
		p.expect(Assign)
		f.Value = p.parseExpr()
		lit.Fields = append(lit.Fields, f)
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	return lit
}

func (p *parser) parsePrimary() Expr {
	tok := p.tok
	switch tok.Kind {
	case TokIdent:
		p.next()
		return &Ident{Pos: tok.Pos, Name: tok.Text}

	case TokIntLit:
		p.next()
		return &IntLit{Pos: tok.Pos, Text: tok.Text, Value: tok.Int, Base: tok.Base}

	case TokRealLit:
		p.next()
		return &RealLit{Pos: tok.Pos, Text: tok.Text, Value: tok.Real}

	case TokTypedLit:
		p.next()
		return &TypedLit{Pos: tok.Pos, Prefix: tok.Prefix, Body: tok.Str}

	case TokStringLit:
		p.next()
		return &StringLit{Pos: tok.Pos, Value: tok.Str}

	case TokWStringLit:
		p.next()
		return &StringLit{Pos: tok.Pos, Value: tok.Str, Wide: true}

	case KwTrue:
		p.next()
		return &BoolLit{Pos: tok.Pos, Value: true}

	case KwFalse:
		p.next()
		return &BoolLit{Pos: tok.Pos, Value: false}

	case LParen:
		// `(name := value, ...)` is a structured initialiser rather than a
		// parenthesised expression.  One token past the name settles it.
		if lit := p.tryStructLit(); lit != nil {
			return lit
		}
		p.next()
		e := p.parseExpr()
		p.expect(RParen)
		return e
	}

	p.errorAt(tok.Pos, "expected an expression, found %s", p.describe())
	return nil
}
