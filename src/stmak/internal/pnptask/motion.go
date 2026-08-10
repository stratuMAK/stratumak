// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// Motion constants shared with the C side. They are spelled out here rather than
// imported because the generated GMI carries the calls, not the enums.
const (
	// motionTypeTraverse is EMC_MOTION_TYPE_TRAVERSE: every move this module
	// makes is a rapid. There is no programmed feed rate on a pick-and-place
	// machine — the speeds come from the INI and the axis limits.
	motionTypeTraverse = 1

	// The TP termination conditions (tp.h). Travel blends its corners
	// parabolically within BLEND_TOLERANCE; a Z stroke stops exactly, because a
	// pick starts and ends at a standstill.
	tpTermCondStop      = 0 // TC_TERM_COND_STOP
	tpTermCondParabolic = 2 // TC_TERM_COND_PARABOLIC
)

var (
	// dispatchSettleTicks is how many cycles pass between dispatching motion and
	// the first in-position check. Motion status is published by the servo
	// thread, so immediately after a SetLine it still describes the machine
	// standing still — the same stale-inpos race milltask's waitMotionDone skips
	// past (sequencer.go).
	dispatchSettleTicks = 5

	// moveFuzz is the smallest displacement worth commanding, in mm. It matches
	// CART_FUZZ in posemath.h, which is what the C++ canon drops moves against:
	// a "move" below it is numerical residue of a coordinate computation and
	// would cost a queue entry and a blend for no motion.
	moveFuzz = 1.0e-8
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

// commandFor is the machine position that puts picker pk over a target: the
// targeting convention of §8, command = target − offset.
//
// Everything taught — station coordinates, tray corners, the dead-zone drawings
// (D23) — lives in one frame, the frame a picker's position is expressed in. The
// conversion to machine coordinates happens here and nowhere else, at the last
// step before a move is commanded, so route planning and station geometry never
// have to know which picker is being moved.
func (c *control) commandFor(pk int, target pnproute.Point) pnproute.Point {
	off := c.pickerOffset(pk)
	return pnproute.Point{X: target.X - off.X, Y: target.Y - off.Y}
}

// pickerAt is where picker pk sits given the position the module has commanded —
// the inverse of commandFor, and the start point a route is planned from.
func (c *control) pickerAt(pk int) pnproute.Point {
	off := c.pickerOffset(pk)
	return pnproute.Point{X: c.cmdPos.X + off.X, Y: c.cmdPos.Y + off.Y}
}

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

// line commands one straight move to an absolute pose at the requested velocity
// and acceleration, both capped by what the participating axes can do. A move
// that displaces nothing is dropped rather than queued.
func (c *control) line(to motctl.Pose, velReq, accReq float64) error {
	vel, acc := c.moveLimits(c.cmdPos, to, velReq, accReq)
	if vel <= 0 || acc <= 0 {
		return nil
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

// moveLimits blends the requested velocity and acceleration against the per-axis
// maxima, so no axis is asked for more than its own limit while the axes stay
// coordinated. A request of 0 means "no override": the [TRAJ] defaults apply,
// which is what MOVE_VEL/MOVE_ACC = 0 and Z_VEL/Z_ACC = 0 mean in the INI.
//
// A move with no displacement returns zeroes, which is how line drops it.
func (c *control) moveLimits(from, to motctl.Pose, velReq, accReq float64) (vel, acc float64) {
	d := [3]float64{
		math.Abs(to.X - from.X),
		math.Abs(to.Y - from.Y),
		math.Abs(to.Z - from.Z),
	}
	for i := range d {
		if d[i] < moveFuzz {
			d[i] = 0
		}
	}
	velMax := blendAxisLimit(d, c.m.limits.AxisMaxVel[:])
	accMax := blendAxisLimit(d, c.m.limits.AxisMaxAcc[:])
	if velMax <= 0 || accMax <= 0 {
		return 0, 0
	}
	vel = velReq
	if vel <= 0 {
		vel = c.m.limits.MaxVelocity
	}
	if vel <= 0 || velMax < vel {
		vel = velMax
	}
	acc = accReq
	if acc <= 0 {
		acc = c.m.limits.MaxAcceleration
	}
	if acc <= 0 || accMax < acc {
		acc = accMax
	}
	return vel, acc
}

// blendAxisLimit is the coordinated limit for a displacement d under per-axis
// maxima: the slowest axis sets the time the move takes, and the limit is the
// path length over that time. It is internal/task/motionlimits.go's blendLimit
// reduced to the three linear axes this module moves.
func blendAxisLimit(d [3]float64, max []float64) float64 {
	tmax := 0.0
	for i := range d {
		if d[i] > 0 && max[i] > 0 {
			if t := d[i] / max[i]; t > tmax {
				tmax = t
			}
		}
	}
	if tmax <= 0 {
		return 0
	}
	return math.Sqrt(d[0]*d[0]+d[1]*d[1]+d[2]*d[2]) / tmax
}

// ---------------------------------------------------------------------------
// The legs a job is made of (§7.3)
// ---------------------------------------------------------------------------

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
	start := c.pickerAt(pk)
	route, err := j.planner.Plan(start, target)
	if err != nil {
		return faultf(errPlanningFailed,
			"no route for picker %d from (%.3f, %.3f) to (%.3f, %.3f): %v",
			pk, start.X, start.Y, target.X, target.Y, err)
	}
	if err := c.m.mc.SetTermCond(tpTermCondParabolic, c.m.cfg.BlendTolerance); err != nil {
		return faultf(errMotionError, "setting the blend termination condition: %v", err)
	}
	for _, wp := range route.Waypoints {
		cmd := c.commandFor(pk, wp)
		to := c.cmdPos
		to.X, to.Y, to.Z = cmd.X, cmd.Y, j.height
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
		// published it before the first in-position check.
		for i := 0; i < dispatchSettleTicks; i++ {
			if !c.tick() {
				return errStopping
			}
			if err := c.abortCheck(); err != nil {
				return err
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
