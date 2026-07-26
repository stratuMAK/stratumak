// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package check performs semantic validation of a parsed GMI API, focused on
// the inline @constraints declared on fields and parameters. It runs after
// parsing and before code generation so that mistyped or contradictory
// constraints fail the build instead of emitting dead or broken checks.
package check

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
)

// Validate returns every semantic error found in the API. It does not stop at
// the first — a compiler should report all mistakes in one run.
func Validate(api *ast.API) []error {
	c := &checker{api: api}
	c.checkDuplicateNames()
	c.checkTypeExistence()
	for _, t := range api.Types {
		for _, f := range t.Fields {
			site := fmt.Sprintf("%s: type %s.%s", f.Pos, t.Name, f.Name)
			c.checkSite(site, f.Type, f.Constraints)
		}
	}
	for _, fn := range api.Funcs {
		for _, p := range fn.Params {
			site := fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name)
			if len(p.Constraints) > 0 {
				// Constraints only make sense on marshaled inputs.
				if p.IsOut {
					c.errf(site, "constraints on an out (output) parameter are meaningless")
				} else if p.IsPtr {
					c.errf(site, "constraints on a ptr (opaque, unmarshaled) parameter are not supported")
				}
			}
			c.checkSite(site, p.Type, p.Constraints)
		}
	}
	c.check64BitParamPositions()
	c.checkRcError()
	c.checkNullableElements()
	c.checkMaps()
	return c.errs
}

// checkMaps confines map[string]T to the one position the emitters support:
// the full return type of a watch-only func (@watch with no @method). A watch
// frame is Go-marshaled JSON end to end, so a map is natural there — it is
// what makes per-key delta encoding (@watch_delta) work. Everywhere else a
// value must cross the C ABI (callbacks vtable, REST dispatch structs), where
// a map has no representation, so any other placement must fail the build
// rather than generate a vtable the C side cannot express. This is also why a
// map-returning watch func is emitted for Go providers only (the C-provider
// path skips it, with a comment in the generated header).
func (c *checker) checkMaps() {
	const onlyWatch = "map[string]T is JSON-only (no C ABI); it is supported only as the " +
		"full return type of a watch-only func (@watch true, no @method)"
	var containsMap func(t ast.TypeRef) bool
	containsMap = func(t ast.TypeRef) bool {
		if t.Kind == ast.TypeMap {
			return true
		}
		if t.Elem != nil {
			return containsMap(*t.Elem)
		}
		return false
	}
	for _, t := range c.api.Types {
		for _, f := range t.Fields {
			if containsMap(f.Type) {
				c.errf(fmt.Sprintf("%s: type %s.%s", f.Pos, t.Name, f.Name), onlyWatch)
			}
		}
	}
	for _, fn := range c.api.Funcs {
		site := fmt.Sprintf("%s: func %s", fn.Pos, fn.Name)
		if fn.WatchDelta && !fn.Watch {
			c.errf(site, "@watch_delta is only meaningful on a @watch func")
		}
		if fn.WatchDelta && fn.Watch && fn.Return != nil &&
			fn.Return.Kind == ast.TypeSlice && fn.Return.Elem != nil &&
			fn.Return.Elem.Kind == ast.TypePrimitive && fn.Return.Elem.Name == ast.PrimU8 {
			// A []u8 watch is pushed as raw binary frames; there are no JSON
			// keys to diff, so a declared delta would be silently ignored.
			c.errf(site, "@watch_delta cannot apply to a binary ([]u8) watch")
		}
		for _, p := range fn.Params {
			if containsMap(p.Type) {
				c.errf(fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name), onlyWatch)
			}
		}
		if fn.Return == nil {
			continue
		}
		if fn.Return.Kind == ast.TypeMap {
			if !(fn.Watch && fn.Method == "") {
				c.errf(site+" return", onlyWatch)
			}
			if fn.Return.Elem != nil && containsMap(*fn.Return.Elem) {
				c.errf(site+" return", "nested maps are not supported")
			}
		} else if containsMap(*fn.Return) {
			c.errf(site+" return", onlyWatch)
		}
	}
	for _, cb := range c.api.Callbacks {
		site := fmt.Sprintf("%s: callback %s", cb.Pos, cb.Name)
		for _, p := range cb.Params {
			if containsMap(p.Type) {
				c.errf(site, onlyWatch)
			}
		}
		if cb.Return != nil && containsMap(*cb.Return) {
			c.errf(site, onlyWatch)
		}
	}
	for _, ss := range c.api.StreamServers {
		for _, fn := range ss.Funcs {
			site := fmt.Sprintf("%s: stream_server %s.%s", fn.Pos, ss.Name, fn.Name)
			for _, p := range fn.Params {
				if containsMap(p.Type) {
					c.errf(site, onlyWatch)
				}
			}
			if fn.Return != nil && containsMap(*fn.Return) {
				c.errf(site, onlyWatch)
			}
		}
	}
}

