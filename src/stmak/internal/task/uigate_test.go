// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emccmd"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

// wantGated asserts a refusal by the program.ui-disable gate: a state fault (409,
// not a controller malfunction) that names the gate.
func wantGated(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, errProgramUIDisabled) {
		t.Fatalf("%s: err = %v, want the program.ui-disable refusal", what, err)
	}
	var f *apiserver.Fault
	if !errors.As(err, &f) || f.Kind != apiserver.FaultState {
		t.Errorf("%s: err = %v, want a FaultState", what, err)
	}
}

func TestUIGateToggles(t *testing.T) {
	tk, _, _ := newTestTask()
	m := &milltaskModule{task: tk}

	// Pin low: no force needed.
	if _, err := m.SetOptionalStop(true, false); err != nil {
		t.Fatalf("SetOptionalStop with the pin low: %v", err)
	}

	if _, err := m.SetBlockDelete(false, false); err != nil {
		t.Fatalf("SetBlockDelete with the pin low: %v", err)
	}

	tk.setProgramUIDisabled(true)
	_, err := m.SetOptionalStop(false, false)
	wantGated(t, "SetOptionalStop", err)
	_, err = m.SetBlockDelete(true, false)
	wantGated(t, "SetBlockDelete", err)
	if !tk.optionalStop || tk.blockDelete {
		t.Errorf("a refused toggle changed the task: optional_stop=%v block_delete=%v",
			tk.optionalStop, tk.blockDelete)
	}

	// force is what a caller owning program flow passes.
	if _, err := m.SetOptionalStop(false, true); err != nil {
		t.Fatalf("forced SetOptionalStop: %v", err)
	}
	if _, err := m.SetBlockDelete(true, true); err != nil {
		t.Fatalf("forced SetBlockDelete: %v", err)
	}
	if tk.optionalStop || !tk.blockDelete {
		t.Errorf("forced toggles did not reach the task: optional_stop=%v block_delete=%v",
			tk.optionalStop, tk.blockDelete)
	}

	// The halui path reaches the Task directly and is never gated.
	if err := tk.SetOptionalStop(true); err != nil || !tk.optionalStop {
		t.Errorf("Task.SetOptionalStop under the pin: err=%v optional_stop=%v", err, tk.optionalStop)
	}
}

func TestUIGateAuto(t *testing.T) {
	tk, _, _ := newTestTask()
	m := &milltaskModule{task: tk}
	tk.setProgramUIDisabled(true)

	for name, cmd := range map[string]emccmd.AutoCmd{
		"run": emccmd.AutoCmd_AUTO_RUN, "step": emccmd.AutoCmd_AUTO_STEP,
		"pause": emccmd.AutoCmd_AUTO_PAUSE, "resume": emccmd.AutoCmd_AUTO_RESUME,
		"reverse": emccmd.AutoCmd_AUTO_REVERSE, "forward": emccmd.AutoCmd_AUTO_FORWARD,
	} {
		_, err := m.AutoCmd(cmd, 0, false)
		wantGated(t, "AutoCmd "+name, err)
	}
	// Forced, the command reaches the task, which refuses it for its own reason
	// (the machine is off), not for the gate.
	if _, err := m.AutoCmd(emccmd.AutoCmd_AUTO_RUN, 0, true); errors.Is(err, errProgramUIDisabled) {
		t.Errorf("forced AutoCmd was refused by the gate: %v", err)
	}
}

func TestUIGateAbort(t *testing.T) {
	tk, _, _ := newTestTask()
	m := &milltaskModule{task: tk}
	bringUp(t, tk)
	tk.setProgramUIDisabled(true)

	// Nothing running: aborting an MDI command, a jog or homing is not program
	// flow and stays available.
	if _, err := m.Abort(false); err != nil {
		t.Fatalf("Abort with no program running: %v", err)
	}

	tk.mu.Lock()
	tk.mode = ModeAuto
	tk.interpState = InterpPaused
	tk.mu.Unlock()
	_, err := m.Abort(false)
	wantGated(t, "Abort of a paused program", err)
	if !tk.programRunning() {
		t.Fatal("the refused abort stopped the program")
	}

	if _, err := m.Abort(true); err != nil {
		t.Fatalf("forced Abort: %v", err)
	}
	if tk.programRunning() {
		t.Error("forced Abort left the program running")
	}
}

func TestUIGateStat(t *testing.T) {
	tk, _, _ := newTestTask()
	tk.setProgramUIDisabled(true)
	if !tk.BuildStat().Task.ProgramUiDisabled {
		t.Error("stat does not publish program_ui_disabled")
	}
}

// wantMachineGated asserts a refusal by the machine.ui-disable gate.
func wantMachineGated(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, errMachineUIDisabled) {
		t.Fatalf("%s: err = %v, want the machine.ui-disable refusal", what, err)
	}
	var f *apiserver.Fault
	if !errors.As(err, &f) || f.Kind != apiserver.FaultState {
		t.Errorf("%s: err = %v, want a FaultState", what, err)
	}
}

func TestMachineUIGate(t *testing.T) {
	tk, _, _ := newTestTask()
	m := &milltaskModule{task: tk}
	state := func() TaskState {
		tk.mu.Lock()
		defer tk.mu.Unlock()
		return tk.state
	}

	// Pin low: no force needed.
	if _, err := m.SetState(int32(StateEstopReset), false); err != nil {
		t.Fatalf("SetState(EstopReset) with the pin low: %v", err)
	}

	tk.setMachineUIDisabled(true)
	// Every transition is refused, estop included: the pin is for a part of a
	// machine whose estop chain is in hardware.
	for name, s := range map[string]TaskState{
		"on": StateOn, "off": StateOff, "estop": StateEstop, "estop reset": StateEstopReset,
	} {
		_, err := m.SetState(int32(s), false)
		wantMachineGated(t, "SetState "+name, err)
	}
	if got := state(); got != StateEstopReset {
		t.Fatalf("a refused SetState changed the task: state = %v", got)
	}

	// force is what a caller owning the machine passes.
	if _, err := m.SetState(int32(StateOn), true); err != nil {
		t.Fatalf("forced SetState(On): %v", err)
	}
	if got := state(); got != StateOn {
		t.Fatalf("forced SetState(On) did not reach the task: state = %v", got)
	}

	// The halui path reaches the Task directly and is never gated.
	if err := tk.SetState(int32(StateEstop)); err != nil || state() != StateEstop {
		t.Errorf("Task.SetState(Estop) under the pin: err=%v state=%v", err, state())
	}

	// The program gate is a separate pin: machine.ui-disable leaves program
	// flow alone.
	if _, err := m.SetOptionalStop(true, false); err != nil {
		t.Errorf("SetOptionalStop under machine.ui-disable: %v", err)
	}
}

func TestMachineUIGateStat(t *testing.T) {
	tk, _, _ := newTestTask()
	tk.setMachineUIDisabled(true)
	st := tk.BuildStat().Task
	if !st.MachineUiDisabled {
		t.Error("stat does not publish machine_ui_disabled")
	}
	if st.ProgramUiDisabled {
		t.Error("machine.ui-disable leaked into program_ui_disabled")
	}
}
