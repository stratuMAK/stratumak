// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcstat"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
)

// richMockStatus returns a MotionStatus with realistic data for testing GetStat.
type richMockStatus struct {
	status motstat.MotionStatus
}

func (m *richMockStatus) GetStatus() (motstat.MotionStatus, error) { return m.status, nil }
func (m *richMockStatus) GetPosCmd() (motstat.Pose, error)         { return m.status.CartePosCmd, nil }
func (m *richMockStatus) GetPosFb() (motstat.Pose, error)          { return m.status.CartePosFb, nil }
func (m *richMockStatus) GetInpos() (int32, error)                 { return m.status.Inpos, nil }
func (m *richMockStatus) GetExecId() (int32, error)                { return int32(m.status.Id), nil }
func (m *richMockStatus) GetQueueDepth() (int32, error)            { return m.status.QueueDepth, nil }
func (m *richMockStatus) GetCommandNumEcho() (int32, error)        { return m.status.CommandNumEcho, nil }
func (m *richMockStatus) GetCommandStatus() (int32, error)         { return m.status.CommandStatus, nil }
func (m *richMockStatus) GetSynchDi(i int32) (int32, error) {
	if i >= 0 && int(i) < len(m.status.SynchDi) {
		return m.status.SynchDi[i], nil
	}
	return 0, nil
}
func (m *richMockStatus) GetAnalogInput(i int32) (float64, error) {
	if i >= 0 && int(i) < len(m.status.AnalogInput) {
		return m.status.AnalogInput[i], nil
	}
	return 0, nil
}

