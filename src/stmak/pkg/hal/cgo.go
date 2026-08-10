// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package hal

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#cgo LDFLAGS:

#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include "hal.h"

// Helper to convert hal_type_t to int for Go
static inline int get_hal_type(hal_type_t t) { return (int)t; }

// Helper to convert hal_pin_dir_t to int for Go
static inline int get_hal_dir(hal_pin_dir_t d) { return (int)d; }

// Port helper wrappers: take hal_port_t* and dereference for the C API.
static inline bool go_hal_port_write(hal_port_t* p, const char* src, unsigned count) {
    return hal_port_write(*p, src, count);
}
static inline bool go_hal_port_peek(hal_port_t* p, char* dest, unsigned count) {
    return hal_port_peek(*p, dest, count);
}
static inline unsigned go_hal_port_readable(hal_port_t* p) {
    return hal_port_readable(*p);
}
static inline void go_hal_port_clear(hal_port_t* p) {
    hal_port_clear(*p);
}

// go_hal_pin_new dispatches to the typed hal_pin_*_new by hal_type_t so the Go
// side needs one wrapper instead of five near-identical copies. ptr is a
// pointer-sized slot in HAL shared memory; hal_pin_*_new stores the data cell
// address in *ptr and updates it when the pin is linked to a signal via net.
static inline int go_hal_pin_new(const char* name, hal_pin_dir_t dir, void** ptr, int comp_id, hal_type_t type) {
    switch (type) {
    case HAL_BIT:   return hal_pin_bit_new(name, dir, (hal_bit_t**)ptr, comp_id);
    case HAL_FLOAT: return hal_pin_float_new(name, dir, (hal_float_t**)ptr, comp_id);
    case HAL_S32:   return hal_pin_s32_new(name, dir, (hal_s32_t**)ptr, comp_id);
    case HAL_U32:   return hal_pin_u32_new(name, dir, (hal_u32_t**)ptr, comp_id);
    case HAL_PORT:  return hal_pin_port_new(name, dir, (hal_port_t**)ptr, comp_id);
    default:        return -EINVAL;
    }
}

// go_hal_param_new is the parameter counterpart of go_hal_pin_new, dispatching
// to the typed hal_param_*_new by hal_type_t. There is no HAL_PORT case: a
// parameter can only be HAL_BIT/HAL_FLOAT/HAL_S32/HAL_U32.
//
// Unlike a pin, a parameter takes its data cell address DIRECTLY: parameters
// are never linked to signals, so HAL never repoints the cell and there is no
// pointer slot to keep. data_addr is the value cell itself.
static inline int go_hal_param_new(const char* name, hal_param_dir_t dir, void* data_addr, int comp_id, hal_type_t type) {
    switch (type) {
    case HAL_BIT:   return hal_param_bit_new(name, dir, (hal_bit_t*)data_addr, comp_id);
    case HAL_FLOAT: return hal_param_float_new(name, dir, (hal_float_t*)data_addr, comp_id);
    case HAL_S32:   return hal_param_s32_new(name, dir, (hal_s32_t*)data_addr, comp_id);
    case HAL_U32:   return hal_param_u32_new(name, dir, (hal_u32_t*)data_addr, comp_id);
    default:        return -EINVAL;
    }
}

*/
import "C"
import (
	"unsafe"
)

// halInit wraps hal_init_ex() to create a new HAL userspace component.
// Returns the component ID on success, or an error on failure.
func halInit(name string) (int, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	compID := C.hal_init_ex(cName, nil, C.COMPONENT_TYPE_USER)
	if compID < 0 {
		return 0, halError(int(compID), "hal_init_ex")
	}

	return int(compID), nil
}

// halReady wraps hal_ready() to mark a component as ready.
// Returns an error on failure.
func halReady(compID int) error {
	ret := C.hal_ready(C.int(compID))
	return halError(int(ret), "hal_ready")
}

// halExit wraps hal_exit() to clean up a component.
// Returns an error on failure.
func halExit(compID int) error {
	ret := C.hal_exit(C.int(compID))
	return halError(int(ret), "hal_exit")
}

// halMalloc wraps hal_malloc() to allocate memory in HAL shared memory.
// Returns an unsafe.Pointer to the allocated memory, or nil on failure.
// The allocated memory is freed when the component exits.
func halMalloc(size int) unsafe.Pointer {
	return C.hal_malloc(C.long(size))
}

