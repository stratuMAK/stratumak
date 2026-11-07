// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package rtbridgetest is a cyclic function using the whole RT bridge, for
// pkg/hal's TriBuf, PinMap and Doorbell tests.
//
// cgo is not permitted in _test.go files, so the preamble lives here in a
// test-support package, like internal/hallib/rtfuncttest. Only test binaries
// import it. The function itself is in rtbridgetest_rt.c, a real translation
// unit that "make rt-effects-check" verifies.
package rtbridgetest

/*
#cgo CFLAGS: -I${SRCDIR}/../../../pkg/cmodule -I${SRCDIR}/../../../../hal -I${SRCDIR}/../../../.. -I${SRCDIR}/../../../../rtapi -I${SRCDIR}/../../../../../include

#include "rtbridgetest_rt.h"

static void call_direct(void *r, long period) { rbt_cycle(r, period); }

static void link_rig(void *blk, void *to_rt, void *to_go, void *in, void *out, void *bell) {
    rbt_t *r = (rbt_t *)blk;
    r->to_rt = (stmak_tribuf_t *)to_rt;
    r->to_go = (stmak_tribuf_t *)to_go;
    r->in    = (stmak_pinmap_t *)in;
    r->out   = (stmak_pinmap_t *)out;
    r->bell  = (stmak_doorbell_t *)bell;
}

static size_t rig_size(void) { return sizeof(rbt_t); }
static uint32_t rig_cycle(void *blk) { return ((rbt_t *)blk)->cycle; }
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"

	_ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib"
)

// The frames, through their cgo types. The tests build and compare them with
// untyped constants.
type (
	Pins    = C.rbt_pins_t
	GoFrame = C.rbt_go_frame_t
	RTFrame = C.rbt_rt_frame_t
)

// Rig is one function instance with everything it walks.
type Rig struct {
	ToRT *hal.TriBuf[GoFrame]
	ToGo *hal.TriBuf[RTFrame]
	In   *hal.PinMap
	Out  *hal.PinMap
	Bell *hal.Doorbell
	blk  unsafe.Pointer
}

// New allocates a rig whose maps take up to capacity pins each.
func New(capacity int) (*Rig, error) {
	r := &Rig{}
	var err error
	fail := func(e error) (*Rig, error) { r.Free(); return nil, e }

	if r.ToRT, err = hal.NewTriBuf[GoFrame](); err != nil {
		return fail(err)
	}
	if r.ToGo, err = hal.NewTriBuf[RTFrame](); err != nil {
		return fail(err)
	}
	if r.In, err = hal.NewPinMap(hal.In, unsafe.Sizeof(Pins{}), capacity); err != nil {
		return fail(err)
	}
	if r.Out, err = hal.NewPinMap(hal.Out, unsafe.Sizeof(Pins{}), capacity); err != nil {
		return fail(err)
	}
	if r.Bell, err = hal.NewDoorbell(); err != nil {
		return fail(err)
	}
	if r.blk = hal.RTCalloc(uintptr(C.rig_size())); r.blk == nil {
		return fail(errors.New("rtbridgetest: RTCalloc failed"))
	}
	C.link_rig(r.blk, r.ToRT.RTPtr(), r.ToGo.RTPtr(), r.In.RTPtr(), r.Out.RTPtr(), r.Bell.RTPtr())
	return r, nil
}

// BindIn binds one pin of each type into the In map.
func (r *Rig) BindIn(b *hal.Pin[bool], s *hal.Pin[int32], u *hal.Pin[uint32], f *hal.Pin[float64]) error {
	return bindAll(r.In, b, s, u, f)
}

// BindOut binds one pin of each type into the Out map.
func (r *Rig) BindOut(b *hal.Pin[bool], s *hal.Pin[int32], u *hal.Pin[uint32], f *hal.Pin[float64]) error {
	return bindAll(r.Out, b, s, u, f)
}

func bindAll(m *hal.PinMap, b *hal.Pin[bool], s *hal.Pin[int32], u *hal.Pin[uint32], f *hal.Pin[float64]) error {
	var p Pins
	base := unsafe.Pointer(&p)
	return errors.Join(
		hal.BindBit(m, b, base, &p.B),
		hal.BindS32(m, s, base, &p.S),
		hal.BindU32(m, u, base, &p.U),
		hal.BindFloat(m, f, base, &p.F),
	)
}

// Arg is the function's arg block.
func (r *Rig) Arg() unsafe.Pointer { return r.blk }

// Funct is the function's address, for ExportFunct.
func Funct() hal.CFunct { return hal.CFunct(unsafe.Pointer(C.rbt_cycle_fp)) }

// Invoke runs the function once without a HAL thread.
func (r *Rig) Invoke(period int64) { C.call_direct(r.blk, C.long(period)) }

// Cycles is the function's own run counter.
func (r *Rig) Cycles() uint32 { return uint32(C.rig_cycle(r.blk)) }

// Free releases everything. Only once the function is off every thread.
func (r *Rig) Free() {
	if r.ToRT != nil {
		r.ToRT.Free()
	}
	if r.ToGo != nil {
		r.ToGo.Free()
	}
	if r.In != nil {
		r.In.Free()
	}
	if r.Out != nil {
		r.Out.Free()
	}
	if r.Bell != nil {
		r.Bell.Free()
	}
	hal.RTFree(r.blk)
	r.blk = nil
}
