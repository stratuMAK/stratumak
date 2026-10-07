// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emccmd"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

// wantGated asserts a refusal by the ui-auto-disable gate: a state fault (409,
// not a controller malfunction) that names the gate.
func wantGated(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, errUIAutoDisabled) {
		t.Fatalf("%s: err = %v, want the ui-auto-disable refusal", what, err)
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

	tk.setUIAutoDisabled(true)
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
	tk.setUIAutoDisabled(true)

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
	if _, err := m.AutoCmd(emccmd.AutoCmd_AUTO_RUN, 0, true); errors.Is(err, errUIAutoDisabled) {
		t.Errorf("forced AutoCmd was refused by the gate: %v", err)
	}
}

func TestUIGateAbort(t *testing.T) {
	tk, _, _ := newTestTask()
	m := &milltaskModule{task: tk}
	bringUp(t, tk)
	tk.setUIAutoDisabled(true)

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
	tk.setUIAutoDisabled(true)
	if !tk.BuildStat().Task.UiAutoDisabled {
		t.Error("stat does not publish ui_auto_disabled")
	}
}
