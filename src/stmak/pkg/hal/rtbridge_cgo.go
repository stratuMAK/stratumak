//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package hal

/*
#cgo CFLAGS: -I${SRCDIR}/../cmodule

#include "stmak_tribuf.h"
#include "stmak_pinmap.h"
#include "stmak_doorbell.h"

// void * on the Go side: cgo then never has to translate the structs, and
// the C helpers are the only place their layout matters.
static size_t go_tribuf_size(uint32_t frame) { return stmak_tribuf_size(frame); }
static void go_tribuf_init(void *b, uint32_t frame) { stmak_tribuf_init((stmak_tribuf_t *)b, frame); }
static void go_tribuf_publish_copy(void *b, const void *f) { stmak_tribuf_publish_copy((stmak_tribuf_t *)b, f); }
static int go_tribuf_read_copy(void *b, void *f) { return stmak_tribuf_read_copy((stmak_tribuf_t *)b, f); }

static size_t go_pinmap_size(uint32_t cap) { return stmak_pinmap_size(cap); }
static void go_pinmap_init(void *m, uint32_t cap) { stmak_pinmap_init((stmak_pinmap_t *)m, cap); }
static int go_pinmap_add(void *m, void *slot, uint32_t ofs, uint32_t type) {
    return stmak_pinmap_add((stmak_pinmap_t *)m, slot, ofs, type);
}

static size_t go_doorbell_size(void) { return sizeof(stmak_doorbell_t); }
*/
import "C"

import (
	"errors"
	"unsafe"
)

// The Go mirror of stmak_doorbell_t must have the C layout: Doorbell loads its
// words with sync/atomic through it. A mismatch is a build defect, so it stops
// the binary at start rather than letting Wait read the wrong word.
func init() {
	if unsafe.Sizeof(doorbell{}) != uintptr(C.go_doorbell_size()) {
		panic("pkg/hal: doorbell mirror does not match stmak_doorbell_t")
	}
}

func tribufSize(frame uintptr) uintptr {
	return uintptr(C.go_tribuf_size(C.uint32_t(frame)))
}

func tribufInit(blk unsafe.Pointer, frame uintptr) {
	C.go_tribuf_init(blk, C.uint32_t(frame))
}

// tribufPublishCopy and tribufReadCopy pass a Go pointer for the duration of
// the call only, to plain data (NewTriBuf checked): within the cgo rules.
func tribufPublishCopy(blk, frame unsafe.Pointer) {
	C.go_tribuf_publish_copy(blk, frame)
}

func tribufReadCopy(blk, frame unsafe.Pointer) bool {
	return C.go_tribuf_read_copy(blk, frame) != 0
}

func pinmapSize(capacity int) uintptr {
	return uintptr(C.go_pinmap_size(C.uint32_t(capacity)))
}

func pinmapInit(blk unsafe.Pointer, capacity int) {
	C.go_pinmap_init(blk, C.uint32_t(capacity))
}

func pinmapAdd(blk, slot unsafe.Pointer, ofs uintptr, typ PinType) error {
	if C.go_pinmap_add(blk, slot, C.uint32_t(ofs), C.uint32_t(typ)) != 0 {
		return errors.New("stmak_pinmap_add refused the entry")
	}
	return nil
}
