// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/internal/programfilter"
)

// mcodeAbort signals the M-code handler worker to stop.
func (t *Task) mcodeAbort() {
	if t.mcode != nil {
		t.mcode.Abort()
	}
}

// --- Command guards ----------------------------------------------------
//
// Every cmdMu command runs its FULL guard set twice: once in a preflight,
// WITHOUT cmdMu, so a request that cannot succeed is rejected immediately
// instead of queueing behind whatever command currently holds the lock; and
// once inside the serialized body, which remains the authoritative check
// (state may change while waiting for cmdMu). A preflight can only produce a
// spurious early rejection — indistinguishable from the command racing the
// state change — never a wrong acceptance, so correctness lives entirely in
// the body. Both call the SAME shared *Locked guard primitives below (each
// assumes t.mu is held), so the two copies cannot drift.

// canSwitchModeAutoLocked rejects a mode switch away from AUTO while an AUTO
// program/MDI is mid-run. Must hold t.mu.
func (t *Task) canSwitchModeAutoLocked(target TaskMode) error {
	if t.mode == ModeAuto && t.interpState != InterpIdle && target != ModeAuto {
		t.operatorError("Can't switch mode while mode is AUTO and interpreter is not IDLE")
		return ErrBusy
	}
	return nil
}

// canPowerOnLocked reports whether STATE_ON is reachable from the current state.
// Must hold t.mu.
func (t *Task) canPowerOnLocked() error {
	if t.state != StateOn && t.state != StateEstopReset && t.state != StateOff {
		t.operatorError("Can't turn the machine ON from the current state")
		return ErrNotOn
	}
	return nil
}

// rejectIfBusyLocked rejects while a program or MDI is mid-run, emitting msg
// (empty msg = reject silently). Must hold t.mu.
func (t *Task) rejectIfBusyLocked(msg string) error {
	if t.programBusy() {
		if msg != "" {
			t.operatorError(msg)
		}
		return ErrBusy
	}
	return nil
}

// rejectIfBusy is rejectIfBusyLocked for callers that do not already hold t.mu.
func (t *Task) rejectIfBusy(msg string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rejectIfBusyLocked(msg)
}

// mdiQueueFullLocked rejects an MDI when the interpreter is busy and the MDI
// queue is already full. Must hold t.mu.
func (t *Task) mdiQueueFullLocked() error {
	if t.interpState != InterpIdle && len(t.mdiQueue) >= t.maxMDIQueued {
		t.operatorError("MDI queue full")
		return ErrBusy
	}
	return nil
}

// preflightOn rejects when the machine is not on (Flood/Mist/Lube et al.).
func (t *Task) preflightOn() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requireOn()
}

// preflightSetState mirrors setState's reject conditions.
func (t *Task) preflightSetState(target TaskState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch target {
	case StateOff:
		return t.requireNotEstop()
	case StateOn:
		return t.canPowerOnLocked()
	}
	return nil
}

// preflightSetMode mirrors SetMode's AUTO-running guard.
func (t *Task) preflightSetMode(target TaskMode) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canSwitchModeAutoLocked(target)
}

// preflightNotBusy rejects while a program or MDI is mid-run, emitting msg.
func (t *Task) preflightNotBusy(msg string) error {
	return t.rejectIfBusy(msg)
}

// autoRunGuardLocked checks the AutoRun preconditions. It is the single copy of
// this guard chain (including operator strings), called by preflightAuto (the
// lock-free early reject) and by autoCommand (the authoritative check). Must be
// called with t.mu held; it does not release it.
func (t *Task) autoRunGuardLocked() error {
	if t.programBusy() {
		t.operatorError("Can't run a program while one is running")
		return ErrBusy
	}
	if err := t.requireProgram(); err != nil {
		return err
	}
	if err := t.requireHomed(); err != nil {
		t.operatorError("Can't run a program when not homed")
		return fmt.Errorf("can't run program when not homed")
	}
	if t.externalOffsetApplied() {
		t.operatorError("Can't run a program with external offsets applied")
		return fmt.Errorf("can't run program with external offsets applied")
	}
	return nil
}

// autoStepGuardLocked checks the AutoStep preconditions: a program must be
// loaded, and when starting fresh (interp idle) the machine must be homed and no
// other run in progress. Shared by preflightAuto and autoCommand. Must hold t.mu.
func (t *Task) autoStepGuardLocked() error {
	if err := t.requireProgram(); err != nil {
		return err
	}
	if t.interpState == InterpIdle {
		if t.programBusy() {
			t.operatorError("Can't run a program while one is running")
			return ErrBusy
		}
		if err := t.requireHomed(); err != nil {
			t.operatorError("Can't run a program when not homed")
			return fmt.Errorf("can't run program when not homed")
		}
	}
	return nil
}

// preflightAuto mirrors autoCommand's guard chain for the serialized cases,
// reusing the same guard funcs so the two can't drift.
func (t *Task) preflightAuto(cmd int32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.requireOn(); err != nil {
		return err
	}
	if err := t.canSwitchMode(ModeAuto); err != nil {
		// canSwitchMode is silent (also used by MDI, which words its own
		// message); surface the AUTO-specific reason here.
		t.operatorError("Can't switch to AUTO mode while the machine is busy")
		return err
	}
	switch cmd {
	case AutoRun:
		return t.autoRunGuardLocked()
	case AutoStep:
		return t.autoStepGuardLocked()
	}
	return nil
}

// requireHomedForMDILocked is the MDI "not homed" guard shared by preflightMDI
// and MDI (same operator string). Must hold t.mu.
func (t *Task) requireHomedForMDILocked() error {
	if err := t.requireHomed(); err != nil {
		t.operatorError("Can't issue MDI command when not homed")
		return fmt.Errorf("can't issue MDI command when not homed")
	}
	return nil
}

// preflightMDI mirrors MDI's guard chain (mode check via canSwitchMode; the body
// switches via ensureMode).
func (t *Task) preflightMDI() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.requireOn(); err != nil {
		return err
	}
	if err := t.canSwitchMode(ModeMDI); err != nil {
		t.operatorError("Must be in MDI mode to issue MDI command")
		return err
	}
	if err := t.requireHomedForMDILocked(); err != nil {
		return err
	}
	return t.mdiQueueFullLocked()
}

// preflightJog mirrors jogInternal's canJog guard for moving jogs.
func (t *Task) preflightJog() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canJog()
}

// preflightSpindle mirrors Spindle/Brake ownership guards: manual on/off/
// brake is rejected while a program runs; overrides are always allowed.
func (t *Task) preflightSpindle(cmd int32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.requireOn(); err != nil {
		return err
	}
	switch cmd {
	case SpindleForward, SpindleReverse, SpindleOff:
		return t.spindleOwnedByProgram()
	}
	return nil
}

// preflightManualMode mirrors Home/OverrideLimits: requires ON (Home) or
// gates the manual-mode switch when ON (OverrideLimits handles estop itself).
func (t *Task) preflightManualMode(requireOn bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if requireOn {
		if err := t.requireOn(); err != nil {
			return err
		}
	} else if t.state != StateOn {
		// OverrideLimits off the ON path: nothing to gate.
		return nil
	}
	if err := t.canSwitchMode(ModeManual); err != nil {
		// canSwitchMode is silent (shared with MDI); surface the reason here.
		t.operatorError("Can't switch to manual mode while the machine is busy")
		return err
	}
	return nil
}

// signalAbort fires the fast abort signals WITHOUT taking cmdMu: close the
// sequencer abort channel, abort the M-code worker, and abort motion. All
// three are idempotent and safe from any goroutine.
//
// This must never be gated on cmdMu: the abort signal is what unblocks a
// command that is stuck holding cmdMu (e.g. an MDI producer blocked in
// EnqueueCmd backpressure on a paused sequencer). Abort-class entry points
// call this first, then take cmdMu for the state cleanup.
func (t *Task) signalAbort() {
	t.mu.Lock()
	t.closeOnceLocked(t.seqAbort)
	t.mu.Unlock()
	t.mcodeAbort()
	// An in-flight [FILTER] conversion is part of what abort means: the
	// operator asked for everything in progress to stop, and a filter can run
	// for minutes.
	t.cancelFiltering()
	_ = t.motion.Abort()
}

// This file implements the 27 emccmd GMI methods.
// Each method corresponds to a UI command entry point.
// Pattern: guard → action → state update.

// AutoCmd constants.
const (
	AutoRun     int32 = 0
	AutoPause   int32 = 1
	AutoResume  int32 = 2
	AutoStep    int32 = 3
	AutoReverse int32 = 4
	AutoForward int32 = 5 // resume forward after run-in-reverse (emccmd.gmi AUTO_FORWARD)
)

// JogType constants.
const (
	JogStop       int32 = 0
	JogContinuous int32 = 1
	JogIncrement  int32 = 2
	JogAbsolute   int32 = 3
)

// SpindleCmd constants. Values MUST match the emccmd SpindleCmd wire enum
// (generated/gmi/emccmd) — the GMI provider forwards the raw wire value, and
// halui calls these by name, so both paths go through the same numbering.
// Increase/Decrease previously used 2/-2 and never matched the wire enum's
// 10/11, so spindle +/- override was a silent no-op from GMI/WebSocket clients.
const (
	SpindleOff      int32 = 0
	SpindleForward  int32 = 1
	SpindleReverse  int32 = -1
	SpindleIncrease int32 = 10
	SpindleDecrease int32 = 11
	SpindleConstant int32 = 12
)

// IO abort reason codes (EMC_IO_ABORT_REASON_ENUM, emc.hh / iocontrol_stat.h).
// The reason is passed to io.IoAbort AND, via interp.Abort, to Interp::on_abort
// where it is the numeric argument to a configured [RS274NGC]ON_ABORT_COMMAND.
const (
	emcAbortTaskExecError       int32 = 1  // EMC_ABORT_TASK_EXEC_ERROR
	emcAbortAuxEstop            int32 = 2  // EMC_ABORT_AUX_ESTOP
	emcAbortMotionOrIoRcsError  int32 = 3  // EMC_ABORT_MOTION_OR_IO_RCS_ERROR
	emcAbortTaskStateOff        int32 = 4  // EMC_ABORT_TASK_STATE_OFF
	emcAbortTaskStateEstopReset int32 = 5  // EMC_ABORT_TASK_STATE_ESTOP_RESET
	emcAbortTaskStateEstop      int32 = 6  // EMC_ABORT_TASK_STATE_ESTOP
	emcAbortTaskStateNotOn      int32 = 7  // EMC_ABORT_TASK_STATE_NOT_ON
	emcAbortTaskAbort           int32 = 8  // EMC_ABORT_TASK_ABORT
	emcAbortInterpreterError    int32 = 9  // EMC_ABORT_INTERPRETER_ERROR (readahead)
	emcAbortInterpreterErrorMDI int32 = 10 // EMC_ABORT_INTERPRETER_ERROR_MDI
)

// shutdownOpts configures a teardown (stopSignals + finishShutdown, or the
// all-in-one machineShutdown). A full off/estop turns everything off, unhomes
// volatile-home joints, re-synchs the interpreter, and ends in ExecDone; an
// error/enable-lost stop only aborts motion+IO, leaves coolant/lube/homing
// alone (a following error must not lose the home reference), skips the synch,
// and latches ExecError.
type shutdownOpts struct {
	// abortReason is the EMC_ABORT_* code passed to io.IoAbort AND to interp
	// on_abort (abortInterp), where it becomes the numeric argument of a
	// configured ON_ABORT_COMMAND. The zero value (0) is out-of-enum (the C
	// enum starts at TASK_EXEC_ERROR=1) — always set it explicitly, ideally
	// via fullShutdownOpts.
	abortReason  int32
	disable      bool      // motion.Disable() — full off only
	coolantOff   bool      // flood + mist off (and clear their status flags)
	lubeOff      bool      // lube off (and clear lubeOn)
	unhome       bool      // JointUnhome(-2) volatile-home joints
	synch        bool      // re-synch interp to machine position after reset
	terminalExec ExecState // ExecDone (clean stop) or ExecError (fault)
	reason       string    // human-readable cause, the interp.Abort message
}

// stopSignals fires the lock-free stop signals shared by every teardown: abort
// the sequencer/mcode/motion, optionally disable motion, stop all spindles,
// optionally turn off coolant/lube, abort IO, and optionally unhome. MUST run
// WITHOUT cmdMu (the abort is what unblocks a command stuck holding cmdMu in
// EnqueueCmd backpressure) and without t.mu. Clears the coolant/lube status
// flags it acts on so all callers agree.
func (t *Task) stopSignals(o shutdownOpts) {
	t.AbortSequencer()
	t.mcodeAbort()
	_ = t.motion.Abort()
	if o.disable {
		_ = t.motion.Disable()
	}
	_ = t.motion.SpindleOff(-1) // all-spindles broadcast (motmod EMCMOT_SPINDLE_OFF, spindle==-1)
	if o.coolantOff {
		_ = t.io.CoolantFloodOff()
		_ = t.io.CoolantMistOff()
	}
	if o.lubeOff {
		_ = t.io.LubeOff()
	}
	_ = t.io.IoAbort(o.abortReason)
	if o.unhome {
		_ = t.motion.JointUnhome(-2) // unhome only volatile-home joints
	}
	if o.coolantOff || o.lubeOff {
		t.mu.Lock()
		if o.coolantOff {
			t.floodOn = false
			t.mistOn = false
		}
		if o.lubeOff {
			t.lubeOn = false
		}
		t.mu.Unlock()
	}
}

