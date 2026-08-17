// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// Motion constants shared with the C side — the single mirror lives in
// internal/motsetup, shared with milltask.
const (
	// motionTypeTraverse: every move this module makes is a rapid. There is no
	// programmed feed rate on a pick-and-place machine — the speeds come from
	// the INI and the axis limits.
	motionTypeTraverse = motsetup.MotionTypeTraverse

	// The TP termination conditions. Travel blends its corners parabolically
	// within BLEND_TOLERANCE; a Z stroke stops exactly, because a pick starts
	// and ends at a standstill.
	tpTermCondStop      = motsetup.TPTermCondStop
	tpTermCondParabolic = motsetup.TPTermCondParabolic

	// moveFuzz is the smallest displacement worth commanding, in mm (the
	// shared CART_FUZZ mirror): a "move" below it is numerical residue of a
	// coordinate computation and would cost a queue entry and a blend for no
	// motion.
	moveFuzz = motsetup.CartFuzz
)

var (
	// dispatchSettleTicks is how many cycles pass between dispatching motion and
	// the first in-position check. Motion status is published by the servo
	// thread, so immediately after a SetLine it still describes the machine
	// standing still — the same stale-inpos race milltask's waitMotionDone skips
	// past (sequencer.go).
	dispatchSettleTicks = 5
)

// ---------------------------------------------------------------------------
// Picker targeting (§8)
// ---------------------------------------------------------------------------

// pickerOffset is picker pk's XY offset from the machine position, as its params
// currently read. Read live rather than cached: the offsets are taught with
// halcmd setp (D3), and a job commanded after a correction has to use it.
func (c *control) pickerOffset(pk int) pnproute.Point {
	p := &c.m.pins.pickers[pk]
	return pnproute.Point{X: p.xOffset.Get(), Y: p.yOffset.Get()}
}

// The targeting convention of §8: command = target − offset, and the picker
// sits at commanded + offset. Everything taught — station coordinates, tray
// corners, the dead-zone drawings (D23) — lives in one frame, the frame a
// picker's position is expressed in; the conversion to machine coordinates
// happens inside travel() and nowhere else, from ONE offset snapshot per leg,
// so route planning and station geometry never have to know which picker is
// being moved and a mid-leg setp cannot warp a planned polyline.

// ---------------------------------------------------------------------------
// Commanded-position tracking
// ---------------------------------------------------------------------------

// seedCmdPos re-anchors the tracked commanded position from motion, once per job.
//
// Everything a job commands is tracked in cmdPos so that back-to-back travel
// lines can be built without waiting for status to catch up — a route's segments
// are dispatched into the TP queue precisely so the TP can blend them, and
// status still describes the first one while the last is being queued. But a
// manual jog moves the machine without going through this file, so the tracking
// has to be re-read from motion before a job trusts it.
func (c *control) seedCmdPos() error {
	if err := c.waitMotionDone(); err != nil {
		return err
	}
	pos, err := c.m.ms.GetPosCmd()
	if err != nil {
		return faultf(errMotionError, "reading the commanded position: %v", err)
	}
	c.cmdPos = toMotctlPose(pos)
	return nil
}

// toMotctlPose converts a status pose into a command pose. The two are the same
// nine numbers in two generated packages.
func toMotctlPose(p motstat.Pose) motctl.Pose {
	return motctl.Pose{X: p.X, Y: p.Y, Z: p.Z, A: p.A, B: p.B, C: p.C, U: p.U, V: p.V, W: p.W}
}

// ---------------------------------------------------------------------------
// Move dispatch
// ---------------------------------------------------------------------------

