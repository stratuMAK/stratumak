// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import "testing"

// The pending buffer is drained only while a get_positions subscriber polls;
// with no subscriber it must stay bounded (docs/dev/GMI_PYTHON_REVIEW_FINDINGS.md GP-5:
// a client that vanished without stop_logger let it grow without bound).
func TestPoslogPendingBounded(t *testing.T) {
	pl := &posLogger{}
	total := 3*poslogMaxPending + 17
	for i := 0; i < total; i++ {
		pl.appendPoint(posPoint{MotionType: 1, Pos: [9]float64{float64(i)}})
	}
	if len(pl.pending) > poslogMaxPending {
		t.Fatalf("pending grew past the cap: %d > %d", len(pl.pending), poslogMaxPending)
	}
	// Drop-oldest: the newest point must always survive.
	last := pl.pending[len(pl.pending)-1]
	if last.Pos[0] != float64(total-1) {
		t.Fatalf("newest point lost: got %v, want %v", last.Pos[0], total-1)
	}
	// The ring keeps its own independent cap.
	if pl.npts > poslogMaxPoints {
		t.Fatalf("ring grew past its cap: %d > %d", pl.npts, poslogMaxPoints)
	}
}

// drainPending must hand out the buffer and reset it.
func TestPoslogDrainResetsPending(t *testing.T) {
	pl := &posLogger{}
	for i := 0; i < 5; i++ {
		pl.appendPoint(posPoint{MotionType: 1, Pos: [9]float64{float64(i)}})
	}
	out := pl.drainPending()
	if len(out) != 5 {
		t.Fatalf("drain returned %d points, want 5", len(out))
	}
	if again := pl.drainPending(); again != nil {
		t.Fatalf("second drain returned %d points, want nil", len(again))
	}
}
