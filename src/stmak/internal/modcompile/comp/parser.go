// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Parse parses a .comp file and returns a Package.
func Parse(filename, src string) (*ast.Package, error) {
	header, userCode := splitComp(src)

	sc := NewScanner(filename, header)
	p := &parser{
		sc:   sc,
		file: filename,
		pkg: &ast.Package{
			Component: ast.Component{
				Options: make(map[string]string),
			},
		},
	}
	p.next() // prime the lookahead

	if err := p.parseFile(); err != nil {
		return nil, err
	}

	p.pkg.Component.VerbatimC = userCode

	// Where that verbatim C starts in the .comp, so the backend can emit
	// #line directives and the compiler reports user code against the file
	// the developer edits.  Every splitComp branch returns userCode as a
	// literal suffix of src, so the offset is just the length difference --
	// no branch-by-branch line arithmetic to keep in step with splitComp.
	if userCode != "" {
		p.pkg.Component.VerbatimCPos = ast.Pos{
			File: filename,
			Line: 1 + strings.Count(src[:len(src)-len(userCode)], "\n"),
			Col:  1,
		}
	}

	// Reject unsupported RTAPI_MP_ARRAY_* macros.
	// cmod/gomod allows multiple 'load' commands with different parameters instead.
	for _, macro := range []string{"RTAPI_MP_ARRAY_STRING", "RTAPI_MP_ARRAY_INT"} {
		if strings.Contains(userCode, macro) {
			return nil, fmt.Errorf("%s: '%s' is not supported in cmod/gomod; "+
				"use multiple 'load' commands with different parameters instead", filename, macro)
		}
	}

	return p.pkg, nil
}

// splitComp splits the source at the first "\n;;\n" separator into
// a header part (declarations) and a user-code part (verbatim C).
// If no separator is found, the entire source is the header.
func splitComp(src string) (header, userCode string) {
	const sep = "\n;;\n"
	idx := strings.Index(src, sep)
	if idx < 0 {
		// Try ";;\n" at the very start of the file.
		if strings.HasPrefix(src, ";;\n") {
			return "", src[3:]
		}
		// Try "\n;;" at EOF.
		if strings.HasSuffix(src, "\n;;") {
			return src[:len(src)-3], ""
		}
		// No separator — whole file is header.
		return src, ""
	}
	return src[:idx], src[idx+len(sep):]
}

// ---------------------------------------------------------------------------
// parser
// ---------------------------------------------------------------------------

type parser struct {
	sc   *Scanner
	file string
	cur  Token
	pkg  *ast.Package
}

func (p *parser) next() Token {
	prev := p.cur
	p.cur = p.sc.Next()
	return prev
}

func (p *parser) errorf(format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("%s: %s", p.cur.Pos, msg)
}

// expect consumes the current token if it matches kind, otherwise returns error.
func (p *parser) expect(kind TokenKind) (Token, error) {
	if p.cur.Kind != kind {
		return Token{}, p.errorf("expected %s, got %s (%q)", kind, p.cur.Kind, p.cur.Val)
	}
	return p.next(), nil
}

// expectSemi consumes a semicolon.
func (p *parser) expectSemi() error {
	_, err := p.expect(TokSemi)
	return err
}

// ---------------------------------------------------------------------------
// Top-level parsing
// ---------------------------------------------------------------------------