// checkNullableElements rejects `[]T?` and `[N]T?`.
//
// The grammar binds the `?` to the element, so `[]string?` declares a slice
// whose *elements* may be null — which no IDL here has ever meant. All three
// former uses meant "the slice itself is optional", and that needs no marker:
// a nil slice already marshals to JSON null and reaches C as a NULL data
// pointer with zero length.
//
// This went unnoticed while a nullable string was silently demoted to a plain
// string, so `[]string?` and `[]string` generated identical code. Once
// nullability became real the two diverged into `[]*string`, and a per-element
// pointer is both unmarshalable from the existing wire format and meaningless
// to every consumer. Rejecting it keeps the ambiguity from being decided by
// accident a second time.
func (c *checker) checkNullableElements() {
	var walk func(site string, t ast.TypeRef)
	walk = func(site string, t ast.TypeRef) {
		switch t.Kind {
		case ast.TypeSlice, ast.TypeArray, ast.TypeMap:
			if t.Elem == nil {
				return
			}
			if t.Elem.Nullable {
				// For a map the same reasoning holds one step earlier: a
				// missing key already expresses absence.
				c.errf(site, "nullable element type %q inside a collection: the `?` binds to the "+
					"element, not the collection. A nil/empty collection already expresses "+
					"absence — drop the `?`", t.Elem.Name)
				return
			}
			walk(site, *t.Elem)
		}
	}
	for _, t := range c.api.Types {
		for _, f := range t.Fields {
			walk(fmt.Sprintf("%s: type %s.%s", f.Pos, t.Name, f.Name), f.Type)
		}
	}
	for _, fn := range c.api.Funcs {
		for _, p := range fn.Params {
			walk(fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name), p.Type)
		}
		if fn.Return != nil {
			walk(fmt.Sprintf("%s: func %s return", fn.Pos, fn.Name), *fn.Return)
		}
	}
}

// checkRcError validates the @rc_error shape. It is the contract that lets a
// data-returning call report a failure — the i32 return carries the status and
// the out parameter carries the payload — so a declaration that cannot express
// it (no i32 return, no out param, or an rc that is also claimed to be a value)
// has to fail the build rather than generate a bridge whose two sides disagree
// about which value is the answer.
func (c *checker) checkRcError() {
	for _, fn := range c.api.Funcs {
		if !fn.RcError {
			continue
		}
		site := fmt.Sprintf("%s: func %s", fn.Pos, fn.Name)
		if fn.ReturnsValue {
			c.errf(site, "@rc_error and @returns_value are mutually exclusive: the i32 return is either a status or a value")
		}
		if fn.Return == nil || fn.Return.Kind != ast.TypePrimitive || fn.Return.Name != ast.PrimI32 {
			c.errf(site, "@rc_error requires an i32 return (the status channel)")
		}
		if len(fn.RcErrorOuts()) == 0 {
			c.errf(site, "@rc_error requires at least one out parameter (the payload)")
		}
		if fn.Path != "" && len(fn.RcErrorOuts()) > 1 {
			c.errf(site, "a REST-routed @rc_error func must have exactly one out parameter (the response body); it has %d", len(fn.RcErrorOuts()))
		}
	}
	c.checkSliceOutParams()
}