// finishShutdown resets the interpreter (after the producer has stopped touching
// it) and restarts the sequencer, latching the terminal state atomically after
// the join. The caller manages cmdMu: monitor callers hold it around this phase
// so the interp reset cannot race a command that owns the interpreter; setState
// callers already hold cmdMu. Must be called with t.mu NOT held.
func (t *Task) finishShutdown(o shutdownOpts) {
	// Wait for the producer to stop before resetting the interpreter (motion/IO
	// already stopped by stopSignals).
	t.waitRunProgramDone()
	if t.interp != nil {
		t.abortInterp(o.abortReason, o.reason)
		_ = t.interp.Close()
		_ = t.interp.Reset()
		if o.synch {
			t.canon.syncEndPointFromMachine()
			_ = t.interp.Synch()
		}
		t.updateActiveCodes(t.interp) // republish stat caches after reset
	}
	t.restartSequencer(InterpIdle, o.terminalExec)
}

// restartSequencer restarts the sequencer (aborting+joining the old one) and
// then commits the terminal interp/exec state atomically. Committing AFTER the
// join is essential: the exiting sequencer writes no state on abort-exit, so
// once StartSequencer returns nothing else can clobber this commit — the
// ExecError-clobber race that d600b0e448/152c9ae9d3 fixed. Every teardown that
// ends by restarting the sequencer uses this. Must be called with t.mu NOT held.
func (t *Task) restartSequencer(terminalInterp InterpState, terminalExec ExecState) {
	t.StartSequencer()
	// Every teardown that lands here reset the interpreter while the sequencer
	// was down, so InitCanon's default-term-cond re-emission was dropped — and
	// the TP's term cond survives tpClear. Re-push the default unconditionally
	// (the TP state is unknown here), mirroring 2.9 where the post-abort
	// Interp::init's SET_TERM_COND reaches motion. AutoRun/AutoStep's plain
	// StartSequencer deliberately does NOT do this: a run boundary emits only
	// on change (via InitCanon), keeping program captures free of redundant
	// SET_TERM_COND like the certified 2.9 parity golds.
	t.pushDefaultTermCond()
	t.mu.Lock()
	t.interpState = terminalInterp
	t.execState = terminalExec
	t.mu.Unlock()
}

// pushDefaultTermCond re-asserts the canon's CURRENT modal blending mode to
// the TP and primes the canon's on-change cache with it. For moments when the
// TP term cond is unknown or known-stale: boot (module Start, where the canon
// state holds the G64/0.0254mm default — matching 2.9 INIT_CANON) and
// sequencer restarts after teardowns. It must push the canon's modal state,
// NOT the hard default: the interp's G61/G64 P modal survives aborts and mode
// switches (2.9 preserves the TP term cond across both — tpClear keeps it),
// so pushing the default here silently wiped an operator's MDI `G64 P<tol>`
// on the mode switch before every AUTO run, leaving the TP blending at
// 0.0254mm — near-exact-stop corners — regardless of the programmed
// tolerance. Sent DIRECTLY (not via the sequencer queue) so it is strictly
// ordered against the direct motion commands the caller issues next (e.g.
// SetMode's SetCoord) — a queued emission would land at an arbitrary point
// relative to them, making motion-logger captures nondeterministic.
func (t *Task) pushDefaultTermCond() {
	if t.canon == nil {
		return
	}
	cond := canonModeToTPTermCond(t.canon.state.motionMode)
	tol := t.canon.state.motionTolerance
	if err := t.motion.SetTermCond(cond, tol); err != nil {
		t.logger.Error("term-cond push failed", "err", err)
		t.canon.lastTermSet = false // TP state unknown; force re-emit on next G61/G64
		return
	}
	t.canon.lastTermCond = cond
	t.canon.lastTermTol = tol
	t.canon.lastTermSet = true
}

// fullShutdownOpts returns the options for a full machine off/estop teardown.
func fullShutdownOpts(abortReason int32, reason string) shutdownOpts {
	return shutdownOpts{
		abortReason: abortReason, disable: true, coolantOff: true, lubeOff: true,
		unhome: true, synch: true, terminalExec: ExecDone, reason: reason,
	}
}

// machineShutdown performs the full machine teardown shared by ESTOP and
// STATE_OFF: abort the sequencer/mcode/motion, disable motion, stop all
// spindles, coolant and lube, abort IO, unhome volatile-home joints, and reset
// the interpreter. Mirrors C++ emcTaskSetState(OFF/ESTOP). Must be called
// WITHOUT t.mu held (setState callers already hold cmdMu). abortReason is the
// EMC_ABORT_* code passed to io.IoAbort and interp on_abort; reason is the
// matching human-readable cause (the interp.Abort message).
func (t *Task) machineShutdown(abortReason int32, reason string) {
	o := fullShutdownOpts(abortReason, reason)
	t.stopSignals(o)
	t.finishShutdown(o)
}

// SetState handles state transitions: estop, estop_reset, off, on.
func (t *Task) SetState(state int32) error {
	// ESTOP is abort-class: fire the stop signals before queueing on cmdMu so
	// the machine starts stopping even while another command holds the lock.
	if TaskState(state) == StateEstop {
		t.signalAbort()
	}
	if err := t.preflightSetState(TaskState(state)); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	return t.setState(state)
}

// setState is the cmdMu-serialized body of SetState.
func (t *Task) setState(state int32) error {
	t.mu.Lock()

	target := TaskState(state)
	switch target {
	case StateEstop:
		wasOn := t.state == StateOn
		t.state = StateEstop
		t.interpState = InterpIdle
		t.execState = ExecDone
		t.mdiQueue = t.mdiQueue[:0]
		t.taskCommand = ""
		t.stepping = false
		// The loaded program SURVIVES estop. 2.9 closes only the interpreter's
		// file handle on abort (emcTaskPlanClose, emctask.cc) and leaves
		// emcStatus->task.file set, then re-opens it lazily on the next run
		// ("if (!taskplanopen && task.file[0]) emcTaskPlanOpen(task.file)",
		// emctaskmain.cc). We already mirror that: finishShutdown below closes
		// and resets the interp, and the AUTO run/step paths re-open
		// t.programFile before producing. Clearing the two fields here was the
		// only divergence — and an operator-visible one: an estop emptied
		// stat.task.file, so every UI lost its title, its run gate, and the
		// program it had loaded, with no way back but re-opening the file.
		t.mu.Unlock()

		if wasOn {
			// Commanded ESTOP reports TASK_STATE_ESTOP; the external/aux estop
			// input detected by the monitor reports AUX_ESTOP (2.9 emctask.cc
			// EMC_TASK_STATE_ESTOP vs the main-loop aux-estop handler).
			t.machineShutdown(emcAbortTaskStateEstop, "estop")
		} else {
			// Machine already down: light teardown, but still restart the
			// sequencer (signalAbort closed seqAbort, so the goroutine is
			// exiting). restartSequencer also commits the terminal interp/exec
			// state after the join. Without the restart, every later canon
			// EnqueueCmd fails "sequencer aborted" and MDI silently produces no
			// motion after estop-reset + machine-on.
			_ = t.motion.Disable()
			_ = t.io.LubeOff()
			t.restartSequencer(InterpIdle, ExecDone)
		}
		_ = t.io.EstopOn()

		t.mu.Lock()
		t.floodOn = false
		t.mistOn = false
		t.lubeOn = false
		t.mu.Unlock()
		return nil

	case StateEstopReset:
		if t.state == StateEstopReset {
			t.mu.Unlock()
			return nil // idempotent
		}
		if t.state != StateEstop {
			t.mu.Unlock()
			// Message it like every other refused state change: with the
			// client-side toast gone, a bare error here is fully invisible.
			t.operatorError("Can't reset E-stop from the current state")
			return ErrEstop
		}
		// Request estop-off from IO (sets user-enable-out=1).
		// The actual state transition only happens when emc-enable-in
		// confirms it, matching 2.9's determineState() behavior.
		t.mu.Unlock()
		_ = t.io.EstopOff()
		_ = t.io.LubeOff() // C++ emcTaskSetState(ESTOP_RESET) turns lube off
		t.mu.Lock()
		t.lubeOn = false
		t.mu.Unlock()

		// 2.9 emcTaskSetState(ESTOP_RESET) also runs emcTaskAbort +
		// emcIoAbort(5) + emcSpindleAbort(all) + emcAbortCleanup(5) +
		// emcTaskPlanSynch (emctask.cc): estop-reset is the recovery point
		// where the IO tool-change handshake and the interpreter's abort state
		// are cleared — including when estop was entered from a non-ON state,
		// whose light teardown did no IoAbort at all. The reason (5) is the
		// argument a configured ON_ABORT_COMMAND sees.
		_ = t.motion.Abort()
		_ = t.io.IoAbort(emcAbortTaskStateEstopReset)
		_ = t.motion.SpindleOff(-1) // all-spindles broadcast
		if t.interp != nil {
			// No producer can be alive in estop (every estop entry joined or
			// precluded it), and setState holds cmdMu — the interp is ours.
			t.waitRunProgramDone()
			t.abortInterp(emcAbortTaskStateEstopReset, "estop reset")
			t.canon.syncEndPointFromMachine()
			_ = t.interp.Synch()
		}

		// Check if IO confirms estop cleared (immediate HAL loopback case).
		// If IO status is not available, trust that the monitor will handle it.
		if t.ioStat != nil {
			if st, err := t.ioStat.GetIOFullStatus(); err == nil && !st.Estop {
				t.mu.Lock()
				t.state = StateEstopReset
				t.mu.Unlock()
			}
		}
		return nil

	case StateOff:
		if err := t.requireNotEstop(); err != nil {
			t.mu.Unlock()
			return err
		}
		wasOn := t.state == StateOn
		// Report ESTOP_RESET: the C milltask has no distinct OFF state —
		// determineState()/the monitor return ESTOP_RESET when traj is
		// disabled and not in estop, and Axis toggles ESTOP_RESET ↔ ON.
		t.state = StateEstopReset
		t.interpState = InterpIdle
		t.execState = ExecDone
		t.mdiQueue = t.mdiQueue[:0]
		t.taskCommand = ""
		t.stepping = false
		t.mu.Unlock()

		// Full safe shutdown (matches C++ emcTaskSetState(OFF)) — previously
		// this only did motion.Disable(), leaving spindles/coolant/lube on and
		// volatile-home joints homed.
		if wasOn {
			t.machineShutdown(emcAbortTaskStateOff, "machine off")
		} else {
			_ = t.motion.Disable()
			_ = t.io.LubeOff()
		}

		t.mu.Lock()
		t.floodOn = false
		t.mistOn = false
		t.lubeOn = false
		t.mu.Unlock()
		return nil

	case StateOn:
		if t.state == StateOn {
			t.mu.Unlock()
			return nil // idempotent
		}
		if err := t.canPowerOnLocked(); err != nil {
			t.mu.Unlock()
			return err
		}
		t.mu.Unlock()
		if err := t.motion.Enable(); err != nil {
			return err
		}
		// Settle before committing state=ON: Enable()'s ack only means the RT
		// command handler ran — the enabled flag lands in a *published* status
		// at the end of that servo cycle. Committing ON earlier opens a window
		// where a client poll right after this command completes still reads
		// enabled=false (the hard-limits CI flake), and where the monitor's
		// checkMotionEnabled pairs state=ON with a genuinely pre-enable
		// snapshot. 2.9 had no such window by construction: task_state was
		// DERIVED from motion.traj.enabled (determineState()). A timeout means
		// motion refused or instantly revoked the enable (e.g. a tripped limit
		// without override) — stay off and report, rather than flapping
		// ON→EstopReset through the monitor.
		if err := t.waitMotionEnabledStatus(); err != nil {
			t.operatorError("Can't enable motion")
			t.logger.Warn("machine-on: motion did not enable", "err", err)
			return err
		}
		// Enable override scaling so feed/spindle override controls work.
		_ = t.motion.FeedScaleEnable(1)
		_ = t.motion.FeedHoldEnable(1)
		for s := int32(0); s < int32(t.numSpindles); s++ {
			_ = t.motion.SpindleScaleEnable(s, 1)
		}
		_ = t.io.LubeOn() // C++ emcTaskSetState(ON) turns lube on
		t.mu.Lock()
		t.state = StateOn
		t.lubeOn = true
		// Clear any lingering error state from a previous fault so the
		// machine can accept commands again after off/on recovery.
		if t.execState == ExecError {
			t.execState = ExecDone
			t.interpState = InterpIdle
		}
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()
	return nil
}

// motionEnableSettleTimeout bounds how long SetState(ON) waits for the enable
// to show up in a published motion status. Two servo cycles suffice on a
// healthy machine, so a hit deadline means the enable was refused/revoked, not
// that the machine is slow. A var, not a const, so tests exercising the
// refusal path don't have to burn the full production deadline.
var motionEnableSettleTimeout = 1 * time.Second

const motionEnableSettleInterval = 2 * time.Millisecond

// waitMotionEnabledStatus blocks until a published motion status reports
// Enabled, or motionEnableSettleTimeout expires. See the SetState(ON) call
// site for why committing state=ON before this holds is a race.
func (t *Task) waitMotionEnabledStatus() error {
	if t.status == nil {
		return nil
	}
	deadline := time.Now().Add(motionEnableSettleTimeout)
	for {
		if ms, err := t.status.GetStatus(); err == nil && ms.Enabled != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("motion status still disabled %v after enable ack",
				motionEnableSettleTimeout)
		}
		time.Sleep(motionEnableSettleInterval)
	}
}

