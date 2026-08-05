// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#include "classicladder_rt.h"
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"

	api "github.com/stratuMAK/stratumak/src/stmak/generated/gmi/classicladder"
)

// Variable names, both directions: "%I3" <-> (VAR_PHYS_INPUT, 3).
//
// Ported from 2.9 vars_names.c and its name table in vars_names_list.c. Every
// editing surface needs this pair — a property editor has to parse what the
// operator types, and anything showing an expression has to render the
// @type/offset@ form back into something readable. Expressions are stored in
// the internal form, so this is the only place that knows the written one.

// varNameEntry is one row of 2.9's TableConvIdVarName: the written form of a
// variable region, the type it maps to, how many of them exist, and whether a
// program may assign to it.
type varNameEntry struct {
	// prefix and suffix bracket the offset in the written name: prefix "T",
	// suffix ".D" spells "%T3.D". 2.9 keeps these as one printf format; split
	// here because we parse with them as well as print.
	prefix   string
	suffix   string
	varType  int
	writable bool
	// size reports how many offsets exist, which depends on the configuration
	// rather than being a constant (2.9 patches its table at startup in
	// UpdateSizesOfConvVarNameTable).
	size func(s *C.cl_sizes_t) int
}

const stepCount = C.CL_MAX_STEPS

// varNameTable mirrors 2.9's table, in its order. Entries are matched by
// longest written form rather than by position, so "%X0.V" cannot be taken for
// "%X0" and "%IW0" cannot be taken for "%I0".
var varNameTable = []varNameEntry{
	{"B", "", C.CL_VAR_MEM_BIT, true, func(s *C.cl_sizes_t) int { return int(s.nbr_bits) }},

	{"T", ".D", C.CL_VAR_TIMER_DONE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_timers) }},
	{"T", ".R", C.CL_VAR_TIMER_RUNNING, false, func(s *C.cl_sizes_t) int { return int(s.nbr_timers) }},
	{"T", ".P", C.CL_VAR_TIMER_PRESET, true, func(s *C.cl_sizes_t) int { return int(s.nbr_timers) }},
	{"T", ".V", C.CL_VAR_TIMER_VALUE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_timers) }},

	{"M", ".R", C.CL_VAR_MONOSTABLE_RUNNING, false, func(s *C.cl_sizes_t) int { return int(s.nbr_monostables) }},
	{"M", ".P", C.CL_VAR_MONOSTABLE_PRESET, true, func(s *C.cl_sizes_t) int { return int(s.nbr_monostables) }},
	{"M", ".V", C.CL_VAR_MONOSTABLE_VALUE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_monostables) }},

	{"C", ".D", C.CL_VAR_COUNTER_DONE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_counters) }},
	{"C", ".E", C.CL_VAR_COUNTER_EMPTY, false, func(s *C.cl_sizes_t) int { return int(s.nbr_counters) }},
	{"C", ".F", C.CL_VAR_COUNTER_FULL, false, func(s *C.cl_sizes_t) int { return int(s.nbr_counters) }},
	{"C", ".P", C.CL_VAR_COUNTER_PRESET, true, func(s *C.cl_sizes_t) int { return int(s.nbr_counters) }},
	{"C", ".V", C.CL_VAR_COUNTER_VALUE, true, func(s *C.cl_sizes_t) int { return int(s.nbr_counters) }},

	// IEC timers are written %TM, not %TI — the ladder view has this wrong.
	{"TM", ".Q", C.CL_VAR_TIMER_IEC_DONE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_timers_iec) }},
	{"TM", ".P", C.CL_VAR_TIMER_IEC_PRESET, true, func(s *C.cl_sizes_t) int { return int(s.nbr_timers_iec) }},
	{"TM", ".V", C.CL_VAR_TIMER_IEC_VALUE, false, func(s *C.cl_sizes_t) int { return int(s.nbr_timers_iec) }},

	{"I", "", C.CL_VAR_PHYS_INPUT, false, func(s *C.cl_sizes_t) int { return int(s.nbr_phys_inputs) }},
	{"Q", "", C.CL_VAR_PHYS_OUTPUT, true, func(s *C.cl_sizes_t) int { return int(s.nbr_phys_outputs) }},
	{"W", "", C.CL_VAR_MEM_WORD, true, func(s *C.cl_sizes_t) int { return int(s.nbr_words) }},
	{"IW", "", C.CL_VAR_PHYS_WORD_INPUT, false, func(s *C.cl_sizes_t) int { return int(s.nbr_s32_in) }},
	{"QW", "", C.CL_VAR_PHYS_WORD_OUTPUT, true, func(s *C.cl_sizes_t) int { return int(s.nbr_s32_out) }},
	{"IF", "", C.CL_VAR_PHYS_FLOAT_INPUT, false, func(s *C.cl_sizes_t) int { return int(s.nbr_float_in) }},
	{"QF", "", C.CL_VAR_PHYS_FLOAT_OUTPUT, true, func(s *C.cl_sizes_t) int { return int(s.nbr_float_out) }},
	{"E", "", C.CL_VAR_ERROR_BIT, true, func(s *C.cl_sizes_t) int { return int(s.nbr_error_bits) }},

	{"X", ".V", C.CL_VAR_STEP_TIME, false, func(s *C.cl_sizes_t) int { return stepCount }},
	{"X", "", C.CL_VAR_STEP_ACTIVITY, false, func(s *C.cl_sizes_t) int { return stepCount }},
}