// halPinNew wraps hal_pin_*_new() (dispatched C-side by typ via go_hal_pin_new)
// to create a new pin of the given HAL type. It returns the double-pointer
// (unsafe.Pointer to the pointer slot HAL fills in) so the caller dereferences
// at access time — HAL updates that slot when the pin is linked to a signal via
// net, so the double-pointer must be preserved.
func halPinNew(name string, dir Direction, compID int, typ PinType) (unsafe.Pointer, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	// Allocate one pointer-sized slot in HAL shared memory for hal_pin_*_new to
	// fill in (every hal_*_t* has the same size).
	ptrPtr := halMalloc(int(unsafe.Sizeof(uintptr(0))))
	if ptrPtr == nil {
		return nil, newError("hal_malloc", "failed to allocate HAL shared memory", -12)
	}

	ret := C.go_hal_pin_new(cName, C.hal_pin_dir_t(dir), (*unsafe.Pointer)(ptrPtr), C.int(compID), C.hal_type_t(typ))
	if ret < 0 {
		return nil, halError(int(ret), "hal_pin_new")
	}

	// Return the double-pointer itself — the caller must dereference at access
	// time because HAL updates the slot when the pin is linked to a signal via net.
	return ptrPtr, nil
}

// halParamNew wraps hal_param_*_new() (dispatched C-side by typ via
// go_hal_param_new) to create a new parameter of the given HAL type. It returns
// the value cell itself — not a double-pointer as halPinNew does — because a
// parameter is never linked to a signal, so nothing ever repoints the cell.
func halParamNew(name string, dir ParamDirection, compID int, typ PinType) (unsafe.Pointer, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	// One hal_data_u-sized cell covers every parameter type. Sizing it to the
	// widest member is what makes it correctly aligned: hal_malloc aligns by
	// the requested size, so the 8 bytes of the union give the 8-byte alignment
	// hal_float_t needs, whatever the actual type is.
	cell := halMalloc(int(C.sizeof_hal_data_u))
	if cell == nil {
		return nil, newError("hal_malloc", "failed to allocate HAL shared memory", -12)
	}
	// hal_param_*_new explicitly does not initialise *data_addr — the owner is
	// expected to load a default. Zero the cell so a parameter that is only
	// ever written from outside still starts from a defined value.
	C.memset(cell, 0, C.sizeof_hal_data_u)

	ret := C.go_hal_param_new(cName, C.hal_param_dir_t(dir), cell, C.int(compID), C.hal_type_t(typ))
	if ret < 0 {
		return nil, halError(int(ret), "hal_param_new")
	}

	return cell, nil
}

// halPortWrite writes data bytes to the port referenced by portPtr.
// Returns true if all bytes were written successfully.
func halPortWrite(portPtr *C.hal_port_t, data []byte) bool {
	if len(data) == 0 {
		// Nothing to write; avoid passing a nil/empty-slice pointer to the C function.
		return true
	}
	ret := C.go_hal_port_write(portPtr, (*C.char)(unsafe.Pointer(unsafe.SliceData(data))), C.uint(len(data)))
	return bool(ret)
}

// halPortPeek reads count bytes from the port without consuming them.
// Returns the bytes read, or nil if not enough data is available.
func halPortPeek(portPtr *C.hal_port_t, count uint) []byte {
	if count == 0 {
		return []byte{}
	}
	buf := make([]byte, count)
	ret := C.go_hal_port_peek(portPtr, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.uint(count))
	if !bool(ret) {
		return nil
	}
	return buf
}

// halPortReadable returns the number of bytes available to read from the port.
func halPortReadable(portPtr *C.hal_port_t) uint {
	return uint(C.go_hal_port_readable(portPtr))
}

// halPortClear empties the port of all data.
func halPortClear(portPtr *C.hal_port_t) {
	C.go_hal_port_clear(portPtr)
}

// halError translates a HAL C error code to a Go error.
// Returns nil if the code is 0 (success).
// Error codes are negative errno values as returned by HAL/RTAPI functions.
//
// No Detail is attached here. Detailed failure reasons travel in-band through
// the hal_*_ex(err, errlen) call signatures (see internal/halcmd); this package
// calls the plain variants, so there is no reason string to attach. A
// component's hal_lib failures still reach the operator: they are logged to the
// ring, and when they happen during a module load the launcher attaches them to
// the load error.
func halError(code int, op string) error {
	return CodeError(op, code, "")
}