// line commands one straight move to an absolute pose at the requested per-axis
// velocity and acceleration, blended by moveLimits into the coordinated path
// values that keep every participating axis inside its own limit. A move
// that displaces nothing is dropped rather than queued; a move that displaces
// something but has no usable limits is a FAULT, never a silent drop — a
// silently skipped Z stroke runs the pick at travel height, grips nothing, and
// marks good slots empty one after another.
func (c *control) line(to motctl.Pose, velReq, accReq float64) error {
	if !isFinite(to.X) || !isFinite(to.Y) || !isFinite(to.Z) {
		// A NaN/Inf target reaches this point through the float input pins
		// (a z-offset wired to a broken sensor value, say); motion must never
		// see it, and NaN would also slip every comparison below.
		return faultf(errMotionError,
			"move target (%v, %v, %v) is not finite — check the z-offset and picker offset inputs",
			to.X, to.Y, to.Z)
	}
	if err := c.checkTargetInLimits(to); err != nil {
		return err
	}
	vel, acc, moved := c.moveLimits(c.cmdPos, to, velReq, accReq)
	if !moved {
		return nil
	}
	if vel <= 0 || acc <= 0 {
		return faultf(errMotionError,
			"the axis limits leave no usable velocity/acceleration for the move to (%.3f, %.3f, %.3f) — check [AXIS_*]MAX_VELOCITY/MAX_ACCELERATION",
			to.X, to.Y, to.Z)
	}
	c.moveID++
	// iniMaxvel is the same as vel: there is no feed override to scale here, so
	// the blended axis limit is both the commanded and the maximum velocity.
	if err := c.m.mc.SetLine(to, vel, vel, acc, motionTypeTraverse, c.moveID, 0, -1); err != nil {
		return faultf(errMotionError, "commanding a move to (%.3f, %.3f, %.3f): %v", to.X, to.Y, to.Z, err)
	}
	c.cmdPos = to
	c.motionDispatched = true
	return nil
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// checkTargetInLimits refuses a target outside the pushed axis position limits
// before it is dispatched. Motion would reject the command anyway (inRange),
// but only with a bare command status — this names the axis, the value and the
// limits, and points at the inputs that can push a taught position out of
// range: the geometric validation at load runs in the taught frame, while what
// is commanded is target − picker offset with the live z-offset added, so a
// picker's offset can carry a station near the envelope edge past a soft limit
// for that picker only, intermittently by which picker happens to be free.
func (c *control) checkTargetInLimits(to motctl.Pose) error {
	vals := [3]float64{to.X, to.Y, to.Z}
	for i, letter := range [3]string{"X", "Y", "Z"} {
		min, max := c.m.limits.AxisMinPos[i], c.m.limits.AxisMaxPos[i]
		if vals[i] < min-moveFuzz || vals[i] > max+moveFuzz {
			return faultf(errMotionError,
				"move target %s = %.3f mm is outside the axis limits [%.3f, %.3f] — check the station coordinates against the picker x/y-offset params and the z-offset inputs",
				letter, vals[i], min, max)
		}
	}
	return nil
}

// moveLimits turns the requested velocity and acceleration into the coordinated
// path values for one move, under the per-axis maxima.
//
// The request is a PER-AXIS ceiling, not a path feed. This is where a
// pick-and-place machine parts ways with a milling one: a cutter needs a
// constant path feed, so milltask caps the *blended* result and every axis
// slows down to hold it. Here the goal is the shortest possible move time, so
// an axis that could keep running must keep running — MOVE_VEL/MOVE_ACC and
// Z_VEL/Z_ACC cap each axis's own maximum, and the path value is whatever falls
// out of the blend. A travel whose first segment is X-only and whose second is
// a 45° XY corner leaves X at MOVE_VEL throughout, with the path speed rising
// to MOVE_VEL·√2 across the corner; read as a path feed, the same corner used
// to brake X to MOVE_VEL/√2 for no reason.
//
// A request of 0 means "no ceiling beyond the axis limits", which is what
// MOVE_VEL/MOVE_ACC = 0 and Z_VEL/Z_ACC = 0 mean in the INI. A Z stroke moves
// one axis, so for it the two readings coincide.
//
// moved separates "nothing to do" from "cannot do it": a move with no
// displacement returns moved=false and line drops it, while real displacement
// with zero limits comes back moved=true and zeroed limits, which line faults
// on.
func (c *control) moveLimits(from, to motctl.Pose, velReq, accReq float64) (vel, acc float64, moved bool) {
	d := [3]float64{
		math.Abs(to.X - from.X),
		math.Abs(to.Y - from.Y),
		math.Abs(to.Z - from.Z),
	}
	for i := range d {
		if d[i] < moveFuzz {
			d[i] = 0
		}
		if d[i] > 0 {
			moved = true
		}
	}
	if !moved {
		return 0, 0, false
	}
	vel = blendAxisLimit(d, cappedAxisLimits(c.m.limits.AxisMaxVel[:], velReq))
	acc = blendAxisLimit(d, cappedAxisLimits(c.m.limits.AxisMaxAcc[:], accReq))
	if vel <= 0 || acc <= 0 {
		return 0, 0, true
	}
	return vel, acc, true
}

// cappedAxisLimits lowers the three linear axes' maxima to a per-axis ceiling.
// A ceiling of 0 is "no ceiling" and leaves them as they are; an axis maximum
// of 0 stays 0 rather than being raised to the ceiling, so a machine whose
// [AXIS_*]MAX_VELOCITY/MAX_ACCELERATION is unusable still reaches line's fault
// instead of quietly running the move at the INI ceiling.
func cappedAxisLimits(max []float64, ceiling float64) [3]float64 {
	var out [3]float64
	copy(out[:], max)
	if ceiling > 0 {
		for i, m := range out {
			if m > ceiling {
				out[i] = ceiling
			}
		}
	}
	return out
}

// blendAxisLimit is the coordinated limit for a displacement d under per-axis
// maxima: the slowest axis sets the time the move takes, and the limit is the
// path length over that time. The computation is the shared C++ canon port in
// internal/motsetup, restricted to the three linear axes this module moves.
func blendAxisLimit(d, max [3]float64) float64 {
	var d9, m9 [9]float64
	copy(d9[:], d[:])
	copy(m9[:], max[:])
	limit, _ := motsetup.BlendLimit(d9, m9, true, false)
	return limit
}

// ---------------------------------------------------------------------------
// The legs a job is made of (§7.3)
// ---------------------------------------------------------------------------

// notePlanTime publishes the job's worst route-planning latency on plan-time
// (D13's < 100 ms budget, made observable from outside — see pins.go). Timed on
// the failing path too: a plan that fails still cost the time it took, and a
// PLANNING_FAILED that took 300 ms is worth seeing.
func (c *control) notePlanTime(j *job, d time.Duration) {
	if d <= j.planMax {
		return
	}
	j.planMax = d
	c.m.pins.planTime.Set(d.Seconds())
}

// travel moves picker pk to a target in the taught frame, at the job's movement
// height, along a route planned around the dead zones.
//
// The route is planned here, immediately before the leg runs (§7.4), from where
// the picker currently is: the planner's static graph is built once at load, so
// this is the ~1 ms two-node insertion, comfortably inside D13's budget. The
// segments go into the TP queue back to back under a parabolic blend, which is
// what turns the planner's clearance-rounded corners into smooth constant-Z
// travel — and why CLEARANCE > BLEND_TOLERANCE is enforced at load: the TP blends
// corners *inward*, toward the zone the route was planned around.
func (c *control) travel(j *job, pk int, target pnproute.Point) error {
	if err := c.setMotionMode(motstat.MOTION_COORD); err != nil {
		return err
	}
	// The scene is read here, not at job start: deadzone-select describes the
	// machine as it is right now, and a leg that begins after the machine
	// changed (a sphere opened while the job waited out a busy station) has to
	// be planned around the obstacles that are there for *this* leg. Like the
	// offset below it is one snapshot for the whole leg — the route is a rigid
	// polyline, so a selector change between two waypoint dispatches belongs to
	// the next leg, not to the middle of this one.
	planner, err := c.m.planners.at(c.in.deadzoneSelect)
	if err != nil {
		return err
	}
	j.deadzone = c.in.deadzoneSelect

	// One offset snapshot for the whole leg: the route below is planned as one
	// rigid polyline around the dead-zone clearances, and a halcmd setp landing
	// between two waypoint dispatches must shift the next leg, not warp this
	// one mid-flight. (D3's "read live" means per leg, not per waypoint.)
	off := c.pickerOffset(pk)
	start := pnproute.Point{X: c.cmdPos.X + off.X, Y: c.cmdPos.Y + off.Y}
	planStart := time.Now()
	route, err := planner.Plan(start, target)
	c.notePlanTime(j, time.Since(planStart))
	if err != nil {
		return faultf(errPlanningFailed,
			"no route for picker %d from (%.3f, %.3f) to (%.3f, %.3f) in dead-zone file %d: %v",
			pk, start.X, start.Y, target.X, target.Y, j.deadzone, err)
	}
	if err := c.m.mc.SetTermCond(tpTermCondParabolic, c.m.cfg.BlendTolerance); err != nil {
		return faultf(errMotionError, "setting the blend termination condition: %v", err)
	}
	for _, wp := range route.Waypoints {
		to := c.cmdPos
		to.X, to.Y, to.Z = wp.X-off.X, wp.Y-off.Y, j.height
		// The first waypoint is where the picker already is, so its line drops
		// itself in moveLimits; a Z still off the movement height is corrected by
		// it rather than left for the next leg to trip over.
		if err := c.line(to, c.m.cfg.MoveVel, c.m.cfg.MoveAcc); err != nil {
			return err
		}
	}
	return c.waitMotionDone()
}

// zStroke moves Z alone to an absolute height at the Z_VEL/Z_ACC limits, with the
// queue drained on both sides (§7.3): a pick or a place starts and ends from a
// full stop, so the approach cannot be blended into the travel that preceded it.
func (c *control) zStroke(z float64) error {
	if math.Abs(z-c.cmdPos.Z) < moveFuzz {
		return nil
	}
	if err := c.setMotionMode(motstat.MOTION_COORD); err != nil {
		return err
	}
	if err := c.waitMotionDone(); err != nil {
		return err
	}
	if err := c.m.mc.SetTermCond(tpTermCondStop, 0); err != nil {
		return faultf(errMotionError, "setting the exact-stop termination condition: %v", err)
	}
	to := c.cmdPos
	to.Z = z
	if err := c.line(to, c.m.cfg.ZVel, c.m.cfg.ZAcc); err != nil {
		return err
	}
	return c.waitMotionDone()
}

// retract lifts Z to the movement height if it is below it. A job normally starts
// there already; one that does not is the aftermath of an aborted job, which left
// the head down at a station (§7.3).
func (c *control) retract(height float64) error {
	if c.cmdPos.Z >= height-moveFuzz {
		return nil
	}
	return c.zStroke(height)
}

// ---------------------------------------------------------------------------
// Waiting for motion
// ---------------------------------------------------------------------------

// motionDrained reports whether the machine is in position with an empty TP
// queue right now.
func (c *control) motionDrained() bool {
	return c.statusOK && c.status.Inpos != 0 && c.status.QueueDepth == 0
}

// waitMotionDone waits until the queue has drained and the machine is in
// position. There is no timeout: how long a move takes is the machine's business,
// and every cycle of the wait still runs the abort check, so an estop or a
// machine-off ends it (§6.5).
func (c *control) waitMotionDone() error {
	if !c.motionDispatched {
		// Nothing was commanded since the last drain, so a machine that already
		// reads drained is drained — a back-to-back barrier (the drain before a
		// Z stroke right after a travel) costs nothing.
		if c.motionDrained() {
			return nil
		}
	} else {
		// Motion was dispatched: skip a few cycles so the servo thread has
		// published it before the first in-position check. Only cycles whose
		// status read SUCCEEDED count — a failed read re-serves the last
		// snapshot, and enough of those spanning the dispatch would hand the
		// drain check below the pre-dispatch picture (Inpos=1, queue empty),
		// letting the full-stop barrier pass while the machine is still
		// moving. Failed reads lengthen this wait; they can never shorten it.
		for settled := 0; settled < dispatchSettleTicks; {
			if !c.tick() {
				return errStopping
			}
			if err := c.abortCheck(); err != nil {
				return err
			}
			if c.commErrors == 0 {
				settled++
			}
		}
	}
	if err := c.waitUntil(errMotionError, 0, "motion to finish", c.motionDrained); err != nil {
		return err
	}
	c.motionDispatched = false
	return nil
}

// dwell holds for a settle time, ticking the control loop so the wait is
// abortable and the output pins keep being published.
//
// A settle time of 0 still costs one cycle. Every dwell in an action sequence is
// followed by a check of the picker or fixture feedback, and that feedback comes
// out of the input snapshot: returning without ticking would have the check judge
// a sample taken *before* the command it is checking was even issued, so a
// machine with the settle times left at 0 would fail every pick with
// PICKER_CLOSE_FAILED.
func (c *control) dwell(seconds float64) error {
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for {
		if !c.tick() {
			return errStopping
		}
		if err := c.abortCheck(); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
		}
	}
}

// settleTimeout turns a settle-time param into a wait timeout. A configured zero
// means "no dwell", and waitUntil reads a non-positive timeout as "no timeout at
// all" — so a machine with the settle times left at 0 gets exactly one cycle to
// show the feedback rather than an unbounded wait.
func settleTimeout(seconds float64) time.Duration {
	if d := time.Duration(seconds * float64(time.Second)); d > 0 {
		return d
	}
	return pollInterval
}
