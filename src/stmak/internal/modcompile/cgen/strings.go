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
// of its own. LEN and FIND count characters instead and need none.
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
	if !g.usedStrFuncs[v.Func] && g.err == nil {
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

// anyStrProducer reports whether any string function builds a result, and so
// whether the append primitive and the fault reporter are reached.
func (g *generator) anyStrProducer() bool {
	if g.usedStrFuncs == nil {
		// The instance struct is laid out before the bodies are emitted, and
		// it needs the fault counter that a truncated result increments -- so
		// the scan has to be able to answer before generate() runs it.
		g.usedStrFuncs = g.scanStrFuncs()
	}
	for f := range g.usedStrFuncs {
		if producesString(f) {
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