// entryForType finds the table row describing a variable type.
func entryForType(varType int) *varNameEntry {
	for i := range varNameTable {
		if varNameTable[i].varType == varType {
			return &varNameTable[i]
		}
	}
	return nil
}

// formatVarName renders a variable in its written form, e.g. "%C2.D".
// Reports false for an unknown type or an offset outside the configured size —
// 2.9 renders those as "???", which is a display string, not a name.
func (m *classicladder) formatVarName(varType, offset int) (string, bool) {
	e := entryForType(varType)
	if e == nil || offset < 0 || offset >= e.size(&m.rt.sizes) {
		return "", false
	}
	return "%" + e.prefix + strconv.Itoa(offset) + e.suffix, true
}

// parseVarName reads a variable from the start of s and reports how many bytes
// it consumed, so a caller scanning an expression can continue after it.
//
// The longest match wins: "%X0.V" is the step timer and not the step activity
// followed by a stray ".V", and "%IW0" is a word input and not "%I" with a
// leftover. 2.9 reaches the same answer by ordering its table carefully; being
// explicit about it here means a new entry cannot quietly shadow an old one.
func (m *classicladder) parseVarName(s string) (varType, offset, consumed int, err error) {
	if !strings.HasPrefix(s, "%") {
		return 0, 0, 0, fmt.Errorf("variable name must start with %%: %q", s)
	}
	body := s[1:]

	bestLen := -1
	for i := range varNameTable {
		e := &varNameTable[i]
		if !strings.HasPrefix(body, e.prefix) {
			continue
		}
		rest := body[len(e.prefix):]
		// Digits, then the suffix if the entry has one.
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 0 {
			continue
		}
		if !strings.HasPrefix(rest[n:], e.suffix) {
			continue
		}
		off, convErr := strconv.Atoi(rest[:n])
		if convErr != nil || off < 0 || off >= e.size(&m.rt.sizes) {
			continue
		}
		total := 1 + len(e.prefix) + n + len(e.suffix)
		if total > bestLen {
			bestLen, varType, offset = total, e.varType, off
		}
	}
	if bestLen < 0 {
		return 0, 0, 0, fmt.Errorf("not a variable name: %q", s)
	}
	return varType, offset, bestLen, nil
}

// varIsWritable reports whether a program may assign to this variable. An
// operate expression targeting a read-only variable — a timer's elapsed value,
// a physical input — is a mistake worth catching before it is stored.
func varIsWritable(varType int) bool {
	e := entryForType(varType)
	return e != nil && e.writable
}

