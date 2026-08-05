// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// #cgo CXXFLAGS: -I${SRCDIR}/../../.. -I${SRCDIR}/../../../../include -I${SRCDIR}/../../../cnc/rs274ngc -I${SRCDIR}/../../../cnc/tooldata -I${SRCDIR}/../../generated/gmi/interp_ext -I${SRCDIR}/../../generated/gmi/interp_ctx -I${SRCDIR}/../../generated/gmi/mcode_handler -I${SRCDIR}/../../pkg/cmodule -std=c++17
// #cgo CFLAGS: -I${SRCDIR}/../../.. -I${SRCDIR}/../../../../include
// #cgo LDFLAGS: -L${SRCDIR}/../../../../lib -lrs274 -lstdc++ -ldl
// #include "task_interp_shim.h"
// #include <stdlib.h>
import "C"
import (
	"fmt"
	"unsafe"
)

// CInterp wraps the C++ InterpBase via the C shim.
// It implements the Interpreter interface.
type CInterp struct {
	handle unsafe.Pointer
}

// NewCInterp creates the default interpreter (rs274ngc).
func NewCInterp() (*CInterp, error) {
	h := C.interp_new()
	if h == nil {
		return nil, fmt.Errorf("interp_new: failed to create interpreter")
	}
	return &CInterp{handle: h}, nil
}

// NewCInterpFromLib loads an interpreter from a shared library path.
func NewCInterpFromLib(shlib string) (*CInterp, error) {
	cs := C.CString(shlib)
	defer C.free(unsafe.Pointer(cs))
	h := C.interp_from_lib(cs)
	if h == nil {
		return nil, fmt.Errorf("interp_from_lib: failed to load %q", shlib)
	}
	return &CInterp{handle: h}, nil
}

// RawInterp returns the underlying C++ Interp* — the ctx the interp_ext GMI
// provider must expose so that a cmod's register_oword/register_remap_* calls
// reach this interpreter instance. interp_new() returns the InterpBase*/Interp*
// directly as the handle (single inheritance), which is exactly what
// interp_ext_register_* static_cast to Interp*.
func (i *CInterp) RawInterp() unsafe.Pointer {
	return i.handle
}

