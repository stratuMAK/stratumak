// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// A slog handler that keeps what it was handed.
type keepHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *keepHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *keepHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *keepHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *keepHandler) WithGroup(string) slog.Handler      { return h }

func (h *keepHandler) count(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

// A burst that overflows the ring must not silence it for good.  The
// machine did exactly this: the thread start pushed more messages than the
// ring holds before the drain goroutine ran, one position was claimed and
// dropped, and the consumer then waited on that position for the rest of
// the run -- hours without a single C-module log line.
func TestLogRingRecoversFromOverflow(t *testing.T) {
	r := newStmakLogRing()
	defer r.destroy()
	h := &keepHandler{}
	logger := slog.New(h)

	// Overflow without draining: the last few are dropped, and each drop is
	// a hole in the sequence.
	const burst = int(C_STMAK_LOG_RING_SIZE) + 5
	dropped := 0
	for i := 0; i < burst; i++ {
		if !r.emit(3 /* ERROR */, "burst", fmt.Sprintf("burst %d", i)) {
			dropped++
		}
	}
	if dropped != 5 {
		t.Fatalf("expected 5 drops from a burst of %d, got %d", burst, dropped)
	}

	r.drainAll(logger)
	if got := h.count("burst "); got != int(C_STMAK_LOG_RING_SIZE) {
		t.Fatalf("drained %d of the ring's %d messages", got, C_STMAK_LOG_RING_SIZE)
	}
	if got := h.count("log ring full"); got != 1 {
		t.Fatalf("expected the drop to be reported once, got %d", got)
	}

	// The message after the burst is the one that matters: before the fix the
	// consumer sat on the first hole and this never came out.
	if !r.emit(3, "station", "after the burst") {
		t.Fatal("ring should have room again after draining")
	}
	r.drainAll(logger)
	if got := h.count("after the burst"); got != 1 {
		t.Fatalf("the message after the burst did not come through (got %d)", got)
	}

	// And it keeps working -- more than a full lap, so every slot has been
	// reused past the holes.
	for i := 0; i < 3*int(C_STMAK_LOG_RING_SIZE); i++ {
		if !r.emit(1, "steady", "steady") {
			t.Fatalf("drop at %d in steady state", i)
		}
		if i%100 == 0 {
			r.drainAll(logger)
		}
	}
	r.drainAll(logger)
	if got := h.count("steady"); got != 3*int(C_STMAK_LOG_RING_SIZE) {
		t.Fatalf("steady state lost messages: %d of %d", got, 3*int(C_STMAK_LOG_RING_SIZE))
	}
}
