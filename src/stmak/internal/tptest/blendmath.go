// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package tptest exposes trajectory-planner math to Go tests.
//
// It exists because the TP is C compiled into motmod.so (a cmod, which
// exports nothing) with no shared library to link against — see the comment
// in tp_sources.c for how the sources are pulled in.
//
// Only what the tests need is wrapped. This is a test-support package: it is
// not imported by any production code.
package tptest

// #cgo CFLAGS: -DULAPI
// #cgo CFLAGS: -I${SRCDIR}/../../.. -I${SRCDIR}/../../../../include
// #cgo CFLAGS: -I${SRCDIR}/../../../cnc/tp -I${SRCDIR}/../../../cnc/kinematics
// #cgo CFLAGS: -I${SRCDIR}/../../../cnc/motion -I${SRCDIR}/../../../rtapi
// #cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../pkg/cmodule
// #cgo LDFLAGS: -L${SRCDIR}/../../../../lib -lposemath -lm
//
// #include "posemath.h"
// #include "tp_types.h"
// #include "blendmath.h"
//
// extern const double stmak_tp_angle_epsilon;
// extern const double stmak_tp_angle_epsilon_sq;
import "C"

// Angle tolerances from tp_types.h — the units the parallelism predicates
// take their tolerance in.
//
// Read from C constants (defined in tp_sources.c), NOT from the macros:
// TP_ANGLE_EPSILON_SQ is a composed expression macro and cgo evaluates such
// macros to 0 without complaint. See the comment beside those definitions.
var (
	AngleEpsilon   = float64(C.stmak_tp_angle_epsilon)
	AngleEpsilonSq = float64(C.stmak_tp_angle_epsilon_sq)
)

// Vec is a cartesian vector, mirroring PmCartesian.
type Vec struct{ X, Y, Z float64 }

func (v Vec) c() C.PmCartesian {
	return C.PmCartesian{x: C.double(v.X), y: C.double(v.Y), z: C.double(v.Z)}
}

// CartCartParallel reports whether u1 and u2 are parallel to within tol,
// where tol is a squared-angle tolerance.
func CartCartParallel(u1, u2 Vec, tol float64) bool {
	c1, c2 := u1.c(), u2.c()
	return C.pmCartCartParallel(&c1, &c2, C.double(tol)) != 0
}

// CartCartAntiParallel reports whether u1 and u2 are anti-parallel to within
// tol, where tol is a squared-angle tolerance.
func CartCartAntiParallel(u1, u2 Vec, tol float64) bool {
	c1, c2 := u1.c(), u2.c()
	return C.pmCartCartAntiParallel(&c1, &c2, C.double(tol)) != 0
}