func (i *CInterp) IniLoad(inifile string) error {
	cs := C.CString(inifile)
	defer C.free(unsafe.Pointer(cs))
	rc := int(C.interp_ini_load(i.handle, cs))
	if rc != InterpOK {
		return fmt.Errorf("interp_ini_load: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Init() error {
	rc := int(C.interp_init(i.handle))
	if rc != InterpOK {
		return fmt.Errorf("interp_init: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Open(filename string) error {
	cs := C.CString(filename)
	defer C.free(unsafe.Pointer(cs))
	rc := int(C.interp_open(i.handle, cs))
	if rc != InterpOK {
		return fmt.Errorf("interp_open: rc=%d %s", rc, i.ErrorText(rc))
	}
	return nil
}

func (i *CInterp) Read() (int, error) {
	rc := int(C.interp_read(i.handle))
	if rc == InterpEndfile {
		return rc, nil // end of file is not an error
	}
	if rc > InterpOK && rc != InterpExecuteFinish {
		return rc, fmt.Errorf("interp_read: rc=%d %s", rc, i.ErrorText(rc))
	}
	return rc, nil
}

func (i *CInterp) ReadString(line string) (int, error) {
	cs := C.CString(line)
	defer C.free(unsafe.Pointer(cs))
	rc := int(C.interp_read_string(i.handle, cs))
	if rc > InterpOK && rc != InterpExecuteFinish && rc != InterpEndfile {
		return rc, fmt.Errorf("interp_read_string: rc=%d %s", rc, i.ErrorText(rc))
	}
	return rc, nil
}

func (i *CInterp) Execute() (int, error) {
	rc := int(C.interp_execute(i.handle))
	if rc > InterpExecuteFinish {
		return rc, fmt.Errorf("interp_execute: rc=%d %s", rc, i.ErrorText(rc))
	}
	return rc, nil
}

func (i *CInterp) ExecuteString(cmd string) (int, error) {
	cs := C.CString(cmd)
	defer C.free(unsafe.Pointer(cs))
	rc := int(C.interp_execute_string(i.handle, cs))
	if rc > InterpExecuteFinish {
		return rc, fmt.Errorf("interp_execute_string: rc=%d %s", rc, i.ErrorText(rc))
	}
	return rc, nil
}

func (i *CInterp) Synch() error {
	rc := int(C.interp_synch(i.handle))
	if rc != InterpOK {
		return fmt.Errorf("interp_synch: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Close() error {
	rc := int(C.interp_close(i.handle))
	if rc != InterpOK {
		return fmt.Errorf("interp_close: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Reset() error {
	rc := int(C.interp_reset(i.handle))
	if rc != InterpOK {
		return fmt.Errorf("interp_reset: rc=%d", rc)
	}
	return nil
}

// stateTagSize returns sizeof the packed C state_tag_t the canon's
// update_tag callback points at. It is a compile-time constant, so cache it:
// copyPackedStateTag runs once per executed G-code block and should not pay
// a cgo transition just to re-fetch it.
var cachedStateTagSize = int(C.interp_state_tag_size())

func stateTagSize() int {
	return cachedStateTagSize
}

// copyPackedStateTag copies the packed state_tag_t an update_tag callback
// points at. The GMI boundary delivers the pointer as a uint64, valid only
// for the duration of the synchronous callback; the copy happens in C so Go
// never converts a bare integer to a pointer.
func copyPackedStateTag(ptr uint64) []byte {
	sz := stateTagSize()
	if ptr == 0 || sz <= 0 {
		return nil
	}
	buf := make([]byte, sz)
	C.interp_copy_state_tag(C.ulonglong(ptr), unsafe.Pointer(&buf[0]))
	return buf
}

// RestoreFromTag rolls the interpreter's modal state back to a captured
// packed state tag (2.9 emcTaskStateRestore → Interp::restore_from_tag).
// A nil/empty tag is a no-op.
func (i *CInterp) RestoreFromTag(tag []byte) error {
	if len(tag) == 0 {
		return nil
	}
	rc := int(C.interp_restore_from_tag(i.handle, unsafe.Pointer(&tag[0])))
	if rc != InterpOK {
		return fmt.Errorf("interp_restore_from_tag: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Abort(reason int, message string) error {
	cs := C.CString(message)
	defer C.free(unsafe.Pointer(cs))
	rc := int(C.interp_on_abort(i.handle, C.int(reason), cs))
	if rc != InterpOK {
		return fmt.Errorf("interp_on_abort: rc=%d", rc)
	}
	return nil
}

func (i *CInterp) Line() int {
	return int(C.interp_line(i.handle))
}

func (i *CInterp) SequenceNumber() int {
	return int(C.interp_sequence_number(i.handle))
}

// CallLevel returns the interpreter's current subroutine nesting level
// (0 = top level, > 0 = inside a subroutine/remap).
func (i *CInterp) CallLevel() int {
	return int(C.interp_call_level(i.handle))
}

func (i *CInterp) ErrorText(errcode int) string {
	var buf [256]C.char
	p := C.interp_error_text(i.handle, C.int(errcode), &buf[0], 256)
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func (i *CInterp) FileName() string {
	// Sized to _setup.filename (PATH_MAX): Interp::file_name answers a name
	// that does not fit with the EMPTY string, not a truncation — an
	// undersized buffer here would silently switch off the whole (file, line)
	// trace for any program under a deep enough PROGRAM_PREFIX.
	var buf [4096]C.char
	p := C.interp_file_name(i.handle, &buf[0], C.size_t(len(buf)))
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func (i *CInterp) Command() string {
	var buf [256]C.char
	p := C.interp_command(i.handle, &buf[0], 256)
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func (i *CInterp) Destroy() {
	if i.handle != nil {
		C.interp_delete(i.handle)
		i.handle = nil
	}
}

// SetCanonCallbacks installs the canon callback table into the interpreter.
// The callbacks pointer must remain valid for the lifetime of the interpreter.
func (i *CInterp) SetCanonCallbacks(cb unsafe.Pointer) {
	C.interp_set_canon_callbacks(i.handle, (*C.canon_callbacks_t)(cb))
}

// SetLoglevel sets the interpreter log verbosity.
func (i *CInterp) SetLoglevel(level int) {
	C.interp_set_loglevel(i.handle, C.int(level))
}

// SetLoopOnMainM99 controls whether M99 in main loops back.
func (i *CInterp) SetLoopOnMainM99(state bool) {
	v := C.int(0)
	if state {
		v = 1
	}
	C.interp_set_loop_on_main_m99(i.handle, v)
}

func (i *CInterp) SetTaskMode(mode int) {
	C.interp_set_task_mode(i.handle, C.int(mode))
}

const (
	activeGCodesLen   = 17 // ACTIVE_G_CODES
	activeMCodesLen   = 10 // ACTIVE_M_CODES
	activeSettingsLen = 5  // ACTIVE_SETTINGS
)

func (i *CInterp) ActiveGCodes() []int32 {
	var buf [activeGCodesLen]C.int
	C.interp_active_g_codes(i.handle, &buf[0], C.int(activeGCodesLen))
	out := make([]int32, activeGCodesLen)
	for j := range buf {
		out[j] = int32(buf[j])
	}
	return out
}

func (i *CInterp) ActiveMCodes() []int32 {
	var buf [activeMCodesLen]C.int
	C.interp_active_m_codes(i.handle, &buf[0], C.int(activeMCodesLen))
	out := make([]int32, activeMCodesLen)
	for j := range buf {
		out[j] = int32(buf[j])
	}
	return out
}

func (i *CInterp) ActiveSettings() []float64 {
	var buf [activeSettingsLen]C.double
	C.interp_active_settings(i.handle, &buf[0], C.int(activeSettingsLen))
	out := make([]float64, activeSettingsLen)
	for j := range buf {
		out[j] = float64(buf[j])
	}
	return out
}

// ActiveModesFromTag decodes a packed state tag into TASK_STAT-style active
// G-/M-code and settings arrays (Interp::active_modes) — how 2.9 task derives
// the status codes of the EXECUTING motion segment from its tag. Returns
// ok=false when the tag is invalid or the decode fails. Entries active_modes
// leaves untouched are pre-filled with -1 (codes) / 0 (settings), matching
// the unset convention of the ActiveGCodes/ActiveMCodes arrays.
func (i *CInterp) ActiveModesFromTag(tag []byte) (gc, mc []int32, st []float64, ok bool) {
	if len(tag) < stateTagSize() {
		return nil, nil, nil, false
	}
	var gbuf [activeGCodesLen]C.int
	var mbuf [activeMCodesLen]C.int
	var sbuf [activeSettingsLen]C.double
	for j := range gbuf {
		gbuf[j] = -1
	}
	for j := range mbuf {
		mbuf[j] = -1
	}
	rc := C.interp_active_modes_from_tag(i.handle, unsafe.Pointer(&tag[0]),
		&gbuf[0], &mbuf[0], &sbuf[0])
	if rc != 0 {
		return nil, nil, nil, false
	}
	gc = make([]int32, activeGCodesLen)
	mc = make([]int32, activeMCodesLen)
	st = make([]float64, activeSettingsLen)
	for j := range gbuf {
		gc[j] = int32(gbuf[j])
	}
	for j := range mbuf {
		mc[j] = int32(mbuf[j])
	}
	for j := range sbuf {
		st[j] = float64(sbuf[j])
	}
	return gc, mc, st, true
}

// Verify CInterp implements Interpreter.
var _ Interpreter = (*CInterp)(nil)
