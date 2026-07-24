// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"time"
)

// Guard errors returned when a command is rejected due to state/mode.
var (
	ErrNotOn     = fmt.Errorf("machine not on")
	ErrWrongMode = fmt.Errorf("wrong mode for command")
	ErrEstop     = fmt.Errorf("machine in estop")
	ErrBusy      = fmt.Errorf("interpreter busy")
	ErrNoProgram = fmt.Errorf("no program loaded")
	ErrNotHomed  = fmt.Errorf("not homed")
)

// requireOnQuiet checks that the machine is powered on WITHOUT surfacing an
// operator error. Hot-path commands where a refusal must stay silent — jog,
// which fires on key-repeat and would otherwise flood the panel — use this
// directly; requireOn adds the operator message for the rest.
func (t *Task) requireOnQuiet() error {
	if t.state != StateOn {
		return fmt.Errorf("%w: state is %s", ErrNotOn, t.state)
	}
	return nil
}

// requireOn checks that the machine is powered on and, on failure, tells the
// operator why. The operator-error channel is the authoritative error surface
// (the REST status is control-flow only, logged to stderr), so a refused
// command that returned bare would be invisible on the panel.
func (t *Task) requireOn() error {
	if err := t.requireOnQuiet(); err != nil {
		t.operatorError("Machine must be ON")
		return err
	}
	return nil
}

// canSwitchMode reports whether a switch to the required mode would be
// accepted, without performing it: always when already in that mode, otherwise
// only while the interpreter is idle and no joint is homing. This is the
// reject half of ensureMode, shared with the command preflights so both
// evaluate identical conditions. Must be called with t.mu held.
func (t *Task) canSwitchMode(required TaskMode) error {
	if t.mode == required {
		return nil
	}
	// Cannot switch mode while interpreter is active.
	if t.interpState != InterpIdle {
		return fmt.Errorf("%w: interpreter busy, cannot switch to %s", ErrBusy, required)
	}
	// Cannot switch mode while homing is in progress.
	if t.anyJointHoming() {
		return fmt.Errorf("%w: homing in progress", ErrBusy)
	}
	return nil
}

// ensureMode switches to the required mode if safe (interpreter idle), or
// returns nil if already in the right mode. This replaces client-side
// ensure_mode() calls — the server decides whether a mode switch is allowed.
// The switch is sticky: the mode stays where the command put it (2.9 AXIS
// semantics — mode only changes when a command needs it or a client asks).
// A repeat command in the same mode is a true no-op. The one 2.9 client that
// restored the previous mode after MDI — halui (halui.cc halui_old_mode) —
// does so locally in halui.go, not here.
// Must be called with t.mu held. May temporarily unlock t.mu for I/O.
func (t *Task) ensureMode(required TaskMode) error {
	if err := t.canSwitchMode(required); err != nil {
		return err
	}
	if t.mode == required {
		return nil
	}
	// Perform the mode switch inline (same logic as SetMode but already holding
	// mu). The light modeAbortLocked (2.9 emcTaskAbort) is essential here: the
	// full abort's spindle/IO/coolant stop would kill a spindle a previous
	// command started.
	switch required {
	case ModeManual:
		t.modeAbortLocked()
		t.mode = ModeManual
		t.mu.Unlock()
		_ = t.motion.SetFree()
		t.waitMotionFree()
		t.mu.Lock()
	case ModeMDI:
		t.modeAbortLocked()
		t.mode = ModeMDI
		t.mu.Unlock()
		_ = t.motion.SetCoord()
		if t.interp != nil {
			_ = t.interp.Synch()
		}
		t.mu.Lock()
	case ModeAuto:
		t.modeAbortLocked()
		t.mode = ModeAuto
		t.mu.Unlock()
		_ = t.motion.SetCoord()
		if t.interp != nil {
			_ = t.interp.Synch()
		}
		t.mu.Lock()
	default:
		return fmt.Errorf("%w: unknown mode %d", ErrWrongMode, required)
	}
	return nil
}

// waitMotionFree polls motion status until the motion controller is in FREE
// mode (not coord and not teleop). Must be called WITHOUT t.mu held.
func (t *Task) waitMotionFree() {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ms, err := t.status.GetStatus()
		if err == nil && ms.Coord == 0 && ms.Teleop == 0 {
			return
		}
		time.Sleep(pollInterval)
	}
}

