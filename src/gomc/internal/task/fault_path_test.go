// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
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
	onAbort     func() // optional hook, called inside Abort (ordering asserts)
}

func (r *recordingInterp) Abort(reason int, _ string) error {
	r.abortReason.Store(int64(reason))
	r.abortCalls.Add(1)
	if r.onAbort != nil {
		r.onAbort()
	}
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

// faultSeqMotion fails the first SetLine (to trigger a sequencer hard fault) and
// counts the Abort + SpindleOff calls seqFaultExit is expected to make.
type faultSeqMotion struct {
	mockMotion
	failSetLine bool
	aborts      atomic.Int64
	spindleOffs atomic.Int64
}

func (m *faultSeqMotion) SetLine(_ Pose, _, _, _ float64, _ int32, _ int32, _ float64, _ int32) error {
	if m.failSetLine {
		return errors.New("injected motion fault")
	}
	return nil
}
func (m *faultSeqMotion) Abort() error           { m.aborts.Add(1); return nil }
func (m *faultSeqMotion) SpindleOff(int32) error { m.spindleOffs.Add(1); return nil }

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
	// 2.9 orders emcIoAbort + emcSpindleAbort BEFORE emcAbortCleanup: the
	// spindle must already be commanded off when on_abort runs (on_abort may
	// execute an unbounded ON_ABORT_COMMAND).
	var spindleOffBeforeOnAbort atomic.Bool
	ri.onAbort = func() {
		spindleOffBeforeOnAbort.Store(mot.hasCall("SpindleOff") && io.ioAbortCalls.Load() > 0)
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
	if !spindleOffBeforeOnAbort.Load() {
		t.Fatal("faultMDI must IoAbort + stop spindles BEFORE running on_abort (2.9 order)")
	}

	task.mu.Lock()
	es := task.execState
	task.mu.Unlock()
	if es != ExecError {
		t.Fatalf("execState = %v, want ExecError", es)
	}
}

// TestSeqFaultExit_StopsMachineWithExecErrorReason is the regression test for
// the sequencer-side EXEC_ERROR parity fix. A rejected motion command drives the
// sequencer into seqFaultExit, which must now stop the hardware like 2.9's
// EMC_TASK_EXEC_ERROR path — abort motion, IoAbort with reason TASK_EXEC_ERROR(1),
// and stop every spindle — not just latch ExecError and rely on the monitor.
func TestSeqFaultExit_StopsMachineWithExecErrorReason(t *testing.T) {
	restore := SetPollInterval(time.Millisecond)
	t.Cleanup(restore)

	mot := &faultSeqMotion{failSetLine: true}
	io := &recordingIO{}
	stat := &mockStatus{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, io, stat, logger)
	task.numSpindles = 2

	task.StartSequencer()
	t.Cleanup(task.StopSequencer)

	task.EnqueueCmd(&LinearMoveCmd{ID: 1})

	select {
	case <-task.seqDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sequencer did not exit on the injected motion fault")
	}

	if mot.aborts.Load() == 0 {
		t.Error("seqFaultExit must abort motion (2.9 emcTaskAbort)")
	}
	if io.ioAbortCalls.Load() == 0 {
		t.Fatal("seqFaultExit must IoAbort the IO controller (2.9 emcIoAbort)")
	}
	if got := io.ioAbortReason.Load(); got != int64(emcAbortTaskExecError) {
		t.Fatalf("io.IoAbort reason = %d, want %d (EMC_ABORT_TASK_EXEC_ERROR)", got, emcAbortTaskExecError)
	}
	if got := mot.spindleOffs.Load(); got != 2 {
		t.Fatalf("SpindleOff called %d times, want 2 (one per spindle)", got)
	}

	task.mu.Lock()
	es := task.execState
	task.mu.Unlock()
	if es != ExecError {
		t.Fatalf("execState = %v, want ExecError", es)
	}
}

// TestSeqFault_RecoversInterpWithoutProducer is the regression test for the
// producer-less leg of the sequencer fault path. A queued command rejected by
// the sequencer after the producer is gone (MDI whose mdiDoneCmd never runs,
// or a program past its last enqueue) used to leave the interpreter's
// toolchange/probe flags stale forever: canon swallows enqueue errors, so the
// faultProgram/faultMDI cascade the old seqFaultExit comment promised never
// fired, and the next MDI ran against the dirty interp. seqFaultExit must now
// hand off to recoverSeqFault, which runs on_abort with
// EMC_ABORT_TASK_EXEC_ERROR (2.9 emcAbortCleanup(1)) and restarts the
// sequencer so the task accepts new work.
func TestSeqFault_RecoversInterpWithoutProducer(t *testing.T) {
	restore := SetPollInterval(time.Millisecond)
	t.Cleanup(restore)

	mot := &faultSeqMotion{failSetLine: true}
	io := &recordingIO{}
	stat := &mockStatus{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, io, stat, logger)
	task.numSpindles = 1
	ri := &recordingInterp{}
	task.SetInterpreter(ri)

	task.StartSequencer()
	t.Cleanup(task.StopSequencer)

	// No producer goroutine exists: the command is enqueued directly, like an
	// MDI whose interpreter work already finished.
	task.EnqueueCmd(&LinearMoveCmd{ID: 1})

	if !waitForCond(2*time.Second, func() bool { return ri.abortCalls.Load() > 0 }) {
		t.Fatal("sequencer fault must run interp on_abort even with no live producer")
	}
	if got := ri.abortReason.Load(); got != int64(emcAbortTaskExecError) {
		t.Fatalf("interp.Abort reason = %d, want %d (EMC_ABORT_TASK_EXEC_ERROR)", got, emcAbortTaskExecError)
	}
	// Recovery restarts the sequencer (with the terminal ExecError latched), so
	// the next command does not fail on a dead queue.
	if !waitForCond(2*time.Second, func() bool { return task.SeqRunning() }) {
		t.Fatal("recoverSeqFault must restart the sequencer")
	}
	task.mu.Lock()
	es := task.execState
	faulted := task.seqFaulted
	task.mu.Unlock()
	if es != ExecError {
		t.Fatalf("execState = %v, want ExecError", es)
	}
	if faulted {
		t.Fatal("seqFaulted must be cleared once recovery has run")
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
