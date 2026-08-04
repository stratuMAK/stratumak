// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcstat"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
)

// BuildStat constructs a complete StatFull from task state + motion status.
// This is the single source of truth for all stat consumers (REST, WS, halui).
func (t *Task) BuildStat() *emcstat.StatFull {
	t.mu.Lock()
	// Active G/M codes and call level come from the caches published by
	// updateActiveCodes after each interpreter execute. BuildStat must NOT
	// call into the interpreter: the producer goroutine may be executing it
	// concurrently and the C++ interp is not thread-safe. (The C milltask
	// could re-query every cycle only because it was single-threaded.)
	callLevel := t.callLevel
	// Bump the liveness counter once per status build (mirrors NML heartbeat).
	t.heartbeat++
	heartbeat := t.heartbeat
	// Canon state snapshot for offset reporting (value copy — the live
	// t.canon.state is mutated lock-free by the producer goroutine).
	cs := t.canonSnap
	// Remaining G4 dwell time (only meaningful while waiting for the delay).
	var delayLeft float64
	if t.execState == ExecWaitingForDelay {
		if d := time.Until(t.dwellEnd); d > 0 {
			delayLeft = d.Seconds()
		}
	}
	// Compute RCS command status (matches NML stat.state: 1=DONE,2=EXEC,3=ERROR).
	var rcsStatus int32 // RCS command status (1=DONE, 2=EXEC, 3=ERROR)
	switch t.execState {
	case ExecError:
		rcsStatus = 3 // RCS_ERROR
	case ExecDone:
		rcsStatus = 1 // RCS_DONE
	default:
		rcsStatus = 2 // RCS_EXEC (any waiting state)
	}
	stat := &emcstat.StatFull{
		Task: emcstat.StatTaskInfo{
			Mode:              emcstat.TaskMode(t.mode),
			State:             emcstat.TaskState(t.state),
			InterpState:       emcstat.InterpState(t.interpState),
			ExecState:         emcstat.ExecState(t.execState),
			File:              t.programFile,
			SourceFile:        t.sourceFile,
			Filtering:         t.filtering,
			FilterProgress:    t.filterProgress,
			OptionalStop:      t.optionalStop,
			BlockDelete:       t.blockDelete,
			TaskPaused:        t.interpState == InterpPaused,
			G5xIndex:          cs.g5xIndex,
			QueuedMdiCommands: int32(len(t.mdiQueue)),
			ReadLine:          t.readLine,
			CurrentLine:       t.currentLine,
			Line:              t.currentLine,
			ProgramUnits:      cs.lengthUnits,
			DelayLeft:         delayLeft,
			Command:           t.taskCommand,
			CallLevel:         callLevel,
			InputTimeout:      t.inputTimeout,
		},
		Flood:          t.floodOn,
		Mist:           t.mistOn,
		JointsCount:    int32(t.numJoints),
		AxisMask:       t.axisMask,
		LinearUnits:    t.linearUnits,
		State:          rcsStatus,
		RcsStatus:      rcsStatus,
		Debug:          t.debug,
		KinematicsType: emcstat.KinematicsType_IDENTITY,
		JogAxis:        t.jogAxis,
		JogIncrement:   t.jogIncrement,
		JogSpeed:       t.jogSpeed,
		AjogSpeed:      t.ajogSpeed,
		// Readahead codes as the fallback. Aliased, not copied: updateActiveCodes
		// always reassigns these fields to fresh interp slices (never mutates in
		// place), so the value is immutable and safe to share into the snapshot.
		// Overwritten below by the executing segment's tag when it has one.
		ActiveGcodes:   t.activeGcodes,
		ActiveMcodes:   t.activeMcodes,
		ActiveSettings: t.activeSettings,
		G5xOffset: emcstat.Position{
			X: cs.g5xOffset.X, Y: cs.g5xOffset.Y, Z: cs.g5xOffset.Z,
			A: cs.g5xOffset.A, B: cs.g5xOffset.B, C: cs.g5xOffset.C,
			U: cs.g5xOffset.U, V: cs.g5xOffset.V, W: cs.g5xOffset.W,
		},
		G92Offset: emcstat.Position{
			X: cs.g92Offset.X, Y: cs.g92Offset.Y, Z: cs.g92Offset.Z,
			A: cs.g92Offset.A, B: cs.g92Offset.B, C: cs.g92Offset.C,
			U: cs.g92Offset.U, V: cs.g92Offset.V, W: cs.g92Offset.W,
		},
		RotationXy: cs.xyRotation,
		PreviewSeq: t.previewSeq,
		BootId:     t.bootID,
		Heartbeat:  heartbeat,
		// Config-derived scalars (task-side; not a motion echo).
		AngularUnits:    t.angularUnits,
		MaxAcceleration: t.maxAcceleration,
		Estop:           boolToI32(t.state == StateEstop),
	}
	numJoints := t.numJoints
	numSpindles := t.numSpindles
	linearUnits := t.linearUnits
	angularUnits := t.angularUnits
	jointLinear := t.jointLinear
	t.mu.Unlock()

	// Spindle slice is allocated here; joints/axes are allocated in their loops
	// below (they're emitted as full fixed-length arrays indexed by
	// joint/axis number, once the motion status is available).
	if numSpindles > 0 {
		stat.Spindle = make([]emcstat.SpindleInfo, numSpindles)
	}

	// Read motion status (lock-free triple buffer, never fails).
	// The fallback cache is shared by concurrent BuildStat callers (WS push
	// loops, poslog, C stat callers) and must be accessed under mu.
	ms, err := t.status.GetStatus()
	t.mu.Lock()
	if err != nil {
		// Should not happen with triple buffer, but handle gracefully.
		if !t.hasMotionStatus {
			t.mu.Unlock()
			return stat
		}
		ms = t.lastMotionStatus
	} else {
		t.lastMotionStatus = ms
		t.hasMotionStatus = true
	}
	t.mu.Unlock()

	// Kinematics type from motion module.
	stat.KinematicsType = emcstat.KinematicsType(ms.KinType)

	// Motion info.
	switch {
	case ms.Coord != 0:
		stat.Motion.Mode = emcstat.TrajMode_COORD
	case ms.Teleop != 0:
		stat.Motion.Mode = emcstat.TrajMode_TELEOP
	default:
		stat.Motion.Mode = emcstat.TrajMode_FREE
	}
	stat.Motion.Enabled = ms.Enabled != 0
	stat.Motion.InPosition = ms.Inpos != 0
	stat.Motion.Paused = ms.Paused != 0
	stat.Motion.Feedrate = ms.FeedScale
	stat.Motion.Rapidrate = ms.RapidScale
	stat.Motion.MaxVelocity = ms.LimitVel
	stat.Motion.Velocity = ms.RequestedVel
	stat.Motion.CurrentVel = ms.CurrentVel
	stat.Motion.DistanceToGo = ms.DistanceToGo
	stat.Motion.MotionId = ms.Id
	// One locked lookup (+ prune) for both the line and the executing segment's
	// state tag, reused below instead of three separate map acquisitions (E2).
	info, tagged := t.motionInfoAndPrune(ms.Id)
	motionLine := int32(0)
	// The file that line belongs to. An o-word call into a separate file
	// restarts the interpreter's line numbering, so motionLine alone does not
	// identify a program location — a UI that highlights it without checking
	// the file lights up an unrelated line of the loaded program.
	motionFile := ""
	if tagged {
		motionLine = info.LineNo
		motionFile = info.File
	}
	stat.Motion.MotionLine = motionLine
	stat.Motion.MotionType = ms.MotionType
	stat.Motion.FeedOverrideEnabled = ms.FeedScaleEnabled != 0
	stat.Motion.AdaptiveFeedEnabled = ms.AdaptiveFeedEnabled != 0
	stat.Motion.FeedHoldEnabled = ms.FeedHoldEnabled != 0
	stat.Motion.Queue = ms.QueueDepth
	stat.Motion.QueueFull = ms.QueueFull != 0
	stat.Task.MotionLine = motionLine
	stat.Task.MotionFile = motionFile

	// Traj-level scalars and motion I/O (serialized from the motion status).
	stat.CycleTime = ms.TrajCycleTime
	stat.Acceleration = ms.Acc
	stat.ActiveQueue = ms.ActiveDepth
	stat.ProbeVal = ms.Probe.Val
	stat.ProbeTripped = ms.Probe.Tripped != 0
	stat.Probing = ms.Probe.Probing != 0
	stat.Ain = ms.AnalogInput
	stat.Aout = ms.AnalogOutput
	stat.Din = ms.SynchDi
	stat.Dout = ms.SynchDo
	// Resolve the active G/M codes and current line from the state tag of the
	// segment actually executing (motion echoes only the id back). This makes
	// status reflect what the machine is running now rather than the
	// interpreter's readahead. Both the AUTO read loop and executeMDI tag the
	// segments they queue, so this covers program and MDI moves alike; it falls
	// back to the readahead codes set above only when nothing tagged is executing
	// (idle, or the brief window before a just-queued move's tag lands).
	// Tag slices are isolated at tag time (fresh interp slices), so alias them.
	if tagged && info.Gcodes != nil {
		stat.ActiveGcodes = info.Gcodes
		stat.ActiveMcodes = info.Mcodes
		stat.ActiveSettings = info.Settings
		stat.Task.CurrentLine = info.LineNo
		stat.Task.Line = info.LineNo
	}
	stat.Motion.Dtg = emcstat.Position{
		X: ms.Dtg.X, Y: ms.Dtg.Y, Z: ms.Dtg.Z,
		A: ms.Dtg.A, B: ms.Dtg.B, C: ms.Dtg.C,
		U: ms.Dtg.U, V: ms.Dtg.V, W: ms.Dtg.W,
	}

	// Positions.
	stat.Position = poseToPosition(ms.CartePosCmd)
	stat.ActualPosition = poseToPosition(ms.CartePosFb)
	stat.ProbedPosition = poseToPosition(ms.Probe.Pos)
	// Tool offset comes from the canon (task) side: stratuMAK folds it into the
	// coordinate math (toAbsolute) and never sends it to motion, so
	// ms.ToolOffset is always zero. This matches C++, which reports
	// task.toolOffset from the SET_OFFSET command (emctaskmain.cc:1889), not a
	// motion echo — and is consistent with G5x/G92 above (also from cs).
	stat.ToolOffset = emcstat.Position{
		X: cs.toolOffset.X, Y: cs.toolOffset.Y, Z: cs.toolOffset.Z,
		A: cs.toolOffset.A, B: cs.toolOffset.B, C: cs.toolOffset.C,
		U: cs.toolOffset.U, V: cs.toolOffset.V, W: cs.toolOffset.W,
	}

	// Tool info from IO controller — one status read for all three fields.
	if t.io != nil {
		if tis, pp, tfp, err := t.io.GetToolStatus(); err == nil {
			stat.ToolInSpindle = tis
			// io already tracks the prepped tool by SLOT (idx), which is
			// exactly what classic stat.pocket_prepped reported — a
			// subscript into stat.tool_table. Straight through.
			stat.PocketPrepped = pp
			stat.ToolFromPocket = tfp
		}
	}

	// Joints array — emitted at full motion length (EMCMOT_MAX_JOINTS), indexed
	// by joint number. Configured joints (i < numJoints) carry live motion state
	// plus task-side config (units/type); joints beyond that report motion's
	// unconfigured defaults so any joint number is addressable. joints_count
	// bounds the configured set.
	stat.Joints = make([]emcstat.JointInfo, len(ms.Joints))
	for i := range ms.Joints {
		j := &ms.Joints[i]
		// Per-joint config (task side). Unconfigured joints default to a linear
		// joint with unit scale (matches classic emcmot joint defaults).
		jt := int32(1) // EMC_JOINT_LINEAR
		units := 1.0
		minPos, maxPos := j.MinPosLimit, j.MaxPosLimit
		minFe, maxFe := j.MinFerror, j.MaxFerror
		inpos := j.Inpos != 0
		if i < numJoints {
			if jointLinear[i] {
				units = linearUnits
			} else {
				jt = 2 // EMC_JOINT_ANGULAR
				units = angularUnits
			}
		} else {
			// stratuMAK motion leaves joints beyond numJoints zeroed; report the
			// classic emcmot unconfigured-joint defaults so parity clients see
			// the same (position limits +/-1, following-error limits 1, and
			// in-position true since an unconfigured joint never moves).
			minPos, maxPos = -1.0, 1.0
			minFe, maxFe = 1.0, 1.0
			inpos = true
		}
		stat.Joints[i] = emcstat.JointInfo{
			Homed:   j.Homed != 0,
			Homing:  j.Homing != 0,
			Enabled: j.Enabled != 0,
			Fault:   j.Fault != 0,
			Inpos:   inpos,
			// stratuMAK motion exposes no per-joint soft-limit-tripped flag; report
			// the hard-limit switches and leave the soft-limit flags cleared.
			MinSoftLimit: false,
			MaxSoftLimit: false,
			MinHardLimit: j.OnNegLimit != 0,
			MaxHardLimit: j.OnPosLimit != 0,
			// A non-zero override mask means limit checking is currently
			// overridden; report it on every joint (matches 2.9 taskintf.cc,
			// which UIs read via joint[0] as a global indicator).
			OverrideLimits:   ms.OverrideLimitMask != 0,
			JointType:        jt,
			Units:            units,
			Backlash:         0.0, // stratuMAK has no backlash compensation
			MinPositionLimit: minPos,
			MaxPositionLimit: maxPos,
			MinFerror:        minFe,
			MaxFerror:        maxFe,
			FerrorCurrent:    j.Ferror,
			FerrorHighmark:   j.FerrorHighMark,
			Velocity:         j.VelCmd,
			Input:            j.PosFb,
			Output:           j.PosCmd,
		}
		if i < len(stat.JointActualPosition) {
			stat.Homed[i] = j.Homed != 0
			stat.JointActualPosition[i] = j.PosFb
			stat.JointPosition[i] = j.PosCmd
			// Classic linuxcnc.stat().limit[j] is a bitmask, not a direction:
			// minHardLimit=1, maxHardLimit=2, minSoftLimit=4, maxSoftLimit=8.
			// stratuMAK motion exposes only the hard-limit switches (OnNegLimit is
			// the min/negative switch, OnPosLimit the max/positive switch); the
			// soft-limit bits stay clear, matching MinSoftLimit/MaxSoftLimit above.
			var mask int32
			if j.OnNegLimit != 0 {
				mask |= 1 // min hard limit
			}
			if j.OnPosLimit != 0 {
				mask |= 2 // max hard limit
			}
			stat.Limit[i] = mask
		}
	}

	// Axes array — emitted at full motion length (EMC_AXIS_MAX), indexed by axis
	// number (0=X..8=W). Unconfigured axes carry motion's zeroed defaults.
	stat.Axis = make([]emcstat.AxisInfo, len(ms.Axes))
	for i := range ms.Axes {
		ax := &ms.Axes[i]
		stat.Axis[i] = emcstat.AxisInfo{
			MinPositionLimit: ax.MinPosLimit,
			MaxPositionLimit: ax.MaxPosLimit,
			// Commanded teleop velocity, matching C++ axis->teleop_vel_cmd
			// (taskintf.cc:574) — NOT the static vel_limit.
			Velocity: ax.Velocity,
		}
	}

	// Spindles.
	for i := 0; i < numSpindles && i < 8; i++ {
		sp := &ms.Spindles[i]
		stat.Spindle[i] = emcstat.SpindleInfo{
			Speed:           sp.Speed,
			Direction:       sp.Direction,
			Brake:           sp.Brake != 0,
			Enabled:         sp.State != 0,
			Override:        sp.Scale,
			OverrideEnabled: ms.SpindleScaleEnabled != 0,
			Homed:           sp.Homed != 0,
			OrientState:     sp.OrientState,
			OrientFault:     sp.OrientFault,
		}
	}

	return stat
}

// poseToPosition converts a motstat.Pose to emcstat.Position.
func poseToPosition(p motstat.Pose) emcstat.Position {
	return emcstat.Position{
		X: p.X, Y: p.Y, Z: p.Z,
		A: p.A, B: p.B, C: p.C,
		U: p.U, V: p.V, W: p.W,
	}
}

// boolToI32 maps a bool to the 0/1 int form used by several NML-parity status
// fields (e.g. estop) that classic linuxcnc.stat() exposes as ints.
func boolToI32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