// checkSliceOutParams rejects the slice-out shapes the emitters cannot express.
// A slice out parameter exists to carry an @rc_error payload — the callee
// allocates it, so it needs the owning {data, len} struct — and the generated
// converters name their locals for a single such payload per function.
func (c *checker) checkSliceOutParams() {
	for _, fn := range c.api.Funcs {
		n := 0
		for _, p := range fn.Params {
			if !p.IsOut || p.Type.Kind != ast.TypeSlice {
				continue
			}
			n++
			site := fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name)
			if !fn.RcError {
				c.errf(site, "a slice out parameter is only supported on an @rc_error func (it carries the payload)")
			}
			if n > 1 {
				c.errf(site, "only one slice out parameter per func is supported")
			}
		}
	}
}

// check64BitParamPositions rejects a 64-bit (i64/u64) parameter in a REST path
// or query position. Such a value reaches the server via apiserver.encodeParams
// as a bare JSON number, which silently truncates above 2^53 in a JavaScript
// client (and a ",string" server field would reject it). 64-bit params are only
// supported in a request body (POST/PUT/PATCH), where they are string-encoded
// end to end. Functions with no @path are not REST routes (internal/cgo only)
// and are unaffected.
func (c *checker) check64BitParamPositions() {
	for _, fn := range c.api.Funcs {
		if fn.Path == "" {
			continue // not a REST route
		}
		pathParams := pathParamNames(fn.Path)
		isQueryMethod := fn.Method == "" || fn.Method == "GET" || fn.Method == "DELETE"
		for _, p := range fn.Params {
			if !p.Type.Is64BitInt() {
				continue
			}
			if p.IsOut {
				continue // reply, not request: never a path or query parameter
			}
			site := fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name)
			switch {
			case pathParams[p.Name]:
				c.errf(site, "64-bit int (%s) cannot be a REST path parameter: it truncates above 2^53 in a JavaScript client; pass it in the request body (POST/PUT/PATCH)", p.Type.Name)
			case isQueryMethod:
				c.errf(site, "64-bit int (%s) cannot be a query parameter of a %s endpoint: it truncates above 2^53 in a JavaScript client; use a request body (POST/PUT/PATCH)", p.Type.Name, restMethod(fn.Method))
			}
		}
	}
}

// pathParamNames returns the set of {name} placeholders in a REST path pattern.
func pathParamNames(path string) map[string]bool {
	out := map[string]bool{}
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			return out
		}
		out[path[i+1:i+j]] = true
		path = path[i+j+1:]
	}
}

// restMethod spells the effective HTTP method (empty defaults to GET).
func restMethod(m string) string {
	if m == "" {
		return "GET"
	}
	return m
}

type checker struct {
	api  *ast.API
	errs []error
}

func (c *checker) errf(site, format string, a ...interface{}) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s", site, fmt.Sprintf(format, a...)))
}

// checkDuplicateNames rejects name collisions that would otherwise emit
// duplicate C/Go declarations (an uncompilable enum { A, A }, two struct members
// or params named the same, a re-declared type) — caught here as a source-level
// diagnostic instead of a confusing compiler error in generated output.
//
// types, enums, callbacks and imports share ONE namespace: a TypeNamed
// reference resolves to exactly one of them (see checkTypeExistence), so a name
// may name only one. funcs and stream servers each have their own namespace.
// Field names are unique within a struct, param names within a callable, and
// member names within an enum. Duplicate enum *values* are intentionally allowed
// (aliases — see ast.DistinctMembers); only duplicate member names are rejected.
func (c *checker) checkDuplicateNames() {
	// Shared type namespace.
	type decl struct {
		kind string
		pos  ast.Pos
	}
	typeNS := map[string]decl{}
	noteType := func(name, kind string, pos ast.Pos) {
		if prev, ok := typeNS[name]; ok {
			c.errf(pos.String(), "duplicate name %q (already declared as %s at %s)", name, prev.kind, prev.pos)
			return
		}
		typeNS[name] = decl{kind, pos}
	}
	for _, t := range c.api.Types {
		noteType(t.Name, "type", t.Pos)
	}
	for _, e := range c.api.Enums {
		noteType(e.Name, "enum", e.Pos)
	}
	for _, cb := range c.api.Callbacks {
		noteType(cb.Name, "callback", cb.Pos)
	}
	for _, im := range c.api.Imports {
		noteType(im.Name, "import", im.Pos)
	}

	// funcs and stream servers: one namespace each.
	c.checkUnique("func", funcDecls(c.api.Funcs))
	c.checkUnique("stream_server", streamServerDecls(c.api.StreamServers))

	// Fields within each struct.
	for _, t := range c.api.Types {
		c.checkUnique(fmt.Sprintf("field of type %s", t.Name), fieldDecls(t.Fields))
	}
	// Params within each callable, member names within each enum.
	for _, fn := range c.api.Funcs {
		c.checkUnique(fmt.Sprintf("param of func %s", fn.Name), paramDecls(fn.Params))
	}
	for _, cb := range c.api.Callbacks {
		c.checkUnique(fmt.Sprintf("param of callback %s", cb.Name), paramDecls(cb.Params))
	}
	for _, ss := range c.api.StreamServers {
		for _, fn := range ss.Funcs {
			c.checkUnique(fmt.Sprintf("param of stream_server %s.%s", ss.Name, fn.Name), paramDecls(fn.Params))
		}
	}
	for _, e := range c.api.Enums {
		c.checkUnique(fmt.Sprintf("member of enum %s", e.Name), enumMemberDecls(e.Values))
	}
}