// SetMode switches between MANUAL, MDI, and AUTO.
// This is an explicit mode switch (e.g. from a mode selector or HAL pin).
// It clears any transactional mode save — the user deliberately chose this mode.
func (t *Task) SetMode(mode int32) error {
	if err := t.preflightSetMode(TaskMode(mode)); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	t.mu.Lock()

	// Accepted in any state (incl. ESTOP/OFF), matching C++ EMC_TASK_SET_MODE —
	// a UI mode selector must be honored even before the machine is enabled, so
	// the reported mode tracks the selector. The motion-mode calls below are
	// no-ops while motion is disabled. (Only the AUTO-running guard applies.)
	target := TaskMode(mode)

	// Reject mode switch while AUTO is running (matches C milltask behavior).
	if err := t.canSwitchModeAutoLocked(target); err != nil {
		t.mu.Unlock()
		return err
	}

	// Explicit mode switch cancels any transactional save.
	t.modeTx = false

	switch target {
	case ModeManual:
		if t.mode != ModeManual {
			t.modeAbortLocked() // unlocks/re-locks internally for I/O
		}
		t.mode = ModeManual
		homed := t.allHomed()
		t.mu.Unlock()
		// A fully-homed machine defaults to teleop (axis) jog in manual, like
		// C++ emcTaskSetMode(MANUAL); otherwise free (joint) mode.
		if homed {
			return t.motion.SetTeleop()
		}
		return t.motion.SetFree()
	case ModeMDI:
		if t.mode != ModeMDI {
			t.modeAbortLocked()
		}
		t.mode = ModeMDI
		// Synch only while the interpreter is idle — a re-assertion of the
		// current mode during a run must not touch the producer-owned interp.
		canSynch := t.interp != nil && !t.programBusy()
		t.mu.Unlock()
		_ = t.motion.SetCoord()
		if canSynch {
			t.canon.syncEndPointFromMachine()
			_ = t.interp.Synch()
		}
		return nil
	case ModeAuto:
		if t.mode != ModeAuto {
			t.modeAbortLocked()
		}
		t.mode = ModeAuto
		canSynch := t.interp != nil && !t.programBusy()
		t.mu.Unlock()
		_ = t.motion.SetCoord()
		if canSynch {
			t.canon.syncEndPointFromMachine()
			_ = t.interp.Synch()
		}
		return nil
	}
	t.mu.Unlock()
	return ErrWrongMode
}

// resolveProgram resolves a G-code path against the program directories.
//
// The resolver is built in loadConfig, where the INI is available; a Task that
// never loaded a config (only tests do that) falls back to the shared default
// so the check is never silently skipped.
func (t *Task) resolveProgram(file string) (string, error) {
	r := t.programRes
	if r == nil {
		r = pathres.ProgramResolver(nil, ".")
	}
	if r == nil {
		return "", fmt.Errorf("path resolver not initialised")
	}
	return r.Resolve(file, pathres.Read)
}

// ProgramOpen opens a G-code file for execution.
//
// The filename arrives over REST, so it is resolved and containment-checked
// before the interpreter sees it.  G-code is user data rather than
// configuration, so the allowed roots are the program directories
// (pathres.ProgramDirs: PROGRAM_PREFIX + SUBROUTINE_PATH + the system share
// and nc_files directories) — the same set ngcpreview's get_file enforces
// (user ruling, 2026-07-22).
func (t *Task) ProgramOpen(file string) error {
	// Busy is checked first so a request that would be rejected anyway keeps
	// reporting ErrBusy rather than a path error.
	if err := t.preflightNotBusy("Can't open a program while one is running"); err != nil {
		return err
	}
	resolved, err := t.resolveProgram(file)
	if err != nil {
		// The resolver error enumerates every allowed root — worth logging, too
		// long for the operator panel. Give the operator a short reason; the
		// full detail stays in the log and in the returned error (dev-facing).
		// A plain missing file must say so: reporting it as "not allowed"
		// sends the operator diagnosing permissions for a deleted file (the
		// recent-files case).
		t.logger.Warn("program open denied", "file", file, "err", err)
		if errors.Is(err, pathres.ErrNotFound) {
			t.operatorError(fmt.Sprintf("can't open %s: no such file", file))
		} else {
			t.operatorError(fmt.Sprintf("can't open %s: not in an allowed program directory", file))
		}
		return err
	}
	// One spelling for every program path we hand out: stat.file, motion_file
	// and the preview's file table are compared against each other by clients.
	source := pathres.Canonical(resolved)

	// A file inside the filtered-output tree already IS a filter's product;
	// re-matching its extension against [FILTER] would convert converted
	// G-code (a client that filtered for itself keeps the source's
	// extension on its output — qtvcp and gladevcp still do).
	if pathres.InFilteredDir(source) {
		return t.openProgramFile(source, source)
	}

	// A [FILTER] file is not G-code yet. Converting it is the controller's job
	// now — classic left it to each GUI, so a UI without filtering could not
	// open these programs at all, and the controller's own [DISPLAY]OPEN_FILE
	// handed the raw source to the interpreter.
	filter, err := programfilter.Lookup(t.iniGet, source)
	if err != nil {
		t.logger.Warn("filter configuration rejected", "file", source, "err", err)
		t.operatorError(err.Error())
		return err
	}
	if filter != nil {
		return t.startFiltering(source, filter)
	}
	return t.openProgramFile(source, source)
}

// openProgramFile points the interpreter at gcode and publishes it as the
// loaded program. source is what the operator asked for — the same path
// unless a filter produced gcode from it.
func (t *Task) openProgramFile(gcode, source string) error {
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.openProgramFileLocked(gcode, source)
}

// openProgramFileLocked is openProgramFile's body, for callers already
// holding cmdMu and mu (the filter goroutine, which must take both around its
// interpreter work).
func (t *Task) openProgramFileLocked(gcode, source string) error {
	// Opening a file is allowed in any machine state (incl. ESTOP) and any
	// mode, but NOT while a program is running/paused: closing and reopening
	// t.interp here would race the runProgram goroutine's use of it. C++
	// likewise rejects PROGRAM_OPEN in the READING/PAUSED/WAITING states.
	if err := t.rejectIfBusyLocked("Can't open a program while one is running"); err != nil {
		return err
	}
	// A newer open supersedes an in-flight conversion: cancel it and
	// invalidate its generation, or its result would silently replace THIS
	// program minutes from now, when the filter finishes and the operator has
	// long moved on. No-op when called from the filter goroutine itself,
	// which clears the flag before publishing.
	if cancel := t.cancelFilteringLocked(); cancel != nil {
		defer cancel()
	}
	if t.interp != nil {
		// Close any previously open file before opening a new one.
		_ = t.interp.Close()
		if err := t.interp.Open(gcode); err != nil {
			t.operatorError(fmt.Sprintf("can't open %s", gcode))
			return err
		}
	}
	t.programFile = gcode
	t.sourceFile = source
	t.programOpen = true
	t.previewSeq++
	return nil
}

// startFiltering launches the conversion of a [FILTER] source file and
// returns immediately.
//
// It cannot run inline: a real filter (image-to-gcode on a photograph) takes
// seconds to minutes, and blocking the command would freeze every client for
// the duration with nothing to show. Instead the status carries `filtering`
// and the percentage the filter reports, and the previously loaded program
// stays open and runnable until the new one is ready.
func (t *Task) startFiltering(source string, filter *programfilter.Filter) error {
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	// Filesystem work stays outside t.mu: every status build takes that lock,
	// and a slow TMPDIR must not stall polling. cmdMu alone is enough here —
	// filtering can only be started by a command, and commands serialize on it.
	dir := t.filteredDirOrDefault()
	if err := pathres.EnsureFilteredDir(dir); err != nil {
		t.operatorError(fmt.Sprintf("can't prepare filtered output for %s: %v", source, err))
		return err
	}

	t.mu.Lock()
	if err := t.rejectIfBusyLocked("Can't open a program while one is running"); err != nil {
		t.mu.Unlock()
		return err
	}
	if t.filtering {
		t.mu.Unlock()
		err := fmt.Errorf("another program is still being filtered")
		t.operatorError("Can't open a program while another is still being filtered")
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.filterGen++
	gen := t.filterGen
	// The conversion writes to a generation-unique temp name and is renamed
	// over the real one only on success: the currently loaded filtered
	// program — possibly under the SAME base name — survives a filter that
	// fails, times out, or is superseded.
	base := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	if base == "" {
		base = "program"
	}
	final := filepath.Join(dir, base+".ngc")
	tmp := fmt.Sprintf("%s.%d.part", final, gen)
	t.filtering = true
	t.filterProgress = 0
	t.filterCancel = cancel
	t.mu.Unlock()

	t.filterWG.Add(1)
	go func() {
		defer t.filterWG.Done()
		t.runFilter(ctx, gen, source, tmp, final, filter)
	}()
	return nil
}

// filteredDirOrDefault is this instance's private filtered-output directory.
// The module wires the instance name in at start; the fallback only exists
// for tasks built without a module (tests).
func (t *Task) filteredDirOrDefault() string {
	if t.filteredDir != "" {
		return t.filteredDir
	}
	return pathres.FilteredInstanceDir("milltask")
}

// runFilter converts source into tmp and, if nothing has superseded it,
// renames the result over final and opens it. Runs on its own goroutine;
// every touch of task state takes the lock, and every one of them re-checks
// the generation, because an abort or a newer open may have happened while
// the filter was running.
func (t *Task) runFilter(ctx context.Context, gen int64, source, tmp, final string, filter *programfilter.Filter) {
	err := filter.Run(ctx, source, tmp, func(pct int) {
		t.mu.Lock()
		if t.filterGen == gen {
			t.filterProgress = int32(pct)
		}
		t.mu.Unlock()
	})

	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()

	if t.filterGen != gen {
		// Superseded by an abort or a newer open: this result is stale, and
		// publishing it would replace whatever the operator asked for since.
		t.mu.Unlock()
		// Best effort: an output nobody will open is litter in this instance's
		// own temp dir, and the caller that superseded us has already moved on.
		_ = os.Remove(tmp)
		return
	}
	t.filtering = false
	t.filterProgress = 0
	t.filterCancel = nil

	if err != nil {
		t.mu.Unlock()
		// The filter's own stderr is the only thing that explains why a file
		// would not convert, so it goes to the operator, not just the log.
		// The previously loaded program is untouched: the conversion never
		// wrote anywhere near it.
		t.logger.Warn("program filter failed", "file", source, "err", err)
		t.operatorError(err.Error())
		return
	}

	// Publish. The atomic rename is what makes "the previous program stays
	// loaded until the new one is ready" hold even when old and new share a
	// base name.
	if rerr := os.Rename(tmp, final); rerr != nil {
		t.mu.Unlock()
		_ = os.Remove(tmp)
		t.logger.Warn("filtered program could not be published", "file", final, "err", rerr)
		t.operatorError(fmt.Sprintf("can't publish filtered program for %s: %v", source, rerr))
		return
	}
	openErr := t.openProgramFileLocked(final, source)
	t.mu.Unlock()
	if openErr != nil {
		t.logger.Warn("filtered program could not be opened", "file", final, "err", openErr)
		return
	}
	// With the new program open, its predecessor (and any stray .part) has
	// no reader left — and only one filtered program is loaded at a time, so
	// get_file and the preview must not find a stale one.
	t.sweepFilteredDir(final)
}

// sweepFilteredDir removes everything in this instance's filtered-output
// directory except keep, the just-published program. Runs under cmdMu (which
// serializes it against the next open) but never under t.mu — status polling
// must not wait on the filesystem.
func (t *Task) sweepFilteredDir(keep string) {
	dir := filepath.Dir(keep)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if p == keep {
			continue
		}
		// A leftover that refuses to go is litter in this instance's own temp
		// dir, not a fault to report.
		_ = os.Remove(p)
	}
}

