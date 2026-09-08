// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import "math"

// The motion constants and the coordinated-limit computation shared by the
// task modules. milltask and pnptask command the same motion stack, so the
// C-side mirrors (tc_types.h, motion_types.h, posemath.h) and the Go port of
// the C++ getStraightVelocity/getStraightAcceleration logic live here once —
// two hand-maintained copies had already begun to drift in shape, and a
// correction to one would silently not reach the other.

// CartFuzz is the minimum per-axis displacement that counts as motion,
// matching CART_FUZZ (posemath.h) used by the C++ canon's
// applyMinDisplacement.
const CartFuzz = 1.0e-8

// TP termination conditions (must match tc_types.h), and the motion type a
// rapid is commanded under (must match motion_types.h).
const (
	TPTermCondStop      = 0 // TC_TERM_COND_STOP
	TPTermCondExact     = 1 // TC_TERM_COND_EXACT
	TPTermCondParabolic = 2 // TC_TERM_COND_PARABOLIC (blend)

	MotionTypeTraverse = 1 // EMC_MOTION_TYPE_TRAVERSE
)

// Axis index groups: linear axes X,Y,Z,U,V,W and angular axes A,B,C.
var linearAxes = [...]int{0, 1, 2, 6, 7, 8}
var angularAxes = [...]int{3, 4, 5}

// BlendLimit computes the coordinated limit (velocity or acceleration) for a
// move with per-axis displacements d and per-axis maxima max, following the
// C++ getStraightVelocity/getStraightAcceleration logic:
//
//	t[i] = d[i]/max[i];  tmax = max over participating axes;
//	dtot = |xyz| (or |uvw| if no xyz, or |abc| for a pure angular move);
//	limit = dtot / tmax
//
// so no single axis exceeds its own maximum. It also returns tmax (the
// limiting per-axis time), used by the arc velocity computation. Returns 0 for
// a move to nowhere (the caller decides what that means: milltask substitutes
// the programmed feed rate as the C++ canon does, pnptask drops or faults the
// move).
func BlendLimit(d [9]float64, max [9]float64, cartesian, angular bool) (limit, tmax float64) {
	tmax = 0.0
	tAxis := func(i int) {
		if d[i] > 0 && max[i] > 0 {
			if ti := d[i] / max[i]; ti > tmax {
				tmax = ti
			}
		}
	}
	var dtot float64
	xyz := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
	uvw := math.Sqrt(d[6]*d[6] + d[7]*d[7] + d[8]*d[8])
	switch {
	case cartesian && !angular:
		for _, i := range linearAxes {
			tAxis(i)
		}
		if d[0] > 0 || d[1] > 0 || d[2] > 0 {
			dtot = xyz
		} else {
			dtot = uvw
		}
	case !cartesian && angular:
		for _, i := range angularAxes {
			tAxis(i)
		}
		dtot = math.Sqrt(d[3]*d[3] + d[4]*d[4] + d[5]*d[5])
	case cartesian && angular:
		// NIST IR6556 2.1.2.5(A): coordinate like a linear move, letting the
		// angular axes take the same time as the linear ones.
		for _, i := range linearAxes {
			tAxis(i)
		}
		for _, i := range angularAxes {
			tAxis(i)
		}
		if d[0] > 0 || d[1] > 0 || d[2] > 0 {
			dtot = xyz
		} else {
			dtot = uvw
		}
	}
	if tmax <= 0 {
		return 0, tmax
	}
	return dtot / tmax, tmax
}
