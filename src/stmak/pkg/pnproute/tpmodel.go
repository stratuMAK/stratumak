// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import "math"

// A model of the trajectory planner (tpmod), to choose the fastest path by
// (D50).
//
// It reproduces the rules that decide how fast tpmod runs a leg as pnptask
// queues it:
//
//   - A polyline is queued as lines under a blend tolerance. Every corner
//     becomes a blend arc sized as blendmath.c blendComputeParameters sizes
//     it, run at no more than sqrt(0.87·a·R) and half the acceleration along
//     the arc. Lines get their per-axis limits in full (pnptask's
//     moveLimits); the last line is an exact stop (D40).
//   - A curve is queued as its arc chain (D48). Lines get their per-axis
//     limits, arcs min(ax, ay) and min(vx, vy), and the tangential/normal
//     split of blendmath.c pmCircleActualMaxVel, which is decided by the
//     segment's ini_maxvel: pnptask passes the requested speed there (D49).
//     A last arc keeps the initial ratio 0.5 (never finalized), which is why
//     a chain ends in a line.
//   - No segment passes in less than 1.02 servo cycles
//     (TP_MIN_SEGMENT_CYCLES).
//
// Calibrated against the coating machine (8 measured legs within 0.5 %) and
// checked against tpmod in a milltask sim (60 random legs, median −0.5 %,
// worst −4.6 %); the evidence is in pnp-route-test. It ignores lookahead
// depth, feed overrides and jerk filtering. It is the only tpmod-specific
// part of the shaper: a better trajectory planner brings its own model.

// Limits are the machine's kinematic limits as the TP model sees them.
type Limits struct {
	AccX, AccY float64 // per-axis acceleration, mm/s², any MOVE_ACC ceiling applied
	VelX, VelY float64 // per-axis velocity, mm/s, any MOVE_VEL ceiling applied
	VelMax     float64 // the path velocity cap ([TRAJ]MAX_LINEAR_VELOCITY), mm/s; 0 = none
	Cycle      float64 // the servo period, s; 0 = no minimum segment duration
}

func (l Limits) valid() bool {
	return l.AccX > 0 && l.AccY > 0 && l.VelX > 0 && l.VelY > 0 && l.VelMax >= 0 && l.Cycle >= 0
}

func (l Limits) vmax() float64 {
	if l.VelMax > 0 {
		return l.VelMax
	}
	return math.Inf(1)
}

// profile is a speed over a sampled path and the time it takes.
type profile struct {
	v []float64
	T float64
}

// accelFn returns the tangential acceleration available forward and for
// braking at sample i when moving at speed v.
type accelFn func(i int, v float64) (fwd, bwd float64)

// passes is the classic forward/backward integration: start from the speed
// limit at every sample, then cap each sample by what the one before
// (forward) and after (backward) can reach. With exact limits it is the
// time-optimal speed along a fixed path.
func passes(sm []sample, vlim []float64, acc accelFn) profile {
	n := len(sm)
	if n == 0 {
		return profile{}
	}
	v := append([]float64(nil), vlim...)
	v[0], v[n-1] = 0, 0
	for i := 0; i+1 < n; i++ {
		f, _ := acc(i, v[i])
		v[i+1] = math.Min(v[i+1], math.Sqrt(v[i]*v[i]+2*math.Max(f, 0)*(sm[i+1].s-sm[i].s)))
	}
	for i := n - 1; i > 0; i-- {
		_, b := acc(i, v[i])
		v[i-1] = math.Min(v[i-1], math.Sqrt(v[i]*v[i]+2*math.Max(b, 0)*(sm[i].s-sm[i-1].s)))
	}
	t := 0.0
	for i := 1; i < n; i++ {
		if ds := sm[i].s - sm[i-1].s; ds > 0 {
			t += 2 * ds / math.Max(v[i-1]+v[i], 1e-9)
		}
	}
	return profile{v: v, T: t}
}

