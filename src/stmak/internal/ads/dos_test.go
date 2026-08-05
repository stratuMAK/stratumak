// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ads

import (
	"encoding/binary"
	"testing"
)

// These regression tests cover the remote, unauthenticated crash/OOM vectors
// found in the Tier-2 review (docs/dev/ADS_REVIEW_FINDINGS.md A1–A3). A client reaches
// every one of these through a single AMS packet, so a failure here is a crash
// of the motion controller. Each test would panic or OOM the test process
// before the fix.

// sumHeader encodes one N×12 SumRead/SumWrite sub-request header.
func sumHeader(indexGroup, indexOffset, length uint32) []byte {
	h := make([]byte, 12)
	binary.LittleEndian.PutUint32(h[0:], indexGroup)
	binary.LittleEndian.PutUint32(h[4:], indexOffset)
	binary.LittleEndian.PutUint32(h[8:], length)
	return h
}

// A1: SumWrite with a sub-request Length of 0xFFFFFFFF must not panic. Before
// the fix, dataOffset+ln wrapped uint32, the guard passed, and the slice
// writeData[12:11] panicked (low > high) → process crash.
func TestSumWriteLengthOverflowNoPanic(t *testing.T) {
	st := NewSymbolTable()
	st.Register("stA.nVal", newDintPin(0))

	writeData := sumHeader(IdxGrpProcessImageRW, 0, 0xFFFFFFFF) // 1 sub-request, insane length
	resp, errCode := st.ReadWriteData(IdxGrpSumWrite, 1, 0, writeData)
	if errCode != ErrNoError {
		t.Fatalf("SumWrite top-level errCode = 0x%X, want 0 (per-request errors are in the body)", errCode)
	}
	if len(resp) != 4 {
		t.Fatalf("SumWrite response len = %d, want 4 (one error code)", len(resp))
	}
	if sub := binary.LittleEndian.Uint32(resp[0:]); sub != ErrInternal {
		t.Errorf("SumWrite sub-request errCode = 0x%X, want ErrInternal (0x%X)", sub, ErrInternal)
	}
}

// A2: a huge sub-request count (client-controlled indexOffset) must be rejected
// before any allocation. Before the fix these sized a slice directly and a tiny
// packet forced a multi-GB allocation → OOM death.
func TestSumReadWriteHugeCountRejected(t *testing.T) {
	st := NewSymbolTable()
	st.Register("stA.nVal", newDintPin(0))

	tiny := sumHeader(IdxGrpProcessImageRW, 0, 4) // one valid-looking header only

	if _, ec := st.ReadWriteData(IdxGrpSumRead, 0xFFFFFFFF, 0, tiny); ec != ErrInternal {
		t.Errorf("SumRead huge count errCode = 0x%X, want ErrInternal", ec)
	}
	if _, ec := st.ReadWriteData(IdxGrpSumWrite, 0xFFFFFFFF, 0, tiny); ec != ErrInternal {
		t.Errorf("SumWrite huge count errCode = 0x%X, want ErrInternal", ec)
	}
	// A count that fits the header buffer still works (regression guard on the
	// bound being len(writeData)/12, not overly strict): one 12-byte header → 1.
	if _, ec := st.ReadWriteData(IdxGrpSumRead, 1, 0, tiny); ec != ErrNoError {
		t.Errorf("SumRead valid single-request errCode = 0x%X, want 0", ec)
	}
}

// A3: a process-image read larger than the whole process image must be rejected
// before allocating. Before the fix, make([]byte, length) with length≈4 GB
// OOM-killed the controller. This is also the path the notification sendLoop
// takes every cycle.
func TestProcessImageReadHugeLengthRejected(t *testing.T) {
	st := NewSymbolTable()
	st.Register("stA.nVal", newDintPin(0)) // nextOffset becomes 4

	if _, ec := st.ReadData(IdxGrpProcessImageRW, 0, 0xFFFFFFFF); ec != ErrInvalidOffset {
		t.Errorf("process-image read huge length errCode = 0x%X, want ErrInvalidOffset", ec)
	}
	// A legitimate in-image read still succeeds.
	if data, ec := st.ReadData(IdxGrpProcessImageRW, 0, 4); ec != ErrNoError || len(data) != 4 {
		t.Errorf("valid process-image read = (len %d, 0x%X), want (4, 0)", len(data), ec)
	}
}
