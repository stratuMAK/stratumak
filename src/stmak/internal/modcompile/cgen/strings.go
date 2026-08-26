// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// IEC's string functions in C, where a function cannot return an array.
//
// Each one that produces a STRING is given a buffer to build it in, and
// returns that buffer -- so the call renders as an ordinary C expression and
// nests wherever ST nests it. The buffers are locals of the enclosing body,
// one per call site rather than one shared: CONCAT(CONCAT(a, b), c) is the
// shape these appear in, and a shared buffer would have the inner call
// overwrite what the outer one was still reading.
//
// One per call site also means the sizes are known at compile time and visible
// in the generated C, which is what keeps a chain of concatenations off the
// real-time stack without anybody having to reason about it: sema caps each
// result at ast.MaxStringLen, so the worst case a body can cost is that many
// bytes per call site.

// strTempName is the buffer a call site's result is built in.
func strTempName(n int) string { return fmt.Sprintf("__st_s%d", n) }

// producesString reports whether a call builds a STRING, and so needs a buffer
// of its own. LEN and FIND count characters instead and need none; everything
// else, the *_TO_STRING family included, builds one.
func producesString(f ast.StrFunc) bool {
	return f != ast.StrLen && f != ast.StrFind
}

// declareStrTemps gives every string result in a body a buffer and declares
// them at its top.
//
// The set has to match what emission uses exactly: the build runs with -Wall
// -Werror, so a buffer nothing writes into is as fatal as a helper nothing
// calls. Keying by the node itself is what makes that hold -- the walk finds
// the same nodes the emitter will reach, and the rule for whether one gets a
// buffer is local to the node.
func (e *stEmitter) declareStrTemps(body []ast.Stmt, depth int) {
	e.strTemps = nil
	var decls []string
	walkStmts(body, func(ast.Stmt) {}, func(x ast.Expr) {
		v, ok := x.(*ast.StrFuncExpr)
		if !ok || !producesString(v.Func) {
			return
		}
		if e.strTemps == nil {
			e.strTemps = make(map[*ast.StrFuncExpr]int)
		}
		n := len(e.strTemps)
		e.strTemps[v] = n
		decls = append(decls, fmt.Sprintf("%schar %s[%d];\n",
			indent(depth), strTempName(n), v.Typ.StringLen()+1))
	})
	if len(decls) == 0 {
		return
	}
	e.g.printf("%s/* Where this body's string results are built, one per call. */\n",
		indent(depth))
	for _, d := range decls {
		e.g.printf("%s", d)
	}
	e.g.printf("\n")
}

// strFunc renders one call.
func (e *stEmitter) strFunc(v *ast.StrFuncExpr) string {
	g := e.g
	if v.Func != ast.StrFrom && !g.usedStrFuncs[v.Func] && g.err == nil {
		// The pre-scan decides which helpers exist; reaching one it did not
		// find would emit a call to a function nobody defined.  Saying so here
		// beats a linker error naming generated C.
		g.err = fmt.Errorf("%s: %s was reached but no helper was emitted for it",
			v.Pos, v.Func)
	}

	arg := func(i int) string { return e.expr(v.Args[i]) }

	switch v.Func {
	case ast.StrLen:
		return fmt.Sprintf("__st_len(%s)", arg(0))
	case ast.StrFind:
		return fmt.Sprintf("__st_find(%s, %s)", arg(0), arg(1))
	}

	n, ok := e.strTemps[v]
	if !ok {
		// Unreachable: declareStrTemps walks the same tree with the same rule.
		// Recording an error beats emitting C that names a buffer nobody
		// declared, which would fail in the compiler with a generated line
		// number -- exactly what this frontend exists to prevent.
		if g.err == nil {
			g.err = fmt.Errorf("%s: no buffer was reserved for this %s",
				v.Pos, v.Func)
		}
		return "\"\""
	}
	buf := strTempName(n)

	if v.Func == ast.StrFrom {
		return e.strFrom(v, buf)
	}

	// The message a truncated result is reported under. It names the position
	// in the .st, because a body may hold several of these and "CONCAT
	// truncated" on its own does not say which.
	what := cStringLiteral(fmt.Sprintf("%s at %s truncated", v.Func, v.Pos))

	args := make([]string, 0, len(v.Args))
	for i := range v.Args {
		args = append(args, arg(i))
	}
	call := fmt.Sprintf("__st_%s(__comp_inst, %s, (int)sizeof(%s)",
		strHelperName(v.Func), buf, buf)
	for _, a := range args {
		call += ", " + a
	}
	return call + ", " + what + ")"
}

