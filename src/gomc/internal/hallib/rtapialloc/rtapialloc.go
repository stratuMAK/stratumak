// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package rtapialloc is a thin cgo wrapper around the RT-safe allocator
// (rtapi_calloc / rtapi_free in uspace_rtapi_lib.c) for use in tests.
//
// cgo is not permitted directly in _test.go files, so the C wrapper lives here
// in a normal source file (mirroring the internal/hallib/hallibtest pattern)
// and the test calls these Go functions. It blank-imports internal/hallib so
// the test binary links the rtapi C symbols.
//
// Only test binaries import this package, so the wrappers are never compiled
// into the production gomc-server binary.
package rtapialloc

/*
#include <stddef.h>

extern void *rtapi_calloc(size_t size);
extern void  rtapi_free(void *ptr);
*/
import "C"

import (
	"unsafe"

	_ "github.com/sittner/linuxcnc/src/gomc/internal/hallib"
)

// Calloc returns a zero-initialized, RT-safe (mlock'd, pre-faulted) block of n
// bytes, or nil on allocation failure. This is what the cmod environment hands
// components as env->rtapi->calloc.
func Calloc(n int) unsafe.Pointer { return C.rtapi_calloc(C.size_t(n)) }

// Free releases a block obtained from Calloc.
func Free(p unsafe.Pointer) { C.rtapi_free(p) }