// idealProfile is the time-optimal speed along the path under per-axis
// limits: at every point the acceleration budget of each axis is shared
// between turning (v²·κ, sideways) and speeding up or braking (along the
// path). It knows nothing of the TP; the chain's requested speeds come from
// it.
func idealProfile(sm []sample, L Limits) profile {
	vlim := make([]float64, len(sm))
	for i, s := range sm {
		tx, ty := math.Cos(s.phi), math.Sin(s.phi)
		nx, ny := -ty, tx
		v := L.vmax()
		if math.Abs(tx) > 1e-12 {
			v = math.Min(v, L.VelX/math.Abs(tx))
		}
		if math.Abs(ty) > 1e-12 {
			v = math.Min(v, L.VelY/math.Abs(ty))
		}
		if k := math.Abs(s.kappa); k > 0 {
			if math.Abs(nx) > 1e-12 {
				v = math.Min(v, math.Sqrt(L.AccX/(k*math.Abs(nx))))
			}
			if math.Abs(ny) > 1e-12 {
				v = math.Min(v, math.Sqrt(L.AccY/(k*math.Abs(ny))))
			}
		}
		if s.stop {
			v = 0
		}
		vlim[i] = v
	}
	acc := func(i int, v float64) (float64, float64) {
		s := sm[i]
		an := v * v * s.kappa
		tx, ty := math.Cos(s.phi), math.Sin(s.phi)
		lo, hi := math.Inf(-1), math.Inf(1)
		for _, ax := range [2][3]float64{{tx, -ty * an, L.AccX}, {ty, tx * an, L.AccY}} {
			t, a, A := ax[0], ax[1], ax[2]
			if math.Abs(t) < 1e-12 {
				continue // this axis only carries the turn, which vlim already bounds
			}
			l, h := (-A-a)/t, (A-a)/t
			if t < 0 {
				l, h = h, l
			}
			lo, hi = math.Max(lo, l), math.Min(hi, h)
		}
		if lo > hi {
			return 0, 0
		}
		return hi, -lo
	}
	return passes(sm, vlim, acc)
}

// lineLimits mirrors pnptask's moveLimits for a line in direction u: the
// slowest axis sets the time, so the path limit is min over axes of
// limit/|u_axis| (motsetup.BlendLimit), then the TP's path cap.
func lineLimits(u Point, L Limits) (vel, acc float64) {
	vel, acc = math.Inf(1), math.Inf(1)
	if math.Abs(u.X) > 1e-9 {
		vel, acc = math.Min(vel, L.VelX/math.Abs(u.X)), math.Min(acc, L.AccX/math.Abs(u.X))
	}
	if math.Abs(u.Y) > 1e-9 {
		vel, acc = math.Min(vel, L.VelY/math.Abs(u.Y)), math.Min(acc, L.AccY/math.Abs(u.Y))
	}
	return math.Min(vel, L.vmax()), acc
}

// circleRatioTan is the TP's tangential share of an arc's acceleration once
// the turn needs more than the normal share (tc.c BLEND_ACC_RATIO_TANGENTIAL).
const circleRatioTan = 0.5

// circleLimits is the TP's speed and tangential acceleration on an arc of
// radius r requested at speed feed, with maximum speed maxvel (the segment's
// ini_maxvel) and acceleration acc. The split is decided by maxvel, not by
// the requested speed (tc.c tcUpdateCircleAccRatio -> blendmath.c
// pmCircleActualMaxVel): the turn gets what maxvel needs, up to 0.87·acc,
// and the rest goes along the path; above that the maximum is capped at
// sqrt(0.87·acc·r) and the split fixed at 0.5 / 0.87. The arc then runs at
// min(feed, that maximum).
func circleLimits(r, feed, maxvel, acc float64) (vel, at float64) {
	cut := math.Sqrt(1-circleRatioTan*circleRatioTan) * acc
	an := maxvel * maxvel / math.Max(r, 1e-12)
	if an > cut {
		return math.Min(feed, math.Sqrt(cut*r)), circleRatioTan * acc
	}
	return math.Min(feed, maxvel), acc * math.Sqrt(1-(an/acc)*(an/acc))
}

