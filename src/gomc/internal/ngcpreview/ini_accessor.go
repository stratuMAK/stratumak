// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ngcpreview

// INI accessor bridge for the preview interpreter: C callbacks backed by a Go
// *inifile.IniFile, so Interp::init reads REMAP / SUBROUTINE_PATH /
// PROGRAM_PREFIX / RANDOM_TOOLCHANGER etc. from the machine's INI and the
// preview runs the same interpreter configuration the machine executes
// (finding N-3 — without this the preview interp ran fully defaulted and
// e.g. remapped M6 or SUBROUTINE_PATH o-calls failed only in the preview).
//
// Mirrors internal/task/ini_accessor.go against the rs274ngc preview shim
// (emc/rs274ngc/interp_shim.h). The //export names differ because exported
// cgo symbols are global to the final binary.

/*
#include "interp_shim.h"
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

extern char* goPreviewIniGet(uintptr_t ctx, char *section, char *key);
extern char* goPreviewIniGetNth(uintptr_t ctx, char *section, char *key, int n);

// ctx travels as uintptr_t (a cgo.Handle integer), never as a Go pointer —
// see the cgo-handle-transit rule from the gmicompile review.
static inline interp_shim_ini_accessor_t make_preview_ini_accessor(uintptr_t ctx) {
    interp_shim_ini_accessor_t acc;
    acc.ctx = (void *)ctx;
    acc.get = (const char* (*)(void*, const char*, const char*))goPreviewIniGet;
    acc.get_nth = (const char* (*)(void*, const char*, const char*, int))goPreviewIniGetNth;
    return acc;
}
*/
import "C"
import (
	"runtime/cgo"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

// previewIniHandle wraps the IniFile plus a C-heap buffer for returned
// strings (valid until the next get/get_nth call, per the accessor contract).
type previewIniHandle struct {
	ini *inifile.IniFile

	// randomToolchanger overrides [EMCIO]RANDOM_TOOLCHANGER ("0"/"1"; ""
	// leaves the INI to answer).  The interp asks for that key through this
	// accessor, but the tool store — not the INI — is what decides whether an
	// idx is a carousel pocket here, and it is told so on its load line.
	// Letting the INI answer would let a preview disagree with the machine it
	// previews for.
	randomToolchanger string

	cbuf   *C.char
	cbufSz C.size_t
}

func (h *previewIniHandle) returnStr(s string) *C.char {
	need := C.size_t(len(s) + 1)
	if need > h.cbufSz {
		if h.cbuf != nil {
			C.free(unsafe.Pointer(h.cbuf))
		}
		h.cbuf = (*C.char)(C.malloc(need))
		h.cbufSz = need
	}
	C.memcpy(unsafe.Pointer(h.cbuf), unsafe.Pointer(unsafe.StringData(s)), C.size_t(len(s)))
	*(*C.char)(unsafe.Pointer(uintptr(unsafe.Pointer(h.cbuf)) + uintptr(len(s)))) = 0
	return h.cbuf
}

// newPreviewIniAccessor builds the C accessor struct; the returned handle
// must outlive the interpreter run and be released via freePreviewIniAccessor.
// randomToolchanger is the tool store's answer ("0"/"1", or "" when the store
// could not be asked), which wins over the INI for that one key.
func newPreviewIniAccessor(ini *inifile.IniFile, randomToolchanger string) (C.interp_shim_ini_accessor_t, cgo.Handle) {
	h := &previewIniHandle{ini: ini, randomToolchanger: randomToolchanger}
	handle := cgo.NewHandle(h)
	acc := C.make_preview_ini_accessor(C.uintptr_t(handle))
	return acc, handle
}

func freePreviewIniAccessor(handle cgo.Handle) {
	if handle == 0 {
		return
	}
	h := handle.Value().(*previewIniHandle)
	if h.cbuf != nil {
		C.free(unsafe.Pointer(h.cbuf))
		h.cbuf = nil
	}
	handle.Delete()
}

//export goPreviewIniGet
func goPreviewIniGet(ctx C.uintptr_t, section *C.char, key *C.char) *C.char {
	h := cgo.Handle(ctx).Value().(*previewIniHandle)
	sec, k := C.GoString(section), C.GoString(key)
	if h.randomToolchanger != "" && sec == "EMCIO" && k == "RANDOM_TOOLCHANGER" {
		return h.returnStr(h.randomToolchanger)
	}
	if h.ini == nil {
		return nil
	}
	val := h.ini.Get(sec, k)
	if val == "" {
		return nil
	}
	return h.returnStr(val)
}

//export goPreviewIniGetNth
func goPreviewIniGetNth(ctx C.uintptr_t, section *C.char, key *C.char, n C.int) *C.char {
	h := cgo.Handle(ctx).Value().(*previewIniHandle)
	if h.ini == nil {
		return nil
	}
	val := h.ini.GetN(C.GoString(section), C.GoString(key), int(n))
	if val == "" {
		return nil
	}
	return h.returnStr(val)
}