// cancelFiltering stops an in-flight conversion and invalidates its result.
// Called by abort and at shutdown.
func (t *Task) cancelFiltering() {
	t.mu.Lock()
	cancel := t.cancelFilteringLocked()
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelFilteringLocked invalidates an in-flight conversion and hands its
// context-cancel function to the caller, who must invoke it (holding t.mu is
// fine — it only signals the filter's context). Nil when nothing is in flight.
func (t *Task) cancelFilteringLocked() context.CancelFunc {
	cancel := t.filterCancel
	t.filterCancel = nil
	if t.filtering {
		t.filterGen++ // whatever it produces now is stale
		t.filtering = false
		t.filterProgress = 0
	}
	return cancel
}

// AutoCommand handles run/pause/resume/step/reverse in AUTO mode.
//
// Pause, resume, and step-while-running are abort/signal-class: they must stay
// responsive while another command holds cmdMu — resuming is the only way to
// unwedge an MDI producer blocked in EnqueueCmd backpressure on a paused
// sequencer — so when no mode switch is needed they run without cmdMu. Run,
// program-starting step, reverse, and any variant that needs a mode switch are
// mutating commands and serialize on cmdMu.
func (t *Task) AutoCommand(cmd int32, line int32) error {
	switch cmd {
	case AutoPause, AutoResume, AutoStep:
		if handled, err := t.autoSignal(cmd); handled {
			return err
		}
	}
	if err := t.preflightAuto(cmd); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	return t.autoCommand(cmd, line)
}

// autoSignal executes pause/resume/step without cmdMu when the machine is on,
// already in AUTO mode, and (for step) a program is mid-run. Returns
// handled=false when the request needs the serialized path (mode switch or
// program start).
func (t *Task) autoSignal(cmd int32) (bool, error) {
	t.mu.Lock()
	if t.state != StateOn || t.mode != ModeAuto {
		t.mu.Unlock()
		return false, nil
	}

	switch cmd {
	case AutoPause:
		return true, t.doPauseLocked() // releases t.mu
	case AutoResume:
		return true, t.doResumeLocked() // releases t.mu
	case AutoStep:
		if t.interpState == InterpIdle {
			t.mu.Unlock()
			return false, nil // program not started — serialized start path
		}
		return true, t.doStepActiveLocked() // releases t.mu
	}
	t.mu.Unlock()
	return false, nil
}

// doPauseLocked pauses a running program/MDI. The caller holds t.mu; this
// releases it. It is a no-op (returns nil) unless the interpreter is actively
// reading — pausing while idle would set InterpPaused with no producer to ever
// clear it, wedging programBusy() until Abort/E-stop (C10). Shared by both
// AutoCommand dispatch paths so the guard lives in one place (D4).
func (t *Task) doPauseLocked() error {
	if t.interpState != InterpReading {
		t.mu.Unlock()
		return nil
	}
	t.interpState = InterpPaused
	t.closeOnceLocked(t.seqPauseCh) // signal the sequencer to pause
	t.mu.Unlock()
	return t.motion.Pause()
}

// doResumeLocked resumes a paused program/MDI. The caller holds t.mu; this
// releases it. No-op when not paused: a stray resume must NOT close seqResumeCh
// (seqCheckPause consumes it only after a real pause, so a stray close stays
// closed and disarms the NEXT pause — including a mandatory M0 stop, which the
// machine would then run straight through).
func (t *Task) doResumeLocked() error {
	if t.interpState != InterpPaused {
		t.mu.Unlock()
		return nil
	}
	t.interpState = InterpReading
	t.stepping = false
	t.closeOnceLocked(t.seqResumeCh) // wake the sequencer from pause
	t.mu.Unlock()
	return t.motion.Resume()
}

// doStepActiveLocked advances one step of an already-started program/MDI
// (interpState != Idle). The caller holds t.mu; this releases it. It sets the
// stepping flag; when paused it steps one motion segment — via motion.Step when
// the TP still has queued moves (the TP re-pauses after the ID changes), else by
// waking the sequencer to push the next command. Shared by both dispatch paths.
func (t *Task) doStepActiveLocked() error {
	if err := t.requireProgram(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.stepping = true
	if t.interpState != InterpPaused {
		// Already running — the flag is enough; the sequencer pauses after the
		// current motion command completes.
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()

	// Paused — step one motion segment.
	qd, _ := t.status.GetQueueDepth()
	if qd > 0 {
		_ = t.motion.Step(0)
		return nil
	}
	// TP is empty — wake the sequencer to push more commands.
	t.mu.Lock()
	resumeCh := t.seqResumeCh
	t.mu.Unlock()
	t.closeOnce(resumeCh)
	return nil
}

// autoCommand is the cmdMu-serialized body of AutoCommand.
func (t *Task) autoCommand(cmd int32, line int32) error {
	// Same front-door recovery as MDI: never start a program against an
	// interpreter left dirty by a sequencer hard fault (AutoRun's Close/Open
	// resets the file position but not the interp's abort-relevant flags).
	t.recoverSeqFaultLocked()

	t.mu.Lock()

	if err := t.requireOn(); err != nil {
		t.mu.Unlock()
		return err
	}
	if err := t.ensureMode(ModeAuto); err != nil {
		t.mu.Unlock()
		// The preflight passed or this command would not be here: the refusal
		// happened in the race window before cmdMu (another client started
		// homing or an MDI), and without a message the operator's Run click
		// does nothing with no explanation anywhere but stderr.
		t.operatorError(fmt.Sprintf("Cannot switch to Auto mode: %v", err))
		return err
	}

	switch cmd {
	case AutoRun:
		// Shared guard (also run lock-free by preflightAuto). Rejects while a
		// program/MDI is mid-run — a second producer goroutine would race the
		// first on the non-thread-safe interpreter — plus not-homed / offsets.
		if err := t.autoRunGuardLocked(); err != nil {
			t.mu.Unlock()
			return err
		}
		if t.interp == nil {
			t.mu.Unlock()
			return fmt.Errorf("no interpreter configured")
		}
		// Running a program is a deliberate mode commitment — no restore.
		t.modeTx = false
		t.interpState = InterpReading
		t.stepping = false
		t.runDone = make(chan struct{})
		runDone := t.runDone
		interp := t.interp
		startLine := line
		file := t.programFile
		t.mu.Unlock()
		t.StartSequencer()
		// Ensure motion controller is in coord mode (may have been lost
		// after abort or error — matches C milltask emcTaskSetMode AUTO).
		_ = t.motion.SetCoord()
		// Re-open the program file to reset the interpreter to the beginning.
		// Without this, a second Run after completion or stop would fail with
		// "File ended with no percent sign" because the interpreter is at EOF.
		_ = interp.Close()
		if err := interp.Open(file); err != nil {
			t.setInterpState(InterpIdle)
			return fmt.Errorf("re-open program: %w", err)
		}
		t.canon.syncEndPointFromMachine()
		if err := interp.Synch(); err != nil {
			t.logger.Error("interp synch failed before run", "err", err)
		}
		// Hand the producer its own generation's abort channel — reading the
		// field would race later generations created by teardown paths.
		t.mu.Lock()
		abort := t.seqAbort
		t.mu.Unlock()
		go t.runProgram(interp, startLine, runDone, abort)
		return nil

	case AutoPause:
		return t.doPauseLocked() // releases t.mu (C10-gated, shared with autoSignal)

	case AutoResume:
		return t.doResumeLocked() // releases t.mu

	case AutoStep:
		if t.interpState == InterpIdle {
			// Program not started yet — start it in step mode. Shared guard
			// (requireProgram + programBusy + homed when idle), also run by
			// preflightAuto.
			if err := t.autoStepGuardLocked(); err != nil {
				t.mu.Unlock()
				return err
			}
			if t.interp == nil {
				t.mu.Unlock()
				return fmt.Errorf("no interpreter configured")
			}
			t.stepping = true
			t.interpState = InterpReading
			t.runDone = make(chan struct{})
			runDone := t.runDone
			interp := t.interp
			file := t.programFile
			t.mu.Unlock()
			t.StartSequencer()
			_ = t.motion.SetCoord()
			_ = interp.Close()
			if err := interp.Open(file); err != nil {
				t.setInterpState(InterpIdle)
				return fmt.Errorf("re-open program: %w", err)
			}
			t.canon.syncEndPointFromMachine()
			if err := interp.Synch(); err != nil {
				t.logger.Error("interp synch failed before step", "err", err)
			}
			t.mu.Lock()
			abort := t.seqAbort
			t.mu.Unlock()
			go t.runProgram(interp, line, runDone, abort)
			return nil
		}

		// Program already started (paused or running) — step it.
		return t.doStepActiveLocked() // releases t.mu

	case AutoReverse:
		t.mu.Unlock()
		return t.motion.Reverse()

	case AutoForward:
		t.mu.Unlock()
		return t.motion.Forward()
	}
	t.mu.Unlock()
	return nil
}

// MDI executes an MDI command string.
func (t *Task) MDI(command string) error {
	if err := t.preflightMDI(); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	// If the sequencer died on a hard fault, clean the interpreter (on_abort)
	// and restart the sequencer before executing anything against it — this MDI
	// may have won the cmdMu race against the spawned recoverSeqFault.
	t.recoverSeqFaultLocked()

	t.mu.Lock()

	if err := t.requireOn(); err != nil {
		t.mu.Unlock()
		return err
	}
	if err := t.ensureMode(ModeMDI); err != nil {
		t.mu.Unlock()
		t.operatorError("Must be in MDI mode to issue MDI command")
		return err
	}
	if err := t.requireHomedForMDILocked(); err != nil {
		t.mu.Unlock()
		return err
	}
	if t.interp == nil {
		t.mu.Unlock()
		return fmt.Errorf("no interpreter configured")
	}

	// If interpreter is busy, queue the command for later execution.
	if t.interpState != InterpIdle {
		if err := t.mdiQueueFullLocked(); err != nil {
			t.mu.Unlock()
			return err
		}
		t.mdiQueue = append(t.mdiQueue, command)
		t.mu.Unlock()
		return nil
	}

	t.mu.Unlock()
	return t.executeMDI(command)
}

// finishMDI completes an MDI command after its motion has drained. It runs on
// its own goroutine (spawned by mdiDoneCmd.PostWait) and goes through cmdMu
// like any external command — it executes the interpreter for queued MDIs, so
// it must be serialized against every other interpreter-touching command and
// must NOT run on the sequencer goroutine (self-deadlock on interpQueue
// backpressure).
func (t *Task) finishMDI(gen uint64) {
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	// Ownership check: a finishMDI left over from a superseded MDI (an Abort or
	// estop teardown between the mdiDoneCmd handoff and here, followed by a newer
	// MDI that bumped mdiGen) must NOT synch or commit against the newer
	// command's state. executeMDI bumps mdiGen under cmdMu (which we hold), but
	// fault paths (seqFaultExit, the monitor ExecError latches) bump it under
	// t.mu only, so mdiGen CAN advance mid-call. That is safe: a bump can only
	// mark this call stale (a no-op), never revive a stale one, and a fault
	// landing after this check is caught by the execState==ExecError guard
	// below plus the queue those paths already flushed.
	t.mu.Lock()
	stale := gen != t.mdiGen || t.interpActive
	t.mu.Unlock()
	if stale {
		return
	}

	// Synch the interpreter now that the MDI command and its motion have
	// completed. Interp::synch() saves the interpreter parameters to persist,
	// so parameter changes made by the MDI (e.g. G10 L20/L2 coordinate-system
	// offsets from a touch off) become visible to the preview interpreter,
	// which reads parameters from persist on its own interpreter instance.
	// Without this, parameters are only saved at the *next* synch, leaving the
	// preview one MDI command behind (the origin marker uses live status and so
	// updates immediately, but the drawn geometry would lag by one touch off).
	//
	// Guard on programBusy: if an abort/estop reset the machine between the
	// PostWait handoff and this goroutine getting cmdMu, the interpreter may be
	// owned by a new program run already — leave it alone.
	t.mu.Lock()
	canSynch := t.interp != nil && t.interpState != InterpIdle && !t.interpActive
	t.mu.Unlock()
	if canSynch {
		t.canon.syncEndPointFromMachine()
		if err := t.interp.Synch(); err != nil {
			t.logger.Error("interp synch after MDI failed", "err", err)
		}
		t.updateActiveCodes(t.interp)

		// Continue an MDI o-word subroutine that yielded INTERP_EXECUTE_FINISH at
		// a queue-buster (probe, M66, dwell, tool change). Interp::_execute drives
		// the sub with an internal `while(MDImode && call_level)` loop that bails
		// out with EXECUTE_FINISH the moment a sub block needs the motion queue
		// drained, leaving the rest of the sub unexecuted until execute() is
		// called again. The queued motion has now drained (mdiDoneCmd waited for
		// it) and the interp is re-synched, so re-run execute() to advance the
		// sub — mirroring C++ re-issuing emcTaskPlanExecute(0) until the call
		// level unwinds, and the AUTO path's EXECUTE_FINISH handling. A plain MDI
		// line, or a single top-level queue-buster, has call_level 0 here and
		// skips this loop — its block is already complete.
		//
		// E5 fast path: when a continuation Execute enqueues NOTHING (it unwound
		// the sub to call level 0 with no motion and no further queue-buster),
		// there is nothing to drain, so we re-synch and loop directly instead of
		// paying a full mdiDoneCmd round-trip (goroutine spawn + cmdMu +
		// waitMotionDone's settle floor) per empty level. When it DOES enqueue
		// something (motion or another queue-buster, which always leaves
		// call_level > 0), drain it via mdiDoneCmd and re-enter.
		for t.interp.CallLevel() > 0 {
			// The continuation fires canon callbacks (the sub's queued moves,
			// probes, M-codes); point them at this task's canon, as executeMDI
			// and runProgram do before driving the interpreter.
			setActiveCanon(t.canon)
			before := t.canon.enqueued()
			_, err := t.interp.Execute()
			if err == nil {
				// Same open-chain close as executeMDI, for the same reason: a
				// feed move in the sub's tail leaves the naive-CAM chain open,
				// and the next finishMDI round resyncs the endpoint and drops
				// it. Interp::_execute does reach its own canon.finish() here
				// (mdi_interrupt makes it take the o-word branch with MDImode
				// back on) — but only once the sub has fully unwound; a tail
				// that queues motion and re-enters this loop is drained, and
				// re-entered, before that. Must run before the enqueued()
				// comparison, or a tail whose only output is the flushed feed
				// move looks like an empty level and is never drained.
				t.canon.flushSegments()
			}
			t.updateActiveCodes(t.interp)
			if err != nil {
				// Same as executeMDI: a fault mid-continuation must stop the
				// motion the earlier sub blocks already queued, not just set
				// state. faultMDI unifies this path with executeMDI's (they used
				// to diverge: one set ExecError, the other left it untouched).
				t.logger.Error("MDI subroutine continuation failed", "err", err)
				t.faultMDI(fmt.Sprintf("MDI subroutine error: %v", err))
				return
			}
			if t.canon.enqueued() != before {
				// Something was queued — drain it via mdiDoneCmd, then re-enter
				// finishMDI to continue (call_level > 0) or complete (0) the sub.
				t.setExecState(ExecWaitingForMotion)
				if err := t.EnqueueCmd(&mdiDoneCmd{gen: gen}); err != nil {
					if errors.Is(err, errSeqAborted) {
						// Concurrent teardown owns the cleanup — see executeMDI.
						t.flushMDIQueue()
						return
					}
					t.faultMDI(fmt.Sprintf("MDI continuation enqueue failed: %v", err))
				}
				return
			}
			// Nothing queued — re-synch (the interp expects a synch after each
			// EXECUTE_FINISH) and loop; if the sub has unwound to call level 0
			// we fall through to the terminal commit below.
			t.canon.syncEndPointFromMachine()
			if err := t.interp.Synch(); err != nil {
				t.logger.Error("interp synch during MDI continuation failed", "err", err)
			}
		}
	}

	t.mu.Lock()
	if t.interpActive {
		// A program run took over — nothing left to finish here.
		t.mu.Unlock()
		return
	}
	if t.execState == ExecError {
		// A fault teardown latched ExecError after this MDI's motion drained.
		// Never downgrade it to ExecDone here — the machine faulted and the UI
		// must keep showing the error; the teardown already flushed the queue,
		// commits InterpIdle itself (directly or via its restartSequencer), and
		// recovery clears ExecError on estop-reset / off→on. This guard fires
		// ONLY for that concurrent-teardown race: an MDI *accepted* while a
		// previous fault's error was still latched never reaches it, because
		// executeMDI's setExecState(WaitingForMotion) already replaced the
		// latch — a post-fault MDI thus completes normally and ends ExecDone,
		// matching 2.9 where the exec state tracks the current command
		// (TestMDI_UnderLatchedErrorClearsIt pins this).
		t.mu.Unlock()
		return
	}
	t.interpState = InterpIdle
	t.execState = ExecDone

	// Dequeue next MDI command if any are waiting.
	if len(t.mdiQueue) > 0 {
		next := t.mdiQueue[0]
		t.mdiQueue = t.mdiQueue[1:]
		t.mu.Unlock()
		// Execute the next MDI command — this will enqueue another mdiDoneCmd.
		// On failure executeMDI has already faulted (faultMDI: motion stopped,
		// readahead discarded, queue flushed, operator error reported), so the
		// dequeue chain ends here rather than stranding the remaining queue and
		// running a later MDI out of order.
		if err := t.executeMDI(next); err != nil {
			t.logger.Error("queued MDI failed", "cmd", next, "err", err)
		}
		return
	}
	// MDI queue empty — clear the echoed command and restore mode if this was
	// a transactional switch (mirrors C++ clearing task.command once the MDI
	// input queue drains).
	t.taskCommand = ""
	t.restoreModeTx()
	t.mu.Unlock()
}

// executeMDI runs a single MDI command through the interpreter.
// Must be called with cmdMu held, mu NOT held, and interpState == InterpIdle.
func (t *Task) executeMDI(command string) error {
	t.mu.Lock()
	t.taskCommand = command // echoed to stat.task.command (C++ EMC_TASK_PLAN_EXECUTE)
	t.mdiGen++              // new MDI generation — staleness token for finishMDI
	gen := t.mdiGen
	t.mu.Unlock()
	t.setInterpState(InterpReading)

	interp := t.interp

	// Set active canon for M-code callbacks (no ctx parameter).
	setActiveCanon(t.canon)

	// Sync canon endpoint from actual machine position before interp synch,
	// matching C canon's GET_EXTERNAL_POSITION which reads from STAT.
	t.canon.syncEndPointFromMachine()

	// Synch interpreter with current machine position before MDI.
	if err := interp.Synch(); err != nil {
		t.logger.Error("interp synch failed before MDI", "err", err)
	}

	// Execute the MDI string — this triggers canon callbacks that enqueue
	// motion commands to the sequencer.
	startSerial := t.canon.serial()
	rc, err := interp.ExecuteString(command)
	if err == nil && rc != InterpError {
		// Emit whatever the naive-CAM detector is still holding. A feed move
		// leaves see_segment with an OPEN chain (2.9 see_segment buffers every
		// STRAIGHT_FEED and only flushes when the next point cannot link onto
		// it), and nothing else closes it at the end of an MDI line:
		// Interp::_execute calls canon.finish() only on its o-word branch
		// (rs274ngc_pre.cc), which a plain block never reaches. In 2.9 that is
		// survivable — the chain simply waits for the next command's first canon
		// call. Here it is not: executeMDI resyncs the endpoint from the machine
		// on entry, and that resync *drops* the chain (2.9
		// GET_EXTERNAL_POSITION -> drop_segments), so the buffered move was
		// silently discarded and an MDI feed move never reached motion at all.
		// Flushing here also makes it execute immediately, rather than one
		// command late as the 2.9 sequence would.
		t.canon.flushSegments()
	}
	gc, mc, st := t.updateActiveCodes(interp)
	// Tag the segments this MDI just queued with its active codes, exactly as the
	// AUTO read loop does. Without this an MDI move's segment carries no state
	// tag, so BuildStat can't resolve its modal codes and falls back to the
	// interpreter's readahead codes — which right after an aborted AUTO program
	// still hold that program's never-executed tail mode (the g64 test's
	// leftover G64 P1 Q2). Tagging makes an executing MDI move report its own
	// mode.
	t.tagMotionRange(startSerial, t.canon.serial(), gc, mc, st)
	if err != nil {
		// A runtime error at block N of a multi-block MDI (o-word sub) must stop
		// the motion already queued by blocks 1..N-1 — faultMDI aborts motion,
		// discards the queued readahead, and leaves ExecError (C++ clears
		// interp_list + emcTaskAbort/emcIoAbort on MDI INTERP_ERROR).
		t.faultMDI(fmt.Sprintf("MDI error: %v", err))
		return executed(fmt.Errorf("MDI execute: %w", err))
	}

	switch rc {
	case InterpError:
		t.faultMDI("MDI interpreter error")
		return executed(fmt.Errorf("MDI interpreter error"))
	default:
		// Mark exec state as busy so WaitComplete doesn't return prematurely.
		t.setExecState(ExecWaitingForMotion)
		// Wait for queued motion to finish before going idle.
		if err := t.EnqueueCmd(&mdiDoneCmd{gen: gen}); err != nil {
			if errors.Is(err, errSeqAborted) {
				// Not an MDI error: a concurrent teardown (user abort, estop/
				// off, sequencer hard fault) closed the sequencer between synch
				// and enqueue — a designed-for interleaving (Abort's signal
				// phase deliberately runs before it queues on cmdMu). The
				// teardown owns the interp cleanup and terminal state
				// (abortLocked / finishShutdown pending on cmdMu, or
				// recoverSeqFault via seqFaulted). Running faultMDI here
				// reported a spurious operator error and ran on_abort/IoAbort
				// with the MDI-interp-error reason 10 for what 2.9 reports as
				// a plain abort (8) or exec error (1). Just flush the queued
				// MDIs + echo, like mdi_execute_abort.
				t.flushMDIQueue()
				return fmt.Errorf("MDI enqueue: %w", err)
			}
			// Sequencer gone unexpectedly (e.g. never started): without the
			// mdiDoneCmd nothing transitions interpState, wedging it at Reading.
			// Fault it so the state is consistent and the failure is visible.
			t.faultMDI(fmt.Sprintf("MDI enqueue failed: %v", err))
			return executed(fmt.Errorf("MDI enqueue: %w", err))
		}
	}
	return nil
}

// faultProgram handles a fatal interpreter error while a program or MDI command
// is running. It stops any in-flight motion and halts the sequencer so the
// read-ahead segments already queued past the erroring line are NOT dispatched
// (StartSequencer's abort drains interpQueue), then leaves the task in
// ExecError. Without this the machine keeps moving through the queued motion
// after a G-code runtime error. Mirrors the C++ interp_list.clear() +
// emcAbortCleanup(EMC_ABORT_INTERPRETER_ERROR).
//
// Safe to call from any goroutine that is NOT the sequencer goroutine (it joins
// the sequencer via StartSequencer): the runProgram producer, and — via
// faultMDI — the executeMDI/finishMDI callers. It does not touch cmdMu, so an
// MDI caller already holding cmdMu can call it.
func (t *Task) faultProgram(reason int32, msg string) {
	t.operatorError(msg)
	_ = t.motion.Abort() // stop what is already moving
	// Run the interpreter's on_abort so it reset()s and clears the
	// toolchange/probe/input/mdi_interrupt flags that an interrupted remapped
	// procedure may have left set — otherwise the next tool change can fail with
	// "Queue is not empty after tool change", and a stale probe/input flag leaks
	// into the next run. Mirrors 2.9 emcAbortCleanup(reason) -> Interp::on_abort.
	// The reason is also the argument passed to a configured ON_ABORT_COMMAND.
	// Called on the interpreter-owning goroutine (the runProgram producer, or the
	// executeMDI/finishMDI caller), so this interp access stays single-threaded.
	t.abortInterp(reason, msg)
	// restartSequencer aborts+joins the old sequencer and commits the terminal
	// ExecError after the join (a concurrent monitor teardown is safe:
	// StartSequencer generations are serialized by seqLifeMu and both writers
	// commit the same ExecError).
	t.restartSequencer(InterpIdle, ExecError)
}

// abortInterp runs the interpreter's on_abort: Interp::Abort reset()s the
// interp, clears the toolchange/probe/input/mdi_interrupt flags an interrupted
// remapped procedure may have left set, and synchronously executes a configured
// [RS274NGC]ON_ABORT_COMMAND with reason as its numeric argument. Canon output
// from that handler is discarded on EVERY abort path: gomc never replays
// abort-handler motion, and discarding keeps the handler's emissions from
// hitting whatever sequencer state the teardown left behind (error-log spam
// against an aborted queue, or an EnqueueCmd deadlock against a live one).
// The caller must own the interpreter: be the producer goroutine, or hold
// cmdMu with no producer alive (waitRunProgramDone).
func (t *Task) abortInterp(reason int32, msg string) {
	if t.interp == nil {
		return
	}
	t.canon.setDiscard(true)
	defer t.canon.setDiscard(false)
	_ = t.interp.Abort(int(reason), msg)
}

// recoverSeqFault runs the interpreter's on_abort after a sequencer hard fault
// (seqFaultExit) and restarts the sequencer. The sequencer goroutine can do
// neither itself: the interpreter is owned by the producer/MDI goroutine, and
// restartSequencer joins the sequencer — so seqFaultExit spawns this on its own
// goroutine. It serializes through cmdMu like any command and waits for a live
// producer to exit before touching the interpreter.
//
// Without it, a fault with no producer routing into faultProgram/faultMDI (the
// normal case: canon swallows EnqueueCmd errors and runProgram exits via its
// abort select; an already-enqueued mdiDoneCmd simply never executes) left the
// interp's toolchange/probe/input flags stale, and the next MDI ran against
// them ("Queue is not empty after tool change"). Mirrors 2.9's unconditional
// mdi_execute_abort() + emcAbortCleanup(EMC_ABORT_TASK_EXEC_ERROR) in the
// EMC_TASK_EXEC_ERROR case (emctaskmain.cc).
func (t *Task) recoverSeqFault() {
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.recoverSeqFaultLocked()
}

// recoverSeqFaultLocked is recoverSeqFault's cmdMu-held body. The MDI/AUTO
// front doors also call it, so a command that wins the cmdMu race against the
// spawned recoverSeqFault still finds a clean interpreter — the recovery then
// finds seqFaulted already cleared and no-ops. Caller must hold cmdMu (and not
// be a producer goroutine — this waits on runDone).
func (t *Task) recoverSeqFaultLocked() {
	t.mu.Lock()
	faulted := t.seqFaulted
	t.mu.Unlock()
	if !faulted {
		return
	}
	// A producer that was mid-run when the sequencer died exits promptly (its
	// waits/enqueues select on the closed seqAbort); its faultProgram — if any —
	// runs before runDone closes, so after this wait the interpreter is ours.
	t.waitRunProgramDone()
	t.mu.Lock()
	faulted = t.seqFaulted
	t.mu.Unlock()
	if !faulted {
		return // another teardown restarted the sequencer and reset the interp
	}
	t.logger.Debug("recoverSeqFault: running interp on_abort + sequencer restart")
	t.abortInterp(emcAbortTaskExecError, "sequencer exec error")
	// StartSequencer (inside) clears seqFaulted; the terminal ExecError is
	// re-committed after the join.
	t.restartSequencer(InterpIdle, ExecError)
	t.logger.Debug("recoverSeqFault: done")
}

// flushMDIQueue drops the queued MDI commands and clears the MDI echo
// (stat.task.command) — the tail of 2.9's mdi_execute_abort. Shared by faultMDI
// and the aborted-enqueue paths, which must not run the rest of the fault
// machinery.
func (t *Task) flushMDIQueue() {
	t.mu.Lock()
	t.mdiQueue = t.mdiQueue[:0]
	t.taskCommand = ""
	t.mu.Unlock()
}

// faultMDI aborts an MDI command that failed to execute. Beyond faultProgram's
// motion-stop + readahead-discard, it flushes the pending MDI queue and clears
// the MDI echo (stat.task.command), so the failure does not later run the
// queued MDIs out of order and the UI does not keep showing the failed command.
// Mirrors C++ mdi_execute_abort. Called from executeMDI and the finishMDI
// o-word continuation — both off the sequencer goroutine.
func (t *Task) faultMDI(msg string) {
	// Stop the hardware FIRST, in 2.9's order — emcTaskAbort (motion) →
	// emcIoAbort(10) → emcSpindleAbort(all) — and only then run
	// mdi_execute_abort + emcAbortCleanup(10) (the faultProgram call below:
	// on_abort + sequencer join). on_abort may synchronously execute an
	// unbounded ON_ABORT_COMMAND and the join can stall behind a wedged comm
	// call, so a running spindle must already be commanded off before either.
	// The IoAbort also resets any in-progress tool-change handshake left by a
	// failed remapped M6. The AUTO readahead path (faultProgram alone)
	// deliberately does no IoAbort/spindle stop, to match 2.9 which only
	// clears the interp list and runs on_abort there.
	_ = t.motion.Abort()
	_ = t.io.IoAbort(emcAbortInterpreterErrorMDI)
	t.flushMDIQueue()
	_ = t.motion.SpindleOff(-1) // all-spindles broadcast
	t.faultProgram(emcAbortInterpreterErrorMDI, msg)
}

// runStartupCode executes [RS274NGC]RS274NGC_STARTUP_CODE once at task startup,
// mirroring C emctask.cc emcTaskPlanInit: execute the string on the interp and
// drain any INTERP_EXECUTE_FINISH continuations. Its canon output flows to the
// sequencer like any block. Runs regardless of machine state (classic runs it
// at init, before estop-reset); a G-code error is reported but non-fatal.
// Called once from module Start(), after StartSequencer, before any program.
func (t *Task) runStartupCode() {
	if t.startupCode == "" || t.interp == nil {
		return
	}
	interp := t.interp
	setActiveCanon(t.canon)
	t.logger.Info("running startup code", "code", t.startupCode)
	rc, err := interp.ExecuteString(t.startupCode)
	for err == nil && rc == InterpExecuteFinish {
		rc, err = interp.Execute()
	}
	t.updateActiveCodes(interp)
	if err != nil {
		t.operatorError(fmt.Sprintf("startup code %q: %v", t.startupCode, err))
	}
}

// runProgram runs the interpreter read/execute loop for the open program.
// Called in a goroutine from AutoRun/AutoStep.
//
// The interpreter is a "dumb producer": it reads lines, executes canon
// callbacks (which enqueue commands via EnqueueCmd), and only stops on
// abort or end-of-file. Pause and step are handled at the sequencer level.
// abort is this run's sequencer-generation abort channel, passed in so the
// producer never reads the (mutable) t.seqAbort field unsynchronized.
func (t *Task) runProgram(interp Interpreter, startLine int32, runDone chan struct{}, abort <-chan struct{}) {
	t.mu.Lock()
	t.interpActive = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.interpActive = false
		t.mu.Unlock()
		// Signal teardown paths that the producer has stopped touching the
		// interpreter, so interp.Close/Reset is now safe (waitRunProgramDone).
		close(runDone)
	}()

	// Set active canon for M-code callbacks (no ctx parameter).
	setActiveCanon(t.canon)
	defer clearActiveCanon()

	// Run-from-line: if startLine > 0, read/execute lines without enqueuing
	// motion commands until we reach startLine (matching C milltask behavior).
	if startLine > 0 {
		if aborted := t.seekToLine(interp, startLine, abort); aborted {
			return
		}
	}

	for {
		// Check abort between interpreter lines
		select {
		case <-abort:
			return
		default:
		}

		rc, err := interp.Read()
		if err != nil {
			t.logger.Error("interpreter read error", "err", err, "rc", rc)
			t.faultProgram(emcAbortInterpreterError, fmt.Sprintf("Interpreter read error: %v", err))
			return
		}
		if rc == InterpEndfile || rc == InterpExit {
			// Best-effort completion signal: if the sequencer was aborted
			// concurrently the enqueue fails, but recoverSeqFault/the abort
			// select already own teardown (see recoverSeqFault doc).
			_ = t.EnqueueCmd(&interpDoneCmd{})
			return
		}

		// Update readLine after successful read.
		t.mu.Lock()
		t.readLine = int32(interp.Line())
		t.mu.Unlock()

		startSerial := t.canon.serial()
		rc, err = interp.Execute()
		if err != nil {
			t.logger.Error("interpreter execute error", "err", err, "rc", rc)
			t.faultProgram(emcAbortInterpreterError, fmt.Sprintf("Interpreter error: %v", err))
			return
		}
		// Capture this line's active codes and tag the motion segments it just
		// queued, so status reports the executing segment's codes, not readahead.
		gc, mc, st := t.updateActiveCodes(interp)
		t.tagMotionRange(startSerial, t.canon.serial(), gc, mc, st)

		switch rc {
		case InterpExecuteFinish:
			// Interpreter says "wait for motion/IO to complete before
			// continuing" (tool change, probe, dwell, M-code, etc.)
			// Best-effort: an enqueue failure means the sequencer aborted, which
			// waitSequencerDrain detects below and returns on.
			_ = t.EnqueueCmd(waitForMotionSingleton)
			// Wait for sequencer to drain before reading next line
			if t.waitSequencerDrain() {
				return // aborted
			}
			t.canon.syncEndPointFromMachine()
			if err := interp.Synch(); err != nil {
				t.logger.Error("interp synch after execute_finish", "err", err)
			}
		case InterpExit:
			// Best-effort completion signal (see the InterpEndfile case above).
			_ = t.EnqueueCmd(&interpDoneCmd{})
			return
		case InterpError:
			t.logger.Error("interpreter error", "rc", rc)
			t.faultProgram(emcAbortInterpreterError, "interpreter error")
			return
		case InterpOK:
			// Normal — continue reading
		}
	}
}

// seekToLine reads and executes interpreter lines without enqueuing motion
// commands (they are discarded) until we reach startLine. This implements
// "Run from line N": the interpreter processes preamble (offsets, units, tool
// changes) so it's in the correct state, but no actual motion is queued.
// Returns true if aborted.
func (t *Task) seekToLine(interp Interpreter, startLine int32, abort <-chan struct{}) bool {
	// Temporarily redirect canon enqueue to a discard sink.
	t.canon.setDiscard(true)
	defer t.canon.setDiscard(false)

	for {
		select {
		case <-abort:
			return true
		default:
		}

		rc, err := interp.Read()
		if err != nil {
			t.logger.Error("interpreter read error during seek", "err", err)
			t.setInterpState(InterpIdle)
			return true
		}
		if rc == InterpEndfile || rc == InterpExit {
			// Reached end before startLine — run from beginning
			t.logger.Warn("seekToLine: reached end before target line", "target", startLine)
			return true
		}

		lineNow := int32(interp.Line())

		rc, err = interp.Execute()
		if err != nil {
			t.logger.Error("interpreter execute error during seek", "err", err)
			t.setInterpState(InterpIdle)
			return true
		}
		t.updateActiveCodes(interp)

		// Handle EXECUTE_FINISH during seek (needed for tool changes etc.)
		if rc == InterpExecuteFinish {
			t.canon.syncEndPointFromMachine()
			if err := interp.Synch(); err != nil {
				t.logger.Error("interp synch during seek", "err", err)
			}
		}

		// Check if we've reached the target line.
		if lineNow >= startLine {
			// Sync interpreter position with actual machine position
			// so the first real move goes to the right place.
			t.canon.syncEndPointFromMachine()
			if err := interp.Synch(); err != nil {
				t.logger.Error("interp synch after seek", "err", err)
			}
			return false
		}
	}
}

// waitSequencerDrain waits until the sequencer queue is empty and the
// sequencer is idle (ExecDone). Returns true if aborted.
func (t *Task) waitSequencerDrain() bool {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		t.mu.Lock()
		qLen := len(t.interpQueue)
		exec := t.execState
		abort := t.seqAbort
		t.mu.Unlock()
		// Read inflight last: if the sequencer dequeues + marks inflight between
		// the queue read and here, we observe inflight=true and keep waiting.
		inflight := t.seqInflight.Load()

		// seqInflight closes the TOCTOU gap between the sequencer dequeuing a
		// command and that command's first setExecState — without it this can
		// observe (empty, ExecDone) mid-command and let the interpreter synch
		// against a machine position motion hasn't reached yet.
		if qLen == 0 && !inflight && exec == ExecDone {
			return false
		}

		select {
		case <-abort:
			return true
		case <-ticker.C:
		}
	}
}

// Jog handles continuous, incremental, and absolute jogs.
func (t *Task) Jog(jogType int32, jjogmode bool, axisOrJoint int32, velocity, distance float64) error {
	return t.jogInternal(jogType, jjogmode, axisOrJoint, velocity, distance, false)
}

// JogFromHAL is like Jog but marks the jog as HAL-pin-driven (no watchdog timeout).
func (t *Task) JogFromHAL(jogType int32, jjogmode bool, axisOrJoint int32, velocity, distance float64) error {
	return t.jogInternal(jogType, jjogmode, axisOrJoint, velocity, distance, true)
}

func (t *Task) jogInternal(jogType int32, jjogmode bool, axisOrJoint int32, velocity, distance float64, fromHAL bool) error {
	isTeleop := int32(0)
	if !jjogmode {
		isTeleop = 1
	}

	// JOG_STOP is abort-class: releasing the jog button must stop the axis
	// immediately, never queue behind a command holding cmdMu. It needs no
	// state guard — aborting a jog is always safe.
	if jogType == JogStop {
		t.mu.Lock()
		if axisOrJoint >= 0 && int(axisOrJoint) < len(t.activeJogs) {
			t.activeJogs[axisOrJoint].active = false
		}
		t.mu.Unlock()
		return t.motion.JogAbort(axisOrJoint, isTeleop)
	}

	if err := t.preflightJog(); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.canJog(); err != nil {
		return err
	}

	// Ensure motion is in the correct mode for the requested jog type.
	if isTeleop == 1 {
		ms, err := t.status.GetStatus()
		if err == nil && ms.Teleop == 0 {
			t.mu.Unlock()
			ok := t.waitMotionTeleop()
			t.mu.Lock()
			if !ok {
				return fmt.Errorf("cannot jog: motion did not switch to teleop mode")
			}
		}
	} else {
		ms, err := t.status.GetStatus()
		if err == nil && (ms.Coord != 0 || ms.Teleop != 0) {
			_ = t.motion.SetFree()
			t.mu.Unlock()
			t.waitMotionFree()
			t.mu.Lock()
		}
	}

	// Clamp velocity to joint/axis max (matches C milltask emcJogCont/Incr).
	velocity = t.clampJogVel(velocity, axisOrJoint, jjogmode)

	switch jogType {
	case JogContinuous:
		if axisOrJoint >= 0 && int(axisOrJoint) < len(t.activeJogs) {
			t.activeJogs[axisOrJoint] = activeJog{
				active:   true,
				isTeleop: isTeleop,
				fromHAL:  fromHAL,
				lastSeen: time.Now(),
			}
		}
		return t.motion.JogCont(axisOrJoint, velocity, isTeleop)
	case JogIncrement:
		return t.motion.JogIncr(axisOrJoint, velocity, distance, isTeleop)
	case JogAbsolute:
		// For absolute jog, `distance` carries the target position (C++ emcJogAbs
		// maps the message's distance onto emcmotCommand.offset).
		return t.motion.JogAbs(axisOrJoint, velocity, distance, isTeleop)
	}
	return nil
}

// clampJogVel clamps velocity to the joint or axis max velocity.
// Must be called with t.mu held.
func (t *Task) clampJogVel(vel float64, nr int32, jjogmode bool) float64 {
	var maxVel float64
	if jjogmode {
		if nr >= 0 && int(nr) < len(t.jointMaxVel) {
			maxVel = t.jointMaxVel[nr]
		}
	} else {
		if nr >= 0 && int(nr) < len(t.axisMaxVel) {
			maxVel = t.axisMaxVel[nr]
		}
	}
	if maxVel <= 0 {
		return vel
	}
	if vel > maxVel {
		return maxVel
	} else if vel < -maxVel {
		return -maxVel
	}
	return vel
}

// JogStop stops a jog on the specified axis/joint.
func (t *Task) JogStop(jjogmode bool, axisOrJoint int32) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if axisOrJoint >= 0 && int(axisOrJoint) < len(t.activeJogs) {
		t.activeJogs[axisOrJoint].active = false
	}

	isTeleop := int32(0)
	if !jjogmode {
		isTeleop = 1
	}
	return t.motion.JogAbort(axisOrJoint, isTeleop)
}