// waitMotionTeleop waits until motion is in teleop mode (up to 500ms).
// Retries sending SetTeleop if motion hasn't switched (e.g. wasn't INPOS).
func (t *Task) waitMotionTeleop() bool {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ms, err := t.status.GetStatus()
		if err == nil && ms.Teleop != 0 {
			return true
		}
		// Retry sending SetTeleop (motion may have rejected due to !INPOS).
		_ = t.motion.SetTeleop()
		time.Sleep(pollInterval)
	}
	return false
}

// requireNotEstop checks that we are not in estop.
func (t *Task) requireNotEstop() error {
	if t.state == StateEstop {
		t.operatorError("Machine is in E-STOP")
		return ErrEstop
	}
	return nil
}

// requireInterpIdle checks that the interpreter is idle (for jog-while-idle).
func (t *Task) requireInterpIdle() error {
	if t.interpState != InterpIdle {
		return ErrBusy
	}
	return nil
}

// requireProgram checks that a program file is loaded (for AUTO RUN).
func (t *Task) requireProgram() error {
	if !t.programOpen {
		t.operatorError("No program loaded")
		return ErrNoProgram
	}
	return nil
}

// validSpindle validates a spindle index against the CONFIGURED spindle count —
// the authoritative range check that the IDL @min/@max bound (a fixed
// EMCMOT_MAX_SPINDLES literal) cannot make because it does not know numSpindles.
// It covers every transport (REST/WS/halui). spindle_num -1 is the all-spindles
// broadcast, accepted only when allowBroadcast is true. numSpindles is immutable
// after config load, so no lock is needed (like the numJoints guards).
func (t *Task) validSpindle(spindleNum int32, allowBroadcast bool) error {
	if spindleNum == -1 && allowBroadcast {
		return nil
	}
	if spindleNum < 0 || int(spindleNum) >= t.numSpindles {
		return fmt.Errorf("spindle number %d out of range [0,%d)", spindleNum, t.numSpindles)
	}
	return nil
}

// allHomed returns true if all joints are homed.
func (t *Task) allHomed() bool {
	ms, err := t.status.GetStatus()
	if err != nil {
		return false
	}
	for j := 0; j < t.numJoints; j++ {
		if ms.Joints[j].Homed == 0 {
			return false
		}
	}
	return true
}

// anyJointHoming returns true if any joint is currently in a homing sequence.
func (t *Task) anyJointHoming() bool {
	if t.status == nil {
		return false
	}
	ms, err := t.status.GetStatus()
	if err != nil {
		return false
	}
	for j := 0; j < t.numJoints; j++ {
		if ms.Joints[j].Homing != 0 {
			return true
		}
	}
	return false
}

// requireHomed checks that all joints are homed (unless NO_FORCE_HOMING is set).
func (t *Task) requireHomed() error {
	if t.noForceHoming {
		return nil
	}
	if !t.allHomed() {
		return ErrNotHomed
	}
	return nil
}

// canJog returns true if jogging is allowed in current state.
// Jogging is allowed in MANUAL mode, or in AUTO/MDI when interpreter is idle.
//
// Every reject here is intentionally SILENT (no operatorError): jog is a
// hot-path command (key-repeat, continuous jog) and an operator message per
// refused jog would flood the panel. The quiet requireOn variant, the bare
// requireInterpIdle, and the bare ErrWrongMode below are all deliberate.
func (t *Task) canJog() error {
	if err := t.requireOnQuiet(); err != nil {
		return err
	}
	switch t.mode {
	case ModeManual:
		return nil
	case ModeAuto, ModeMDI:
		// Allow jog while idle (not running a program or MDI)
		return t.requireInterpIdle()
	default:
		return ErrWrongMode
	}
}

// externalOffsetApplied checks if external offsets are currently applied.
// Must be called with t.mu held (reads t.status which is immutable after init,
// but the check itself is a motion status read).
func (t *Task) externalOffsetApplied() bool {
	if t.status == nil {
		return false
	}
	ms, err := t.status.GetStatus()
	if err != nil {
		return false
	}
	return ms.ExternalOffsetsApplied != 0
}