// strHelperName is the C helper for one function.
func strHelperName(f ast.StrFunc) string {
	switch f {
	case ast.StrConcat:
		return "concat"
	case ast.StrLeft:
		return "left"
	case ast.StrRight:
		return "right"
	case ast.StrMid:
		return "mid"
	case ast.StrInsert:
		return "insert"
	case ast.StrDelete:
		return "delete"
	case ast.StrReplace:
		return "replace"
	}
	return "len"
}

// ---------------------------------------------------------------------------
// The helpers themselves
// ---------------------------------------------------------------------------

// scanStrFuncs records which string functions the package reaches.
//
// A pre-scan for the same reason the division guards have one: the helpers are
// emitted above every body, so what they are is settled before the first body
// is written out.
func (g *generator) scanStrFuncs() map[ast.StrFunc]bool {
	used := make(map[ast.StrFunc]bool)
	for _, body := range g.allBodies() {
		walkStmts(body, func(ast.Stmt) {}, func(x ast.Expr) {
			if v, ok := x.(*ast.StrFuncExpr); ok {
				used[v.Func] = true
			}
		})
	}
	return used
}

// anyStrProducer reports whether any string function builds a result out of
// other strings, and so whether the append primitive and the fault reporter
// are reached.
//
// The *_TO_STRING family is deliberately not counted. Its buffers hold the
// widest result each type can give, so none of those conversions truncates and
// none of them uses the append primitive -- with the single exception of the
// floating writer, which can meet a value too large to place and says so.
func (g *generator) anyStrProducer() bool {
	if g.usedStrFrom == nil {
		g.usedStrFrom = g.scanStrFrom()
	}
	if g.usedStrFrom["f"] {
		return true
	}
	if g.usedStrFuncs == nil {
		// The instance struct is laid out before the bodies are emitted, and
		// it needs the fault counter that a truncated result increments -- so
		// the scan has to be able to answer before generate() runs it.
		g.usedStrFuncs = g.scanStrFuncs()
	}
	for f := range g.usedStrFuncs {
		if f != ast.StrFrom && producesString(f) {
			return true
		}
	}
	return false
}

// needStrLen reports whether __st_len is reached: by LEN itself, and by every
// function that has to know where the string ends before it can clamp a
// position against it.
func (g *generator) needStrLen() bool {
	for _, f := range []ast.StrFunc{
		ast.StrLen, ast.StrRight, ast.StrMid,
		ast.StrInsert, ast.StrDelete, ast.StrReplace,
	} {
		if g.usedStrFuncs[f] {
			return true
		}
	}
	return false
}