// namedDecl is one declaration's name and position, for uniqueness checking.
type namedDecl struct {
	name string
	pos  ast.Pos
}

// checkUnique reports every duplicate name within one scope. kindLabel names the
// scope+kind for the diagnostic (e.g. "field of type Rec").
func (c *checker) checkUnique(kindLabel string, decls []namedDecl) {
	seen := make(map[string]ast.Pos, len(decls))
	for _, d := range decls {
		if prev, ok := seen[d.name]; ok {
			c.errf(d.pos.String(), "duplicate %s %q (first declared at %s)", kindLabel, d.name, prev)
			continue
		}
		seen[d.name] = d.pos
	}
}

func funcDecls(fns []ast.Func) []namedDecl {
	out := make([]namedDecl, len(fns))
	for i, fn := range fns {
		out[i] = namedDecl{fn.Name, fn.Pos}
	}
	return out
}

func streamServerDecls(ss []ast.StreamServer) []namedDecl {
	out := make([]namedDecl, len(ss))
	for i, s := range ss {
		out[i] = namedDecl{s.Name, s.Pos}
	}
	return out
}

func fieldDecls(fs []ast.Field) []namedDecl {
	out := make([]namedDecl, len(fs))
	for i, f := range fs {
		out[i] = namedDecl{f.Name, f.Pos}
	}
	return out
}

func paramDecls(ps []ast.Param) []namedDecl {
	out := make([]namedDecl, len(ps))
	for i, p := range ps {
		out[i] = namedDecl{p.Name, p.Pos}
	}
	return out
}

func enumMemberDecls(vs []ast.EnumValue) []namedDecl {
	out := make([]namedDecl, len(vs))
	for i, v := range vs {
		out[i] = namedDecl{v.Name, v.Pos}
	}
	return out
}

// checkTypeExistence verifies every named type reference resolves to something
// declared in the API. An unresolved reference otherwise flows to the emitter
// and produces uncompilable generated code (e.g. a Go/C reference to a type that
// does not exist); catching it here yields a source-level `file:line` diagnostic
// instead of a confusing compiler error in generated output.
//
// A TypeNamed is valid iff it names a declared type, enum, callback, or import.
// Callbacks and imports are included because the parser classifies a *forward*
// reference to one as TypeNamed (its callbacks/imports sets are populated in
// declaration order); resolving against the fully-parsed API here avoids a false
// "unknown type" on a legal forward reference. Primitives, and back-referenced
// callbacks/imports, already carry their own TypeKind and need no lookup.
func (c *checker) checkTypeExistence() {
	known := c.knownTypeNames()
	for _, t := range c.api.Types {
		for _, f := range t.Fields {
			c.checkTypeRef(fmt.Sprintf("%s: type %s.%s", f.Pos, t.Name, f.Name), f.Type, known)
		}
	}
	for _, fn := range c.api.Funcs {
		for _, p := range fn.Params {
			c.checkTypeRef(fmt.Sprintf("%s: func %s param %s", p.Pos, fn.Name, p.Name), p.Type, known)
		}
		if fn.Return != nil {
			c.checkTypeRef(fmt.Sprintf("%s: func %s return", fn.Pos, fn.Name), *fn.Return, known)
		}
	}
	for _, cb := range c.api.Callbacks {
		for _, p := range cb.Params {
			c.checkTypeRef(fmt.Sprintf("%s: callback %s param %s", p.Pos, cb.Name, p.Name), p.Type, known)
		}
		if cb.Return != nil {
			c.checkTypeRef(fmt.Sprintf("%s: callback %s return", cb.Pos, cb.Name), *cb.Return, known)
		}
	}
	for _, ss := range c.api.StreamServers {
		for _, fn := range ss.Funcs {
			for _, p := range fn.Params {
				c.checkTypeRef(fmt.Sprintf("%s: stream_server %s.%s param %s", p.Pos, ss.Name, fn.Name, p.Name), p.Type, known)
			}
			if fn.Return != nil {
				c.checkTypeRef(fmt.Sprintf("%s: stream_server %s.%s return", fn.Pos, ss.Name, fn.Name), *fn.Return, known)
			}
		}
	}
}

