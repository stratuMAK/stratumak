// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package rtapialloc

import (
	"testing"
	"unsafe"
)

// TestCallocZeroInit asserts the guarantee behind the cmod env's rtapi->calloc:
// RT-safe memory arrives zero-initialized, and a fresh allocation obtained
// after a dirtied block has been freed is likewise all zero (never handed back
// carrying stale bytes).
//
// This replaces the old tests/rtapi-shmem integration test, which asserted the
// same property against the classic kernel shmem-key API (rtapi_shmem_new /
// _getptr / _delete). stmak deliberately does not expose that key-based API to
// components — cmods get RT-safe working memory through rtapi->calloc — so the
// integration test was obsolete-by-design. The zero-init contract it cared
// about lives here instead.
func TestCallocZeroInit(t *testing.T) {
	const size = 4096 * 100 // same block size the old cmod test used

	// alloc allocates a fresh block and fails the test unless every byte is 0.
	alloc := func(label string) unsafe.Pointer {
		p := Calloc(size)
		if p == nil {
			t.Fatalf("%s: Calloc(%d) returned nil", label, size)
		}
		for i, b := range unsafe.Slice((*byte)(p), size) {
			if b != 0 {
				Free(p)
				t.Fatalf("%s: byte %d = %#x, want 0", label, i, b)
			}
		}
		return p
	}

	// First allocation must come zeroed.
	p := alloc("first alloc")

	// Dirty every byte, then release it.
	s := unsafe.Slice((*byte)(p), size)
	for i := range s {
		s[i] = byte(i % 256)
	}
	Free(p)

	// A subsequent allocation must again come zeroed, not recycled dirty.
	p2 := alloc("second alloc")
	Free(p2)
}
