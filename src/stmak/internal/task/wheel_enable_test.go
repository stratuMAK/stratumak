// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import "testing"

// wheelTestTask is a machine that is on, homed (no joints configured) and
// resting in MDI with motion coordinated -- the state an aborted MDI, an estop
// out of an MDI or a finished program leaves motion in.
func wheelTestTask(t *testing.T) (*Task, *mockMotion, *mockStatus, *halUI) {
	t.Helper()
	task, mot, _ := newTestTask()
	bringUp(t, task)
	if err := task.SetMode(int32(ModeMDI)); err != nil {
		t.Fatalf("SetMode(MDI): %v", err)
	}
	if coord, teleop := mot.mode(); coord != 1 || teleop != 0 {
		t.Fatalf("setup: motion coord=%d teleop=%d, want coordinated", coord, teleop)
	}
	h := &halUI{axisMask: 1 << 0} // X
	return task, mot, task.status.(*mockStatus), h
}

// The jog wheel runs in motion and only in teleop. Its enable going high is
// the moment task puts motion into teleop, as a joystick jog would.
func TestWheelEnable_SwitchesToTeleop(t *testing.T) {
	task, mot, st, h := wheelTestTask(t)

	h.checkWheelEnable(task) // enable low: nothing happens
	if _, teleop := mot.mode(); teleop != 0 {
		t.Fatal("switched to teleop without a wheel enable")
	}

	st.wheelEnable.Store(1 << 0)
	h.checkWheelEnable(task)
	if coord, teleop := mot.mode(); coord != 0 || teleop != 1 {
		t.Fatalf("after wheel enable: motion coord=%d teleop=%d, want teleop", coord, teleop)
	}
	task.mu.Lock()
	mode := task.mode
	task.mu.Unlock()
	if mode != ModeMDI {
		t.Fatalf("task mode %s, want MDI (a jog changes only the motion mode)", mode)
	}
}

// Only the rising edge switches: an enable held high does not keep pulling
// motion out of coordinated mode, e.g. when the next MDI has re-entered it.
func TestWheelEnable_OnlyOnRisingEdge(t *testing.T) {
	task, mot, st, h := wheelTestTask(t)
	st.wheelEnable.Store(1 << 0)
	h.checkWheelEnable(task)

	_ = mot.setMode("SetCoord", 1, 0)
	h.checkWheelEnable(task) // still high
	if coord, teleop := mot.mode(); coord != 1 || teleop != 0 {
		t.Fatalf("held enable switched again: coord=%d teleop=%d", coord, teleop)
	}
}

// Same guard as every jog: never while a program or MDI is running.
func TestWheelEnable_RefusedWhileInterpBusy(t *testing.T) {
	task, mot, st, h := wheelTestTask(t)
	task.mu.Lock()
	task.interpState = InterpReading
	task.mu.Unlock()

	st.wheelEnable.Store(1 << 0)
	h.checkWheelEnable(task)
	if coord, teleop := mot.mode(); coord != 1 || teleop != 0 {
		t.Fatalf("switched while interpreter busy: coord=%d teleop=%d", coord, teleop)
	}

	// The enable is not forgotten: once the interpreter is idle again the
	// pending enable switches motion, without the operator toggling the
	// pendant switch again.
	task.mu.Lock()
	task.interpState = InterpIdle
	task.mu.Unlock()
	h.checkWheelEnable(task)
	if coord, teleop := mot.mode(); coord != 0 || teleop != 1 {
		t.Fatalf("pending enable did not switch once idle: coord=%d teleop=%d", coord, teleop)
	}
}

// Axes outside the configured set are ignored.
func TestWheelEnable_IgnoresUnconfiguredAxis(t *testing.T) {
	task, mot, st, h := wheelTestTask(t)
	st.wheelEnable.Store(1 << 1) // Y, not in axisMask
	h.checkWheelEnable(task)
	if _, teleop := mot.mode(); teleop != 0 {
		t.Fatal("unconfigured axis enable switched motion")
	}
}
