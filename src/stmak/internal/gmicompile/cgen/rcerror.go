// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// --- @rc_error: status return + payload out-param ---
//
// A callback that hands its payload back as the C return value has nowhere to
// report a failure, so every consumer reads a zeroed struct as a valid answer.
// @rc_error is the fix: the i32 return is the status channel and the payload
// travels in an out parameter.
//
// The two Go-facing signatures are deliberately unchanged by the conversion —
// consumer `M(in...) (T, error)` and provider `M(in...) (T, error)` — so an IDL
// can adopt the shape without touching a single Go call site. The churn lands in
// C, where the payload becomes a pointer parameter.

// rcErrorPayload returns the single out parameter of an @rc_error function.
// Callers that only ever see REST/WS-dispatched functions can rely on it being
// present: the checker rejects a REST-routed @rc_error func with none or more
// than one.
func rcErrorPayload(fn ast.Func) (ast.Param, bool) {
	outs := fn.RcErrorOuts()
	if len(outs) != 1 {
		return ast.Param{}, false
	}
	return outs[0], true
}

// restView returns the function as the REST/WebSocket surface sees it: for an
// @rc_error func the payload out-param becomes the declared return and drops out
// of the parameter list, which is exactly the value-returning shape the func had
// before conversion. Every marshaling emitter (Go/TS/Python clients, the WS
// command handlers) works on this view, so the wire format — request fields and
// response body — is identical either way, and the conversion of an existing API
// is invisible to its remote clients. Non-@rc_error functions pass through
// unchanged.
func restView(fn ast.Func) ast.Func {
	payload, ok := rcErrorPayload(fn)
	if !ok {
		return fn
	}
	view := fn
	view.Return = &payload.Type
	view.Params = nil
	for _, p := range fn.Params {
		if !p.IsOut {
			view.Params = append(view.Params, p)
		}
	}
	return view
}

// sliceOutCTypeName returns the C struct that carries a slice-valued out
// parameter: `<api>_<elem>_slice_t`, holding a malloc'd `data` pointer and a
// `len`. It is keyed by element type, not by function, so several functions
// handing back the same element type share one struct.
//
// A slice cannot be an out-parameter the way a struct can — the length is not
// known to the caller, so there is nothing to preallocate — which is why the
// payload needs this owning wrapper. Ownership follows the same rule as a slice
// return: the provider mallocs, the caller frees.
func sliceOutCTypeName(apiName string, t ast.TypeRef) string {
	return fmt.Sprintf("%s_%s_slice_t", apiName, sliceElemTag(t))
}

// sliceElemTag is the identifier fragment naming a slice's element type.
func sliceElemTag(t ast.TypeRef) string {
	elem := t.Elem
	if elem == nil {
		return "void"
	}
	switch elem.Kind {
	case ast.TypeNamed:
		return toSnakeCase(elem.Name)
	case ast.TypePrimitive:
		return elem.Name
	}
	return "void"
}

// isSliceOut reports whether p is a slice-valued out parameter, the shape that
// travels in a `<api>_<elem>_slice_t` rather than as a bare pointer.
func isSliceOut(p ast.Param) bool {
	return p.IsOut && p.Type.Kind == ast.TypeSlice
}

// sliceOutParams returns every distinct slice-out element type used by the API,
// in declaration order — the set of `_slice_t` structs the header must define.
func sliceOutParams(api *ast.API) []ast.TypeRef {
	seen := map[string]bool{}
	var out []ast.TypeRef
	for _, fn := range api.Funcs {
		if fn.Publish {
			continue
		}
		for _, p := range fn.Params {
			if !isSliceOut(p) {
				continue
			}
			tag := sliceElemTag(p.Type)
			if seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, p.Type)
		}
	}
	return out
}
