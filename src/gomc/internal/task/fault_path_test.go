// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// recordingInterp records the reason passed to Abort (= C Interp::on_abort), so
// a test can prove the interpreter-error fault paths run on_abort (which reset()s
// the interp and clears the toolchange/probe/input/mdi_interrupt flags) with the
// correct EMC_ABORT_* reason instead of the old hardcoded 0.
type recordingInterp struct {
	fakeInterp
	abortReason atomic.Int64
	abortCalls  atomic.Int64
}

func (r *recordingInterp) Abort(reason int, _ string) error {
	r.abortReason.Store(int64(reason))
	r.abortCalls.Add(1)
	return nil
}

// recordingIO records IoAbort so a test can prove the MDI interp-error path
// aborts the IO controller (and the AUTO readahead path does not).
type recordingIO struct {
	mockIO
	ioAbortReason atomic.Int64
	ioAbortCalls  atomic.Int64
}

func (r *recordingIO) IoAbort(reason int32) error {
	r.ioAbortReason.Store(int64(reason))
	r.ioAbortCalls.Add(1)
	return nil
}

func newRecordingTask() (*Task, *mockMotion, *recordingIO) {
	mot := &mockMotion{}
	io := &recordingIO{}
	stat := &mockStatus{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t := NewTask(mot, io, stat, logger)
	t.SetIOStatusReader(io)
	t.numSpindles = 1
	return t, mot, io
}

// TestFaultMDI_RunsOnAbortAbortsIOAndSpindle is the regression test for the MDI
// leg of the milltask fault-path parity fix. On an MDI interpreter error, 2.9's
// EMC_TASK_PLAN_EXECUTE INTERP_ERROR path does emcIoAbort(10) + emcSpindleAbort(all)
// + emcAbortCleanup(10) (-> Interp::on_abort(10)). Before the fix, faultMDI only
// stopped motion and latched ExecError: the interpreter kept its stale
// toolchange/probe/input flags, the IO controller was never told to abort, and a
// running spindle kept spinning.
func TestFaultMDI_RunsOnAbortAbortsIOAndSpindle(t *testing.T) {
	restore := SetPollInterval(time.Millisecond)
	t.Cleanup(restore)

	task, mot, io := newRecordingTask()
	task.noForceHoming = true

	ri := &recordingInterp{}
	ri.onExecuteString = func(string) (int, error) {
		return InterpError, fmt.Errorf("bad word")
	}
	task.SetInterpreter(ri)

	bringUp(task)
	if err := task.SetMode(int32(ModeMDI)); err != nil {
		t.Fatalf("SetMode(MDI): %v", err)
	}
	task.StartSequencer()
	t.Cleanup(task.StopSequencer)

	if err := task.MDI("g0 x1"); err == nil {
		t.Fatal("expected MDI to return the interpreter error")
	}

	if ri.abortCalls.Load() == 0 {
		t.Fatal("faultMDI must run interp on_abort (clears toolchange/probe/input flags)")
	}
	if got := ri.abortReason.Load(); got != int64(emcAbortInterpreterErrorMDI) {
		t.Fatalf("interp.Abort reason = %d, want %d (EMC_ABORT_INTERPRETER_ERROR_MDI)", got, emcAbortInterpreterErrorMDI)
	}
	if io.ioAbortCalls.Load() == 0 {
		t.Fatal("faultMDI must IoAbort the IO controller (parity with 2.9 emcIoAbort(10))")
	}
	if got := io.ioAbortReason.Load(); got != int64(emcAbortInterpreterErrorMDI) {
		t.Fatalf("io.IoAbort reason = %d, want %d", got, emcAbortInterpreterErrorMDI)
	}
	if !mot.hasCall("SpindleOff") {
		t.Fatal("faultMDI must stop the spindle(s) (parity with 2.9 emcSpindleAbort)")
	}

	task.mu.Lock()
	es := task.execState
	task.mu.Unlock()
	if es != ExecError {
		t.Fatalf("execState = %v, want ExecError", es)
	}
}

// TestFaultProgram_RunsOnAbortNoIOAbort is the regression test for the AUTO
// (readahead) leg. 2.9's readahead-execute error does interp_list.clear() +
// emcAbortCleanup(EMC_ABORT_INTERPRETER_ERROR=9) -> Interp::on_abort(9) and,
// unlike the MDI path, does NOT emcIoAbort or emcSpindleAbort. The fix makes
// faultProgram run on_abort with reason 9; this test also pins that it stays
// light (no IoAbort) so the two legs don't collapse into one.
func TestFaultProgram_RunsOnAbortNoIOAbort(t *testing.T) {
	restore := SetPollInterval(time.Millisecond)
	t.Cleanup(restore)

	task, _, io := newRecordingTask()
	ri := &recordingInterp{}
	task.SetInterpreter(ri)

	bringUp(task)
	task.StartSequencer()
	t.Cleanup(task.StopSequencer)

	task.faultProgram(emcAbortInterpreterError, "interpreter error")

	if ri.abortCalls.Load() == 0 {
		t.Fatal("faultProgram must run interp on_abort to clear the interrupted-remap flags")
	}
	if got := ri.abortReason.Load(); got != int64(emcAbortInterpreterError) {
		t.Fatalf("interp.Abort reason = %d, want %d (EMC_ABORT_INTERPRETER_ERROR)", got, emcAbortInterpreterError)
	}
	if io.ioAbortCalls.Load() != 0 {
		t.Fatalf("faultProgram (AUTO readahead) must not IoAbort; got %d calls", io.ioAbortCalls.Load())
	}

	task.mu.Lock()
	es := task.execState
	task.mu.Unlock()
	if es != ExecError {
		t.Fatalf("execState = %v, want ExecError", es)
	}
}