func newRichTestTask() (*Task, *richMockStatus) {
	ms := &richMockStatus{
		status: motstat.MotionStatus{
			Enabled:      1,
			Inpos:        1,
			Paused:       0,
			FeedScale:    1.0,
			RapidScale:   0.5,
			LimitVel:     100.0,
			CurrentVel:   42.5,
			DistanceToGo: 12.3,
			Id:           7,
			MotionType:   2,
			Dtg:          motstat.Pose{X: 1.0, Y: 2.0, Z: 3.0},
			CartePosCmd:  motstat.Pose{X: 10, Y: 20, Z: 30, A: 1, B: 2, C: 3},
			CartePosFb:   motstat.Pose{X: 10.1, Y: 20.1, Z: 30.1},
			ToolOffset:   motstat.Pose{X: 0, Y: 0, Z: 50.0},
			KinType:      1, // IDENTITY
			Joints: [16]motstat.JointStatus{
				{Homed: 1, Enabled: 1, PosFb: 10.1, VelCmd: 5.0, MinPosLimit: -100, MaxPosLimit: 100},
				{Homed: 1, Enabled: 1, PosFb: 20.1, VelCmd: 3.0, MinPosLimit: -200, MaxPosLimit: 200},
				{Homed: 0, Enabled: 1, PosFb: 30.1, VelCmd: 1.0, MinPosLimit: -50, MaxPosLimit: 50, OnPosLimit: 1},
			},
			Spindles: [8]motstat.SpindleStatus{
				// State=1 => running. stratuMAK derives Enabled from this explicit
				// motion state field, unlike C++ which infers enabled=speed!=0.
				{Speed: 1000, Direction: 1, State: 1, Brake: 0, Homed: 1, Scale: 1.0},
			},
			// Velocity (commanded teleop vel) is deliberately different from
			// VelLimit so the stat mapping can't accidentally report the limit.
			Axes: [9]motstat.AxisStatus{
				{Velocity: 12.5, MinPosLimit: -100, MaxPosLimit: 100, VelLimit: 50},
				{Velocity: 7.0, MinPosLimit: -200, MaxPosLimit: 200, VelLimit: 50},
				{Velocity: 3.5, MinPosLimit: -50, MaxPosLimit: 50, VelLimit: 25},
			},
			Probe: motstat.ProbeStatus{
				Pos: motstat.Pose{X: 5, Y: 6, Z: 7},
			},
		},
	}

	mot := &mockMotion{}
	io := &mockIO{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t := NewTask(mot, io, ms, logger)
	// Production always wires the IO status reader; without it SetState(EstopReset)
	// cannot confirm estop-off (GetIOFullStatus) and the machine stays in ESTOP.
	t.SetIOStatusReader(io)

	// Configure task state as if it started up.
	t.numJoints = 3
	t.axisMask = 7 // XYZ
	t.linearUnits = 1.0
	t.numSpindles = 1

	return t, ms
}

func TestGetStat_TaskState(t *testing.T) {
	task, _ := newRichTestTask()
	if err := task.SetState(int32(StateEstopReset)); err != nil {
		t.Fatalf("SetState(EstopReset): %v", err)
	}
	if err := task.SetState(int32(StateOn)); err != nil {
		t.Fatalf("SetState(On): %v", err)
	}
	if err := task.SetMode(int32(ModeMDI)); err != nil {
		t.Fatalf("SetMode(MDI): %v", err)
	}

	stat := task.BuildStat()

	if stat.Task.State != emcstat.TaskState_ON {
		t.Errorf("Task.State = %d, want ON(%d)", stat.Task.State, emcstat.TaskState_ON)
	}
	if stat.Task.Mode != emcstat.TaskMode_MDI {
		t.Errorf("Task.Mode = %d, want MDI(%d)", stat.Task.Mode, emcstat.TaskMode_MDI)
	}
	if stat.Task.InterpState != emcstat.InterpState_IDLE {
		t.Errorf("Task.InterpState = %d, want IDLE", stat.Task.InterpState)
	}
}

func TestGetStat_MotionFields(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	stat := task.BuildStat()

	if !stat.Motion.Enabled {
		t.Error("Motion.Enabled should be true")
	}
	if stat.Motion.Feedrate != 1.0 {
		t.Errorf("Motion.Feedrate = %f, want 1.0", stat.Motion.Feedrate)
	}
	if stat.Motion.Rapidrate != 0.5 {
		t.Errorf("Motion.Rapidrate = %f, want 0.5", stat.Motion.Rapidrate)
	}
	if stat.Motion.CurrentVel != 42.5 {
		t.Errorf("Motion.CurrentVel = %f, want 42.5", stat.Motion.CurrentVel)
	}
	if stat.Motion.DistanceToGo != 12.3 {
		t.Errorf("Motion.DistanceToGo = %f, want 12.3", stat.Motion.DistanceToGo)
	}
	if stat.Motion.Dtg.X != 1.0 || stat.Motion.Dtg.Y != 2.0 || stat.Motion.Dtg.Z != 3.0 {
		t.Errorf("Motion.Dtg = %+v, want {1,2,3}", stat.Motion.Dtg)
	}
	if stat.Motion.MotionId != 7 {
		t.Errorf("Motion.MotionId = %d, want 7", stat.Motion.MotionId)
	}
	if stat.Motion.MotionType != 2 {
		t.Errorf("Motion.MotionType = %d, want 2", stat.Motion.MotionType)
	}
}

func TestGetStat_Positions(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	// Tool offset is reported from the canon (task) side, not motion status —
	// stratuMAK folds it into coordinate math and never sends it to motion. Set it
	// distinct from the motion mock's ms.ToolOffset (Z=50) so the assertion
	// below proves the canon source rather than the (now unused) motion echo.
	task.canon.state.toolOffset = Pose{Z: 42.0}
	task.canonSnap = *task.canon.state

	stat := task.BuildStat()

	// Position = commanded cartesian
	if stat.Position.X != 10 || stat.Position.Y != 20 || stat.Position.Z != 30 {
		t.Errorf("Position = %+v, want {10,20,30,...}", stat.Position)
	}
	// ActualPosition = feedback cartesian
	if stat.ActualPosition.X != 10.1 || stat.ActualPosition.Y != 20.1 || stat.ActualPosition.Z != 30.1 {
		t.Errorf("ActualPosition = %+v, want {10.1,20.1,30.1,...}", stat.ActualPosition)
	}
	// ToolOffset (from canon, not the motion mock's ms.ToolOffset=50)
	if stat.ToolOffset.Z != 42.0 {
		t.Errorf("ToolOffset.Z = %f, want 42 (canon source, not motion's 50)", stat.ToolOffset.Z)
	}
	// ProbedPosition
	if stat.ProbedPosition.X != 5 || stat.ProbedPosition.Y != 6 || stat.ProbedPosition.Z != 7 {
		t.Errorf("ProbedPosition = %+v, want {5,6,7,...}", stat.ProbedPosition)
	}
}

func TestGetStat_Joints(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	stat := task.BuildStat()

	if stat.JointsCount != 3 {
		t.Fatalf("JointsCount = %d, want 3", stat.JointsCount)
	}
	// Joints are emitted at full motion length (EMCMOT_MAX_JOINTS = 16), indexed
	// by joint number; JointsCount bounds the configured set.
	if len(stat.Joints) != 16 {
		t.Fatalf("len(Joints) = %d, want 16", len(stat.Joints))
	}

	j0 := stat.Joints[0]
	if !j0.Homed {
		t.Error("Joint[0].Homed should be true")
	}
	if !j0.Enabled {
		t.Error("Joint[0].Enabled should be true")
	}
	// Real min/max position limits go in the dedicated position-limit fields
	// (matching C++ taskintf.cc:935-958); the soft-limit fields are boolean
	// "currently tripped" flags, cleared here (joint is within limits).
	if j0.MinPositionLimit != -100 {
		t.Errorf("Joint[0].MinPositionLimit = %f, want -100", j0.MinPositionLimit)
	}
	if j0.MaxPositionLimit != 100 {
		t.Errorf("Joint[0].MaxPositionLimit = %f, want 100", j0.MaxPositionLimit)
	}
	if j0.MinSoftLimit || j0.MaxSoftLimit {
		t.Errorf("Joint[0].Min/MaxSoftLimit = %v/%v, want false/false", j0.MinSoftLimit, j0.MaxSoftLimit)
	}
	if j0.Velocity != 5.0 {
		t.Errorf("Joint[0].Velocity = %f, want 5.0", j0.Velocity)
	}
	if j0.Input != 10.1 {
		t.Errorf("Joint[0].Input = %f, want 10.1 (pos_fb)", j0.Input)
	}

	// Joint 2 is on positive limit
	j2 := stat.Joints[2]
	if j2.Homed {
		t.Error("Joint[2].Homed should be false")
	}
	if !j2.MaxHardLimit {
		t.Error("Joint[2].MaxHardLimit should be true (on_pos_limit=1)")
	}

	// Homed array
	if !stat.Homed[0] || !stat.Homed[1] || stat.Homed[2] {
		t.Errorf("Homed = %v, want [true,true,false,...]", stat.Homed[:3])
	}
}

func TestGetStat_Axes(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	stat := task.BuildStat()

	if stat.AxisMask != 7 {
		t.Errorf("AxisMask = %d, want 7", stat.AxisMask)
	}
	// Axes are emitted at full motion length (EMC_AXIS_MAX = 9), indexed by axis
	// number; axis_mask bounds the configured set.
	if len(stat.Axis) != 9 {
		t.Fatalf("len(Axis) = %d, want 9", len(stat.Axis))
	}

	ax0 := stat.Axis[0]
	if ax0.MinPositionLimit != -100 {
		t.Errorf("Axis[0].MinPositionLimit = %f, want -100", ax0.MinPositionLimit)
	}
	if ax0.MaxPositionLimit != 100 {
		t.Errorf("Axis[0].MaxPositionLimit = %f, want 100", ax0.MaxPositionLimit)
	}
	// Velocity must be the commanded teleop velocity, not the static VelLimit
	// (which is 50 here). Regression guard for the C++-parity fix.
	if ax0.Velocity != 12.5 {
		t.Errorf("Axis[0].Velocity = %f, want 12.5 (teleop vel, not vel_limit 50)", ax0.Velocity)
	}
	// Assert a second, distinct axis too, so an index/off-by-one bug can't hide.
	if ax2 := stat.Axis[2]; ax2.Velocity != 3.5 {
		t.Errorf("Axis[2].Velocity = %f, want 3.5", ax2.Velocity)
	}
}

func TestGetStat_Spindle(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	stat := task.BuildStat()

	if len(stat.Spindle) != 1 {
		t.Fatalf("len(Spindle) = %d, want 1", len(stat.Spindle))
	}
	sp := stat.Spindle[0]
	if sp.Speed != 1000 {
		t.Errorf("Spindle[0].Speed = %f, want 1000", sp.Speed)
	}
	if sp.Direction != 1 {
		t.Errorf("Spindle[0].Direction = %d, want 1", sp.Direction)
	}
	// DIVERGENCE (intentional, arguably better): Enabled comes from the explicit
	// motion spindle State field (set by M3/M4/M5 in command.c), not the C++
	// enabled=speed!=0 heuristic (taskintf.cc:1987).
	if !sp.Enabled {
		t.Error("Spindle[0].Enabled should be true (State=1)")
	}
	// Homed asserts the motstat->stat plumbing only: the motion module currently
	// hard-codes spindle homed=0 (motstat_handlers.c: "spindle home status not
	// in spindle_status_t"), so in production this field is always false today.
	if !sp.Homed {
		t.Error("Spindle[0].Homed plumbing should carry the mock value")
	}
}

func TestGetStat_ScalarFields(t *testing.T) {
	task, _ := newRichTestTask()
	bringUp(t, task)

	stat := task.BuildStat()

	if stat.KinematicsType != emcstat.KinematicsType_IDENTITY {
		t.Errorf("KinematicsType = %d, want IDENTITY(1)", stat.KinematicsType)
	}
	if stat.LinearUnits != 1.0 {
		t.Errorf("LinearUnits = %f, want 1.0", stat.LinearUnits)
	}
}

func TestGetStat_NilTask(t *testing.T) {
	// milltaskModule with nil task returns safe defaults.
	m := &milltaskModule{}
	stat, err := m.GetStat()
	if err != nil {
		t.Fatal(err)
	}
	if stat.Task.State != emcstat.TaskState_ESTOP {
		t.Errorf("nil task: State = %d, want ESTOP", stat.Task.State)
	}
}

// The canon's endPoint is the origin of the next commanded move, so it must be
// seeded from the commanded Cartesian pose and not from the feedback pose.
//
// Sourcing it from feedback made every re-synced segment carry the standing
// following error of every axis as a spurious displacement.  For a pure rotary
// move that is fatal rather than cosmetic: pmLine9Target takes the first
// non-zero component as the segment length, pmCartLineInit calls a delta
// non-zero above CART_FUZZ (1e-8), and so a few microns of XYZ error became the
// length of a thirty-degree A move.  The planner covered those microns in two
// servo cycles and emitted the endpoint, and the joint chased a full-travel
// step into a following error.
//
// The fixture's CartePosCmd and CartePosFb differ, so this distinguishes the
// two rather than passing on either.
func TestSyncEndPointUsesCommandedPose(t *testing.T) {
	task, ms := newRichTestTask()
	if task.canon == nil {
		t.Fatal("test task has no canon")
	}

	task.canon.syncEndPointFromMachine()
	got := task.canon.state.endPoint
	cmd := ms.status.CartePosCmd
	fb := ms.status.CartePosFb

	if got.X != cmd.X || got.Y != cmd.Y || got.Z != cmd.Z ||
		got.A != cmd.A || got.B != cmd.B || got.C != cmd.C {
		t.Errorf("endPoint = (%g,%g,%g,%g,%g,%g), want CartePosCmd (%g,%g,%g,%g,%g,%g)",
			got.X, got.Y, got.Z, got.A, got.B, got.C,
			cmd.X, cmd.Y, cmd.Z, cmd.A, cmd.B, cmd.C)
	}
	if got.X == fb.X && got.Y == fb.Y && got.Z == fb.Z {
		t.Errorf("endPoint took CartePosFb (%g,%g,%g)", fb.X, fb.Y, fb.Z)
	}
}

// The [DISPLAY] override ceilings must bind every client, not only a UI that
// sizes its sliders from the same keys.
//
// Motion clamps these at 0 and nothing else, and 2.9's ceilings lived in
// halui — a separate process there, gone in the port. So an incremental input
// (an encoder wired to halui.feed-override.counts or
// halui.spindle.0.override.counts) walked straight past MAX_FEED_OVERRIDE and
// the spindle window: the value simply kept climbing, since the increment path
// reads the current value back from the value pin rather than accumulating
// privately. Clamped silently, as 2.9 did — an encoder parked at the end of
// its travel should not fill the message area.
func TestOverrideSettersClampToConfiguredLimits(t *testing.T) {
	task, _ := newRichTestTask()
	mot := task.motion.(*mockMotion)

	// Stand in for the [DISPLAY] keys the machine that found this uses.
	task.maxFeedOverride = 2.0
	task.minSpindleOverride = 0.5
	task.maxSpindleOverride = 1.5
	task.maxVelocity = 700.0
	task.numSpindles = 1

	for _, c := range []struct {
		name string
		set  func()
		pick func() float64
		want float64
	}{
		{"feed over ceiling", func() { _ = task.SetFeedOverride(5.0) },
			func() float64 { f, _, _, _ := mot.scales(); return f }, 2.0},
		{"feed under floor", func() { _ = task.SetFeedOverride(-1.0) },
			func() float64 { f, _, _, _ := mot.scales(); return f }, 0.0},
		{"feed inside", func() { _ = task.SetFeedOverride(1.25) },
			func() float64 { f, _, _, _ := mot.scales(); return f }, 1.25},
		{"rapid over ceiling", func() { _ = task.SetRapidOverride(3.0) },
			func() float64 { _, r, _, _ := mot.scales(); return r }, 1.0},
		{"spindle over ceiling", func() { _ = task.SetSpindleOverride(9.0, 0) },
			func() float64 { _, _, sp, _ := mot.scales(); return sp }, 1.5},
		{"spindle under floor", func() { _ = task.SetSpindleOverride(0.1, 0) },
			func() float64 { _, _, sp, _ := mot.scales(); return sp }, 0.5},
		{"spindle inside", func() { _ = task.SetSpindleOverride(1.2, 0) },
			func() float64 { _, _, sp, _ := mot.scales(); return sp }, 1.2},
		{"max velocity over ceiling", func() { _ = task.SetMaxVelocity(5000.0) },
			func() float64 { _, _, _, v := mot.scales(); return v }, 700.0},
	} {
		c.set()
		if got := c.pick(); got != c.want {
			t.Errorf("%s: motion got %g, want %g", c.name, got, c.want)
		}
	}

	// An unnamed ceiling must not clamp everything to zero: clampRange treats
	// a non-positive hi as "unconfigured".
	task.maxFeedOverride = 0
	_ = task.SetFeedOverride(1.7)
	if f, _, _, _ := mot.scales(); f != 1.7 {
		t.Errorf("unconfigured ceiling: motion got %g, want 1.7 (unclamped)", f)
	}
}
