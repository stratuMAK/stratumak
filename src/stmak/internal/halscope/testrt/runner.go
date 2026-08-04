// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package testrt runs the halscope RT state-machine unit suite
// (test_halscope_rt.c) under `go test`.
//
// The suite is C: it #includes halscope_rt.c directly so it can reach the
// static functions, and it stubs HAL/RTAPI out via the headers in testmock/.
// That is why it lives in its own Go package rather than in internal/halscope
// — the parent package already compiles halscope_rt.c through cgo, and
// compiling it twice in one package would collide.
//
// cgo is not supported in _test.go files, so the bridge has to sit in a
// regular file; halscope_rt_test.go is the actual test.
//
// In the cgo flags below, testmock must precede the real include dirs so that
// hal.h and rtapi.h resolve to the stubs; the rest mirrors the parent
// package's cgo flags, one directory level deeper.
package testrt

// #cgo CFLAGS: -std=c11 -I${SRCDIR}/testmock -I${SRCDIR} -I${SRCDIR}/..
// #cgo CFLAGS: -I${SRCDIR}/../../../generated/gmi/halscope
// #cgo CFLAGS: -I${SRCDIR}/../../../.. -I${SRCDIR}/../../../../../include
// #cgo LDFLAGS: -lm
//
// int halscope_rt_run_all(void);
import "C"

// RunAll runs every suite and returns the number of failed assertions.
// greatest's per-test report goes to stdout, which `go test` captures.
func RunAll() int {
	return int(C.halscope_rt_run_all())
}
