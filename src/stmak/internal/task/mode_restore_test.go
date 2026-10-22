// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import "testing"

// interruptedMDIFromManual puts task in the state an MDI issued from Manual
// leaves while it is still running: ensureMode switched to MDI and saved
// Manual for the restore, and motion is coordinated.
func interruptedMDIFromManual(t *testing.T) (*Task, *mockMotion) {
	t.Helper()
	task, mot, _ := newTestTask()
	bringUp(t, task)
	task.mu.Lock()
	task.mode = ModeMDI
	task.modeBeforeTx = ModeManual
	task.modeTx = true
	task.mu.Unlock()
	_ = mot.setMode("SetCoord", 1, 0)
	return task, mot
}

// An aborted MDI never reaches finishMDI: the abort itself must return to
// Manual and put motion back into teleop, or the jog wheel stays dead.
func TestAbortedMDI_RestoresManualTeleop(t *testing.T) {
	task, mot := interruptedMDIFromManual(t)

	if err := task.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	task.mu.Lock()
	mode, tx := task.mode, task.modeTx
	task.mu.Unlock()
	if mode != ModeManual || tx {
		t.Fatalf("after abort: mode %s modeTx %v, want Manual and no pending restore", mode, tx)
	}
	if coord, teleop := mot.mode(); coord != 0 || teleop != 1 {
		t.Fatalf("after abort: motion coord=%d teleop=%d, want teleop", coord, teleop)
	}
}

// Estop out of an MDI tears it down with motion disabled; the mode goes back
// to Manual there and machine on puts motion into teleop again (motion keeps
// its coordinated flag across disable/enable).
func TestEstopDuringMDI_MachineOnReturnsToTeleop(t *testing.T) {
	task, mot := interruptedMDIFromManual(t)

	if err := task.SetState(int32(StateEstop)); err != nil {
		t.Fatalf("estop: %v", err)
	}
	task.mu.Lock()
	mode, tx := task.mode, task.modeTx
	task.mu.Unlock()
	if mode != ModeManual || tx {
		t.Fatalf("after estop: mode %s modeTx %v, want Manual and no pending restore", mode, tx)
	}

	_ = mot.setMode("SetCoord", 1, 0)
	bringUp(t, task)
	if coord, teleop := mot.mode(); coord != 0 || teleop != 1 {
		t.Fatalf("after machine on: motion coord=%d teleop=%d, want teleop", coord, teleop)
	}
}