// ArcMaxVel is the ini_maxvel an arc of radius r requested at speed vel is
// queued with (D49): the requested speed, capped by the arc's own limit
// min(sqrt(0.87·a·r), min(vx, vy)) with a = min(ax, ay) -- the value canon
// would pass. pnptask has no feed override to leave room for, and the
// requested speed puts the TP on the split that speed needs instead of the
// fixed 0.5 of every arc below R = v²/(0.87·a).
func ArcMaxVel(r, vel float64, L Limits) float64 {
	a := math.Min(L.AccX, L.AccY)
	canon := math.Min(math.Sqrt(math.Sqrt(0.75)*a*r), math.Min(L.VelX, L.VelY))
	return math.Min(vel, canon)
}

// minSegVel is the TP's floor on segment duration (TP_MIN_SEGMENT_CYCLES):
// a segment may not pass in less than 1.02 cycles.
func minSegVel(length float64, L Limits) float64 {
	if L.Cycle <= 0 {
		return math.Inf(1)
	}
	return length / (L.Cycle * 1.02)
}

// tpPolyline models the TP running a polyline queued as lines under blend
// tolerance tol, the way pnptask queues a route.
func tpPolyline(poly []Point, tol float64, L Limits) profile {
	pts := dedupePoints(poly, 1e-9)
	prims := filletize(pts, tol)
	sm := samplePrims(prims, curveStep)
	segVel := make([]float64, len(pts))
	segAcc := make([]float64, len(pts))
	for i := 0; i+1 < len(pts); i++ {
		v, a := lineLimits(unit(pts[i+1].sub(pts[i])), L)
		segVel[i] = math.Min(v, minSegVel(pts[i].dist(pts[i+1]), L))
		segAcc[i] = a
	}
	blendAcc := math.Min(L.AccX, L.AccY) // calculateInscribedDiameter in the XY plane
	pv := make([]float64, len(prims))
	pa := make([]float64, len(prims))
	for i, pm := range prims {
		switch pm.kind {
		case primLine:
			pv[i], pa[i] = segVel[pm.seg], segAcc[pm.seg]
		case primArc:
			feed := math.Min(segVel[pm.vtx-1], segVel[pm.vtx])
			pv[i] = math.Min(feed, math.Sqrt(0.866*blendAcc*pm.r))
			pa[i] = 0.5 * blendAcc
		case primStop:
			pv[i], pa[i] = 0, blendAcc
		}
	}
	return tpRun(sm, pv, pa)
}

// tpChain models the TP running segs queued one by one, tangent to each
// other, the last one an exact stop, each at its requested speed Vel.
func tpChain(segs []Segment, L Limits) profile {
	prims := make([]prim, len(segs))
	pv := make([]float64, len(segs))
	pa := make([]float64, len(segs))
	for i, sg := range segs {
		prims[i] = sg.prim()
		switch sg.Kind {
		case SegmentLine:
			v, a := lineLimits(unit(sg.End.sub(sg.Start)), L)
			pv[i], pa[i] = math.Min(sg.Vel, v), a
		case SegmentArc:
			f := math.Min(sg.Vel, math.Min(L.VelX, L.VelY))
			acc := math.Min(L.AccX, L.AccY)
			maxvel := ArcMaxVel(sg.Radius, f, L)
			if i == len(segs)-1 {
				// Never finalized: the initial ratio of 0.5.
				pv[i], pa[i] = math.Min(f, maxvel), circleRatioTan*acc
				break
			}
			pv[i], pa[i] = circleLimits(sg.Radius, f, maxvel, acc)
		}
		pv[i] = math.Min(pv[i], minSegVel(sg.Length(), L))
	}
	return tpRun(samplePrims(prims, curveStep), pv, pa)
}

// tpRun runs passes with per-prim speed and acceleration limits.
func tpRun(sm []sample, pv, pa []float64) profile {
	vl := make([]float64, len(sm))
	al := make([]float64, len(sm))
	for i, s := range sm {
		vl[i], al[i] = pv[s.prim], pa[s.prim]
		if s.stop {
			vl[i] = 0
		}
	}
	// A sample on a join belongs to both prims: take the tighter of the two.
	for i := 1; i < len(sm); i++ {
		if sm[i].prim != sm[i-1].prim {
			vl[i-1] = math.Min(vl[i-1], vl[i])
			al[i-1] = math.Min(al[i-1], al[i])
		}
	}
	return passes(sm, vl, func(i int, _ float64) (float64, float64) { return al[i], al[i] })
}
