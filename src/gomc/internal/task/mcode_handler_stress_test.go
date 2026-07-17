// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// errStressAbort stands in for the sequencer's context.Canceled abort return.
var errStressAbort = errors.New("aborted")

// execOnce models McodeCmd.Execute: Submit, then poll for completion until it
// reports done or the abort channel fires. Returns errStressAbort when the wait
// was cut short by an abort (the real Execute returns context.Canceled) -- the
// one path that leaves Execute WITHOUT having collected its job's result.
func execOnce(h *mcodeHandler, mcode int, abort <-chan struct{}) (int, error) {
	sub, err := h.Submit(mcode, 0, 0, abort)
	if errors.Is(err, context.Canceled) {
		return 0, errStressAbort // abort landed before the worker took the job
	}
	if err != nil {
		return 0, err
	}
	// pollInterval is 10ms in the sequencer; tick faster so the test covers many
	// more Submit/completion interleavings per second than production does.
	tick := time.NewTicker(50 * time.Microsecond)
	defer tick.Stop()
	for {
		select {
		case <-abort:
			return 0, errStressAbort
		case <-tick.C:
			if r, done := sub.check(); done {
				return r, nil
			}
		}
	}
}

// TestMcodeHandlerConcurrentAbort hammers Abort from several goroutines while
// jobs are being submitted. Abort's check-and-close must be atomic: two aborts
// racing (there are three abort paths plus Stop) would otherwise both see an
// open channel and double-close it, panicking the process.
func TestMcodeHandlerConcurrentAbort(t *testing.T) {
	h := newMcodeHandler()
	defer h.Stop()

	if err := h.RegisterHandler(100, func(call *McodeCall) int { return 0 }); err != nil {
		t.Fatal(err)
	}

	// Release the aborters from a common start signal so they collide on the
	// same fresh abortCh: a loose loop almost never lands two in the window
	// together, and would pass against the unlocked check-and-close it guards.
	for round := 0; round < 20000; round++ {
		h.mu.Lock()
		h.abortCh = make(chan struct{}) // as Submit does for each new job
		h.mu.Unlock()

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				h.Abort()
			}()
		}
		close(start)
		wg.Wait() // a double-close would have panicked the process by now
	}
}

// TestMcodeHandlerNoResultCrosstalk drives the M-code handshake the way the
// sequencer does, with aborts cutting the wait short while a handler is still
// running. That is the interleaving the queue-buster tests hit (~1100 M-codes
// with tool-change queue-busters sprinkled through), and it is where a
// completion can be credited to the wrong job.
//
// The invariant under test is the one the classic mdi-queue test asserts at the
// G-code level -- every M-code fires, none are dropped: an Execute that reports
// success MUST correspond to a handler invocation that actually happened.
func TestMcodeHandlerNoResultCrosstalk(t *testing.T) {
	h := newMcodeHandler()
	defer h.Stop()

	var mu sync.Mutex
	ran := 0 // handler invocations that actually completed
	if err := h.RegisterHandler(100, func(call *McodeCall) int {
		// Long enough that an abort can land while the handler is mid-run.
		time.Sleep(200 * time.Microsecond)
		mu.Lock()
		ran++
		mu.Unlock()
		return 0
	}); err != nil {
		t.Fatal(err)
	}

	const iterations = 2000
	completed, aborted, busy := 0, 0, 0

	for i := 0; i < iterations; i++ {
		abort := make(chan struct{})
		// Every other iteration gets an abort racing the handler's completion.
		if i%2 == 0 {
			go func() {
				time.Sleep(150 * time.Microsecond)
				close(abort)
			}()
		}

		done := make(chan error, 1)
		go func() {
			_, err := execOnce(h, 100, abort)
			done <- err
		}()

		select {
		case err := <-done:
			switch {
			case err == nil:
				completed++
			case errors.Is(err, errStressAbort):
				aborted++
				// The sequencer aborted this job; tell the worker to stop, as
				// the real abort path does via Task.mcodeAbort.
				h.Abort()
			default:
				// "worker busy" -- Submit rejected the job outright.
				busy++
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("WEDGE at iteration %d: Execute never observed completion "+
				"(worker idle with nothing left to run)", i)
		}
	}

	mu.Lock()
	actuallyRan := ran
	mu.Unlock()

	t.Logf("iterations=%d completed=%d aborted=%d worker-busy=%d handlers-ran=%d",
		iterations, completed, aborted, busy, actuallyRan)

	// Every success must be backed by a real handler invocation. Aborted jobs
	// may or may not have run, so handlers-ran can legitimately EXCEED
	// completed -- but it can never fall short of it.
	if actuallyRan < completed {
		t.Errorf("result crosstalk: %d Executes reported success but only %d "+
			"handlers ran -- %d M-codes were silently credited to another job's "+
			"completion", completed, actuallyRan, completed-actuallyRan)
	}
	if busy > 0 {
		t.Errorf("Submit rejected %d jobs as \"worker busy\": the handshake lost "+
			"alignment between the sequencer and the worker", busy)
	}
}