// Spindle controls spindle on/off/direction/increase/decrease.
func (t *Task) Spindle(cmd int32, speed float64, spindleNum, wait int32) error {
	// Authoritative range check against the configured spindle count (covers all
	// transports incl. halui). -1 = all-spindles broadcast is accepted.
	if err := t.validSpindle(spindleNum, true); err != nil {
		t.operatorError(err.Error())
		return err
	}
	if err := t.preflightSpindle(cmd); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}

	// The program owns the spindle while it is running: reject manual on/off from
	// a UI/halui during an AUTO run (matching C++ — only spindle-override
	// increase/decrease/constant are accepted while READING). Manual control is
	// allowed when idle or paused. One guard, before the dispatch switch.
	switch cmd {
	case SpindleForward, SpindleReverse, SpindleOff:
		if err := t.spindleOwnedByProgram(); err != nil {
			return err
		}
	}

	switch cmd {
	case SpindleForward:
		return t.motion.SpindleOn(spindleNum, speed, 0, 0, wait)
	case SpindleReverse:
		return t.motion.SpindleOn(spindleNum, -speed, 0, 0, wait)
	case SpindleOff:
		return t.motion.SpindleOff(spindleNum)
	case SpindleIncrease:
		return t.motion.SpindleIncrease(spindleNum) // override, allowed while running
	case SpindleDecrease:
		return t.motion.SpindleDecrease(spindleNum) // override, allowed while running
	}
	return nil
}

