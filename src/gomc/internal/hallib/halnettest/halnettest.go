// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package halnettest provides HAL signal/net/link primitives for tests.
//
// pkg/hal deliberately exposes no signal, net, or port-alloc surface — netting
// pins to signals is halcmd's / the launcher's job, not the binding layer's.
// Exercising the linked path in a unit test (the reason Pin.Get/Set dereference
// the HAL pointer at access time: hal_link repoints a pin's data-pointer slot at
// the signal's data cell) needs to net two pins to a shared signal. cgo is not
// permitted in _test.go files, so these helpers live in a normal (test-support)
// package — like internal/hallib/hallibtest — instead of inline in the test.
//
// They use the same C primitives halcmd uses (hal_signal_new, hal_link, and
// hal_port_alloc into the signal data cell for the HAL_PORT buffer), so callers
// get real netted behaviour without pulling in internal/halcmd.
package halnettest

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../hal -I${SRCDIR}/../../../.. -I${SRCDIR}/../../../../rtapi -I${SRCDIR}/../../../../../include

#include <stdlib.h>
#include <errno.h>
#include "hal.h"
#include "hal_priv.h"

// alloc_port_sig allocates a size-byte buffer for an existing HAL_PORT signal
// and stores the port handle in the signal's data cell. This mirrors halcmd's
// setValue() HAL_PORT branch (hal_port_alloc into the signal data pointer) — a
// port pin has no buffer until its signal is sized this way.
static int alloc_port_sig(const char *name, unsigned size) {
    hal_sig_t *sig;
    int port;
    if (hal_data == NULL) return -EINVAL;
    rtapi_mutex_get(&(hal_data->mutex));
    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        return -ENOENT;
    }
    if (sig->type != HAL_PORT) {
        rtapi_mutex_give(&(hal_data->mutex));
        return -EINVAL;
    }
    port = hal_port_alloc(size);
    if (port < 0) {
        rtapi_mutex_give(&(hal_data->mutex));
        return port;
    }
    *((hal_port_t *)SHMPTR(sig->data_ptr)) = port;
    rtapi_mutex_give(&(hal_data->mutex));
    return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// NewSignal creates a HAL signal of the given hal_type_t value. Returns an error
// carrying the C return code on failure.
func NewSignal(name string, halType int) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if rc := C.hal_signal_new(cname, C.hal_type_t(halType)); rc != 0 {
		return fmt.Errorf("hal_signal_new(%q, %d): rc=%d", name, halType, int(rc))
	}
	return nil
}

// Link links a pin to a signal (hal_link). Returns an error carrying the C
// return code on failure.
func Link(pin, sig string) error {
	cpin := C.CString(pin)
	defer C.free(unsafe.Pointer(cpin))
	csig := C.CString(sig)
	defer C.free(unsafe.Pointer(csig))
	if rc := C.hal_link(cpin, csig); rc != 0 {
		return fmt.Errorf("hal_link(%q, %q): rc=%d", pin, sig, int(rc))
	}
	return nil
}

// AllocPortSignal sizes a HAL_PORT signal's backing buffer to size bytes.
func AllocPortSignal(name string, size uint) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if rc := C.alloc_port_sig(cname, C.uint(size)); rc != 0 {
		return fmt.Errorf("alloc_port_sig(%q, %d): rc=%d", name, size, int(rc))
	}
	return nil
}
