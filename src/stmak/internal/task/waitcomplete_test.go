// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestWaitComplete_SettlesNoMotionMDI — regression for the AXIS touch-off race.
//
// A no-motion MDI (touch-off's "G10 L20 ...") never moves execState off
// ExecDone; only interpState goes busy (set synchronously by the MDI call,
// cleared by the async finishMDI). WaitComplete used to check execState only,
// so a wait_complete issued right after c.mdi() returned immediately, and the
// client's next command (AXIS: program_open to reload the preview) hit the
// "Can't open a program while one is running" reject. WaitComplete must not
// return until the interpreter is idle and the MDI queue is empty.
//
// finishMDI's completion is held open deterministically by blocking the
// SECOND interp.Synch() (the first happens inside executeMDI before
// ExecuteString; the second is finishMDI's post-drain synch).
func TestWaitComplete_SettlesNoMotionMDI(t *testing.T) {
	restore := SetPollInterval(time.Millisecond)
	t.Cleanup(restore)

	mot := &recordMotion{}
	io := &mockIO{}
	stat := &mockStatus{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, io, stat, logger)
	task.SetIOStatusReader(io)
	task.noForceHoming = true

	// Gate finishMDI's post-drain Synch. Armed only around the MDI: bring-up
	// and SetMode also call Synch and must not hit the gate. Once armed, the
	// first Synch is executeMDI's pre-ExecuteString one (passes), the second
	// is finishMDI's (blocks until released).
	gate := make(chan struct{})
	var armed, synchs int32
	fi := &fakeInterp{}
	fi.onSynch = func() {
		if atomic.LoadInt32(&armed) == 1 && atomic.AddInt32(&synchs, 1) >= 2 {
			<-gate
		}
	}
	fi.onExecuteString = func(string) (int, error) {
		// Like G10 L20: canon work but no motion — execState stays ExecDone.
		task.canon.enqueue(&DisplayMsgCmd{Text: "touch-off"})
		return InterpOK, nil
	}

	task.SetInterpreter(fi)
	bringUp(t, task)
	if err := task.SetMode(int32(ModeMDI)); err != nil {
		t.Fatalf("SetMode(MDI): %v", err)
	}
	task.StartSequencer()
	t.Cleanup(task.StopSequencer)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})

	atomic.StoreInt32(&armed, 1)
	if err := task.MDI("G10 L20 P1 X0"); err != nil {
		t.Fatalf("MDI: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- task.WaitComplete(5) }()

	// While finishMDI is gated, the MDI is still in flight — WaitComplete
	// returning here is exactly the touch-off bug.
	select {
	case err := <-done:
		t.Fatalf("WaitComplete returned (err=%v) while the MDI was still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(gate)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitComplete: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitComplete did not return after the MDI settled")
	}

	task.mu.Lock()
	busy := task.programBusy()
	queued := len(task.mdiQueue)
	task.mu.Unlock()
	if busy || queued != 0 {
		t.Fatalf("after WaitComplete: programBusy=%v queuedMDIs=%d, want settled", busy, queued)
	}
}
