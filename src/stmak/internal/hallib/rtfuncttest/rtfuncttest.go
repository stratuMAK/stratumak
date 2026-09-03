// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package rtfuncttest is the C half of a Go module, in miniature, for pkg/hal's
// ExportFunct and RTCalloc tests.
//
// cgo is not permitted in _test.go files, so the preamble that names the cyclic
// function lives here in a normal (test-support) package — the same reason
// internal/hallib/halnettest and internal/hallib/rtapialloc exist. Only test
// binaries import it, so none of it reaches the production stmakd binary.
//
// It is also the worked example of the convention documented on hal.CFunct:
// the cyclic function is DEFINED in rtfuncttest_rt.c, a real translation unit
// that "make rt-effects-check" compiles with -Wfunction-effects, and the
// preamble below only includes its header. Its address reaches Go as an
// ordinary exported symbol (rtft_check_fp), because cgo cannot take the address
// of a C function.
package rtfuncttest

/*
#cgo CFLAGS: -I${SRCDIR}/../../../pkg/cmodule -I${SRCDIR}/../../../../hal -I${SRCDIR}/../../../.. -I${SRCDIR}/../../../../rtapi -I${SRCDIR}/../../../../../include

#include "rtfuncttest_rt.h"

// call_direct invokes the cyclic function without a HAL thread, so a test can
// exercise the walk without RT privileges.
static void call_direct(rtft_scene_t *s, long period) {
    rtft_check(s, period);
}

static void set_query(rtft_scene_t *s, double x, double y) { s->px = x; s->py = y; }
static void set_out(rtft_scene_t *s, void *slot) { s->out = (stmak_hal_bit_t **)slot; }
static long get_calls(rtft_scene_t *s) { return s->calls; }
static long get_last_period(rtft_scene_t *s) { return s->last_period; }
*/
import "C"

import (
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"

	_ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib"
)

// Point is one vertex, in the same units the C side stores.
type Point struct{ X, Y float64 }

// Scene is a handle on one assembled block. It is deliberately not a Go struct
// mirroring the C one: everything the cyclic function reads lives in the
// RTCalloc block, and Go only ever reaches it through the accessors below.
type Scene struct{ p unsafe.Pointer }

// Build assembles polys into a single RT-hardened allocation and returns a
// handle on it, or a zero Scene when the allocation failed.
//
// The copy out of the Go slices happens while this call is on the stack, which
// is what keeps it inside the cgo pointer rules: no Go pointer is ever stored
// in the C block.
func Build(polys [][]Point) Scene {
	npoints := 0
	for _, p := range polys {
		npoints += len(p)
	}

	size := uintptr(C.rtft_size(C.int(len(polys)), C.int(npoints)))
	blk := hal.RTCalloc(size)
	if blk == nil {
		return Scene{}
	}
	C.rtft_layout(blk, C.int(len(polys)), C.int(npoints))

	s := (*C.rtft_scene_t)(blk)
	idx := 0
	for i, poly := range polys {
		first := idx
		for _, pt := range poly {
			C.rtft_set_point(s, C.int(idx), C.double(pt.X), C.double(pt.Y))
			idx++
		}
		if len(poly) > 0 {
			C.rtft_set_poly(s, C.int(i), C.int(first), C.int(len(poly)))
		}
	}
	return Scene{p: blk}
}

// Valid reports whether the scene holds an allocation.
func (s Scene) Valid() bool { return s.p != nil }

// Arg is the void* to hand ExportFunct.
func (s Scene) Arg() unsafe.Pointer { return s.p }

// Free releases the block. Only legal once nothing can still be executing the
// cyclic function — see hal.ExportFunct on where that barrier sits.
func (s Scene) Free() { hal.RTFree(s.p) }

// SetQuery moves the query point. A real module would take it from a pin or an
// RT-safe GMI accessor inside the cyclic function; writing it from outside is
// how the test drives the walk.
func (s Scene) SetQuery(x, y float64) {
	C.set_query((*C.rtft_scene_t)(s.p), C.double(x), C.double(y))
}

// SetOut wires a HAL bit pin's data-pointer slot (hal.Pin.RTDataPtr) as the
// scene's output, or clears it with nil.
func (s Scene) SetOut(slot unsafe.Pointer) {
	C.set_out((*C.rtft_scene_t)(s.p), slot)
}

// Calls returns how many times the cyclic function has run.
func (s Scene) Calls() int64 { return int64(C.get_calls((*C.rtft_scene_t)(s.p))) }

// LastPeriod returns the period argument of the most recent call, in
// nanoseconds — HAL's own view of the thread the function sits on.
func (s Scene) LastPeriod() int64 {
	return int64(C.get_last_period((*C.rtft_scene_t)(s.p)))
}

// Invoke runs the cyclic function directly, without a HAL thread.
func (s Scene) Invoke(periodNsec int64) {
	C.call_direct((*C.rtft_scene_t)(s.p), C.long(periodNsec))
}

// Funct is the address of the cyclic function, in the form ExportFunct takes.
func Funct() hal.CFunct { return hal.CFunct(unsafe.Pointer(C.rtft_check_fp)) }
