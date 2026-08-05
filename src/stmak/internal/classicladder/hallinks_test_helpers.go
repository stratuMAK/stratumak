// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#include "classicladder_rt.h"
#include <stdlib.h>
#include <dlfcn.h>

static void *cl_test_dl_handle(void) {
    return dlopen(NULL, RTLD_NOW);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// cgo wrappers for the HAL-backed tests. Test files cannot import "C", so the
// component lifecycle lives here — and createHALPins is called directly rather
// than reimplemented, which is the whole point of these tests.

func halTestInit(name string) (C.int, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	compID := C.hal_init_ex(cName, C.cl_test_dl_handle(), C.COMPONENT_TYPE_REALTIME)
	if compID < 0 {
		return 0, fmt.Errorf("hal_init_ex returned %d", int(compID))
	}
	return compID, nil
}

func halTestExit(compID C.int) { C.hal_exit(compID) }

func halTestReady(compID C.int) { C.hal_ready(compID) }

func halTestCreatePins(rt *C.classicladder_rt_t, compID C.int, name string) ([]halPinRef, error) {
	return createHALPins(rt, compID, name)
}
