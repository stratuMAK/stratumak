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
	return c.lineCapped(to, velReq, accReq, 0)
}

// lineCapped is line with the path velocity additionally capped at pathVel
// (0 = no cap): a line of a smoothed path runs at its requested speed (D48).
func (c *control) lineCapped(to motctl.Pose, velReq, accReq, pathVel float64) error {
	if !isFinite(to.X) || !isFinite(to.Y) || !isFinite(to.Z) {
		// A NaN/Inf target reaches this point through the float input pins
		// (a z-offset wired to a broken sensor value, say); motion must never
		// see it, and NaN would also slip every comparison below.
		return faultf(errMotionError,
			"move target (%v, %v, %v) is not finite — check the z-offset and picker offset inputs",
			to.X, to.Y, to.Z)
	}
	vel, acc, moved := c.moveLimits(c.cmdPos, to, velReq, accReq)
	if pathVel > 0 && vel > pathVel {
		vel = pathVel
	}
	if !moved {
		// Nothing to dispatch -- typically a route's first waypoint, where
		// the head already stands -- and so nothing to check: a head that
		// homing or a jog left just outside a limit must not be refused the
		// move that takes it away.
		return nil
	}
	if err := c.checkMoveInLimits(c.cmdPos, to); err != nil {
		return err
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
// is commanded is target − picker offset with the live z-offset added.
// checkReach asks the same question for every endpoint and every picker at
// job accept (D28); this per-dispatch check remains the backstop for what can
// still drift afterwards — the offsets and z-offsets are live inputs.
// checkMoveInLimits is checkTargetInLimits for a move from where the head is:
// a target outside a limit is accepted on an axis where the head already
// stands at least as far outside it, so a move that holds that axis or brings
// it back in is not refused -- the way a jog back inside a soft limit is
// allowed. Anything that goes further out is.
func (c *control) checkMoveInLimits(from, to motctl.Pose) error {
	vals := [3]float64{to.X, to.Y, to.Z}
	starts := [3]float64{from.X, from.Y, from.Z}
	for i, letter := range [3]string{"X", "Y", "Z"} {
		min, max := c.m.limits.AxisMinPos[i], c.m.limits.AxisMaxPos[i]
		v, s := vals[i], starts[i]
		below := v < min-moveFuzz && v < s-moveFuzz
		above := v > max+moveFuzz && v > s+moveFuzz
		if below || above {
			return faultf(errMotionError,
				"move target %s = %.3f mm is outside the axis limits [%.3f, %.3f] — check the station coordinates against the picker x/y-offset params and the z-offset inputs",
				letter, v, min, max)
		}
	}
	return nil
}

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
// height, along a route planned around the dead zones, and returns once the
// machine has arrived. Every leg drains: a Z stroke starts from a standstill,
// and the action sequences read gripper and fixture feedback about a machine
// that has stopped moving.
//
// A leg whose route touches the movable region goes through the scene gate
// first (D45): it starts only under the lock with whatever changes the scene,
// waits for it standing, and is planned again after the lock is taken — the
// scene may have changed while it waited, and a route planned before the wait
// is not trusted.
func (c *control) travel(j *job, pk int, target pnproute.Point, d descent) error {
	lg, err := c.travelLeg(j, pk, target, d)
	if err != nil {
		return err
	}
	if d.on && lg.r1 == 0 {
		// A descent with no corner arc is a stroke of its own, after the
		// leg -- and after the scene gate's lock, which covers the leg, as
		// it did before the descent was part of a travel.
		return c.zStroke(d.z)
	}
	return nil
}

// travelLeg plans, gates and runs the leg of a travel, and returns once the
// machine has arrived; a descent joined to it by a corner arc arrives with it.
func (c *control) travelLeg(j *job, pk int, target pnproute.Point, d descent) (*leg, error) {
	lg, err := c.planLeg(j, pk, target, d)
	if err != nil {
		return nil, err
	}
	if c.m.planners.touchesMovable(c.legPath(lg.route)) {
		// The scene may have changed while the gate waited: the leg is
		// planned again under the lock.
		if err := c.acquireScene(target); err != nil {
			return nil, err
		}
		defer c.releaseScene()
		if lg, err = c.planLeg(j, pk, target, d); err != nil {
			return nil, err
		}
		if !c.m.planners.touchesMovable(c.legPath(lg.route)) {
			c.releaseScene()
		}
	}
	if c.lift.on && lg.r0 == 0 {
		// No room for a corner arc on this leg: the lift runs on its own.
		if err := c.flushLift(); err != nil {
			return nil, err
		}
	}
	if err := c.dispatchLeg(j, lg, d); err != nil {
		return nil, err
	}
	return lg, c.waitMotionDone()
}

// descent is the stroke a travel ends in (D52): down to z at the target, with
// a corner arc of radius r joining it to the travel (0: a stroke of its own,
// after the travel has stopped). on false: the travel ends at its height.
type descent struct {
	on   bool
	z, r float64
}

func down(z, r float64) descent { return descent{on: true, z: z, r: r} }

// noDescent is a travel that ends at its height.
var noDescent = descent{}

// pendingLift is a lift out of a station that up() deferred (D52): to Z `to`,
// joined to the next travel by a corner arc of radius up to r.
type pendingLift struct {
	on    bool
	to, r float64
}

// leg is a planned travel: the route, and the radii of the Z corner arcs at
// its start (r0, the lift) and end (r1, the descent); 0 is none.
type leg struct {
	route  *pnproute.Route
	r0, r1 float64
}

// minZCornerRadius is the smallest Z corner arc worth queueing, mm: below it
// the strokes are separate moves.
const minZCornerRadius = 0.5

// minDescentLine is how much of a descent stays a straight vertical line
// below its corner arc, mm: the leg's last segment is that line, queued as
// an exact stop -- never the arc, which tpmod would run at half the
// acceleration as the never-finalized last segment (D48).
const minDescentLine = 1.0

// legPath is a leg as the scene gate judges it: from where the head stands,
// along the geometry commanded -- the route's waypoints, or a smoothed
// path's arc chain (D45, D47).
func (c *control) legPath(route *pnproute.Route) []pnproute.Point {
	wps := route.Path()
	path := make([]pnproute.Point, 0, len(wps)+1)
	path = append(path, pnproute.Point{X: c.cmdPos.X, Y: c.cmdPos.Y})
	return append(path, wps...)
}

// acquireScene takes the scene gate's lock for a leg that touches the movable
// region (D45, spec §4.2): wait standing while scene-hold is up, raise
// scene-busy, and look at scene-hold once more a control cycle later. Raised
// meanwhile, the scene change wins — busy drops and the wait starts over — so
// a leg and a scene change can never both start: the other side starts only
// once busy has stayed low after it raised hold, and keeps hold up until it
// is done. The wait ends like a station's (WAIT_ABORTED on auto-enable or
// wait-abort, D15/D33).
func (c *control) acquireScene(target pnproute.Point) error {
	// Standing, but a lift deferred for this leg (D52) stays deferred: it has
	// not been dispatched, so the head stands at the bottom, and the leg can
	// still take it in its stream. A wait below runs it first (waitStanding):
	// the head waits up.
	if err := c.waitDrained(); err != nil {
		return err
	}
	for {
		if c.in.sceneHold {
			c.m.logger.Info("pnptask: leg waits for the scene", "x", target.X, "y", target.Y)
		}
		if err := c.waitStanding(func() bool { return !c.in.sceneHold }, "scene gate", nil); err != nil {
			return err
		}
		c.m.pins.sceneBusy.Set(true)
		if !c.tick() {
			c.releaseScene()
			return errStopping
		}
		if err := c.abortCheck(); err != nil {
			c.releaseScene()
			return err
		}
		if !c.in.sceneHold {
			return nil
		}
		c.releaseScene()
		c.m.logger.Debug("pnptask: leg yields to a scene change", "x", target.X, "y", target.Y)
	}
}

// releaseScene drops scene-busy: the gated leg has drained, or never started.
func (c *control) releaseScene() {
	c.m.pins.sceneBusy.Set(false)
}

// planLeg plans one leg from where the picker currently is.
//
// The route is planned immediately before the leg runs (§7.4): the planner's
// static graph is built once at load, so this is the ~1 ms two-node insertion,
// comfortably inside D13's budget. With PATH_SHAPER = spline it is then shaped
// (D47/D50): two curves through it and the TP model's choice between them and
// the route, ~10 ms on the coating machine, ~50 ms where neither curve comes
// clear.
func (c *control) planLeg(j *job, pk int, target pnproute.Point, d descent) (*leg, error) {
	// The scene is read here, not at job start: deadzone-select describes the
	// machine as it is right now, and a leg that begins after the machine
	// changed (a sphere opened while the job waited out a busy station) has to
	// be planned around the obstacles that are there for *this* leg. Like the
	// offset below it is one snapshot for the whole leg — the route is a rigid
	// polyline, so a selector change between two waypoint dispatches belongs to
	// the next leg, not to the middle of this one.
	planner, err := c.m.planners.at(c.in.deadzoneSelect)
	if err != nil {
		return nil, err
	}
	j.deadzone = c.in.deadzoneSelect

	// One offset snapshot for the whole leg: the route below is planned as one
	// rigid polyline around the dead-zone clearances, and a halcmd setp landing
	// between two waypoint dispatches must shift the next leg, not warp this
	// one mid-flight. (D3's "read live" means per leg, not per waypoint.)
	//
	// The plan runs in MACHINE coordinates (D28). The dead zones and the outer
	// limit are machine-position geometry — taught by driving the machine and
	// noting its positions, outlines that already embody the head's physical
	// extent, both pickers included. The picker offset only decides the
	// endpoint: putting picker pk's point on `target` means putting the
	// machine at target − offset. Planning in the pick-point frame and
	// shifting the waypoints afterwards (as this code once did) translates
	// the whole polyline by the offset and lets the machine cut a zone corner
	// by up to |offset|.
	off := c.pickerOffset(pk)
	start := pnproute.Point{X: c.cmdPos.X, Y: c.cmdPos.Y}
	goal := pnproute.Point{X: target.X - off.X, Y: target.Y - off.Y}
	// The Z corner arcs (D52): a deferred lift into this travel, a descent out
	// of it. Each is at most the station's radius, no longer than its stroke
	// (the descent keeps minDescentLine of straight stroke below it), and
	// together they take at most half the leg's straight length, so the short
	// moves inside a station get small arcs or none.
	length := math.Hypot(goal.X-start.X, goal.Y-start.Y)
	r0, r1 := 0.0, 0.0
	if c.lift.on && c.lift.r > 0 && math.Abs(c.lift.to-j.travel) < moveFuzz {
		r0 = math.Min(c.lift.r, math.Min(c.lift.to-c.cmdPos.Z, length/4))
	}
	if d.on && d.r > 0 && d.z < j.travel-moveFuzz {
		r1 = math.Min(d.r, math.Min(j.travel-d.z-minDescentLine, length/4))
	}
	r0, r1 = cornerFloor(r0), cornerFloor(r1)
	planStart := time.Now()
	route, err := planner.PlanPath(start, goal, pnproute.PathOptions{
		Smooth:         c.m.cfg.SmoothPaths,
		Limits:         c.pathLimits(),
		BlendTolerance: c.m.cfg.BlendTolerance,
		StartLine:      r0,
		EndLine:        r1,
	})
	c.notePlanTime(j, time.Since(planStart))
	if err != nil {
		return nil, faultf(errPlanningFailed,
			"no route for picker %d from machine (%.3f, %.3f) to (%.3f, %.3f) (station point (%.3f, %.3f) − offset (%g, %g)) in dead-zone file %d: %v",
			pk, start.X, start.Y, goal.X, goal.Y, target.X, target.Y, off.X, off.Y, j.deadzone, err)
	}
	r0, r1 = fitCorners(route, r0, r1)
	return &leg{route: route, r0: r0, r1: r1}, nil
}

func cornerFloor(r float64) float64 {
	if r < minZCornerRadius {
		return 0
	}
	return r
}

// fitCorners fits the Z corner arcs to the planned path: an arc's horizontal
// half is a straight stretch of the path at its end. A route's first and last
// lines give at most half their length (the TP blends their other end); a
// curve's chain starts and ends in lines of exactly the asked length, or --
// where the curve was too short for them -- has none, and gets no arc.
func fitCorners(route *pnproute.Route, r0, r1 float64) (float64, float64) {
	if segs := route.Segments; segs != nil {
		isLine := func(sg pnproute.Segment, r float64) bool {
			return sg.Kind == pnproute.SegmentLine && math.Abs(sg.Length()-r) < 1e-6
		}
		if r0 > 0 && !isLine(segs[0], r0) {
			r0 = 0
		}
		if r1 > 0 && (len(segs) < 2 || !isLine(segs[len(segs)-1], r1)) {
			r1 = 0
		}
		return r0, r1
	}
	w := route.Waypoints
	n := len(w)
	if n < 2 {
		return 0, 0 // a leg that does not move
	}
	r0 = cornerFloor(math.Min(r0, math.Hypot(w[1].X-w[0].X, w[1].Y-w[0].Y)/2))
	r1 = cornerFloor(math.Min(r1, math.Hypot(w[n-1].X-w[n-2].X, w[n-1].Y-w[n-2].Y)/2))
	return r0, r1
}

// pathLimits are the XY travel limits as the path planner's TP model sees
// them: the per-axis maxima under the MOVE_VEL/MOVE_ACC ceilings, the TP's
// path cap and the servo period.
func (c *control) pathLimits() pnproute.Limits {
	vel := cappedAxisLimits(c.m.limits.AxisMaxVel[:], c.m.cfg.MoveVel)
	acc := cappedAxisLimits(c.m.limits.AxisMaxAcc[:], c.m.cfg.MoveAcc)
	return pnproute.Limits{
		AccX: acc[0], AccY: acc[1],
		VelX: vel[0], VelY: vel[1],
		VelMax: c.m.limits.MaxVelocity,
		Cycle:  c.m.cfg.ServoPeriod,
	}
}

// dispatchLeg queues a planned leg. The segments go into the TP queue back to
// back under a parabolic blend: a route's corners are blended within
// BLEND_TOLERANCE -- which is what turns the planner's clearance-rounded
// corners into smooth constant-Z travel, and why CLEARANCE > BLEND_TOLERANCE
// is enforced at load: the TP blends corners *inward*, toward the zone the
// route was planned around -- and a smoothed path's chain joins tangentially,
// which the TP finds by itself and runs without a blend (D48).
//
// With Z corner arcs (D52) the leg is one stream from the bottom of the lift
// to the bottom of the descent: the lift's straight stroke up to
// travel − r0, an arc in the vertical plane of the leg's start direction onto
// the travel height, the path from there, an arc down in the vertical plane
// of its end direction, and the descent's straight stroke. Every join is
// tangent, so the TP blends none of them. The arcs' horizontal halves are the
// path's first and last r0/r1 mm, which the planner checked against the zones
// with the rest; that the head only moves sideways in the top r of a stroke is
// what the travel height has to leave room for.
func (c *control) dispatchLeg(j *job, lg *leg, d descent) error {
	if err := c.setMotionMode(motstat.MOTION_COORD); err != nil {
		return err
	}
	if err := c.m.mc.SetTermCond(tpTermCondParabolic, c.m.cfg.BlendTolerance); err != nil {
		return faultf(errMotionError, "setting the blend termination condition: %v", err)
	}
	// The last segment of a leg ends at a standstill, so it is queued as an
	// exact stop. Left parabolic it would run at HALF of MOVE_ACC for its
	// whole length: the planner halves a parabolic segment's acceleration
	// (tc.c, tcGetOverallMaxAccel) to leave room for a blend into the next
	// one, even when nothing ever follows. The corner into it still blends --
	// the planner decides a join by the term cond of the segment BEFORE it,
	// which stays parabolic.
	lastStop := func() error {
		if err := c.m.mc.SetTermCond(tpTermCondStop, 0); err != nil {
			return faultf(errMotionError, "setting the exact-stop termination condition: %v", err)
		}
		return nil
	}
	route := lg.route
	start := pnproute.Point{X: c.cmdPos.X, Y: c.cmdPos.Y}
	lineDir := func(a, b pnproute.Point) pnproute.Point {
		l := math.Hypot(b.X-a.X, b.Y-a.Y)
		return pnproute.Point{X: (b.X - a.X) / l, Y: (b.Y - a.Y) / l}
	}
	// The path's start and end directions, where an arc needs them (fitCorners
	// left an arc only where the path has a line there).
	var d0, d1 pnproute.Point
	if segs := route.Segments; segs != nil {
		d0 = lineDir(segs[0].Start, segs[0].End)
		last := segs[len(segs)-1]
		d1 = lineDir(last.Start, last.End)
	} else if w := route.Waypoints; len(w) >= 2 {
		d0 = lineDir(w[0], w[1])
		d1 = lineDir(w[len(w)-2], w[len(w)-1])
	}

	if c.lift.on {
		// planLeg gave the deferred lift its arc (travel ran it on its own
		// otherwise): the straight stroke up, then the arc onto the travel.
		c.lift = pendingLift{}
		to := c.cmdPos
		to.Z = j.travel - lg.r0
		if err := c.line(to, c.m.cfg.ZVel, c.m.cfg.ZAcc); err != nil {
			return err
		}
		if err := c.zArc(start, d0, lg.r0, true); err != nil {
			return err
		}
	} else {
		// A Z still off the movement height is corrected first, rather than
		// left for the next leg to trip over; at the height it drops itself
		// in moveLimits.
		to := c.cmdPos
		to.Z = j.travel
		if err := c.line(to, c.m.cfg.MoveVel, c.m.cfg.MoveAcc); err != nil {
			return err
		}
	}

	goal := route.Waypoints[len(route.Waypoints)-1]
	descend := d.on && lg.r1 > 0
	if route.Segments == nil {
		pts := append([]pnproute.Point(nil), route.Waypoints[1:]...)
		if len(pts) == 0 {
			return lastStop() // nothing to move: the head is there
		}
		if descend {
			pts[len(pts)-1] = pnproute.Point{X: goal.X - lg.r1*d1.X, Y: goal.Y - lg.r1*d1.Y}
		}
		for i, wp := range pts {
			if i == len(pts)-1 && !descend {
				if err := lastStop(); err != nil {
					return err
				}
			}
			to := c.cmdPos
			to.X, to.Y, to.Z = wp.X, wp.Y, j.travel
			if err := c.line(to, c.m.cfg.MoveVel, c.m.cfg.MoveAcc); err != nil {
				return err
			}
		}
	} else {
		segs := route.Segments
		if lg.r0 > 0 {
			segs = segs[1:] // the lift arc's horizontal half
		}
		if descend {
			segs = segs[:len(segs)-1] // the descent arc's
		}
		lim := c.pathLimits()
		for i, sg := range segs {
			if i == len(segs)-1 && !descend {
				if err := lastStop(); err != nil {
					return err
				}
			}
			to := c.cmdPos
			to.X, to.Y, to.Z = sg.End.X, sg.End.Y, j.travel
			var err error
			if sg.Kind == pnproute.SegmentArc {
				err = c.arc(to, sg, lim)
			} else {
				err = c.lineCapped(to, c.m.cfg.MoveVel, c.m.cfg.MoveAcc, sg.Vel)
			}
			if err != nil {
				return err
			}
		}
	}
	if !descend {
		return nil
	}
	if err := c.zArc(pnproute.Point{X: c.cmdPos.X, Y: c.cmdPos.Y}, d1, lg.r1, false); err != nil {
		return err
	}
	if err := lastStop(); err != nil {
		return err
	}
	to := c.cmdPos
	to.Z = d.z
	return c.line(to, c.m.cfg.ZVel, c.m.cfg.ZAcc)
}

// zArc queues a Z corner arc of radius r (D52) from the tracked commanded
// position, at XY `at`, in the vertical plane of the horizontal direction
// dir: up (from a vertical stroke onto the travel height, ending r further
// along dir and r higher) or down (from the travel onto a vertical stroke,
// ending r further along dir and r lower). Both turn counter-clockwise about
// the horizontal normal z × dir.
//
// Its speed keeps every axis inside its limit over the quarter turn --
// min(vx/|dir.x|, vy/|dir.y|, vz) under the MOVE_VEL and Z_VEL ceilings and
// the path cap -- and its acceleration is the smallest of the axes it moves,
// under MOVE_ACC and Z_ACC; ini_maxvel is that speed capped by the
// centripetal limit (D49).
func (c *control) zArc(at, dir pnproute.Point, r float64, up bool) error {
	z0 := c.cmdPos.Z
	to := c.cmdPos
	to.X, to.Y = at.X+r*dir.X, at.Y+r*dir.Y
	center := motctl.Cartesian{X: to.X, Y: to.Y, Z: z0}
	to.Z = z0 + r
	if !up {
		center = motctl.Cartesian{X: at.X, Y: at.Y, Z: z0 - r}
		to.Z = z0 - r
	}
	if !isFinite(to.X) || !isFinite(to.Y) || !isFinite(to.Z) {
		return faultf(errMotionError, "Z corner arc to (%v, %v, %v) is not finite", to.X, to.Y, to.Z)
	}
	if err := c.checkMoveInLimits(c.cmdPos, to); err != nil {
		return err
	}
	vxy := cappedAxisLimits(c.m.limits.AxisMaxVel[:], c.m.cfg.MoveVel)
	axy := cappedAxisLimits(c.m.limits.AxisMaxAcc[:], c.m.cfg.MoveAcc)
	vz := cappedAxisLimits(c.m.limits.AxisMaxVel[:], c.m.cfg.ZVel)[2]
	acc := cappedAxisLimits(c.m.limits.AxisMaxAcc[:], c.m.cfg.ZAcc)[2]
	vel := vz
	if c.m.limits.MaxVelocity > 0 {
		vel = math.Min(vel, c.m.limits.MaxVelocity)
	}
	for i, u := range [2]float64{dir.X, dir.Y} {
		if math.Abs(u) > 1e-9 {
			vel = math.Min(vel, vxy[i]/math.Abs(u))
			acc = math.Min(acc, axy[i])
		}
	}
	if !(vel > 0) || !(acc > 0) {
		return faultf(errMotionError,
			"no usable velocity/acceleration for the Z corner arc to (%.3f, %.3f, %.3f) — check [AXIS_*]MAX_VELOCITY/MAX_ACCELERATION",
			to.X, to.Y, to.Z)
	}
	iniMaxvel := math.Min(vel, math.Sqrt(math.Sqrt(0.75)*acc*r))
	c.moveID++
	normal := motctl.Cartesian{X: -dir.Y, Y: dir.X}
	if err := c.m.mc.SetCircle(to, center, normal, 0, vel, iniMaxvel, acc, motionTypeTraverse, c.moveID, 0); err != nil {
		return faultf(errMotionError, "commanding a Z corner arc to (%.3f, %.3f, %.3f): %v", to.X, to.Y, to.Z, err)
	}
	c.cmdPos = to
	c.motionDispatched = true
	return nil
}

// arc commands one XY arc of a smoothed path at constant Z, from the tracked
// commanded position to `to` (D48, D49). It runs at the segment's requested
// speed, capped so every axis stays inside its velocity limit over the
// directions the arc sweeps (pnproute.ArcVel), with min(ax, ay) as its
// acceleration, and is queued with that speed as its ini_maxvel,
// capped by the arc's centripetal limit (pnproute.ArcMaxVel): the TP splits an
// arc's acceleration between turning and speeding up by ini_maxvel, and there
// is no feed override here to leave headroom for.
func (c *control) arc(to motctl.Pose, sg pnproute.Segment, lim pnproute.Limits) error {
	if !isFinite(to.X) || !isFinite(to.Y) || !isFinite(to.Z) || !isFinite(sg.Center.X) || !isFinite(sg.Center.Y) {
		return faultf(errMotionError, "arc to (%v, %v, %v) is not finite", to.X, to.Y, to.Z)
	}
	if math.Abs(to.Z-c.cmdPos.Z) >= moveFuzz {
		return faultf(errMotionError, "arc to (%.3f, %.3f) changes Z from %.3f to %.3f", to.X, to.Y, c.cmdPos.Z, to.Z)
	}
	// The whole arc, not only its end, must stay inside the axis limits: the
	// four quadrant points it passes and its end.
	a0 := math.Atan2(c.cmdPos.Y-sg.Center.Y, c.cmdPos.X-sg.Center.X)
	for k := -4; k <= 4; k++ {
		ang := float64(k) * math.Pi / 2
		if d := (ang - a0) * math.Copysign(1, sg.Sweep); d <= 0 || d >= math.Abs(sg.Sweep) {
			continue
		}
		pt := to
		pt.X, pt.Y = sg.Center.X+sg.Radius*math.Cos(ang), sg.Center.Y+sg.Radius*math.Sin(ang)
		if err := c.checkMoveInLimits(c.cmdPos, pt); err != nil {
			return err
		}
	}
	if err := c.checkMoveInLimits(c.cmdPos, to); err != nil {
		return err
	}
	vel := math.Min(sg.Vel, pnproute.ArcVel(sg, lim))
	acc := math.Min(lim.AccX, lim.AccY)
	if !(vel > 0) || !(acc > 0) {
		return faultf(errMotionError,
			"no usable velocity/acceleration for the arc to (%.3f, %.3f) (requested %g mm/s) — check [AXIS_*]MAX_VELOCITY/MAX_ACCELERATION",
			to.X, to.Y, sg.Vel)
	}
	turn := int32(0) // counter-clockwise about +Z
	if sg.Sweep < 0 {
		turn = -1 // clockwise, as canon passes a G2
	}
	c.moveID++
	center := motctl.Cartesian{X: sg.Center.X, Y: sg.Center.Y, Z: to.Z}
	if err := c.m.mc.SetCircle(to, center, motctl.Cartesian{Z: 1}, turn,
		vel, pnproute.ArcMaxVel(sg.Radius, vel, lim), acc, motionTypeTraverse, c.moveID, 0); err != nil {
		return faultf(errMotionError, "commanding an arc to (%.3f, %.3f, %.3f): %v", to.X, to.Y, to.Z, err)
	}
	c.cmdPos = to
	c.motionDispatched = true
	return nil
}

// zStroke moves Z alone to an absolute height at the Z_VEL/Z_ACC limits, with the
// queue drained on both sides (§7.3): a pick or a place starts and ends from a
// full stop, so the approach cannot be blended into the travel that preceded it.
func (c *control) zStroke(z float64) error {
	if err := c.flushLift(); err != nil {
		return err
	}
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
	if c.lift.on {
		// A deferred lift (D52) goes at least this high, still deferred.
		c.lift.to = math.Max(c.lift.to, height)
		return nil
	}
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
	if c.lift.on {
		// A deferred lift (D52) is motion that has to have happened: run it.
		return c.flushLift()
	}
	return c.waitDrained()
}

// waitDrained is waitMotionDone for what has been dispatched, leaving a
// deferred lift deferred.
func (c *control) waitDrained() error {
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
	if err := c.flushLift(); err != nil {
		return err
	}
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
// all" — so a machine with the settle times left at 0 gets a cycle or two (the
// first sample past the deadline decides, see control.sampledAt) to show the
// feedback rather than an unbounded wait.
func settleTimeout(seconds float64) time.Duration {
	if d := time.Duration(seconds * float64(time.Second)); d > 0 {
		return d
	}
	return pollInterval
}

// flushLift runs a lift up() deferred (D52) as a stroke of its own, drained:
// for whatever needs the machine standing at the top -- a wait, a dwell, the
// end of a job, a travel with no room for a corner arc.
func (c *control) flushLift() error {
	if !c.lift.on {
		return nil
	}
	to := c.lift.to
	c.lift = pendingLift{}
	return c.zStroke(to)
}

// effectiveZ is the Z the head is at for the next decision: a deferred
// lift's target, else the commanded Z.
func (c *control) effectiveZ() float64 {
	if c.lift.on {
		return c.lift.to
	}
	return c.cmdPos.Z
}