// knownTypeNames is the set of every name a TypeNamed reference may resolve to.
func (c *checker) knownTypeNames() map[string]bool {
	known := make(map[string]bool, len(c.api.Types)+len(c.api.Enums)+len(c.api.Callbacks)+len(c.api.Imports))
	for _, t := range c.api.Types {
		known[t.Name] = true
	}
	for _, e := range c.api.Enums {
		known[e.Name] = true
	}
	for _, cb := range c.api.Callbacks {
		known[cb.Name] = true
	}
	for _, im := range c.api.Imports {
		known[im.Name] = true
	}
	return known
}

// checkTypeRef recurses into array/slice element types and flags an unresolved
// named type.
func (c *checker) checkTypeRef(site string, t ast.TypeRef, known map[string]bool) {
	switch t.Kind {
	case ast.TypeArray, ast.TypeSlice, ast.TypeMap:
		if t.Elem != nil {
			c.checkTypeRef(site, *t.Elem, known)
		}
	case ast.TypeNamed:
		if !known[t.Name] {
			c.errf(site, "unknown type %q (no matching type, enum, callback, or import)", t.Name)
		}
	}
}

// checkSite validates all constraints attached to one field or parameter.
func (c *checker) checkSite(site string, t ast.TypeRef, cs []ast.Constraint) {
	seen := map[ast.ConstraintKind]bool{}
	var minRaw, maxRaw, minLenRaw, maxLenRaw string

	for _, con := range cs {
		name := ast.ConstraintName(con.Kind)
		if seen[con.Kind] {
			c.errf(site, "duplicate constraint @%s", name)
			continue
		}
		seen[con.Kind] = true

		switch con.Kind {
		case ast.ConstraintMin, ast.ConstraintMax:
			if !isNumeric(t) {
				c.errf(site, "@%s applies to numeric types, not %s", name, t.String())
				break
			}
			if !numericFits(t, con.Num) {
				c.errf(site, "@%s value %s out of range for %s", name, con.Num, t.String())
			}
			if isUnsigned(t) && strings.HasPrefix(con.Num, "-") {
				c.errf(site, "@%s(%s) is negative on unsigned type %s", name, con.Num, t.String())
			}
			if con.Kind == ast.ConstraintMin {
				minRaw = con.Num
			} else {
				maxRaw = con.Num
			}

		case ast.ConstraintMinLen, ast.ConstraintMaxLen:
			if !isCollection(t) {
				c.errf(site, "@%s applies to string/slice/array, not %s", name, t.String())
				break
			}
			n, err := strconv.Atoi(con.Num)
			if err != nil || n < 0 {
				c.errf(site, "@%s must be a non-negative integer, got %s", name, con.Num)
				break
			}
			if t.Kind == ast.TypeArray && n > t.ArrayLen {
				c.errf(site, "@%s(%s) exceeds fixed array length %d", name, con.Num, t.ArrayLen)
			}
			if con.Kind == ast.ConstraintMinLen {
				minLenRaw = con.Num
			} else {
				maxLenRaw = con.Num
			}

		case ast.ConstraintNotEmpty:
			if !isCollection(t) {
				c.errf(site, "@notempty applies to string/slice/array, not %s", t.String())
			}

		case ast.ConstraintRegex:
			if !isString(t) {
				c.errf(site, "@regex applies to string, not %s", t.String())
			}
			// Compiled with Go's regexp precisely because @regex is Go-server-only.
			if _, err := regexp.Compile(con.Str); err != nil {
				c.errf(site, "@regex pattern does not compile: %v", err)
			}

		case ast.ConstraintNotNull:
			// Nullable strings used to be excluded here, because `string?`
			// mapped to a non-pointer Go string and there was nothing for
			// @notnull to test. Now that it is a real *string, "must be
			// supplied" is expressible for a string like any other type — and
			// it is a different requirement from @notempty, which says a
			// supplied value must be non-empty.
			if !t.Nullable {
				c.errf(site, "@notnull is redundant on non-nullable type %s", t.String())
			}

		case ast.ConstraintEnumOpen:
			if _, ok := enumFor(c.api, t); !ok {
				c.errf(site, "@enum_open applies to enum types, not %s", t.String())
			}
		}
	}

	// Cross-constraint sanity.
	if minRaw != "" && maxRaw != "" {
		if lo, hi, ok := asFloatPair(minRaw, maxRaw); ok && lo > hi {
			c.errf(site, "@min(%s) > @max(%s)", minRaw, maxRaw)
		}
	}
	if minLenRaw != "" && maxLenRaw != "" {
		if lo, hi, ok := asFloatPair(minLenRaw, maxLenRaw); ok && lo > hi {
			c.errf(site, "@minlen(%s) > @maxlen(%s)", minLenRaw, maxLenRaw)
		}
	}

	// @min/@max on an enum duplicates the automatic membership check.
	if _, isEnum := enumFor(c.api, t); isEnum && (seen[ast.ConstraintMin] || seen[ast.ConstraintMax]) {
		c.errf(site, "@min/@max on enum %s is redundant with automatic enum validation", t.Name)
	}
}