// varClasses renders the naming table for clients, with the counts this
// instance was actually configured with. Classes configured to zero are left
// out: a name that cannot have a valid offset is not worth offering.
func (m *classicladder) varClasses() []api.VarClass {
	out := make([]api.VarClass, 0, len(varNameTable))
	for i := range varNameTable {
		e := &varNameTable[i]
		n := e.size(&m.rt.sizes)
		if n <= 0 {
			continue
		}
		out = append(out, api.VarClass{
			Prefix:   e.prefix,
			Suffix:   e.suffix,
			VarType:  int32(e.varType),
			Count:    int32(n),
			Writable: e.writable,
		})
	}
	return out
}

// --- Internal form used by stored expressions ---

// formatVarRef renders the @type/offset@ form that expressions are stored in.
func formatVarRef(varType, offset int) string {
	return fmt.Sprintf("@%d/%d@", varType, offset)
}

// exprToNames rewrites an expression from its stored form into written names,
// for display: "@200/0@:=@270/1@+1" becomes "%W0:=%IW1+1". Anything that is not
// a variable reference is passed through, so operators and constants survive.
// A reference whose type or offset is unknown is left in its stored form rather
// than dropped, so nothing silently disappears from an expression.
func (m *classicladder) exprToNames(expr string) string {
	var b strings.Builder
	for i := 0; i < len(expr); {
		if expr[i] != '@' {
			b.WriteByte(expr[i])
			i++
			continue
		}
		end := strings.IndexByte(expr[i+1:], '@')
		if end < 0 {
			b.WriteString(expr[i:])
			break
		}
		inner := expr[i+1 : i+1+end]
		if name, ok := m.varRefToName(inner); ok {
			b.WriteString(name)
		} else {
			b.WriteString(expr[i : i+end+2])
		}
		i += end + 2
	}
	return b.String()
}

// varRefToName converts the inside of an @...@ reference to a written name.
// Indexed references ("200/0[200/1]") keep their index, itself converted.
func (m *classicladder) varRefToName(inner string) (string, bool) {
	idx := ""
	if open := strings.IndexByte(inner, '['); open >= 0 {
		if !strings.HasSuffix(inner, "]") {
			return "", false
		}
		idxName, ok := m.varRefToName(inner[open+1 : len(inner)-1])
		if !ok {
			return "", false
		}
		idx = "[" + idxName + "]"
		inner = inner[:open]
	}
	slash := strings.IndexByte(inner, '/')
	if slash < 0 {
		return "", false
	}
	varType, err1 := strconv.Atoi(inner[:slash])
	offset, err2 := strconv.Atoi(inner[slash+1:])
	if err1 != nil || err2 != nil {
		return "", false
	}
	name, ok := m.formatVarName(varType, offset)
	if !ok {
		return "", false
	}
	return name + idx, true
}

// namesToExpr is the inverse: "%W0:=%IW1+1" becomes "@200/0@:=@270/1@+1", which
// is what the compiler and the file format expect. An unrecognisable name is an
// error rather than a pass-through, so a typo does not reach the engine as a
// silently inert expression.
func (m *classicladder) namesToExpr(text string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '%' {
			b.WriteByte(text[i])
			i++
			continue
		}
		varType, offset, n, err := m.parseVarName(text[i:])
		if err != nil {
			return "", err
		}
		i += n
		// An index in the written form applies to the variable just read.
		if i < len(text) && text[i] == '[' {
			close := strings.IndexByte(text[i:], ']')
			if close < 0 {
				return "", fmt.Errorf("unterminated index in %q", text)
			}
			idxType, idxOffset, idxN, idxErr := m.parseVarName(text[i+1 : i+close])
			if idxErr != nil {
				return "", idxErr
			}
			if idxN != close-1 {
				return "", fmt.Errorf("trailing characters in index of %q", text)
			}
			b.WriteString(fmt.Sprintf("@%d/%d[%d/%d]@", varType, offset, idxType, idxOffset))
			i += close + 1
			continue
		}
		b.WriteString(formatVarRef(varType, offset))
	}
	return b.String(), nil
}