// interpRunning reports whether an AUTO program is actively executing — not
// idle and not paused — i.e. the interpreter owns the machine. Must be called
// with t.mu held.
func (t *Task) interpRunning() bool {
	return t.mode == ModeAuto && t.interpState != InterpIdle && t.interpState != InterpPaused
}

// programBusy reports whether a program or MDI command is loaded and mid-run
// (executing or paused) — i.e. the interpreter/sequencer owns the machine.
// Unlike interpRunning it also covers MDI execution and the paused state, so it
// is the right guard for operations that would corrupt an in-flight run. Stays
// false when idle/estop/off/manual, so it does not re-break the any-state
// operations (e.g. unhome in estop). Must be called with t.mu held.
func (t *Task) programBusy() bool {
	return t.interpState != InterpIdle || t.interpActive
}

// spindleRunErr rejects a manual spindle/brake command issued during a run.
func (t *Task) spindleRunErr() error {
	t.operatorError("Can't control the spindle manually while a program is running")
	return ErrBusy
}

// spindleOwnedByProgram rejects a manual spindle on/off/brake command while an
// AUTO program owns the spindle (interpRunning): only the override
// increase/decrease/constant are allowed while a program reads. This single
// guard replaces the copies that preflightSpindle, each Spindle on/off case
// arm, and Brake used to keep in sync. Must be called with t.mu held.
func (t *Task) spindleOwnedByProgram() error {
	if t.interpRunning() {
		return t.spindleRunErr()
	}
	return nil
}

