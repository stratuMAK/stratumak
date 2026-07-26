/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPL Version 2
 */
/* tp_sources.c — compiles the trajectory-planner math under test into this
 * package.
 *
 * The blendmath predicates are not reachable any other way: blendmath.c is
 * built into motmod.so (a cmod), which exports nothing, and there is no
 * libtp. cgo only compiles .c files that live in the package directory, so
 * the sources are pulled in here by #include — the same trick
 * test_halscope_rt.c uses for halscope_rt.c.
 *
 * tc.c / spherical_arc.c / emcpose.c are blendmath.c's link closure, not
 * things under test. Compiling them as one translation unit was checked to
 * be collision-free; keeping them in one file makes the closure explicit
 * rather than scattering four near-empty stubs.
 */
#include "emc/tp/blendmath.c"
#include "emc/tp/tc.c"
#include "emc/tp/spherical_arc.c"
#include "emc/nml_intf/emcpose.c"

/* Angle tolerances, materialised as real constants for the Go side.
 *
 * These are NOT read through cgo's macro evaluation: TP_ANGLE_EPSILON_SQ is a
 * composed expression macro, (TP_ANGLE_EPSILON * TP_ANGLE_EPSILON), and cgo
 * silently evaluates such a macro to 0 rather than failing. That turned every
 * positive assertion in the blendmath tests into a false negative until it
 * was caught. Letting the C compiler fold the expression keeps the value
 * exact and tied to tp_types.h. */
const double gomc_tp_angle_epsilon = TP_ANGLE_EPSILON;
const double gomc_tp_angle_epsilon_sq = TP_ANGLE_EPSILON_SQ;
