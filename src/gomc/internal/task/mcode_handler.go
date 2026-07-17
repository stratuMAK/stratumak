// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"context"
	"fmt"
	"sync"
)

// mcodeHandlerNum is the number of user-defined M-codes (M100-M199).
const mcodeHandlerNum = 100

// McodeCall holds the parameters for an M-code handler invocation.
type McodeCall struct {
	Mcode   int32
	P       float64
	Q       float64
	abortCh <-chan struct{} // becomes readable on abort
}

// AbortRequested returns true if abort has been signaled.
func (c *McodeCall) AbortRequested() bool {
	select {
	case <-c.abortCh:
		return true
	default:
		return false
	}
}

// McodeHandlerFunc is the signature for M-code handlers.
// Returns 0 on success, non-zero on error.
// Values 32-63 are mapped to user_defined_result (existing convention).
type McodeHandlerFunc func(call *McodeCall) int

// mcodeHandler manages the M100-M199 handler registry and worker goroutine.
type mcodeHandler struct {
	mu       sync.Mutex
	handlers [mcodeHandlerNum]McodeHandlerFunc

	// Worker state
	jobCh   chan mcodeJob
	abortCh chan struct{} // closed to signal abort to running handler
	doneCh  chan struct{} // closed when worker exits
}

type mcodeJob struct {
	mcode   int
	p, q    float64
	abortCh <-chan struct{}
	// resultCh carries this job's result and nothing else. It is buffered so
	// the worker can never block on a caller that has stopped listening.
	resultCh chan int
}

// mcodeSub is the caller's handle on ONE submitted job. Completion is scoped to
// the job that produced it, so a caller that gives up (abort) cannot have its
// result handed to the next caller: the abandoned job delivers into its own
// resultCh, which is then simply garbage collected.
type mcodeSub struct {
	resultCh <-chan int
}

// check reports (result, true) once THIS submission's handler has finished.
// It is non-blocking, so it drops straight into the sequencer's poll loop.
func (s *mcodeSub) check() (int, bool) {
	select {
	case r := <-s.resultCh:
		return r, true
	default:
		return 0, false
	}
}

// newMcodeHandler creates and starts the handler worker.
func newMcodeHandler() *mcodeHandler {
	h := &mcodeHandler{
		jobCh:   make(chan mcodeJob, 1),
		abortCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go h.worker()
	return h
}

// RegisterHandler registers a handler for an M-code (100-199).
func (h *mcodeHandler) RegisterHandler(mcode int, fn McodeHandlerFunc) error {
	if mcode < 100 || mcode > 199 {
		return fmt.Errorf("mcode_handler: invalid mcode %d (must be 100-199)", mcode)
	}
	idx := mcode - 100
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handlers[idx] != nil {
		return fmt.Errorf("mcode_handler: M%d already has a handler", mcode)
	}
	h.handlers[idx] = fn
	return nil
}

// HasHandler returns true if a handler is registered for the given M-code.
func (h *mcodeHandler) HasHandler(mcode int) bool {
	if mcode < 100 || mcode > 199 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handlers[mcode-100] != nil
}

// Submit submits an M-code for execution and returns a handle on THAT job's
// completion. Blocks until the worker accepts the job, so a caller that
// abandoned a previous job (abort) cannot have its successor rejected while the
// worker is still draining the abandoned one; aborted returns context.Canceled.
//
// Completion is per-job by construction. A bare "done" flag cannot work here:
// it records that A job finished, not WHICH one, so the moment one caller stops
// waiting (McodeCmd.Execute returns on abort while its handler is still
// running) the stale completion is credited to the next caller's job and the
// sequencer runs permanently one job ahead of the worker — silently reporting
// M-codes as complete that never ran.
func (h *mcodeHandler) Submit(mcode int, p, q float64, abort <-chan struct{}) (*mcodeSub, error) {
	if mcode < 100 || mcode > 199 {
		return nil, fmt.Errorf("mcode_handler: invalid mcode %d", mcode)
	}
	h.mu.Lock()
	fn := h.handlers[mcode-100]
	h.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("mcode_handler: no handler for M%d", mcode)
	}

	// Reset abort channel for new job
	h.mu.Lock()
	h.abortCh = make(chan struct{})
	abortCh := h.abortCh
	h.mu.Unlock()

	resultCh := make(chan int, 1)
	job := mcodeJob{mcode: mcode, p: p, q: q, abortCh: abortCh, resultCh: resultCh}
	select {
	case h.jobCh <- job:
		return &mcodeSub{resultCh: resultCh}, nil
	case <-abort:
		return nil, context.Canceled
	}
}

// Abort signals the running handler to stop.
//
// The check-and-close is done under h.mu, not around it: two aborts racing (any
// of the three abort paths vs. Stop) would otherwise both observe "not closed"
// and double-close, which panics — and a lone reader could close a channel that
// Submit had already swapped out from under it, aborting nothing.
func (h *mcodeHandler) Abort() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.abortCh:
		// already closed
	default:
		close(h.abortCh)
	}
}

// Stop shuts down the worker goroutine.
func (h *mcodeHandler) Stop() {
	close(h.jobCh)
	h.Abort()
	<-h.doneCh
}

// worker is the goroutine that executes M-code handlers sequentially.
func (h *mcodeHandler) worker() {
	defer close(h.doneCh)

	for job := range h.jobCh {
		h.mu.Lock()
		fn := h.handlers[job.mcode-100]
		h.mu.Unlock()

		if fn == nil {
			job.resultCh <- -1
			continue
		}

		call := &McodeCall{
			Mcode:   int32(job.mcode),
			P:       job.p,
			Q:       job.q,
			abortCh: job.abortCh,
		}

		// resultCh is buffered and belongs to this job alone, so this never
		// blocks even when the submitter has already given up on it.
		job.resultCh <- fn(call)
	}
}