// Home initiates homing for the specified joint.
func (t *Task) Home(joint int32) error {
	if err := t.preflightManualMode(true); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}
	if err := t.ensureMode(ModeManual); err != nil {
		// Same race-window refusal as autoCommand's: message it, or the
		// operator's Home click dies silently (the MDI body's precedent).
		t.operatorError(fmt.Sprintf("Cannot home: %v", err))
		return err
	}
	// Home requires joint (FREE) mode — motion may be in teleop even when
	// task mode is already ModeManual.
	t.mu.Unlock()
	_ = t.motion.SetFree()
	t.waitMotionFree()
	t.mu.Lock()
	return t.motion.JointHome(joint)
}

// Unhome un-homes the specified joint.
func (t *Task) Unhome(joint int32) error {
	if err := t.preflightNotBusy("Can't unhome while a program is running"); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	// Rejected only while a program or MDI command is mid-run (or paused):
	// clearing a joint's home reference while the interpreter is driving motion
	// would corrupt the run (and, mid-motion, the position reference). Motion's
	// per-joint guard only catches free-mode jog/homing, not coordinated moves,
	// so this guard belongs here.
	if err := t.rejectIfBusy("Can't unhome while a program is running"); err != nil {
		return err
	}
	// Otherwise unhoming is allowed in ANY state (including ESTOP and OFF) and
	// any mode, matching C++ which accepts EMC_JOINT_UNHOME broadly — you often
	// need to clear a homed joint precisely when the machine is in estop. Motion
	// clears the homed flag and forces FREE mode internally, so no requireOn /
	// mode switch is needed (and requireOn would wrongly reject it in estop/off).
	return t.motion.JointUnhome(joint)
}

// OverrideLimits overrides tripped hard limits for a joint so it can be jogged
// off the limit. joint < 0 resumes normal limit checking (mirrors C++
// emcJointOverrideLimits / EMCMOT_OVERRIDE_LIMITS).
//
// State gate mirrors 2.9's emctaskmain table: allowed in ESTOP/OFF/ESTOP_RESET
// in any mode — this is the limit-recovery path, where you set the override
// while the machine is faulted off after hitting a hard limit so it can be
// re-enabled and jogged clear — and when ON only in MANUAL. In gomc's
// auto-mode-switch model, switch to manual when ON rather than rejecting a
// non-manual caller (ensureMode still refuses while a program/MDI is running or
// homing, so we never override limits mid-run).
func (t *Task) OverrideLimits(joint int32) error {
	if err := t.preflightManualMode(false); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == StateOn {
		if err := t.ensureMode(ModeManual); err != nil {
			t.operatorError(fmt.Sprintf("Cannot override limits: %v", err))
			return err
		}
	}
	return t.motion.OverrideLimits(joint)
}

// TeleopEnable enables/disables teleop mode.
// TeleopEnable is accepted in any state (matches C++ TRAJ_SET_TELEOP_ENABLE in
// OFF/ESTOP); it just sets the motion mode.
func (t *Task) TeleopEnable(enable bool) error {
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	if enable {
		return t.motion.SetTeleop()
	}
	return t.motion.SetFree()
}

// SetFeedOverride sets the feed override percentage.
// Override setters are accepted in ANY state (including ESTOP/OFF), matching
// C++ — the scale is stored in the motion controller and takes effect when the
// machine runs, so a UI can set the sliders before enabling. No requireOn.
func (t *Task) SetFeedOverride(rate float64) error {
	return t.motion.SetFeedScale(rate)
}

// SetSpindleOverride sets spindle speed override. spindleNum -1 broadcasts to
// all spindles; the range is validated against the configured spindle count.
func (t *Task) SetSpindleOverride(rate float64, spindleNum int32) error {
	if err := t.validSpindle(spindleNum, true); err != nil {
		t.operatorError(err.Error())
		return err
	}
	return t.motion.SetSpindleScale(spindleNum, rate)
}

// SetRapidOverride sets the rapid override percentage.
func (t *Task) SetRapidOverride(rate float64) error {
	return t.motion.SetRapidScale(rate)
}

func boolToInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// SetFeedOverrideEnable / SetFeedHoldEnable / SetSpindleOverrideEnable are the
// GUI-immediate override-enable toggles (C++ EMC_TRAJ_SET_FO/FH/SO_ENABLE). They
// pass straight to motion like the scale overrides; the G-code path (M48-M53)
// remains separate. All three are forced on at machine-on.
func (t *Task) SetFeedOverrideEnable(enable bool) error {
	return t.motion.FeedScaleEnable(boolToInt32(enable))
}
func (t *Task) SetFeedHoldEnable(enable bool) error {
	return t.motion.FeedHoldEnable(boolToInt32(enable))
}
func (t *Task) SetSpindleOverrideEnable(enable bool, spindleNum int32) error {
	return t.motion.SpindleScaleEnable(spindleNum, boolToInt32(enable))
}

// SetMaxVelocity sets the maximum trajectory velocity.
func (t *Task) SetMaxVelocity(velocity float64) error {
	return t.motion.SetVelLimit(velocity)
}

// Flood turns flood coolant on or off.
func (t *Task) Flood(on bool) error {
	if err := t.preflightOn(); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}
	if on {
		if err := t.io.CoolantFloodOn(); err != nil {
			return err
		}
		t.floodOn = true
		return nil
	}
	if err := t.io.CoolantFloodOff(); err != nil {
		return err
	}
	t.floodOn = false
	return nil
}

// Mist turns mist coolant on or off.
func (t *Task) Mist(on bool) error {
	if err := t.preflightOn(); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}
	if on {
		if err := t.io.CoolantMistOn(); err != nil {
			return err
		}
		t.mistOn = true
		return nil
	}
	if err := t.io.CoolantMistOff(); err != nil {
		return err
	}
	t.mistOn = false
	return nil
}

// Brake engages/disengages spindle brake.
func (t *Task) Brake(on bool, spindleNum int32) error {
	// The brake is an ownership command like spindle on/off: rejected while a
	// program runs. Same preflight chain.
	if err := t.preflightSpindle(SpindleOff); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}
	if err := t.spindleOwnedByProgram(); err != nil {
		return err // program owns the spindle while running
	}
	if on {
		return t.motion.SpindleBrakeEngage(spindleNum)
	}
	return t.motion.SpindleBrakeRelease(spindleNum)
}

// Lube turns lubrication on or off.
func (t *Task) Lube(on bool) error {
	if err := t.preflightOn(); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.requireOn(); err != nil {
		return err
	}
	if on {
		if err := t.io.LubeOn(); err != nil {
			return err
		}
		t.lubeOn = true
		return nil
	}
	if err := t.io.LubeOff(); err != nil {
		return err
	}
	t.lubeOn = false
	return nil
}

// Abort aborts all motion and interpreter execution.
// Matches C milltask emcTaskAbort behavior: abort motion, abort IO,
// stop spindles, turn off coolant, clear interpreter queue, close program.
//
// Abort is abort-class: the signals fire before queueing on cmdMu, because
// they are what unblock a command currently holding cmdMu (EnqueueCmd
// backpressure, wait loops). The cleanup then serializes normally.
func (t *Task) Abort() error {
	t.signalAbort()
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	t.abortLocked()
	t.mu.Unlock()
	return nil
}