func (p *parser) parseFile() error {
	if err := p.parseComponentDecl(); err != nil {
		return err
	}
	for p.cur.Kind != TokEOF {
		if err := p.parseDeclaration(); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) parseComponentDecl() error {
	if p.cur.Kind != TokIdent || p.cur.Val != "component" {
		return p.errorf("expected 'component', got %q", p.cur.Val)
	}
	p.pkg.Component.Pos = p.cur.Pos
	p.next() // skip "component"

	name, err := p.expectName()
	if err != nil {
		return err
	}
	p.pkg.Component.Name = name

	p.pkg.Component.Summary = p.parseOptString()

	return p.expectSemi()
}

func (p *parser) parseDeclaration() error {
	if p.cur.Kind != TokIdent {
		return p.errorf("expected declaration keyword, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	switch p.cur.Val {
	case "pin":
		return p.parsePin()
	case "param":
		return p.parseParam()
	case "function":
		return p.parseFunction()
	case "mcode":
		return p.parseMcode()
	case "variable":
		return p.parseVariable()
	case "option":
		return p.parseOption()
	case "modparam":
		return p.parseModparam()
	case "include":
		return p.parseInclude()
	case "pkgconfig":
		return p.parsePkgConfig()
	case "cflags":
		return p.parseBuildFlags("cflags", &p.pkg.Component.CFlags)
	case "ldflags":
		return p.parseBuildFlags("ldflags", &p.pkg.Component.LDFlags)
	case "description":
		return p.parseDocField(&p.pkg.Component.Description)
	case "license":
		return p.parseDocField(&p.pkg.Component.License)
	case "author":
		return p.parseDocField(&p.pkg.Component.Author)
	case "see_also":
		return p.parseDocField(&p.pkg.Component.SeeAlso)
	case "notes":
		return p.parseDocField(&p.pkg.Component.Notes)
	case "examples":
		return p.parseDocField(&p.pkg.Component.Examples)
	case "gmi_provide":
		return p.parseGMIProvide()
	case "gmi_consume":
		return p.parseGMIConsume()
	case "arch":
		return p.parseArch()
	default:
		return p.errorf("unknown declaration keyword %q", p.cur.Val)
	}
}

// ---------------------------------------------------------------------------
// Pin: "pin" PINDIRECTION TYPE HALNAME OptArray OptSAssign OptPersonality OptString ";"
// ---------------------------------------------------------------------------

func (p *parser) parsePin() error {
	pos := p.cur.Pos
	p.next() // skip "pin"

	dir, err := p.parsePinDir()
	if err != nil {
		return err
	}
	typ, err := p.parseHALType()
	if err != nil {
		return err
	}
	name, err := p.expectHALName()
	if err != nil {
		return err
	}

	arrSize, arrPers := p.parseOptArray()
	def := p.parseOptSAssign()
	pers := p.parseOptPersonality()
	doc := p.parseOptString()

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Pins = append(p.pkg.Component.Pins, ast.Pin{
		Pos:              pos,
		Name:             name,
		Type:             typ,
		Dir:              dir,
		ArraySize:        arrSize,
		ArrayPersonality: arrPers,
		Default:          def,
		Personality:      pers,
		Doc:              doc,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Param: "param" PARAMDIRECTION TYPE HALNAME OptArray OptSAssign OptPersonality OptString ";"
// ---------------------------------------------------------------------------

func (p *parser) parseParam() error {
	pos := p.cur.Pos
	p.next() // skip "param"

	dir, err := p.parseParamDir()
	if err != nil {
		return err
	}
	typ, err := p.parseHALType()
	if err != nil {
		return err
	}
	name, err := p.expectHALName()
	if err != nil {
		return err
	}

	arrSize, arrPers := p.parseOptArray()
	def := p.parseOptSAssign()
	pers := p.parseOptPersonality()
	doc := p.parseOptString()

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Params = append(p.pkg.Component.Params, ast.Param{
		Pos:              pos,
		Name:             name,
		Type:             typ,
		Dir:              dir,
		ArraySize:        arrSize,
		ArrayPersonality: arrPers,
		Default:          def,
		Personality:      pers,
		Doc:              doc,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Function: "function" NAME OptFP OptString ";"
// ---------------------------------------------------------------------------

func (p *parser) parseFunction() error {
	pos := p.cur.Pos
	p.next() // skip "function"

	name, err := p.expectName()
	if err != nil {
		return err
	}

	fp := true // default: uses floating point
	if p.cur.Kind == TokIdent {
		switch p.cur.Val {
		case "fp":
			fp = true
			p.next()
		case "nofp":
			fp = false
			p.next()
		}
	}

	doc := p.parseOptString()

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Functions = append(p.pkg.Component.Functions, ast.Function{
		Pos:  pos,
		Name: name,
		FP:   fp,
		Doc:  doc,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Mcode: "mcode" NUMBER ";"  — declares one M100-M199 handler (one per line).
// The body is written as MCODE(<n>){...} in the verbatim C section.
// ---------------------------------------------------------------------------

func (p *parser) parseMcode() error {
	pos := p.cur.Pos
	p.next() // skip "mcode"

	n, err := p.expectNumber()
	if err != nil {
		return err
	}
	if n < 100 || n > 199 {
		return fmt.Errorf("%s: mcode %d out of range (must be 100-199)", pos, n)
	}
	for _, existing := range p.pkg.Component.Mcodes {
		if existing == n {
			return fmt.Errorf("%s: mcode %d declared more than once", pos, n)
		}
	}
	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Mcodes = append(p.pkg.Component.Mcodes, n)
	return nil
}

// ---------------------------------------------------------------------------
// Variable: "variable" NAME STARREDNAME OptSimpleArray OptAssign ";"
// ---------------------------------------------------------------------------

func (p *parser) parseVariable() error {
	pos := p.cur.Pos
	p.next() // skip "variable"

	// Type name (a single NAME).
	typeName, err := p.expectName()
	if err != nil {
		return err
	}

	// Variable name, possibly with leading * for pointers.
	varName := ""
	for p.cur.Kind == TokStar {
		varName += "*"
		p.next()
	}
	n, err := p.expectName()
	if err != nil {
		return err
	}
	varName += n

	// Optional array size: [ NUMBER ]
	arrSize := 0
	if p.cur.Kind == TokLBrack {
		p.next()
		sz, err := p.expectNumber()
		if err != nil {
			return err
		}
		arrSize = sz
		if _, err := p.expect(TokRBrack); err != nil {
			return err
		}
	}

	// Optional default value: = Value
	def := p.parseOptAssign()

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Variables = append(p.pkg.Component.Variables, ast.Variable{
		Pos:     pos,
		CType:   typeName,
		Name:    varName,
		Array:   arrSize,
		Default: def,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Option: "option" NAME OptValue ";"
// ---------------------------------------------------------------------------

// knownOptions are the options the backends actually consult.  Anything else
// is rejected: an option that parses but is never read is worse than a syntax
// error, because it fails at load time (or not at all) instead of at compile
// time, with nothing pointing back at the line that caused it.
var knownOptions = map[string]bool{
	"userspace":     true,
	"extra_setup":   true,
	"extra_cleanup": true,
	"data":          true,
}

// boolOptions are the known options that are flags: a value, if present, must
// spell yes or no.  Normalized here because the backends disagree on
// truthiness (cgen tests presence, docgen tests == "yes"): a truthy value is
// stored as "yes", a falsy one is not stored at all, and anything else is an
// error instead of silently meaning yes.
var boolOptions = map[string]bool{
	"userspace":     true,
	"extra_setup":   true,
	"extra_cleanup": true,
}

// ignoredOptions are classic halcompile options that a .comp may still carry
// and that mean nothing here.  Accepted so existing components keep building,
// but each one warns — the whole point of the whitelist is that a declaration
// never silently does nothing.
var ignoredOptions = map[string]string{
	"fp":                  "floating point is a per-function flag on stratuMAK: `function <name> fp;` / `nofp`",
	"personality":         "personality is inferred from the pins that use it; the option is not needed",
	"default_personality": "not implemented; pass personality=N on the load line instead",
}

// unsupportedOptions are classic halcompile options with no counterpart here.
// Recognized by name so the error can say what happened to each, instead of
// the bare "unknown option" a typo gets: every one of these parsed silently
// on earlier releases, and a component carrying one deserves a pointer at the
// migration rather than a dead end.
var unsupportedOptions = map[string]string{
	"count_function": "cmod/gomod is always multi-instance — every 'load' command creates instances, so there is no count to compute",
	"default_count":  "cmod/gomod is always multi-instance — every 'load' command creates instances, so there is no count to default",
	"constructable":  "cmod/gomod is always multi-instance — every 'load' command creates instances",
	"rtapi_app":      "modcompile always generates the module entry points; put per-instance setup in EXTRA_SETUP (`option extra_setup;`)",
	"userinit":       "put argv handling in EXTRA_SETUP (`option extra_setup;`), which receives argc/argv",
	"homemod":        "custom homing modules are not built from a .comp",
	"tpmod":          "custom trajectory-planning modules are not built from a .comp",
}

func (p *parser) parseOption() error {
	pos := p.cur.Pos
	p.next() // skip "option"

	name, err := p.expectName()
	if err != nil {
		return err
	}

	val := p.parseOptValue()

	if err := p.expectSemi(); err != nil {
		return err
	}

	switch {
	case knownOptions[name]:
		// Validated and stored below.

	// Reject unsupported legacy options.
	// cmod/gomod is always multi-instance; use multiple 'load' commands instead.
	case name == "singleton":
		return fmt.Errorf("%s: 'option singleton' is not supported in cmod/gomod; "+
			"use multiple 'load' commands instead (each creates a separate instance)", pos)

	// These two are documented in the classic comp manual and used to be
	// silently dropped here, which cost the author a mystery undefined symbol
	// at load time.  Name the replacement.
	case name == "extra_compile_args":
		return fmt.Errorf("%s: 'option extra_compile_args' is not supported; use `cflags \"...\";` "+
			"(or `pkgconfig <module>;` for a pkg-config library)", pos)
	case name == "extra_link_args":
		return fmt.Errorf("%s: 'option extra_link_args' is not supported; use `ldflags \"...\";` "+
			"(or `pkgconfig <module>;` for a pkg-config library)", pos)

	default:
		if why, ok := ignoredOptions[name]; ok {
			// Warned about, and deliberately not stored: an option nothing
			// reads has no business surfacing in --parse output as if it did
			// something.
			p.pkg.Warnings = append(p.pkg.Warnings,
				fmt.Sprintf("%s: 'option %s' is ignored — %s", pos, name, why))
			return nil
		}
		if why, ok := unsupportedOptions[name]; ok {
			return fmt.Errorf("%s: 'option %s' is not supported in cmod/gomod; %s",
				pos, name, why)
		}
		return fmt.Errorf("%s: unknown option %q; supported: %s",
			pos, name, knownOptionList())
	}

	if boolOptions[name] {
		switch val {
		case "1", "yes", "true":
			val = "yes"
		case "0", "no", "false":
			// The explicit default.  Stored as absent, which is the shape the
			// backends test for.
			return nil
		default:
			return fmt.Errorf("%s: 'option %s' is a flag: expected yes or no, got %q",
				pos, name, val)
		}
	} else if name == "data" && val == "1" {
		// parseOptValue's bare-option default; a data block needs a C type.
		return fmt.Errorf("%s: 'option data' needs a value: the C type of the "+
			"per-instance data block", pos)
	}

	p.pkg.Component.Options[name] = val
	return nil
}

// knownOptionList returns the sorted set of accepted option names for error
// messages.
func knownOptionList() string {
	names := make([]string, 0, len(knownOptions))
	for n := range knownOptions {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Modparam: "modparam" NAME NAME OptSAssign OptString ";"
// ---------------------------------------------------------------------------

func (p *parser) parseModparam() error {
	pos := p.cur.Pos
	p.next() // skip "modparam"

	typeName, err := p.expectName()
	if err != nil {
		return err
	}
	paramName, err := p.expectName()
	if err != nil {
		return err
	}

	def := p.parseOptSAssign()
	doc := p.parseOptString()

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Modparams = append(p.pkg.Component.Modparams, ast.Modparam{
		Pos:     pos,
		Type:    typeName,
		Name:    paramName,
		Default: def,
		Doc:     doc,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Include: "include" Header ";"
// ---------------------------------------------------------------------------

func (p *parser) parseInclude() error {
	p.next() // skip "include"

	var header string
	if p.cur.Kind == TokString || p.cur.Kind == TokTString {
		header = "\"" + p.cur.Val + "\""
		p.next()
	} else {
		// Angle-bracket header: the scanner hasn't consumed '<' yet
		// because Next() would return TokLT.  Use the special method.
		if p.cur.Kind == TokLT {
			// Back up: ScanAngleHeader needs the '<' not yet consumed.
			// Since we already consumed it via Next(), we reconstruct.
			// Actually — we need to handle this differently.
			// The '<' was already consumed by Next() into p.cur.
			// Scan remaining content until '>'.
			var b strings.Builder
			b.WriteString("<")
			for p.cur.Kind != TokGT && p.cur.Kind != TokSemi && p.cur.Kind != TokEOF {
				p.next()
				if p.cur.Kind == TokGT {
					break
				}
				b.WriteString(p.cur.Val)
			}
			b.WriteString(">")
			header = b.String()
			if p.cur.Kind == TokGT {
				p.next() // consume '>'
			}
		} else {
			return p.errorf("expected string or <header> in include, got %s", p.cur.Kind)
		}
	}

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Includes = append(p.pkg.Component.Includes, header)
	return nil
}

// ---------------------------------------------------------------------------
// External build dependencies:
//
//	"pkgconfig" (NAME | STRING)+ ";"
//	"cflags"  STRING ";"
//	"ldflags" STRING ";"
//
// ---------------------------------------------------------------------------

// parsePkgConfig parses a pkgconfig declaration naming one or more external
// libraries by pkg-config module.  A bare name covers the usual case
// (`pkgconfig libcurl;`, and the scanner's ident rule already admits '-' and
// '.', so `libusb-1.0` needs no quoting); a version constraint has to be
// quoted because of the operator (`pkgconfig "libcurl >= 7.60.0";`).
func (p *parser) parsePkgConfig() error {
	pos := p.cur.Pos
	p.next() // skip "pkgconfig"

	var specs []ast.PkgConfigDep
	for p.cur.Kind == TokIdent || p.cur.Kind == TokString || p.cur.Kind == TokTString {
		spec := p.cur.Val
		if p.cur.Kind != TokIdent {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				return p.errorf("empty pkgconfig module spec")
			}
		}
		// pkg-config's own grammar takes comma-separated module lists, so a
		// comma spec would compile — but each PkgConfigDep is one module to
		// everything else here (`--deps` prints one name per declaration), so
		// the second module would silently vanish from the dependency report.
		if strings.Contains(spec, ",") {
			return fmt.Errorf("%s: pkgconfig: %q — one module per spec; separate modules "+
				"with spaces: pkgconfig libcurl zlib;", p.cur.Pos, spec)
		}
		specs = append(specs, ast.PkgConfigDep{Pos: p.cur.Pos, Spec: spec})
		p.next()
	}
	if len(specs) == 0 {
		return p.errorf("expected at least one pkg-config module name after 'pkgconfig'")
	}
	// A version constraint written unquoted lands here as a stray operator.
	// Say so, rather than leaving the author with "expected ;".
	if p.cur.Kind != TokSemi {
		return fmt.Errorf("%s: pkgconfig: unexpected %s (%q); a version constraint must be "+
			"quoted, e.g. pkgconfig \"%s >= 1.2.3\";",
			pos, p.cur.Kind, p.cur.Val, specs[len(specs)-1].Name())
	}
	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.PkgConfig = append(p.pkg.Component.PkgConfig, specs...)
	return nil
}

// parseBuildFlags parses a cflags/ldflags declaration.  The value is a literal
// string, split on whitespace and passed to the compiler as separate
// arguments — there is deliberately no shell, no $(...) and no variable
// expansion: `modcompile --install` runs as root, so a .comp stays data.
func (p *parser) parseBuildFlags(kw string, dst *[]string) error {
	pos := p.cur.Pos
	p.next() // skip the keyword

	if p.cur.Kind != TokString && p.cur.Kind != TokTString {
		return fmt.Errorf("%s: %s takes a quoted string, e.g. %s \"-I/opt/vendor/include\";",
			pos, kw, kw)
	}
	val := p.cur.Val
	p.next()

	if err := p.expectSemi(); err != nil {
		return err
	}

	fields := strings.Fields(val)
	if len(fields) == 0 {
		return fmt.Errorf("%s: empty %s string", pos, kw)
	}
	for _, f := range fields {
		// $(...) / $VAR / `...` would need a shell to mean anything, and
		// running one here would execute the .comp's own text as root under
		// `sudo modcompile --install`.  Refuse loudly instead of passing the
		// literal through to the compiler, where it fails obscurely.
		if hasShellSubstitution(f) {
			return fmt.Errorf("%s: %s: %q — command and variable substitution are not "+
				"supported here (a .comp is not a shell script). Use `pkgconfig <module>;` "+
				"for a pkg-config library, or build the module from a Makefile with "+
				"`modcompile --preprocess` plus --cflags/--ldflags",
				pos, kw, f)
		}
		*dst = append(*dst, f)
	}
	return nil
}

// linkerDollarTokens are the dynamic string tokens the runtime linker expands
// by itself — no shell involved — so -Wl,-rpath,$ORIGIN, the standard way a
// module finds a vendor .so shipped beside it, must pass the substitution
// check.
var linkerDollarTokens = []string{"ORIGIN", "LIB", "PLATFORM"}

// hasShellSubstitution reports whether a build flag contains something only a
// shell (or make) would expand — $VAR, ${VAR}, $(...), backticks — as opposed
// to the linker tokens above, braced or bare.
func hasShellSubstitution(f string) bool {
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case '`':
			return true
		case '$':
			rest := f[i+1:]
			braced := strings.HasPrefix(rest, "{")
			if braced {
				rest = rest[1:]
			}
			var tok string
			for _, t := range linkerDollarTokens {
				if strings.HasPrefix(rest, t) {
					tok = t
					break
				}
			}
			if tok == "" {
				return true
			}
			rest = rest[len(tok):]
			if braced {
				if !strings.HasPrefix(rest, "}") {
					return true
				}
			} else if rest != "" && (isIdentChar(rest[0])) {
				// $ORIGINAL is a variable, not the $ORIGIN token.
				return true
			}
			i = len(f) - len(rest) - 1
		}
	}
	return false
}

func isIdentChar(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// ---------------------------------------------------------------------------
// GMI API bindings: "gmi_provide" NAME ";" / "gmi_consume" NAME ";"
// ---------------------------------------------------------------------------

func (p *parser) parseGMIProvide() error {
	p.next() // skip "gmi_provide"

	name, err := p.expectName()
	if err != nil {
		return err
	}
	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.GMIProvide = append(p.pkg.Component.GMIProvide, name)
	return nil
}

func (p *parser) parseGMIConsume() error {
	p.next() // skip "gmi_consume"

	name, err := p.expectName()
	if err != nil {
		return err
	}

	// Optional "from <module>" clause for default provider instance.
	var from string
	if p.cur.Kind == TokIdent && p.cur.Val == "from" {
		p.next() // skip "from"
		from, err = p.expectName()
		if err != nil {
			return err
		}
	}

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.GMIConsume = append(p.pkg.Component.GMIConsume, ast.GMIConsumeEntry{
		API:  name,
		From: from,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Arch: "arch" NAME (NAME)* ";"
//
// Restricts the module to the listed target architectures.  On any other
// target the backend emits a stub instead of the real module, so a build
// (e.g. an arm64 package) still succeeds for a driver that only compiles on
// x86.  At least one name is required and each must be known to ast.ArchMacros.
// ---------------------------------------------------------------------------

func (p *parser) parseArch() error {
	pos := p.cur.Pos
	p.next() // skip "arch"

	var archs []string
	for p.cur.Kind == TokIdent {
		name, err := p.expectName()
		if err != nil {
			return err
		}
		if _, ok := ast.ArchMacros[name]; !ok {
			return fmt.Errorf("%s: unknown arch %q; known: %s",
				pos, name, knownArchList())
		}
		archs = append(archs, name)
	}
	if len(archs) == 0 {
		return p.errorf("expected at least one architecture name after 'arch'")
	}

	if err := p.expectSemi(); err != nil {
		return err
	}

	p.pkg.Component.Archs = append(p.pkg.Component.Archs, archs...)
	return nil
}

// knownArchList returns the sorted set of accepted arch names for error messages.
func knownArchList() string {
	names := make([]string, 0, len(ast.ArchMacros))
	for name := range ast.ArchMacros {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Doc fields: "description" String ";" etc.
// ---------------------------------------------------------------------------

func (p *parser) parseDocField(target *string) error {
	p.next() // skip keyword

	s, err := p.expectString()
	if err != nil {
		return err
	}
	*target = s

	return p.expectSemi()
}

// ---------------------------------------------------------------------------
// Sub-parsers for types, directions, values
// ---------------------------------------------------------------------------

func (p *parser) parsePinDir() (ast.PinDir, error) {
	if p.cur.Kind != TokIdent {
		return 0, p.errorf("expected pin direction (in/out/io), got %s", p.cur.Kind)
	}
	switch p.cur.Val {
	case "in":
		p.next()
		return ast.PinIn, nil
	case "out":
		p.next()
		return ast.PinOut, nil
	case "io":
		p.next()
		return ast.PinIO, nil
	default:
		return 0, p.errorf("expected pin direction (in/out/io), got %q", p.cur.Val)
	}
}

func (p *parser) parseParamDir() (ast.ParamDir, error) {
	if p.cur.Kind != TokIdent {
		return 0, p.errorf("expected param direction (r/rw), got %s", p.cur.Kind)
	}
	switch p.cur.Val {
	case "r":
		p.next()
		return ast.ParamR, nil
	case "rw":
		p.next()
		return ast.ParamRW, nil
	default:
		return 0, p.errorf("expected param direction (r/rw), got %q", p.cur.Val)
	}
}

func (p *parser) parseHALType() (ast.HALType, error) {
	if p.cur.Kind != TokIdent {
		return 0, p.errorf("expected HAL type, got %s", p.cur.Kind)
	}
	var t ast.HALType
	switch p.cur.Val {
	case "bit":
		t = ast.HALBit
	case "float":
		t = ast.HALFloat
	case "s32", "signed":
		t = ast.HALS32
	case "u32", "unsigned":
		t = ast.HALU32
	case "port":
		t = ast.HALPort
	default:
		return 0, p.errorf("expected HAL type (bit/float/s32/u32/port), got %q", p.cur.Val)
	}
	p.next()
	return t, nil
}

// expectName expects a plain C identifier (NAME).
func (p *parser) expectName() (string, error) {
	if p.cur.Kind != TokIdent || !IsName(p.cur.Val) {
		return "", p.errorf("expected identifier, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	val := p.cur.Val
	p.next()
	return val, nil
}

// expectHALName expects a HAL-style name (HALNAME).
func (p *parser) expectHALName() (string, error) {
	if p.cur.Kind != TokIdent {
		return "", p.errorf("expected HAL name, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	val := p.cur.Val
	p.next()
	return val, nil
}

// expectNumber expects an integer number literal and returns its value.
func (p *parser) expectNumber() (int, error) {
	if p.cur.Kind != TokNumber {
		return 0, p.errorf("expected number, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	val := p.cur.Val
	p.next()
	n, err := strconv.ParseInt(val, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid number %q: %w", p.cur.Pos, val, err)
	}
	return int(n), nil
}

// expectString expects a string or triple-quoted string.
func (p *parser) expectString() (string, error) {
	if p.cur.Kind != TokString && p.cur.Kind != TokTString {
		return "", p.errorf("expected string, got %s (%q)", p.cur.Kind, p.cur.Val)
	}
	val := p.cur.Val
	p.next()
	return val, nil
}

// ---------------------------------------------------------------------------
// Optional components
// ---------------------------------------------------------------------------

// parseOptString returns the string value of the current token if it's a
// string literal, or "" otherwise.
func (p *parser) parseOptString() string {
	if p.cur.Kind == TokString || p.cur.Kind == TokTString {
		val := p.cur.Val
		p.next()
		return val
	}
	return ""
}

// parseOptArray parses OptArray: "[" NUMBER (":" PersonalityExpr)? "]"
// Returns (size, personalityExpr).
func (p *parser) parseOptArray() (int, string) {
	if p.cur.Kind != TokLBrack {
		return 0, ""
	}
	p.next() // skip [

	// Must be a number.
	if p.cur.Kind != TokNumber {
		return 0, ""
	}
	size, _ := strconv.ParseInt(p.cur.Val, 0, 64)
	p.next()

	pers := ""
	if p.cur.Kind == TokColon {
		p.next() // skip :
		pers = p.collectPersonalityExpr(TokRBrack)
	}

	if p.cur.Kind == TokRBrack {
		p.next() // skip ]
	}

	return int(size), pers
}

// parseOptSAssign parses OptSAssign: "=" SValue | empty.
// Returns the value as a string, or "".
func (p *parser) parseOptSAssign() string {
	if p.cur.Kind != TokEq {
		return ""
	}
	p.next() // skip =
	return p.parseSValue()
}

// parseOptAssign parses OptAssign: "=" Value | empty (for variables).
func (p *parser) parseOptAssign() string {
	if p.cur.Kind != TokEq {
		return ""
	}
	p.next() // skip =
	return p.parseValue()
}

// parseOptPersonality parses OptPersonality: "if" PersonalityExpr | empty.
func (p *parser) parseOptPersonality() string {
	if p.cur.Kind != TokIdent || p.cur.Val != "if" {
		return ""
	}
	p.next() // skip "if"
	return p.collectPersonalityExpr(TokString, TokTString, TokSemi)
}

// parseOptValue parses OptValue: Value | String | TString | empty (→ "1").
func (p *parser) parseOptValue() string {
	switch p.cur.Kind {
	case TokString, TokTString:
		val := p.cur.Val
		p.next()
		return val
	case TokNumber, TokFPNum:
		val := p.cur.Val
		p.next()
		return val
	case TokIdent:
		val := p.cur.Val
		p.next()
		return val
	default:
		return "1"
	}
}

// parseSValue parses a single value token (for pin/param defaults).
// Handles optional leading sign for numbers.
func (p *parser) parseSValue() string {
	// Possible leading sign.
	sign := ""
	if p.cur.Kind == TokMinus || p.cur.Kind == TokPlus {
		sign = p.cur.Val
		p.next()
	}
	switch p.cur.Kind {
	case TokNumber, TokFPNum:
		val := sign + p.cur.Val
		p.next()
		return val
	case TokString:
		val := p.cur.Val
		p.next()
		return val
	case TokIdent:
		val := p.cur.Val
		p.next()
		return val
	default:
		if sign != "" {
			return sign // shouldn't happen, but be safe
		}
		return ""
	}
}

// parseValue parses a Value (for variable defaults).
func (p *parser) parseValue() string {
	return p.parseSValue()
}

// collectPersonalityExpr collects a personality expression as a raw string,
// stopping when it encounters any of the given terminator token kinds.
func (p *parser) collectPersonalityExpr(terminators ...TokenKind) string {
	var parts []string
	for {
		for _, t := range terminators {
			if p.cur.Kind == t {
				return strings.Join(parts, " ")
			}
		}
		if p.cur.Kind == TokEOF {
			return strings.Join(parts, " ")
		}
		parts = append(parts, p.cur.Val)
		p.next()
	}
}