// emitStrRuntime emits the helpers this component's string functions need, and
// nothing else.
//
// Positions are IEC's throughout, so they are one-based and a length is a
// count of characters. One outside the string is clamped rather than refused:
// LEFT(s, 20) on a shorter string is how people ask for "up to twenty", not a
// mistake, and faulting on it would fill the log with correct code. Losing
// characters off the end of the result is different -- that is data going
// missing -- and is counted and reported.
func (g *generator) emitStrRuntime() {
	if len(g.usedStrFuncs) == 0 {
		return
	}

	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Structured Text string functions\n")
	g.printf(" *\n")
	g.printf(" * Each result is built in a buffer the call site owns and returns it, since\n")
	g.printf(" * C returns no arrays.  Positions are IEC's: one-based, and clamped to the\n")
	g.printf(" * string rather than trusted.\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	if g.needStrLen() {
		g.printf("static int __st_len(const char *s) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_len(const char *s) {\n")
		g.printf("    int n = 0;\n")
		g.printf("    while (s[n] != '\\0') n++;\n")
		g.printf("    return n;\n")
		g.printf("}\n\n")
	}

	if g.anyStrProducer() {
		// The one primitive the producers are built from: append at most n
		// characters, never past the buffer, and say whether anything was
		// left over.  n < 0 means "all of it".
		g.printf("static int __st_app(char *dst, int cap, int len, const char *src, int n, int *lost) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_app(char *dst, int cap, int len, const char *src, int n, int *lost) {\n")
		g.printf("    int i = 0;\n")
		g.printf("    while ((n < 0 || i < n) && src[i] != '\\0') {\n")
		g.printf("        if (len >= cap - 1) { *lost = 1; break; }\n")
		g.printf("        dst[len++] = src[i++];\n")
		g.printf("    }\n")
		g.printf("    dst[len] = '\\0';\n")
		g.printf("    return len;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrConcat] {
		g.printf("static char *__st_concat(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_concat(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, const char *what) {\n")
		g.printf("    int lost = 0;\n")
		g.printf("    int n = __st_app(dst, cap, 0, a, -1, &lost);\n")
		g.printf("    (void)__st_app(dst, cap, n, b, -1, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrLeft] {
		g.printf("static char *__st_left(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_left(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, const char *what) {\n")
		g.printf("    int lost = 0;\n")
		// Clamped to the buffer before the cast: l is an ST integer and could
		// be anything, and (int) of a value past INT_MAX is undefined.  More
		// than cap characters never fit anyway, so nothing is lost by it.
		g.printf("    if (l < 0) l = 0;\n")
		g.printf("    if (l > cap) l = cap;\n")
		g.printf("    (void)__st_app(dst, cap, 0, s, (int)l, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrRight] {
		g.printf("static char *__st_right(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_right(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, const char *what) {\n")
		g.printf("    int lost = 0, n = __st_len(s);\n")
		g.printf("    if (l < 0) l = 0;\n")
		g.printf("    if (l > n) l = n;\n")
		g.printf("    (void)__st_app(dst, cap, 0, s + (n - (int)l), (int)l, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrMid] {
		g.printf("static char *__st_mid(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, int64_t p, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_mid(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, int64_t p, const char *what) {\n")
		g.printf("    int lost = 0, n = __st_len(s);\n")
		g.printf("    if (p < 1) p = 1;\n")
		g.printf("    if (p > n) { dst[0] = '\\0'; return dst; }\n")
		g.printf("    if (l < 0) l = 0;\n")
		g.printf("    if (l > n - (p - 1)) l = n - (p - 1);\n")
		g.printf("    (void)__st_app(dst, cap, 0, s + (int)(p - 1), (int)l, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrInsert] {
		// P is the position after which IN2 goes, so 0 puts it at the front.
		g.printf("static char *__st_insert(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, int64_t p, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_insert(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, int64_t p, const char *what) {\n")
		g.printf("    int lost = 0, n = __st_len(a), k;\n")
		g.printf("    if (p < 0) p = 0;\n")
		g.printf("    if (p > n) p = n;\n")
		g.printf("    k = __st_app(dst, cap, 0, a, (int)p, &lost);\n")
		g.printf("    k = __st_app(dst, cap, k, b, -1, &lost);\n")
		g.printf("    (void)__st_app(dst, cap, k, a + (int)p, -1, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrDelete] {
		g.printf("static char *__st_delete(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, int64_t p, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_delete(inst_t *__comp_inst, char *dst, int cap, const char *s, int64_t l, int64_t p, const char *what) {\n")
		g.printf("    int lost = 0, n = __st_len(s), k;\n")
		g.printf("    if (p < 1) p = 1;\n")
		g.printf("    if (p > n + 1) p = n + 1;\n")
		g.printf("    if (l < 0) l = 0;\n")
		g.printf("    if (l > n - (p - 1)) l = n - (p - 1);\n")
		g.printf("    k = __st_app(dst, cap, 0, s, (int)(p - 1), &lost);\n")
		g.printf("    (void)__st_app(dst, cap, k, s + (int)(p - 1 + l), -1, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrReplace] {
		g.printf("static char *__st_replace(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, int64_t l, int64_t p, const char *what) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_replace(inst_t *__comp_inst, char *dst, int cap, const char *a, const char *b, int64_t l, int64_t p, const char *what) {\n")
		g.printf("    int lost = 0, n = __st_len(a), k;\n")
		g.printf("    if (p < 1) p = 1;\n")
		g.printf("    if (p > n + 1) p = n + 1;\n")
		g.printf("    if (l < 0) l = 0;\n")
		g.printf("    if (l > n - (p - 1)) l = n - (p - 1);\n")
		g.printf("    k = __st_app(dst, cap, 0, a, (int)(p - 1), &lost);\n")
		g.printf("    k = __st_app(dst, cap, k, b, -1, &lost);\n")
		g.printf("    (void)__st_app(dst, cap, k, a + (int)(p - 1 + l), -1, &lost);\n")
		g.printf("    if (lost) __st_fault(__comp_inst, what);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFuncs[ast.StrFind] {
		// The position of the first occurrence, one-based, or 0.  An empty
		// needle is 0 as well: there is nothing to find, and answering 1 would
		// have a search that found nothing report a hit.
		g.printf("static int __st_find(const char *a, const char *b) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_find(const char *a, const char *b) {\n")
		g.printf("    int i, j;\n")
		g.printf("    if (b[0] == '\\0') return 0;\n")
		g.printf("    for (i = 0; a[i] != '\\0'; i++) {\n")
		g.printf("        for (j = 0; b[j] != '\\0' && a[i + j] == b[j]; j++) { }\n")
		g.printf("        if (b[j] == '\\0') return i + 1;\n")
		g.printf("    }\n")
		g.printf("    return 0;\n")
		g.printf("}\n\n")
	}
}

// ---------------------------------------------------------------------------
// The *_TO_STRING family
// ---------------------------------------------------------------------------

// Writing a value out is arithmetic on digits, done here rather than through
// snprintf.
//
// This runs in the servo thread every cycle, and what printf does inside is
// exactly the kind of thing the STMAK_NONBLOCKING contract exists to keep out
// of one -- the project admits it in the log path only, wrapped in a trusted
// region and rate-limited besides. A loop over digits is a loop the compiler
// can see all of.
//
// The buffer at each call site is sized to the widest result its type can
// give, so none of these can truncate. The floating one is the exception, and
// only at the far end of the range: above about nine million million the
// decimals no longer fit an int64 scaled by a million, and past int64 entirely
// there is nothing to place. It says so rather than printing a wrong number.

// strFromKinds is the source types the family accepts, mapped to the helper
// each is written out by. Several share one: every signed integer is written
// the same way once it is an int64.
func strFromHelper(k ast.TypeKind) string {
	switch k {
	case ast.TypeBool:
		return "b"
	case ast.TypeSInt, ast.TypeInt, ast.TypeDInt, ast.TypeLInt:
		return "i"
	case ast.TypeUSInt, ast.TypeUInt, ast.TypeUDInt, ast.TypeULInt:
		return "u"
	case ast.TypeReal, ast.TypeLReal:
		return "f"
	case ast.TypeTime:
		return "t"
	case ast.TypeDateTime:
		return "dt"
	case ast.TypeDate:
		return "d"
	case ast.TypeTimeOfDay:
		return "tod"
	}
	return ""
}

// strFrom renders one *_TO_STRING call.
func (e *stEmitter) strFrom(v *ast.StrFuncExpr, buf string) string {
	g := e.g
	from := v.Args[0].ExprType()
	helper := strFromHelper(from.Kind)
	if helper == "" {
		if g.err == nil {
			g.err = fmt.Errorf("%s: no way to write out a %s", v.Pos, from)
		}
		return `""`
	}
	g.usedStrFrom[helper] = true
	if helper == "dt" || helper == "d" || helper == "tod" {
		// A point in time is split before it is written, and the split is
		// what makes a day before 1970 come out right.
		g.needDayPart = true
	}

	if helper == "f" {
		// The only one that can fail to place its value, so the only one that
		// takes somewhere to say so.
		what := cStringLiteral(fmt.Sprintf("%s_TO_STRING at %s: the value is too large to write out",
			from.String(), v.Pos))
		return fmt.Sprintf("__st_str_f(__comp_inst, %s, (int)sizeof(%s), %s, %s)",
			buf, buf, e.expr(v.Args[0]), what)
	}
	return fmt.Sprintf("__st_str_%s(%s, (int)sizeof(%s), %s)",
		helper, buf, buf, e.expr(v.Args[0]))
}

// scanStrFrom records which of the family's helpers the package reaches.
func (g *generator) scanStrFrom() map[string]bool {
	used := make(map[string]bool)
	for _, body := range g.allBodies() {
		walkStmts(body, func(ast.Stmt) {}, func(x ast.Expr) {
			v, ok := x.(*ast.StrFuncExpr)
			if !ok || v.Func != ast.StrFrom {
				return
			}
			if h := strFromHelper(v.Args[0].ExprType().Kind); h != "" {
				used[h] = true
			}
		})
	}
	return used
}

// needStrNum reports whether the signed digit writer is reached. Everything
// that pads or signs a number goes through it.
func (g *generator) needStrNum() bool {
	for _, h := range []string{"i", "f", "dt", "d", "tod"} {
		if g.usedStrFrom[h] {
			return true
		}
	}
	return false
}

// needStrUNum reports whether the unsigned digit writer is reached: by the
// unsigned integers, and by a duration, whose magnitude outgrows int64 by one
// at the very bottom of the range.
func (g *generator) needStrUNum() bool { return g.usedStrFrom["u"] || g.usedStrFrom["t"] }

// needStrWord reports whether the fixed-word writer is reached: by BOOL, and
// by a floating value that is not a number at all.
func (g *generator) needStrWord() bool { return g.usedStrFrom["b"] || g.usedStrFrom["f"] }

// needStrCivil reports whether the calendar is reached.
func (g *generator) needStrCivil() bool { return g.usedStrFrom["dt"] || g.usedStrFrom["d"] }

// emitStrFromRuntime emits the writers this component reaches, and no others.
func (g *generator) emitStrFromRuntime() {
	if len(g.usedStrFrom) == 0 {
		return
	}

	g.printf("/* ---------------------------------------------------------------------------\n")
	g.printf(" * Writing values out as text\n")
	g.printf(" *\n")
	g.printf(" * Digit arithmetic rather than snprintf: this runs in the servo thread every\n")
	g.printf(" * cycle, and a loop over digits is a loop the compiler can see all of.  Each\n")
	g.printf(" * buffer is sized to the widest result its type can give, so none of these\n")
	g.printf(" * truncates.\n")
	g.printf(" * ------------------------------------------------------------------------- */\n\n")

	if g.needStrNum() {
		// One signed writer, zero-padded to width when a field needs it --
		// which the date and clock parts do and a plain number does not.
		g.printf("static int __st_num(char *dst, int cap, int i, int64_t v, int width) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_num(char *dst, int cap, int i, int64_t v, int width) {\n")
		g.printf("    char tmp[24];\n")
		g.printf("    int n = 0;\n")
		// Negated in unsigned, because the most negative int64 has no
		// positive counterpart and -v on it is undefined.
		g.printf("    uint64_t u = (v < 0) ? (uint64_t)(-(v + 1)) + 1u : (uint64_t)v;\n")
		g.printf("    do { tmp[n++] = (char)('0' + (int)(u %% 10u)); u /= 10u; } while (u != 0);\n")
		g.printf("    while (n < width && n < (int)sizeof(tmp)) tmp[n++] = '0';\n")
		g.printf("    if (v < 0 && i < cap - 1) dst[i++] = '-';\n")
		g.printf("    while (n > 0 && i < cap - 1) dst[i++] = tmp[--n];\n")
		g.printf("    dst[i] = '\\0';\n")
		g.printf("    return i;\n")
		g.printf("}\n\n")
	}

	if g.needStrUNum() {
		g.printf("static int __st_unum(char *dst, int cap, int i, uint64_t u) STMAK_NONBLOCKING;\n")
		g.printf("static int __st_unum(char *dst, int cap, int i, uint64_t u) {\n")
		g.printf("    char tmp[24];\n")
		g.printf("    int n = 0;\n")
		g.printf("    do { tmp[n++] = (char)('0' + (int)(u %% 10u)); u /= 10u; } while (u != 0);\n")
		g.printf("    while (n > 0 && i < cap - 1) dst[i++] = tmp[--n];\n")
		g.printf("    dst[i] = '\\0';\n")
		g.printf("    return i;\n")
		g.printf("}\n\n")
	}

	if g.needStrWord() {
		g.printf("static char *__st_word(char *dst, int cap, const char *s) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_word(char *dst, int cap, const char *s) {\n")
		g.printf("    int i = 0;\n")
		g.printf("    while (s[i] != '\\0' && i < cap - 1) { dst[i] = s[i]; i++; }\n")
		g.printf("    dst[i] = '\\0';\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFrom["b"] {
		// IEC's spelling, upper case, which is what a BOOL literal is
		// written as and so what reads back as one.
		g.printf("static char *__st_str_b(char *dst, int cap, int v) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_str_b(char *dst, int cap, int v) {\n")
		g.printf("    return __st_word(dst, cap, v ? \"TRUE\" : \"FALSE\");\n")
		g.printf("}\n\n")
	}

	if g.usedStrFrom["i"] {
		g.printf("static char *__st_str_i(char *dst, int cap, int64_t v) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_str_i(char *dst, int cap, int64_t v) {\n")
		g.printf("    (void)__st_num(dst, cap, 0, v, 0);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFrom["u"] {
		g.printf("static char *__st_str_u(char *dst, int cap, uint64_t v) STMAK_NONBLOCKING;\n")
		g.printf("static char *__st_str_u(char *dst, int cap, uint64_t v) {\n")
		g.printf("    (void)__st_unum(dst, cap, 0, v);\n")
		g.printf("    return dst;\n")
		g.printf("}\n\n")
	}

	if g.usedStrFrom["f"] {
		g.emitStrFloat()
	}
	if g.usedStrFrom["t"] {
		g.emitStrDuration()
	}
	if g.needStrCivil() {
		g.emitStrCivil()
	}
	if g.usedStrFrom["dt"] || g.usedStrFrom["d"] {
		g.emitStrDay()
	}
	if g.usedStrFrom["dt"] || g.usedStrFrom["tod"] {
		g.emitStrClock()
	}
	if g.usedStrFrom["dt"] {
		g.emitStrStamp()
	}
}

// emitStrFloat writes a floating value as a fixed-point decimal.
func (g *generator) emitStrFloat() {
	// Six decimals, which is what the rest of stratuMAK prints a float to --
	// halsampler and the filestream capture both -- so a number logged from a
	// component and the same number captured from its pin read alike.
	g.printf("#define __ST_F_DEC 1000000LL\n")
	// Above this the scaled value no longer fits an int64, so the decimals go
	// and only the whole part is written; past int64 there is nothing to
	// place at all.
	g.printf("#define __ST_F_EXACT 9.0e12\n\n")
	g.printf("static char *__st_str_f(inst_t *__comp_inst, char *dst, int cap, double v, const char *what) STMAK_NONBLOCKING;\n")
	g.printf("static char *__st_str_f(inst_t *__comp_inst, char *dst, int cap, double v, const char *what) {\n")
	g.printf("    char frac[6];\n")
	g.printf("    int i, k, n, neg = 0;\n")
	g.printf("    int64_t whole = 0, f = 0;\n")
	g.printf("\n")
	// A value that is not equal to itself is the one thing a comparison can
	// say about a NaN without <math.h>.
	g.printf("    if (v != v) return __st_word(dst, cap, \"NaN\");\n")
	g.printf("    if (v < 0.0) { neg = 1; v = -v; }\n")
	g.printf("    if (!(v < 9.2e18)) {\n")
	g.printf("        __st_fault(__comp_inst, what);\n")
	g.printf("        return __st_word(dst, cap, neg ? \"-inf\" : \"inf\");\n")
	g.printf("    }\n")
	g.printf("    if (v < __ST_F_EXACT) {\n")
	g.printf("        int64_t scaled = (int64_t)(v * (double)__ST_F_DEC + 0.5);\n")
	g.printf("        whole = scaled / __ST_F_DEC;\n")
	g.printf("        f = scaled %% __ST_F_DEC;\n")
	g.printf("    } else {\n")
	g.printf("        whole = (int64_t)v;\n")
	g.printf("    }\n")
	g.printf("\n")
	g.printf("    i = __st_num(dst, cap, 0, neg ? -whole : whole, 0);\n")
	// A whole part of zero loses its sign to the negation above, so -0.5
	// would come out as 0.5 without this.
	g.printf("    if (neg && whole == 0) {\n")
	g.printf("        i = 0;\n")
	g.printf("        if (i < cap - 1) dst[i++] = '-';\n")
	g.printf("        if (i < cap - 1) dst[i++] = '0';\n")
	g.printf("    }\n")
	// Trailing zeros go, so 1.5 is 1.5 and 2.0 is 2.
	g.printf("    if (f != 0) {\n")
	g.printf("        for (k = 5; k >= 0; k--) { frac[k] = (char)('0' + (int)(f %% 10)); f /= 10; }\n")
	g.printf("        k = 5;\n")
	g.printf("        while (k > 0 && frac[k] == '0') k--;\n")
	g.printf("        if (i < cap - 1) dst[i++] = '.';\n")
	g.printf("        for (n = 0; n <= k && i < cap - 1; n++) dst[i++] = frac[n];\n")
	g.printf("    }\n")
	g.printf("    dst[i] = '\\0';\n")
	g.printf("    return dst;\n")
	g.printf("}\n\n")
}

// emitStrDuration writes a duration as the T# literal it is.
func (g *generator) emitStrDuration() {
	g.printf("static char *__st_str_t(char *dst, int cap, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static char *__st_str_t(char *dst, int cap, int64_t ns) {\n")
	g.printf("    static const char *const unit[7] = {\"d\", \"h\", \"m\", \"s\", \"ms\", \"us\", \"ns\"};\n")
	g.printf("    static const uint64_t worth[7] = {\n")
	g.printf("        86400000000000ULL, 3600000000000ULL, 60000000000ULL,\n")
	g.printf("        1000000000ULL, 1000000ULL, 1000ULL, 1ULL };\n")
	g.printf("    int i = 0, k, n, any = 0;\n")
	g.printf("    uint64_t u = (ns < 0) ? (uint64_t)(-(ns + 1)) + 1u : (uint64_t)ns;\n")
	g.printf("    if (i < cap - 1) dst[i++] = 'T';\n")
	g.printf("    if (i < cap - 1) dst[i++] = '#';\n")
	g.printf("    if (ns < 0 && i < cap - 1) dst[i++] = '-';\n")
	g.printf("    for (k = 0; k < 7; k++) {\n")
	g.printf("        uint64_t q = u / worth[k];\n")
	g.printf("        if (q == 0) continue;\n")
	g.printf("        u -= q * worth[k];\n")
	g.printf("        i = __st_unum(dst, cap, i, q);\n")
	g.printf("        for (n = 0; unit[k][n] != '\\0' && i < cap - 1; n++) dst[i++] = unit[k][n];\n")
	g.printf("        any = 1;\n")
	g.printf("    }\n")
	// A duration of nothing still has to say so, and T#0s is how it is
	// written.
	g.printf("    if (!any) {\n")
	g.printf("        if (i < cap - 1) dst[i++] = '0';\n")
	g.printf("        if (i < cap - 1) dst[i++] = 's';\n")
	g.printf("    }\n")
	g.printf("    dst[i] = '\\0';\n")
	g.printf("    return dst;\n")
	g.printf("}\n\n")
}

// emitStrCivil turns a count of days since 1970 into a calendar date.
func (g *generator) emitStrCivil() {
	// Howard Hinnant's civil_from_days, which is exact for every year an
	// int64 of nanoseconds can reach and needs no table and no branch on leap
	// years -- the four-century era it divides by has a whole number of days.
	g.printf("static void __st_civil(int64_t z, int *yy, int *mm, int *dd) STMAK_NONBLOCKING;\n")
	g.printf("static void __st_civil(int64_t z, int *yy, int *mm, int *dd) {\n")
	g.printf("    int64_t era, doe, yoe, y, doy, mp;\n")
	g.printf("    int d, m;\n")
	// Shifted so the era starts on 0000-03-01, which puts the leap day at the
	// end of the year and out of the arithmetic's way.
	g.printf("    z += 719468;\n")
	g.printf("    era = (z >= 0 ? z : z - 146096) / 146097;\n")
	g.printf("    doe = z - era * 146097;\n")
	g.printf("    yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;\n")
	g.printf("    y = yoe + era * 400;\n")
	g.printf("    doy = doe - (365 * yoe + yoe / 4 - yoe / 100);\n")
	g.printf("    mp = (5 * doy + 2) / 153;\n")
	g.printf("    d = (int)(doy - (153 * mp + 2) / 5 + 1);\n")
	g.printf("    m = (int)(mp < 10 ? mp + 3 : mp - 9);\n")
	g.printf("    *yy = (int)(y + (m <= 2 ? 1 : 0));\n")
	g.printf("    *mm = m;\n")
	g.printf("    *dd = d;\n")
	g.printf("}\n\n")
}

// emitStrDay writes the date part, and a DATE in full.
func (g *generator) emitStrDay() {
	g.printf("static int __st_day_at(char *dst, int cap, int i, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static int __st_day_at(char *dst, int cap, int i, int64_t ns) {\n")
	g.printf("    int y = 0, m = 0, d = 0;\n")
	// Floored through the same helper the DT_TO_DATE split uses, so a day
	// before 1970 lands on the day it is rather than the one after.
	g.printf("    __st_civil(__st_day(ns) / __ST_NS_PER_DAY, &y, &m, &d);\n")
	g.printf("    i = __st_num(dst, cap, i, y, 4);\n")
	g.printf("    if (i < cap - 1) dst[i++] = '-';\n")
	g.printf("    i = __st_num(dst, cap, i, m, 2);\n")
	g.printf("    if (i < cap - 1) dst[i++] = '-';\n")
	g.printf("    return __st_num(dst, cap, i, d, 2);\n")
	g.printf("}\n\n")

	if !g.usedStrFrom["d"] {
		return
	}
	g.printf("static char *__st_str_d(char *dst, int cap, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static char *__st_str_d(char *dst, int cap, int64_t ns) {\n")
	g.printf("    int i = 0;\n")
	g.printf("    if (i < cap - 1) dst[i++] = 'D';\n")
	g.printf("    if (i < cap - 1) dst[i++] = '#';\n")
	g.printf("    (void)__st_day_at(dst, cap, i, ns);\n")
	g.printf("    return dst;\n")
	g.printf("}\n\n")
}

// emitStrClock writes the time-of-day part, and a TIME_OF_DAY in full.
func (g *generator) emitStrClock() {
	g.printf("static int __st_clock_at(char *dst, int cap, int i, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static int __st_clock_at(char *dst, int cap, int i, int64_t ns) {\n")
	g.printf("    int64_t t = __st_tod(ns);\n")
	g.printf("    int64_t sub = t %% 1000000000LL;\n")
	g.printf("    t /= 1000000000LL;\n")
	g.printf("    i = __st_num(dst, cap, i, t / 3600, 2);\n")
	g.printf("    if (i < cap - 1) dst[i++] = ':';\n")
	g.printf("    i = __st_num(dst, cap, i, (t / 60) %% 60, 2);\n")
	g.printf("    if (i < cap - 1) dst[i++] = ':';\n")
	g.printf("    i = __st_num(dst, cap, i, t %% 60, 2);\n")
	// Only when there is one: a whole second reads better without nine zeros
	// after it, and the literal takes either form.
	g.printf("    if (sub != 0) {\n")
	g.printf("        if (i < cap - 1) dst[i++] = '.';\n")
	g.printf("        i = __st_num(dst, cap, i, sub, 9);\n")
	g.printf("    }\n")
	g.printf("    return i;\n")
	g.printf("}\n\n")

	if !g.usedStrFrom["tod"] {
		return
	}
	g.printf("static char *__st_str_tod(char *dst, int cap, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static char *__st_str_tod(char *dst, int cap, int64_t ns) {\n")
	g.printf("    int i = 0;\n")
	g.printf("    const char *p = \"TOD#\";\n")
	g.printf("    while (*p != '\\0' && i < cap - 1) dst[i++] = *p++;\n")
	g.printf("    (void)__st_clock_at(dst, cap, i, ns);\n")
	g.printf("    return dst;\n")
	g.printf("}\n\n")
}

// emitStrStamp writes a point in time as the DT# literal it is.
func (g *generator) emitStrStamp() {
	g.printf("static char *__st_str_dt(char *dst, int cap, int64_t ns) STMAK_NONBLOCKING;\n")
	g.printf("static char *__st_str_dt(char *dst, int cap, int64_t ns) {\n")
	g.printf("    int i = 0;\n")
	g.printf("    const char *p = \"DT#\";\n")
	g.printf("    while (*p != '\\0' && i < cap - 1) dst[i++] = *p++;\n")
	g.printf("    i = __st_day_at(dst, cap, i, ns);\n")
	g.printf("    if (i < cap - 1) dst[i++] = '-';\n")
	g.printf("    (void)__st_clock_at(dst, cap, i, ns);\n")
	g.printf("    return dst;\n")
	g.printf("}\n\n")
}
