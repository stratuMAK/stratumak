// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package parser

import (
	"fmt"
	"strconv"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// Parser parses GMI source into an AST.
type Parser struct {
	scanner   *Scanner
	cur       Token
	file      string
	errors    []string
	consts    map[string]int  // named constants for array size resolution
	callbacks map[string]bool // declared callback names for type resolution
	imports   map[string]bool // imported API names for type resolution
}

// Parse parses a GMI file and returns the AST and any errors.
func Parse(filename, src string) (*ast.API, []string) {
	p := &Parser{
		scanner:   NewScanner(src),
		file:      filename,
		consts:    make(map[string]int),
		callbacks: make(map[string]bool),
		imports:   make(map[string]bool),
	}
	p.advance()
	api := p.parseAPI()
	p.reclassifyForwardRefs(api)
	// Merge lexical errors (all tokens have been scanned by now, since the
	// parser pulls them eagerly through parseAPI), prefixed with the file name.
	for _, e := range p.scanner.errs {
		p.errors = append(p.errors, p.file+":"+e)
	}
	return api, p.errors
}

// reclassifyForwardRefs fixes up TypeNamed references that actually name a
// callback or import declared LATER in the file. parseTypeRef classifies a name
// against the callbacks/imports sets known at the point of use (single pass), so
// a forward reference lands as TypeNamed; now that the whole file is parsed,
// re-resolve against the complete sets so the emitter — which switches on
// TypeKind (callback → function pointer, named → struct) — sees the correct kind
// regardless of declaration order. A no-op when everything is declared before
// use (the usual style).
func (p *Parser) reclassifyForwardRefs(api *ast.API) {
	for i := range api.Types {
		for j := range api.Types[i].Fields {
			p.reclassifyRef(&api.Types[i].Fields[j].Type)
		}
	}
	for i := range api.Funcs {
		for j := range api.Funcs[i].Params {
			p.reclassifyRef(&api.Funcs[i].Params[j].Type)
		}
		p.reclassifyRef(api.Funcs[i].Return)
	}
	for i := range api.Callbacks {
		for j := range api.Callbacks[i].Params {
			p.reclassifyRef(&api.Callbacks[i].Params[j].Type)
		}
		p.reclassifyRef(api.Callbacks[i].Return)
	}
	for i := range api.StreamServers {
		for j := range api.StreamServers[i].Funcs {
			for k := range api.StreamServers[i].Funcs[j].Params {
				p.reclassifyRef(&api.StreamServers[i].Funcs[j].Params[k].Type)
			}
			p.reclassifyRef(api.StreamServers[i].Funcs[j].Return)
		}
	}
}

// reclassifyRef re-resolves one type reference (recursing into array/slice
// element types). Nil-safe so callers can pass an optional *TypeRef return type.
func (p *Parser) reclassifyRef(t *ast.TypeRef) {
	if t == nil {
		return
	}
	switch t.Kind {
	case ast.TypeArray, ast.TypeSlice:
		p.reclassifyRef(t.Elem)
	case ast.TypeNamed:
		if p.callbacks[t.Name] {
			t.Kind = ast.TypeCallback
		} else if p.imports[t.Name] {
			t.Kind = ast.TypeImport
		}
	}
}

func (p *Parser) advance() {
	p.cur = p.scanner.Scan()
}

func (p *Parser) pos() ast.Pos {
	return ast.Pos{File: p.file, Line: p.cur.Line, Col: p.cur.Col}
}

func (p *Parser) errorf(format string, args ...interface{}) {
	msg := fmt.Sprintf("%s:%d:%d: %s", p.file, p.cur.Line, p.cur.Col, fmt.Sprintf(format, args...))
	p.errors = append(p.errors, msg)
}

func (p *Parser) expect(t TokenType) bool {
	if p.cur.Type != t {
		p.errorf("expected %v, got %q", t, p.cur.Text)
		return false
	}
	p.advance()
	return true
}

func (p *Parser) parseAPI() *ast.API {
	api := &ast.API{}

	// Pending function-level annotations collected before a func declaration
	var pendingAnns []annotation

	for p.cur.Type != EOF {
		switch {
		case p.cur.Type == AT:
			ann := p.parseAnnotation()
			if isAPIDirective(ann.name) {
				p.applyAPIDirective(api, ann)
			} else {
				pendingAnns = append(pendingAnns, ann)
			}
		case p.cur.Type == ENUM:
			if len(pendingAnns) > 0 {
				p.errorf("annotations before enum are not supported")
				pendingAnns = nil
			}
			api.Enums = append(api.Enums, p.parseEnum())
		case p.cur.Type == CONST:
			if len(pendingAnns) > 0 {
				p.errorf("annotations before const are not supported")
				pendingAnns = nil
			}
			api.Consts = append(api.Consts, p.parseConst())
		case p.cur.Type == TYPE:
			if len(pendingAnns) > 0 {
				p.errorf("annotations before type are not supported")
				pendingAnns = nil
			}
			api.Types = append(api.Types, p.parseType())
		case p.cur.Type == CALLBACK:
			api.Callbacks = append(api.Callbacks, p.parseCallback(pendingAnns))
			pendingAnns = nil
		case p.cur.Type == STREAM_SERVER:
			if len(pendingAnns) > 0 {
				p.errorf("annotations before stream_server are not supported")
				pendingAnns = nil
			}
			api.StreamServers = append(api.StreamServers, p.parseStreamServer())
		case p.cur.Type == FUNC:
			fn := p.parseFunc(pendingAnns)
			pendingAnns = nil
			api.Funcs = append(api.Funcs, fn)
		default:
			p.errorf("unexpected token %q", p.cur.Text)
			p.advance()
		}
	}

	if len(pendingAnns) > 0 {
		p.errorf("trailing annotations without func declaration")
	}

	return api
}

// annotation is a parsed @name value pair.
type annotation struct {
	name  string
	value string
	pos   ast.Pos
}

// isAPIDirective returns true for top-level API directives.
func isAPIDirective(name string) bool {
	switch name {
	case "api", "version", "prefix", "rest_export", "import", "author", "license":
		return true
	}
	return false
}

// parseAnnotation parses @ name value and returns the pair.
func (p *Parser) parseAnnotation() annotation {
	p.advance() // skip @
	pos := p.pos()
	name := p.cur.Text
	nameLine := p.cur.Line
	p.advance()
	// Collect value tokens on the same line as the annotation name.
	// This handles compound values like "100ms" (tokenized as "100" + "ms").
	var parts []string
	for p.cur.Type != EOF && p.cur.Line == nameLine &&
		p.cur.Type != AT && p.cur.Type != FUNC &&
		p.cur.Type != TYPE && p.cur.Type != ENUM && p.cur.Type != CONST {
		parts = append(parts, p.cur.Text)
		p.advance()
	}
	value := ""
	for _, part := range parts {
		value += part
	}
	return annotation{name: name, value: value, pos: pos}
}

func (p *Parser) parseConst() ast.Const {
	pos := p.pos()
	p.advance() // skip "const"
	name := p.cur.Text
	p.advance()
	p.expect(EQ)
	val, err := strconv.Atoi(p.cur.Text)
	if err != nil {
		p.errorf("const value must be integer, got %q", p.cur.Text)
	}
	p.advance()
	if _, dup := p.consts[name]; dup {
		// A silent overwrite would make array sizes / @min/@max resolve to
		// whichever const came last, and both copies emit into the C header.
		p.errorf("duplicate const %q", name)
	}
	p.consts[name] = val
	return ast.Const{Name: name, Value: val, Pos: pos}
}

func (p *Parser) applyAPIDirective(api *ast.API, ann annotation) {
	switch ann.name {
	case "api":
		api.Name = ann.value
		api.Pos = ann.pos
	case "version":
		if v, err := strconv.Atoi(ann.value); err == nil {
			api.Version = v
		}
	case "prefix":
		api.Prefix = ann.value
	case "rest_export":
		api.RestExport = ann.value == "true"
	case "import":
		p.imports[ann.value] = true
		api.Imports = append(api.Imports, ast.Import{Name: ann.value, Pos: ann.pos})
	case "author":
		api.Authors = append(api.Authors, ann.value)
	case "license":
		api.License = ann.value
	}
}

func (p *Parser) parseEnum() ast.Enum {
	pos := p.pos()
	p.advance() // skip "enum"
	name := p.cur.Text
	p.advance()

	enum := ast.Enum{Name: name, Pos: pos}
	p.expect(LBRACE)

	for p.cur.Type != RBRACE && p.cur.Type != EOF {
		vpos := p.pos()
		vname := p.cur.Text
		p.advance()
		p.expect(EQ)
		val, err := strconv.Atoi(p.cur.Text)
		if err != nil {
			// Fail loud, matching parseConst: a discarded error here would
			// silently give the member value 0 and emit a wrong enum constant.
			p.errorf("enum %s: value for %q must be an integer, got %q", name, vname, p.cur.Text)
		}
		p.advance()
		enum.Values = append(enum.Values, ast.EnumValue{Name: vname, Value: val, Pos: vpos})
	}
	p.expect(RBRACE)
	return enum
}

func (p *Parser) parseType() ast.Type {
	pos := p.pos()
	p.advance() // skip "type"
	name := p.cur.Text
	p.advance()

	typ := ast.Type{Name: name, Pos: pos}
	p.expect(LBRACE)

	for p.cur.Type != RBRACE && p.cur.Type != EOF {
		fpos := p.pos()
		fname := p.cur.Text
		p.advance()
		p.expect(COLON)
		ftype := p.parseTypeRef()
		constraints := p.parseConstraints()
		typ.Fields = append(typ.Fields, ast.Field{Name: fname, Type: ftype, Constraints: constraints, Pos: fpos})
	}
	p.expect(RBRACE)
	return typ
}

// parseConstraints reads zero or more inline @constraints that follow a field
// or parameter type, e.g.  toolno: i32 @min(1) @max(99999)
func (p *Parser) parseConstraints() []ast.Constraint {
	var cs []ast.Constraint
	for p.cur.Type == AT {
		cpos := p.pos()
		p.advance() // skip @
		name := p.cur.Text
		p.advance()
		c := ast.Constraint{Pos: cpos}
		switch name {
		case "min":
			c.Kind, c.Num = ast.ConstraintMin, p.constraintNum()
		case "max":
			c.Kind, c.Num = ast.ConstraintMax, p.constraintNum()
		case "minlen":
			c.Kind, c.Num = ast.ConstraintMinLen, p.constraintNum()
		case "maxlen":
			c.Kind, c.Num = ast.ConstraintMaxLen, p.constraintNum()
		case "notempty":
			c.Kind = ast.ConstraintNotEmpty
		case "notnull":
			c.Kind = ast.ConstraintNotNull
		case "regex":
			c.Kind, c.Str = ast.ConstraintRegex, p.constraintStr()
		case "enum_open":
			c.Kind = ast.ConstraintEnumOpen
		default:
			p.errorf("unknown constraint @%s", name)
			continue
		}
		cs = append(cs, c)
	}
	return cs
}

// constraintNum parses a parenthesized numeric argument: (INT|FLOAT|const-name).
// A named const (e.g. @max(MAX_SPINDLE_INDEX)) resolves to its integer value so a
// bound literal can be defined once instead of hand-typed at every site.
func (p *Parser) constraintNum() string {
	p.expect(LPAREN)
	var v string
	switch p.cur.Type {
	case INT, FLOAT:
		v = p.cur.Text
	case IDENT:
		val, ok := p.consts[p.cur.Text]
		if !ok {
			p.errorf("undefined constant %q in constraint", p.cur.Text)
		}
		v = strconv.Itoa(val)
	default:
		p.errorf("constraint argument must be numeric or a constant, got %q", p.cur.Text)
	}
	p.advance()
	p.expect(RPAREN)
	return v
}

// constraintStr parses a parenthesized string-literal argument (for @regex).
func (p *Parser) constraintStr() string {
	p.expect(LPAREN)
	if p.cur.Type != STRING {
		p.errorf("constraint argument must be a string literal, got %q", p.cur.Text)
	}
	v := p.cur.Text
	p.advance()
	p.expect(RPAREN)
	return v
}

func (p *Parser) parseCallback(anns []annotation) ast.Callback {
	pos := p.pos()
	p.advance() // skip "callback"
	name := p.cur.Text
	p.advance()

	cb := ast.Callback{Name: name, Pos: pos}
	p.callbacks[name] = true

	// Apply preceding annotations. Only @rt_safe is meaningful on a callback
	// type (it stamps the _cb typedef STMAK_API_NONBLOCKING); any other
	// annotation before a callback stays an error, as it did before.
	for _, ann := range anns {
		switch ann.name {
		case "rt_safe":
			cb.RTSafe = ann.value == "true"
		default:
			p.errorf("annotation @%s before callback is not supported", ann.name)
		}
	}

	// Parameters
	p.expect(LPAREN)
	for p.cur.Type != RPAREN && p.cur.Type != EOF {
		ppos := p.pos()
		pname := p.cur.Text
		p.advance()
		p.expect(COLON)
		ptype := p.parseTypeRef()
		byref := false
		isPtr := false
		isOut := false
		if p.cur.Type == IDENT && p.cur.Text == "byref" {
			byref = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "out" {
			isOut = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "ptr" {
			isPtr = true
			p.advance()
		}
		cb.Params = append(cb.Params, ast.Param{Name: pname, Type: ptype, ByRef: byref, IsOut: isOut, IsPtr: isPtr, Pos: ppos})
		if p.cur.Type == COMMA {
			p.advance()
		}
	}
	p.expect(RPAREN)

	// Return type
	if p.cur.Type == ARROW {
		p.advance()
		ret := p.parseTypeRef()
		cb.Return = &ret
	}

	return cb
}

func (p *Parser) parseStreamServer() ast.StreamServer {
	pos := p.pos()
	p.advance() // skip "stream_server"
	name := p.cur.Text
	p.advance()

	ss := ast.StreamServer{Name: name, Pos: pos}
	p.expect(LBRACE)

	for p.cur.Type != RBRACE && p.cur.Type != EOF {
		sf := p.parseStreamFunc()
		ss.Funcs = append(ss.Funcs, sf)
	}
	p.expect(RBRACE)
	return ss
}

func (p *Parser) parseStreamFunc() ast.StreamFunc {
	pos := p.pos()
	name := p.cur.Text
	p.advance()

	sf := ast.StreamFunc{Name: name, Pos: pos}

	p.expect(LPAREN)
	for p.cur.Type != RPAREN && p.cur.Type != EOF {
		ppos := p.pos()
		pname := p.cur.Text
		p.advance()
		p.expect(COLON)
		ptype := p.parseTypeRef()
		byref := false
		isPtr := false
		isOut := false
		if p.cur.Type == IDENT && p.cur.Text == "byref" {
			byref = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "out" {
			isOut = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "ptr" {
			isPtr = true
			p.advance()
		}
		sf.Params = append(sf.Params, ast.Param{Name: pname, Type: ptype, ByRef: byref, IsOut: isOut, IsPtr: isPtr, Pos: ppos})
		if p.cur.Type == COMMA {
			p.advance()
		}
	}
	p.expect(RPAREN)

	if p.cur.Type == ARROW {
		p.advance()
		ret := p.parseTypeRef()
		sf.Return = &ret
	}

	return sf
}

func (p *Parser) parseFunc(anns []annotation) ast.Func {
	pos := p.pos()
	p.advance() // skip "func"
	name := p.cur.Text
	p.advance()

	fn := ast.Func{Name: name, Pos: pos}

	// Parameters
	p.expect(LPAREN)
	for p.cur.Type != RPAREN && p.cur.Type != EOF {
		ppos := p.pos()
		pname := p.cur.Text
		p.advance()
		p.expect(COLON)
		ptype := p.parseTypeRef()
		byref := false
		isPtr := false
		isOut := false
		if p.cur.Type == IDENT && p.cur.Text == "byref" {
			byref = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "out" {
			isOut = true
			p.advance()
		} else if p.cur.Type == IDENT && p.cur.Text == "ptr" {
			isPtr = true
			p.advance()
		}
		constraints := p.parseConstraints()
		fn.Params = append(fn.Params, ast.Param{Name: pname, Type: ptype, ByRef: byref, IsOut: isOut, IsPtr: isPtr, Constraints: constraints, Pos: ppos})
		if p.cur.Type == COMMA {
			p.advance()
		}
	}
	p.expect(RPAREN)

	// Return type
	if p.cur.Type == ARROW {
		p.advance()
		ret := p.parseTypeRef()
		fn.Return = &ret
	}

	// Apply preceding annotations
	for _, ann := range anns {
		switch ann.name {
		case "method":
			fn.Method = ann.value
		case "path":
			fn.Path = ann.value
		case "rt_safe":
			fn.RTSafe = ann.value == "true"
		case "doc":
			fn.Doc = ann.value
		case "watch":
			fn.Watch = ann.value == "true"
		case "watch_default_rate":
			fn.WatchDefaultRate = ann.value
		case "watch_factory":
			fn.WatchFactory = ann.value == "true"
		case "watch_delta":
			fn.WatchDelta = ann.value == "true"
		case "publish":
			fn.Publish = ann.value == "true"
		case "publish_ring_size":
			if n, err := strconv.Atoi(ann.value); err == nil {
				fn.PublishRingSize = n
			}
		case "watch_source":
			fn.WatchSource = ann.value
		case "returns_value":
			fn.ReturnsValue = true
		case "rc_error":
			fn.RcError = true
		default:
			// Fail loud on a typo'd/unknown annotation instead of silently
			// dropping it (e.g. "@methdo" would otherwise lose the HTTP method
			// with no diagnostic). Mirrors parseConstraints, which already
			// errors on an unknown inline @name.
			p.errorf("unknown annotation @%s on func %s", ann.name, fn.Name)
		}
	}

	return fn
}

func (p *Parser) parseTypeRef() ast.TypeRef {
	// []T slice or [N]T / [NAME]T array
	if p.cur.Type == LBRACKET {
		p.advance()
		if p.cur.Type == RBRACKET {
			// []T — slice
			p.advance()
			elem := p.parseTypeRef()
			return ast.TypeRef{Kind: ast.TypeSlice, Elem: &elem}
		}
		// [N]T or [NAME]T — array
		var size int
		var sizeName string
		if p.cur.Type == INT {
			var err error
			size, err = strconv.Atoi(p.cur.Text)
			if err != nil {
				// A discarded error here would silently yield length 0.
				p.errorf("invalid array size %q: %v", p.cur.Text, err)
			} else if size <= 0 {
				// Zero/negative would emit a broken (or 0-length) array; the
				// only valid fixed sizes are positive.
				p.errorf("array size must be a positive integer, got %d", size)
			}
			p.advance()
		} else if p.cur.Type == IDENT {
			sizeName = p.cur.Text
			var ok bool
			size, ok = p.consts[sizeName]
			if !ok {
				p.errorf("undefined constant %q in array size", sizeName)
			}
			p.advance()
		} else {
			p.errorf("expected integer or constant name for array size, got %q", p.cur.Text)
			p.advance()
		}
		p.expect(RBRACKET)
		elem := p.parseTypeRef()
		return ast.TypeRef{Kind: ast.TypeArray, Elem: &elem, ArrayLen: size, ArrayLenName: sizeName}
	}

	name := p.cur.Text
	p.advance()

	// map[string]T — JSON-object map. The key is locked to string because a
	// JSON object key IS a string; any other key type would be a lie on the
	// wire. The map itself cannot be nullable (absent == empty object).
	if name == "map" && p.cur.Type == LBRACKET {
		p.advance()
		if p.cur.Type != IDENT || p.cur.Text != "string" {
			p.errorf("map key type must be string (JSON object keys are strings), got %q", p.cur.Text)
		}
		p.advance()
		p.expect(RBRACKET)
		elem := p.parseTypeRef()
		return ast.TypeRef{Kind: ast.TypeMap, Elem: &elem}
	}

	nullable := false
	if p.cur.Type == QUESTION {
		nullable = true
		p.advance()
	}

	kind := ast.TypeNamed
	if ast.Primitives[name] {
		kind = ast.TypePrimitive
	} else if p.callbacks[name] {
		kind = ast.TypeCallback
	} else if p.imports[name] {
		kind = ast.TypeImport
	}
	return ast.TypeRef{Kind: kind, Name: name, Nullable: nullable}
}
