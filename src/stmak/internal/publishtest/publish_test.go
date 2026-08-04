// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package publishtest exercises the generated @publish drain's multi-subscriber
// delivery at runtime against a real generated package (emcerror). It is the
// regression test for the operator-message-loss bug root-caused in
// GMICOMPILE_REVIEW_FINDINGS.md (G-H1): the generated drain used to expose one
// shared, destructive-flush Watch, so with N WS subscribers each operator
// message reached only one of them. The fix hands each connection its own cursor
// over a retained buffer via WatchFactory. These tests fail against the old codegen.
package publishtest

import (
	"encoding/json"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcerror"
)

// poll drains one subscriber's WatchFunc and returns the decoded events.
func poll(t *testing.T, fn func() (json.RawMessage, error)) []emcerror.PublishErrorEvent {
	t.Helper()
	raw, err := fn()
	if err != nil {
		t.Fatalf("watch fn: %v", err)
	}
	var evs []emcerror.PublishErrorEvent
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return evs
}

func TestPublishDrainMultiSubscriber(t *testing.T) {
	// No C ring — drive the drain through its Go-side Publish path.
	d := emcerror.NewPublishErrorDrain(nil)
	factory := d.WatchFactory()

	// Two subscribers, both connected before any event is published.
	subA, err := factory(nil)
	if err != nil {
		t.Fatalf("factory A: %v", err)
	}
	subB, err := factory(nil)
	if err != nil {
		t.Fatalf("factory B: %v", err)
	}

	for _, msg := range []string{"err1", "err2", "err3"} {
		d.PublishError(emcerror.ErrorKind(1), msg)
	}

	// Both subscribers must receive ALL three events. Under the old shared,
	// destructive-flush Watch the first poller emptied the buffer and the other
	// got []; this is exactly the bug.
	gotA := poll(t, subA)
	gotB := poll(t, subB)
	if len(gotA) != 3 || len(gotB) != 3 {
		t.Fatalf("each subscriber must get all 3 events; got A=%d B=%d (the old shared-flush Watch would split them)", len(gotA), len(gotB))
	}
	for i, want := range []string{"err1", "err2", "err3"} {
		if gotA[i].Text != want || gotB[i].Text != want {
			t.Fatalf("event %d: A=%q B=%q want %q", i, gotA[i].Text, gotB[i].Text, want)
		}
	}

	// Re-polling with no new events yields nothing (no re-delivery).
	if again := poll(t, subA); len(again) != 0 {
		t.Fatalf("re-poll must be empty, got %d", len(again))
	}

	// A late subscriber sees only events published after it subscribed.
	d.PublishError(emcerror.ErrorKind(1), "err4")
	subC, _ := factory(nil)
	d.PublishError(emcerror.ErrorKind(1), "err5")

	gotC := poll(t, subC)
	if len(gotC) != 1 || gotC[0].Text != "err5" {
		t.Fatalf("late subscriber should see only err5, got %+v", gotC)
	}
	// The earlier subscriber, now caught up, sees err4 and err5.
	if latest := poll(t, subA); len(latest) != 2 {
		t.Fatalf("subA should see err4+err5, got %d", len(latest))
	}
}

// TestPublishDrainBoundedBuffer verifies the retained buffer is bounded
// (GMICOMPILE_REVIEW_FINDINGS.md G-M1: the accumulator used to grow unbounded
// with no subscriber). Publishing far more than the cap keeps only the most
// recent PublishErrorWatchBufferSize events for a cursor that never polled.
func TestPublishDrainBoundedBuffer(t *testing.T) {
	d := emcerror.NewPublishErrorDrain(nil)
	factory := d.WatchFactory()
	sub, _ := factory(nil) // subscribes at seq 0, then never keeps up

	const overfill = emcerror.PublishErrorWatchBufferSize + 100
	for i := 0; i < overfill; i++ {
		d.PublishError(emcerror.ErrorKind(1), "x")
	}

	got := poll(t, sub)
	if len(got) > emcerror.PublishErrorWatchBufferSize {
		t.Fatalf("retained buffer must be bounded to %d, got %d", emcerror.PublishErrorWatchBufferSize, len(got))
	}
	if len(got) != emcerror.PublishErrorWatchBufferSize {
		t.Fatalf("a fully-behind cursor should see exactly the retained %d newest events, got %d", emcerror.PublishErrorWatchBufferSize, len(got))
	}
}
