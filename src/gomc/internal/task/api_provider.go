// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"fmt"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/emccmd"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/emcstat"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// Compile-time interface checks.
var _ emccmd.EmccmdCallbacks = (*milltaskModule)(nil)
var _ emcstat.EmcstatCallbacks = (*milltaskModule)(nil)

// errNotReady is returned by command handlers before Start() completes.
var errNotReady = apiserver.NewFault(apiserver.FaultNotReady,
	fmt.Errorf("milltask: not ready"))

// RCS status codes returned to C callers (halui, etc.)
// The C milltask returns RCS_DONE(1) on success; match that for compatibility.
const (
	rcsDone  int32 = 1 // RCS_DONE: command completed
	rcsExec  int32 = 2 // RCS_EXEC: command accepted, executing
	rcsError int32 = 3 // RCS_ERROR: command rejected
)

func (m *milltaskModule) ready() error {
	if m.task == nil || m.stopped.Load() {
		return errNotReady
	}
	return nil
}

// --- EmccmdCallbacks implementation ---

func (m *milltaskModule) SetState(state int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetState(state))
}

func (m *milltaskModule) SetMode(mode int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetMode(mode))
}

func (m *milltaskModule) AutoCmd(cmd emccmd.AutoCmd, line int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.AutoCommand(int32(cmd), line))
}

// rcFor maps a task result onto the (rc, error) pair the emccmd contract wants.
//
// A command the task refused is a transport error — the request was invalid in
// the current state. A command that was accepted and then faulted reports
// RCS_ERROR with no transport error: it reached the machine, the fault is
// already on the error channel, and the caller needs to be able to read the
// state that resulted. See executedError.
func rcFor(err error) (int32, error) {
	if err == nil {
		return rcsDone, nil
	}
	var ee *executedError
	if errors.As(err, &ee) {
		return rcsError, nil
	}
	// A refusal is not a controller malfunction — the machine's state forbids
	// the request. Classify it so the transport does not report a working
	// controller as broken. An error that already carries a kind (errNotReady,
	// or anything a lower layer classified) keeps it.
	var f *apiserver.Fault
	if errors.As(err, &f) {
		return rcsError, err
	}
	return rcsError, apiserver.NewFault(apiserver.FaultState, err)
}

func (m *milltaskModule) Mdi(command string) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.MDI(command))
}

func (m *milltaskModule) Jog(jogType emccmd.JogType, jjogmode bool, axisOrJoint int32, velocity float64, distance float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Jog(int32(jogType), jjogmode, axisOrJoint, velocity, distance))
}

func (m *milltaskModule) JogStop(jjogmode bool, axisOrJoint int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.JogStop(jjogmode, axisOrJoint))
}

func (m *milltaskModule) Spindle(cmd emccmd.SpindleCmd, speed float64, spindleNum int32, wait int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Spindle(int32(cmd), speed, spindleNum, wait))
}

func (m *milltaskModule) Home(joint int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Home(joint))
}

func (m *milltaskModule) Unhome(joint int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Unhome(joint))
}

func (m *milltaskModule) OverrideLimits(joint int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.OverrideLimits(joint))
}

func (m *milltaskModule) TeleopEnable(enable bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.TeleopEnable(enable))
}

func (m *milltaskModule) SetFeedOverride(rate float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetFeedOverride(rate))
}

func (m *milltaskModule) SetSpindleOverride(rate float64, spindleNum int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetSpindleOverride(rate, spindleNum))
}

func (m *milltaskModule) SetRapidOverride(rate float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetRapidOverride(rate))
}

func (m *milltaskModule) SetMaxVelocity(velocity float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetMaxVelocity(velocity))
}

func (m *milltaskModule) SetFoEnable(enable bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetFeedOverrideEnable(enable))
}

func (m *milltaskModule) SetFhEnable(enable bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetFeedHoldEnable(enable))
}

func (m *milltaskModule) SetSoEnable(enable bool, spindleNum int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetSpindleOverrideEnable(enable, spindleNum))
}

func (m *milltaskModule) Flood(on bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Flood(on))
}

func (m *milltaskModule) Mist(on bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Mist(on))
}

func (m *milltaskModule) Brake(on bool, spindleNum int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Brake(on, spindleNum))
}

func (m *milltaskModule) Lube(on bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Lube(on))
}

func (m *milltaskModule) Abort() (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.Abort())
}

func (m *milltaskModule) TaskPlanSynch() (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.TaskPlanSynch())
}

func (m *milltaskModule) SetOptionalStop(on bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetOptionalStop(on))
}

func (m *milltaskModule) SetBlockDelete(on bool) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetBlockDelete(on))
}

func (m *milltaskModule) LoadToolTable(file string) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.LoadToolTable(file))
}

func (m *milltaskModule) ToolUnload() (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.ToolUnload())
}

func (m *milltaskModule) ProgramOpen(file string) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.ProgramOpen(file))
}

func (m *milltaskModule) WaitComplete(timeout float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	if timeout <= 0 {
		timeout = 5.0
	}
	// A wait that did not happen is a state fault, not a controller failure:
	// the machine never settled within the deadline.
	return rcFor(m.task.WaitComplete(timeout))
}

func (m *milltaskModule) SetDebug(debug int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetDebug(debug))
}

func (m *milltaskModule) SetJogAxis(axis int32) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetJogAxis(axis))
}

func (m *milltaskModule) SetJogIncrement(increment float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetJogIncrement(increment))
}

func (m *milltaskModule) SetJogSpeed(speed float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetJogSpeed(speed))
}

func (m *milltaskModule) SetAjogSpeed(speed float64) (int32, error) {
	if err := m.ready(); err != nil {
		return rcsError, err
	}
	return rcFor(m.task.SetAjogSpeed(speed))
}

// --- EmcstatCallbacks implementation ---

func (m *milltaskModule) GetStat() (*emcstat.StatFull, error) {
	t := m.task
	if t == nil {
		return &emcstat.StatFull{
			Task: emcstat.StatTaskInfo{
				State:       emcstat.TaskState_ESTOP,
				Mode:        emcstat.TaskMode_MANUAL,
				InterpState: emcstat.InterpState_IDLE,
				ExecState:   emcstat.ExecState_DONE,
			},
		}, nil
	}
	stat := t.BuildStat()
	return stat, nil
}

// GetInfo describes the machine: which peer modules serve this task, and which
// operator controls are actually wired.
//
// Unlike GetStat this refuses to answer before Start completes, rather than
// handing back a safe-looking default. Clients read /info ONCE and cache it, so
// an empty answer would permanently disable features that do exist — a loud
// not-ready is recoverable, a plausible wrong answer is not.
func (m *milltaskModule) GetInfo() (*emcstat.TaskInfo, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	caps, err := m.buildCaps()
	if err != nil {
		return nil, err
	}
	return &emcstat.TaskInfo{
		Peers: emcstat.TaskPeers{
			Tooltable:        m.ttInstance,
			Preview:          m.previewInstance,
			Manualtoolchange: m.mtcInstance,
			Pyvcp:            m.pyvcpInstance,
		},
		Caps: caps,
	}, nil
}
