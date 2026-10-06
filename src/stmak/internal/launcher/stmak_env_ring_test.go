// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// A slog handler that keeps what it was handed.
type keepHandler struct {
	mu      sync.Mutex
	msgs    []string
	records []slog.Record
}

func (h *keepHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *keepHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}

// attr returns the named attribute of the last record with the prefix.
func (h *keepHandler) attr(prefix, key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		if !strings.HasPrefix(h.records[i].Message, prefix) {
			continue
		}
		val := ""
		h.records[i].Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				val = a.Value.String()
				return false
			}
			return true
		})
		return val
	}
	return ""
}

// level returns the level of the last record with the prefix.
func (h *keepHandler) level(prefix string) slog.Level {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		if strings.HasPrefix(h.records[i].Message, prefix) {
			return h.records[i].Level
		}
	}
	return slog.Level(-99)
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

// A handler that keeps what it was handed, above a level.
type levelHandler struct {
	keepHandler
	level slog.Level
}

func (h *levelHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

const (
	sevDebug = 0
	sevInfo  = 1
	sevWarn  = 2
	sevError = 3
	oper     = 0x10
)

// A burst that overflows the ring is counted, reported, and over: the ring
// carries on afterwards, and does not lose a message in steady state.  The
// machine did the overflow at every thread start; the earlier ring then sat
// on the first dropped position for the rest of the run.
func TestLogRingOverflowIsCountedAndRecovers(t *testing.T) {
	r := newStmakLogRing()
	defer r.destroy()
	h := &keepHandler{}
	logger := slog.New(h)

	// Overflow without draining.  Errors are not subject to the headroom
	// limit, so the drops start exactly at the ring's capacity.
	const burst = int(C_STMAK_LOG_RING_SIZE) + 5
	dropped := 0
	for i := 0; i < burst; i++ {
		if r.emit(sevError, "burst", fmt.Sprintf("burst %d", i)) < 0 {
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
	if got := h.attr("log ring full", "error"); got != "5" {
		t.Fatalf("expected the report to count 5 dropped errors, got %q", got)
	}
	if got := h.level("log ring full"); got != slog.LevelError {
		t.Fatalf("dropped errors should be reported as an error, got %v", got)
	}

	// The message after the burst is the one that matters.
	if r.emit(sevError, "station", "after the burst") != 1 {
		t.Fatal("ring should have room again after draining")
	}
	r.drainAll(logger)
	if got := h.count("after the burst"); got != 1 {
		t.Fatalf("the message after the burst did not come through (got %d)", got)
	}

	// And it keeps working -- more than a full lap, so every slot has been
	// reused past where the drops were.
	for i := 0; i < 3*int(C_STMAK_LOG_RING_SIZE); i++ {
		if r.emit(sevInfo, "steady", "steady") != 1 {
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
	if got := h.count("log ring full"); got != 1 {
		t.Fatalf("steady state reported drops: %d reports", got)
	}
}

// Chatter stops taking slots before the ring is full, so the message that
// explains the chatter still gets in.
func TestLogRingKeepsHeadroomForErrors(t *testing.T) {
	r := newStmakLogRing()
	defer r.destroy()
	h := &keepHandler{}
	logger := slog.New(h)

	for i := 0; i < int(C_STMAK_LOG_RING_HEADROOM_AT); i++ {
		if r.emit(sevInfo, "chatter", "chatter") != 1 {
			t.Fatalf("info %d refused below the headroom mark", i)
		}
	}
	if r.emit(sevInfo, "chatter", "one too many") != -1 {
		t.Fatal("info at the headroom mark should have been dropped")
	}
	if r.emit(sevDebug, "chatter", "one too many") != -1 {
		t.Fatal("debug at the headroom mark should have been dropped")
	}
	if r.emit(sevWarn, "station", "a warning") != 1 {
		t.Fatal("a warning should still fit in the headroom")
	}
	if r.emit(sevError|oper, "station", "the reason") != 1 {
		t.Fatal("an operator error should still fit in the headroom")
	}
	if r.emit(sevInfo|oper, "station", "a notice") != 1 {
		t.Fatal("an operator notice should still fit in the headroom")
	}

	r.drainAll(logger)
	for _, want := range []string{"a warning", "the reason", "a notice"} {
		if h.count(want) != 1 {
			t.Fatalf("%q did not come through", want)
		}
	}
	if got := h.attr("log ring full", "info"); got != "1" {
		t.Fatalf("expected 1 dropped info in the report, got %q", got)
	}
	if got := h.attr("log ring full", "debug"); got != "1" {
		t.Fatalf("expected 1 dropped debug in the report, got %q", got)
	}
	if got := h.level("log ring full"); got != slog.LevelWarn {
		t.Fatalf("dropped chatter is a warning, got %v", got)
	}
}

// A level nobody would print does not take a slot: the floor follows the
// logger's level and any subscriber's.  Operator messages pass regardless.
func TestLogRingFloorFollowsTheSinks(t *testing.T) {
	r := newStmakLogRing()
	defer r.destroy()
	h := &levelHandler{level: slog.LevelWarn}
	logger := slog.New(h)

	r.setMinLevel(logger)
	if r.emit(sevDebug, "m", "debug") != 0 || r.emit(sevInfo, "m", "info") != 0 {
		t.Fatal("below the logger's level should be filtered at the producer")
	}
	if r.fill() != 0 {
		t.Fatalf("filtered messages took %d slots", r.fill())
	}
	if r.emit(sevWarn, "m", "warn") != 1 || r.emit(sevInfo|oper, "m", "notice") != 1 {
		t.Fatal("a warning and an operator notice must be enqueued")
	}

	// A subscriber at DEBUG lowers the floor; leaving raises it again.
	sub := r.subscribe(sevDebug)
	r.setMinLevel(logger)
	if r.emit(sevDebug, "m", "for the subscriber") != 1 {
		t.Fatal("a DEBUG subscriber should reopen the floor")
	}
	r.drainAll(logger)
	if h.count("for the subscriber") != 0 {
		t.Fatal("the logger printed below its level")
	}
	if h.count("warn") != 1 || h.count("notice") != 0 {
		t.Fatalf("logger got warn=%d notice=%d", h.count("warn"), h.count("notice"))
	}
	if got := subPollMsg(sub); got != "for the subscriber" {
		// "warn" and "notice" came first, in order.
		if got != "warn" || subPollMsg(sub) != "notice" || subPollMsg(sub) != "for the subscriber" {
			t.Fatalf("subscriber did not get its messages in order (first %q)", got)
		}
	}
	r.unsubscribe(sub)
	r.setMinLevel(logger)
	if r.emit(sevDebug, "m", "debug again") != 0 {
		t.Fatal("the floor should be back up once the subscriber is gone")
	}
}

// Producers on several threads at once, with the ring never more than a
// quarter full, lose nothing: every message is either delivered or counted.
// The earlier ring's consumer could step over a producer that had claimed
// a position but not yet reached its slot; that message was lost uncounted
// and cost a counted drop one lap later.
func TestLogRingConcurrentProducersLoseNothing(t *testing.T) {
	r := newStmakLogRing()
	defer r.destroy()
	h := &keepHandler{}
	logger := slog.New(h)

	const producers, each = 4, 20000
	var wg sync.WaitGroup
	var dropped atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if r.drainAll(logger) == 0 {
					runtime.Gosched()
				}
			}
		}
	}()
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Keep the ring far from full, so any loss is the race
				// and not the capacity.
				for r.fill() > 256 {
					runtime.Gosched()
				}
				if r.emit(sevInfo, "p", fmt.Sprintf("msg %d %d", p, i)) < 0 {
					dropped.Add(1)
				}
			}
		}(p)
	}
	wg.Wait()
	close(stop)
	r.drainAll(logger)

	got := int64(h.count("msg "))
	if got+dropped.Load() != producers*each {
		t.Fatalf("sent %d, delivered %d, dropped %d: %d lost without a trace",
			producers*each, got, dropped.Load(), producers*each-got-dropped.Load())
	}
	if dropped.Load() != 0 {
		t.Fatalf("%d drops with the ring under a quarter full", dropped.Load())
	}
}
