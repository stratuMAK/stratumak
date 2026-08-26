// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Every ST name reaches the generated C as an identifier, and a few of them
// cannot: `int` is a C keyword, so `VAR int : DINT; END_VAR` becomes
// `int32_t int;` and gcc rejects it.  The error then names a line of generated
// code nobody keeps, which is exactly what this frontend exists to prevent, so
// the name is refused here with a position in the .st instead.
//
// Refusing rather than mangling.  A suffix would need a spelling ST cannot
// produce or it collides in turn, and none of the three production projects
// declares anything by one of these names, so there is nothing to rescue.

// cKeywords are the C keywords that an ST identifier could collide with.
//
// Only the ones an ST program can actually reach: ST has its own IF, WHILE,
// FOR, CASE and so on, and those cannot be identifiers here whatever C thinks
// of them.  The comparison is case-insensitive because ST is: `Int` and `int`
// are one name, and which spelling happened to be declared should not decide
// whether the component builds.
var cKeywords = map[string]bool{
	"auto": true, "break": true, "case": true, "char": true,
	"const": true, "continue": true, "default": true, "do": true,
	"double": true, "else": true, "enum": true, "extern": true,
	"float": true, "for": true, "goto": true, "if": true,
	"inline": true, "int": true, "long": true, "register": true,
	"restrict": true, "return": true, "short": true, "signed": true,
	"sizeof": true, "static": true, "struct": true, "switch": true,
	"typedef": true, "union": true, "unsigned": true, "void": true,
	"volatile": true, "while": true,
	// C99 and C11 spellings that are equally fatal.
	"_Bool": true, "_Complex": true, "_Imaginary": true, "_Atomic": true,
	"_Generic": true, "_Noreturn": true, "_Static_assert": true,
	"_Thread_local": true, "_Alignas": true, "_Alignof": true,
	// Not keywords, but the generated code includes <stdbool.h> and assigns
	// `true` and `false` to bit pins, so a variable of either name shadows
	// what those expand to.
	"bool": true, "true": true, "false": true,
}

// cGenerated are names the generated module already uses for itself: the type
// the component is built around, and the members every instance carries.
//
// Checked only where a name actually lands in inst_t -- a component variable,
// a pin, a parameter -- which is what claimInstanceName already marks.  A
// method parameter or a function local named comp_id is a C local and collides
// with nothing, and refusing it would reject a provider implementing an
// interface whose function happens to take one.
var cGenerated = map[string]bool{
	"inst_t": true, "inst_hal_t": true,
	"base": true, "env": true, "comp_id": true, "name": true, "hal": true,
}

// checkCName refuses a declared name the generated C could not spell.  It
// reports whether the name is usable.
func (c *checker) checkCName(name string, pos ast.Pos, what string) bool {
	if name == "" {
		return true
	}
	if cKeywords[strings.ToLower(name)] || cKeywords[name] {
		c.errorf(pos, "%s cannot be named %s: that is a C keyword, and every name here "+
			"becomes one in the generated module. Pick another name",
			what, name)
		return false
	}
	// The generator's own names all begin with a double underscore --
	// __comp_inst, __pin_x, __st_div_s -- and C reserves that prefix to the
	// implementation besides.
	if strings.HasPrefix(name, "__") {
		c.errorf(pos, "%s cannot be named %s: a leading double underscore is reserved, "+
			"both by C and by the names the generated module uses for itself",
			what, name)
		return false
	}
	return true
}

// checkInstanceCName refuses a name that would collide with a member every
// instance already carries.
func (c *checker) checkInstanceCName(name string, pos ast.Pos, what string) bool {
	if !cGenerated[strings.ToLower(name)] {
		return true
	}
	c.errorf(pos, "%s cannot be named %s: the generated module already uses that name "+
		"for part of every instance. Pick another name", what, name)
	return false
}