// --- type predicates ---
//
// These read t.Kind/t.Name directly; the Nullable flag is orthogonal, so an
// i32? is still numeric.

func isNumeric(t ast.TypeRef) bool {
	if t.Kind != ast.TypePrimitive {
		return false
	}
	switch t.Name {
	case ast.PrimI8, ast.PrimU8, ast.PrimI16, ast.PrimU16, ast.PrimI32, ast.PrimU32,
		ast.PrimI64, ast.PrimU64, ast.PrimF32, ast.PrimF64:
		return true
	}
	return false
}

func isUnsigned(t ast.TypeRef) bool {
	if t.Kind != ast.TypePrimitive {
		return false
	}
	switch t.Name {
	case ast.PrimU8, ast.PrimU16, ast.PrimU32, ast.PrimU64:
		return true
	}
	return false
}

func isString(t ast.TypeRef) bool {
	return t.Kind == ast.TypePrimitive && t.Name == ast.PrimString
}

func isCollection(t ast.TypeRef) bool {
	return isString(t) || t.Kind == ast.TypeSlice || t.Kind == ast.TypeArray
}

// numericFits reports whether raw parses within the field's width/signedness,
// catching e.g. @min(0.5) on i32 or @max(9999999999) on i32.
func numericFits(t ast.TypeRef, raw string) bool {
	switch t.Name {
	case ast.PrimF32:
		_, err := strconv.ParseFloat(raw, 32)
		return err == nil
	case ast.PrimF64:
		_, err := strconv.ParseFloat(raw, 64)
		return err == nil
	case ast.PrimI8, ast.PrimI16, ast.PrimI32, ast.PrimI64:
		_, err := strconv.ParseInt(raw, 10, intBits(t.Name))
		return err == nil
	case ast.PrimU8, ast.PrimU16, ast.PrimU32, ast.PrimU64:
		_, err := strconv.ParseUint(raw, 10, intBits(t.Name))
		return err == nil
	}
	return false
}

func intBits(name string) int {
	switch name {
	case ast.PrimI8, ast.PrimU8:
		return 8
	case ast.PrimI16, ast.PrimU16:
		return 16
	case ast.PrimI32, ast.PrimU32:
		return 32
	default:
		return 64
	}
}

// asFloatPair parses two numeric literals for ordering comparison.
func asFloatPair(a, b string) (float64, float64, bool) {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return x, y, true
}

// enumFor returns the enum a named type refers to, if any.
func enumFor(api *ast.API, t ast.TypeRef) (*ast.Enum, bool) {
	return api.EnumByName(t)
}
