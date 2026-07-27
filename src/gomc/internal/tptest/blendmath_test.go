// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tptest

import (
	"math"
	"testing"
)

// These replace unit_tests/tp/test_blendmath.c, which was built only by the
// top-level meson.build and so had not run since 2019. The assertions are the
// same; expressed natively rather than through greatest.h, which lets the
// angle sweep become a real subtest per angle instead of a bare loop.

// unit returns the unit vector at angle a in the XY plane.
func unit(a float64) Vec { return Vec{X: math.Cos(a), Y: math.Sin(a)} }

// TestAngleEpsilonsAreReal guards the cgo trap that these constants exist to
// dodge: read straight from the macros, TP_ANGLE_EPSILON_SQ comes back as 0,
// and because the predicates are all "< tol" every positive assertion in this
// file then fails while every negative one still passes — a failure mode that
// looks like a maths bug rather than a binding bug. If someone re-points the
// constants at C.TP_ANGLE_EPSILON_SQ, this fails first and says why.
func TestAngleEpsilonsAreReal(t *testing.T) {
	if AngleEpsilon <= 0 {
		t.Fatalf("AngleEpsilon = %g, want > 0 — the C constant did not come through", AngleEpsilon)
	}
	if AngleEpsilonSq <= 0 {
		t.Fatalf("AngleEpsilonSq = %g, want > 0 — cgo cannot evaluate the composed macro; "+
			"read it from gomc_tp_angle_epsilon_sq in tp_sources.c instead", AngleEpsilonSq)
	}
	if want := AngleEpsilon * AngleEpsilon; AngleEpsilonSq != want {
		t.Errorf("AngleEpsilonSq = %g, want %g (tp_types.h defines it as the square)", AngleEpsilonSq, want)
	}
}

// TestCartCartParallel covers pmCartCartParallel: a vector is parallel to
// itself, a vector one epsilon away is not parallel at zero tolerance, the
// tolerance behaves monotonically around that epsilon, and nothing at a
// meaningful angle (including anti-parallel) reads as parallel.
func TestCartCartParallel(t *testing.T) {
	u0 := Vec{X: 1}
	uClose := unit(AngleEpsilon)

	if !CartCartParallel(u0, u0, AngleEpsilonSq) {
		t.Error("a vector must be parallel to itself")
	}
	if CartCartParallel(u0, uClose, 0.0) {
		t.Error("an epsilon-separated vector must not be parallel at zero tolerance")
	}

	// The tolerance must actually bracket the epsilon separation: below it the
	// pair is rejected, above it accepted.
	if CartCartParallel(u0, uClose, 0.5*AngleEpsilonSq) {
		t.Error("half the epsilon tolerance must still reject")
	}
	if !CartCartParallel(u0, uClose, 1.5*AngleEpsilonSq) {
		t.Error("one and a half times the epsilon tolerance must accept")
	}

	// Every multiple of 45 degrees, anti-parallel (k=4) included.
	for k := 1.0; k <= 7; k++ {
		if CartCartParallel(u0, unit(math.Pi/4*k), AngleEpsilonSq) {
			t.Errorf("%.0f deg must not read as parallel", k*45)
		}
	}
}

// TestCartCartAntiParallel is the mirror of the above for
// pmCartCartAntiParallel.
func TestCartCartAntiParallel(t *testing.T) {
	u0 := Vec{X: 1}
	uOpposite := Vec{X: -1}
	uClose := Vec{X: -math.Cos(AngleEpsilon), Y: math.Sin(AngleEpsilon)}

	if !CartCartAntiParallel(u0, uOpposite, AngleEpsilonSq) {
		t.Error("a vector must be anti-parallel to its negation")
	}
	if CartCartAntiParallel(u0, uClose, 0.0) {
		t.Error("an epsilon-separated vector must not be anti-parallel at zero tolerance")
	}

	if CartCartAntiParallel(u0, uClose, 0.5*AngleEpsilonSq) {
		t.Error("half the epsilon tolerance must still reject")
	}
	if !CartCartAntiParallel(u0, uClose, 1.5*AngleEpsilonSq) {
		t.Error("one and a half times the epsilon tolerance must accept")
	}

	for k := 1.0; k <= 7; k++ {
		v := Vec{X: -math.Cos(math.Pi / 4 * k), Y: math.Sin(math.Pi / 4 * k)}
		if CartCartAntiParallel(u0, v, AngleEpsilonSq) {
			t.Errorf("%.0f deg must not read as anti-parallel", k*45)
		}
	}
}
