//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package hal

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include

#include <stdlib.h>
#include <stddef.h>
#include "hal.h"
#include "rtapi.h"

// go_hal_export_funct hands the caller's function address to hal_export_funct
// as a hal_funct_ptr_t.
//
// The cast lives here, in C, because cgo has no way to spell it: a C function
// pointer surfaces on the Go side as *[0]byte, an opaque token with no relation
// to hal_funct_ptr_t. Going through void * and assigning through the pointer
// (rather than casting the value) is the round trip POSIX guarantees and dlsym
// already relies on; it also keeps -Wpedantic quiet.
static int go_hal_export_funct(const char *name, void *funct, void *arg,
                               int uses_fp, int reentrant, int comp_id) {
    hal_funct_ptr_t fp;
    *(void **)(&fp) = funct;
    return hal_export_funct(name, fp, arg, uses_fp, reentrant, comp_id);
}
*/
import "C"

import "unsafe"

// CFunct is the address of a C function with the HAL cyclic-function signature
//
//	void f(void *arg, long period)
//
// declared STMAK_NONBLOCKING (stmak_hal.h's stmak_hal_funct_t, hal.h's
// hal_funct_ptr_t — the same type under two self-contained names).
//
// It is deliberately not a Go func and there is no overload of ExportFunct that
// takes one. A cgo call into Go from the servo thread means scheduler entry,
// possible stack growth and GC interaction, and it is invisible to
// -Wfunction-effects, which cannot see across the cgo boundary — the checker
// would report green over precisely the code that broke the RT path. Keeping
// the only accepted shape a C address is what makes "no Go in the cycle"
// structural rather than documentary.
//
// The function must be DEFINED in a real .c file beside the Go source, never in
// the cgo preamble: a normal translation unit is what "make rt-effects-check"
// can compile with -Wfunction-effects, while a preamble is compiled by cgo's
// own invocation and is not covered. Add the new .c file to that check in the
// same change — a checker that silently stops covering the newest RT code in
// the tree is worse than no checker.
//
// cgo cannot take the address of a C function (C.my_rt_funct is cgo's own call
// wrapper, whose address means nothing to HAL), so the .c file publishes it as
// an ordinary symbol and the preamble includes the header that declares it:
//
//	// my_rt.h
//	void my_rt_funct(void *arg, long period) STMAK_NONBLOCKING;
//	extern const stmak_hal_funct_t my_rt_funct_fp;
//
//	// my_rt.c
//	void my_rt_funct(void *arg, long period) { ... }
//	const stmak_hal_funct_t my_rt_funct_fp = my_rt_funct;
//
//	// in Go
//	fn := hal.CFunct(unsafe.Pointer(C.my_rt_funct_fp))
//
// internal/hallib/rtfuncttest is that shape end to end, and pkg/hal's own tests
// run it on a real HAL thread.
type CFunct unsafe.Pointer

// ExportFunct registers a cyclic function with HAL, under the component's name
// ("mycomp." + name), so a HAL file can put it on a thread with addf.
//
// arg is passed to every invocation. It must point at memory the cyclic
// function may walk from the servo thread — an RTCalloc block, or HAL shared
// memory — never at the Go heap: a Go pointer stored in C memory is exactly
// what the cgo pointer rules forbid, and the collector would be free to move
// what the RT thread is reading.
//
// usesFP declares that the function uses floating point, so HAL saves and
// restores the FPU state around it. reentrant declares that two threads may run
// it concurrently; almost every function wants false.
//
// Call it from the module factory, as cmods do from New(). The addf lines of a
// HAL file execute after every load and before any module is started, so a
// function exported any later would not exist when its addf runs.
//
// The component must have been created with NewRTComponent and must not be
// Ready yet (both are hal_export_funct preconditions, checked here first).
//
// Teardown is the launcher's, and it already happens: on runtime unload it
// removes the component's functions from every thread and waits for a full
// cycle of every realtime thread before calling Destroy; at shutdown the
// synchronous StopThreads is the same barrier. Either way nothing is still
// executing the function by the time Destroy runs, which is what makes it safe
// for Destroy to RTFree the structure the function was walking. Freeing it
// anywhere earlier — in Stop, say — is a use-after-free in the servo thread.
func (c *Component) ExportFunct(name string, fn CFunct, arg unsafe.Pointer, usesFP, reentrant bool) error {
	fullName, err := qualifyName(c, "ExportFunct", name)
	if err != nil {
		return err
	}
	if fn == nil {
		return newError("ExportFunct", ErrNilFunct.Message, ErrNilFunct.Code)
	}

	return c.createFunct("ExportFunct", fullName, func() error {
		cName := C.CString(fullName)
		defer C.free(unsafe.Pointer(cName))

		ret := C.go_hal_export_funct(cName, unsafe.Pointer(fn), arg,
			cBool(usesFP), cBool(reentrant), C.int(c.id))
		return halError(int(ret), "hal_export_funct")
	})
}

// cBool renders a Go bool as the 0/1 int the HAL C API takes.
func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// RTCalloc allocates n zeroed bytes with the RT-hardened allocator
// (rtapi_calloc: mlock'd and page-faulted at allocation time), and returns nil
// on failure or for n == 0.
//
// This is where a structure a cyclic function walks belongs. The pattern is the
// one the EtherCAT driver uses: the high-level work happens once at init, in
// whatever language suits it, and assembles a flat structure here; the cyclic
// function then walks that structure and nothing else. Three properties follow
// and between them remove the need for any synchronisation:
//
//   - Same address space. Go modules are compiled in, so there is no
//     shared-memory boundary to lay out and no marshalling.
//   - The collector never sees it. The block is not Go heap, so there is
//     nothing to pin, move or collect.
//   - Immutable after init. Nothing writes it once the threads are running, so
//     the cyclic reader needs no lock, no atomics and no double-buffer.
//
// A structure that has to CHANGE at runtime does not get to skip that last
// point: it needs explicit atomics, or a published pointer swapped with release
// semantics, the way the halscope RT↔Go ring does.
//
// The cgo pointer rules apply while assembling it: a Go pointer may be passed
// to C for the duration of a call and must not be retained, so init-time
// assembly copies out of Go slices into the block while the call is on the
// stack. It never stores a Go pointer in C memory.
//
// Every block must be released with RTFree, and only after the launcher's RT
// barrier — see ExportFunct.
func RTCalloc(n uintptr) unsafe.Pointer {
	if n == 0 {
		return nil
	}
	return C.rtapi_calloc(C.size_t(n))
}

// RTFree releases a block obtained from RTCalloc. A nil pointer is a no-op.
//
// Call it from the module's Destroy, never from Stop: Stop runs while the
// cyclic function is still on its thread (deliberately — see the stmak.Module
// lifecycle), and only Destroy is past the launcher's RT barrier.
func RTFree(p unsafe.Pointer) {
	if p == nil {
		return
	}
	C.rtapi_free(p)
}