// abortLocked performs the full user-abort sequence, mirroring 2.9's
// EMC_TASK_ABORT handler: emcTaskAbort + emcTaskStateRestore + emcIoAbort +
// emcSpindleAbort(all) + mdi_execute_abort + emcAbortCleanup (on_abort).
// Caller MUST hold t.mu on entry; t.mu is held on return.
// Internally unlocks t.mu for external I/O calls to avoid blocking stat reads.
func (t *Task) abortLocked() { t.abortMachineLocked(true) }

// modeAbortLocked is the light abort a mode switch performs. 2.9's
// emcTaskSetMode runs only emcTaskAbort (+ mdi_execute_abort when leaving to
// MANUAL): motion abort, interp-list clear, plan close/reset, resynch. It does
// NOT stop spindles, abort IO, touch coolant, run on_abort, or restore modal
// state from the executing tag — a spindle started in one mode keeps turning
// across the switch. Every MDI issued through the transactional
// ensureMode(ModeMDI)/restoreModeTx round-trip re-enters MDI mode, so using
// the full abort here stopped a running spindle on the NEXT MDI command (S1000
// M3, then F100 → spindle off).
// Same locking contract as abortLocked.
func (t *Task) modeAbortLocked() { t.abortMachineLocked(false) }

// abortMachineLocked is the shared abort core. full selects the user-abort
// extras (spindle/IO/coolant stop, on_abort, tag restore) on top of the light
// mode-switch teardown.
// Caller MUST hold t.mu on entry; t.mu is held on return.
func (t *Task) abortMachineLocked(full bool) {
	// Captured before the clobber below: the last-dispatched fallback in the
	// tag capture must only apply when this abort interrupts an ACTIVE
	// program. After a normal completion the trailing modal-only lines (e.g.
	// a final G64 P/Q) executed legitimately — rolling them back on the next
	// mode switch (which also aborts) would be wrong.
	wasRunning := t.interpState != InterpIdle
	t.interpState = InterpIdle
	t.execState = ExecDone
	t.mdiQueue = t.mdiQueue[:0]
	t.taskCommand = "" // aborted MDI is no longer executing — clear the stat echo
	t.stepping = false
	t.readLine = 0
	t.currentLine = 0

	interp := t.interp
	wasAuto := t.mode == ModeAuto
	t.mu.Unlock()

	// Capture the executing segment's packed state tag BEFORE stopping motion
	// (2.9 emcTaskStateRestore reads motion.traj.tag; AUTO only). Used below
	// to roll the interp's modal state back from readahead to what was
	// actually executing when the abort hit.
	// t.mu spans the GetStatus + map read: BuildStat's motionInfoAndPrune
	// deletes entries below ITS status id under t.mu, so an unlocked gap here
	// lets a fresher status cycle prune this entry first and the restore would
	// silently no-op. GetStatus is a shared-memory copy (no t.mu paths), cheap
	// enough to hold the lock across on this once-per-abort path.
	var restoreTag []byte
	var restoreID, restoreLine int32
	var usedFallback bool
	if full && wasAuto && t.status != nil {
		t.mu.Lock()
		if ms, err := t.status.GetStatus(); err == nil {
			restoreID = ms.Id
			if info, ok := t.motionMap[ms.Id]; ok {
				restoreTag = info.Tag
				restoreLine = info.LineNo
			}
			// Fallback: motion reports no executing segment. That is NOT
			// "nothing was running" — the TP zeroes its exec id whenever the
			// queue momentarily runs dry (feed starvation at an exact-stop
			// corner is the observed case: the g64 abort test under CI load).
			// With an empty queue everything dispatched has executed, so the
			// last dispatched segment is the one the machine stopped on —
			// restore its modal state instead of silently skipping and
			// leaking readahead-only state (G64 P/Q, a G5x switch) past the
			// abort. Same fallback for a pruned/untagged entry.
			if restoreTag == nil && wasRunning && t.lastMotionID != 0 {
				if info, ok := t.motionMap[t.lastMotionID]; ok && info.Tag != nil {
					restoreTag = info.Tag
					restoreLine = info.LineNo
					restoreID = t.lastMotionID
					usedFallback = true
				}
			}
		}
		t.mu.Unlock()
		// Neither the executing id nor the last dispatched motion yielded a
		// tag while a program was mid-run: the modal rollback below cannot
		// run. Say so instead of letting it surface as a modal-state
		// heisenbug three commands later. (restoreID==0 with no dispatched
		// motion is the legitimate nothing-ran case — stay quiet.)
		if restoreTag == nil && restoreID != 0 {
			t.logger.Warn("abort: no state tag for executing motion — modal restore skipped",
				"motion_id", restoreID)
		}
	}

	// External calls (no mutex held — won't block stat reads).
	t.AbortSequencer()
	t.mcodeAbort()
	_ = t.motion.Abort()
	if full {
		_ = t.io.IoAbort(emcAbortTaskAbort)
		_ = t.motion.SpindleOff(-1) // all-spindles broadcast
		_ = t.io.CoolantFloodOff()
		_ = t.io.CoolantMistOff()
	}

	// Motion/IO are stopped; now wait for the runProgram producer to stop
	// touching the interpreter before we Close/Reset it (avoids a data race on
	// the non-thread-safe interpreter). Only the interpreter reset waits — the
	// machine has already been stopped above.
	t.waitRunProgramDone()

	if interp != nil {
		if full {
			t.abortInterp(emcAbortTaskAbort, "user abort")
		}
		_ = interp.Close()
		_ = interp.Reset()
		// Sync the canon endpoint from the machine BEFORE Synch (R6/C11), so the
		// interpreter re-synchs to the actual stopped position, not the stale
		// read-ahead endpoint — matching finishShutdown and the executeMDI/AutoRun
		// re-syncs that otherwise mask this.
		t.canon.syncEndPointFromMachine()
		_ = interp.Synch()
		t.updateActiveCodes(interp) // republish stat caches after reset
	}

	// Terminal state re-commit: the values set at entry were provisional (a
	// still-draining sequencer iteration may have overwritten them with a
	// wait-state before it observed the abort). restartSequencer commits the
	// authoritative interp/exec state after the join; the flag clear follows
	// (both after the join, no other writer remains). abortLocked returns with
	// t.mu held (its contract), so re-lock and do not unlock.
	t.restartSequencer(InterpIdle, ExecDone)

	// 2.9 parity (emcTaskStateRestore → Interp::restore_from_tag): re-execute
	// the executing segment's modal state so readahead-only changes (G64 P/Q,
	// a G5x switch, tool offset) are rolled back. Runs after restartSequencer
	// because the restore's canon emissions (SET_TERM_COND, SET_G5X_OFFSET)
	// need the fresh sequencer.
	if interp != nil && restoreTag != nil {
		if err := interp.RestoreFromTag(restoreTag); err != nil {
			t.logger.Warn("abort: modal state restore failed", "err", err)
		}
		gc, _, _ := t.updateActiveCodes(interp)
		t.logger.Debug("abort: modal state restored from executing tag",
			"motion_id", restoreID, "line", restoreLine,
			"from_last_dispatched", usedFallback, "gcodes", gc)
	}

	t.mu.Lock()
	if full {
		t.floodOn = false
		t.mistOn = false
	}
}

// waitRunProgramDone blocks until the runProgram goroutine (if any) has exited,
// so the interpreter is safe to Close/Reset. The caller must already have
// signalled the abort (AbortSequencer closed seqAbort) so the producer will
// exit. MUST be called without t.mu held (runProgram takes t.mu as it winds
// down) and MUST NOT be called from within runProgram (e.g. faultProgram) — it
// would wait on itself.
func (t *Task) waitRunProgramDone() {
	t.mu.Lock()
	rd := t.runDone
	t.mu.Unlock()
	if rd != nil {
		<-rd
	}
}

// TaskPlanSynch forces a sync between interpreter and motion.
func (t *Task) TaskPlanSynch() error {
	if err := t.preflightNotBusy(""); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.interp == nil {
		return nil
	}
	// The producer goroutine owns the interpreter during a run — touching it
	// here would be a concurrent access to the non-thread-safe interp.
	if t.programBusy() {
		return ErrBusy
	}
	t.canon.syncEndPointFromMachine()
	return t.interp.Synch()
}

// SetOptionalStop enables/disables optional stop (M1).
func (t *Task) SetOptionalStop(on bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.optionalStop = on
	return nil
}

// SetBlockDelete enables/disables block delete (/).
func (t *Task) SetBlockDelete(on bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.blockDelete = on
	return nil
}

// LoadToolTable reloads the tool table from file.
// LoadToolTable reloads the tool table. An empty file uses the default table
// (matches C++ emcToolLoadToolTable, which accepts the requested filename).
func (t *Task) LoadToolTable(file string) error {
	if err := t.preflightNotBusy("Can't load tool table while a program is running"); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	// Reloading tool data mid-run would race the producer on the interpreter
	// (and change offsets under a running program) — reject while busy.
	if err := t.rejectIfBusy("Can't load tool table while a program is running"); err != nil {
		return err
	}

	err := t.io.ToolLoadTable(file)
	if err != nil {
		t.operatorError(fmt.Sprintf("can't load tool table: %s", err))
		return err
	}
	// Synch interpreter so it re-reads tool_table[] from the
	// tooltable module via GET_EXTERNAL_TOOL_TABLE callbacks.
	if t.interp != nil {
		_ = t.interp.Synch()
	}
	t.mu.Lock()
	t.previewSeq++
	t.mu.Unlock()
	return nil
}

// ToolUnload unloads the tool from the spindle (manual EMC_TOOL_UNLOAD).
// Rejected while a program is running (it changes tool state under the interp).
func (t *Task) ToolUnload() error {
	if err := t.preflightNotBusy("Can't unload the tool while a program is running"); err != nil {
		return err
	}
	t.cmdMu.Lock()
	defer t.cmdMu.Unlock()

	if err := t.rejectIfBusy("Can't unload the tool while a program is running"); err != nil {
		return err
	}

	err := t.io.ToolUnload()
	if err != nil {
		t.operatorError(fmt.Sprintf("can't unload tool: %s", err))
		return err
	}
	if t.interp != nil {
		_ = t.interp.Synch() // re-read tool state after the unload
	}
	return nil
}

// WaitComplete waits for the task to settle: exec state done AND the
// interpreter idle AND no queued MDIs. Checking execState alone raced
// no-motion MDIs (e.g. the AXIS touch-off "G10 L20 ..."): those never leave
// ExecDone — only interpState goes busy (set synchronously by the /mdi POST,
// cleared by the async finishMDI) — so a wait_complete right after c.mdi()
// returned instantly and the client's next command (AXIS: program_open to
// reload the preview) hit the "Can't open a program while one is running"
// reject. Classic NML closed this with the echo_serial_number handshake;
// this is the equivalent for the queue-registered-before-POST-returns model.
func (t *Task) WaitComplete(timeout float64) error {
	deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		t.mu.Lock()
		exec := t.execState
		settled := !t.programBusy() && len(t.mdiQueue) == 0
		t.mu.Unlock()
		if (exec == ExecDone || exec == ExecError) && settled {
			return nil
		}
		if timeout > 0 && time.Now().After(deadline) {
			return fmt.Errorf("WaitComplete: timeout after %.1fs", timeout)
		}
		<-ticker.C
	}
}

// SetDebug sets the debug level.
func (t *Task) SetDebug(debug int32) error {
	t.mu.Lock()
	t.debug = debug // echoed to stat.debug
	t.mu.Unlock()

	// C++ sets both the motion and IO debug levels (emcSetDebug -> motion + IO).
	_ = t.io.SetDebug(debug)
	return t.motion.SetDebug(debug)
}

// SetJogAxis sets the selected jog axis (0=X .. 8=W, -1=none).
func (t *Task) SetJogAxis(axis int32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if axis < -1 || axis >= int32(maxAxes) {
		return fmt.Errorf("SetJogAxis: invalid axis %d", axis)
	}
	t.jogAxis = axis
	return nil
}

// SetJogIncrement sets the jog increment distance (0 = continuous).
func (t *Task) SetJogIncrement(increment float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if increment < 0 {
		return fmt.Errorf("SetJogIncrement: invalid increment %f", increment)
	}
	t.jogIncrement = increment
	return nil
}

// SetJogSpeed sets the linear jog speed (units/sec).
func (t *Task) SetJogSpeed(speed float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if speed < 0 {
		return fmt.Errorf("SetJogSpeed: invalid speed %f", speed)
	}
	t.jogSpeed = speed
	return nil
}

// SetAjogSpeed sets the angular jog speed (deg/sec).
func (t *Task) SetAjogSpeed(speed float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if speed < 0 {
		return fmt.Errorf("SetAjogSpeed: invalid speed %f", speed)
	}
	t.ajogSpeed = speed
	return nil
}
