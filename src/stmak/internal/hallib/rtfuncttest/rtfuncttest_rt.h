// rtfuncttest_rt.h — flat scene walked by the pkg/hal ExportFunct test.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// This is the miniature of the layout docs/dev/GOMOD_RT_DESIGN.md §3 describes,
// and it exists to be verified rather than believed: the Go side assembles it
// once with hal.RTCalloc, the cyclic function below walks it every cycle and
// nothing writes it while the threads run, so no lock, no atomic and no
// double-buffer appears anywhere.
//
// One allocation holds all three levels — scene, then polygons, then points —
// so a single hal.RTFree releases the lot.

#ifndef RTFUNCTTEST_RT_H
#define RTFUNCTTEST_RT_H

#include <stddef.h>

#include "stmak_hal.h"
#include "stmak_rt_check.h"

typedef struct {
    double x, y;
} rtft_point_t;

// rtft_poly_t carries its own bounding box because that is where a cyclic
// function spends most of its time NOT doing work: the box rejects a polygon in
// four comparisons, and it is computed once at init.
typedef struct {
    int                 n;
    const rtft_point_t *pts;
    double              minx, miny, maxx, maxy;
} rtft_poly_t;

typedef struct {
    int                n;
    const rtft_poly_t *polys;

    // The query point. Written by the test between cycles, exactly as a real
    // module would take it from a HAL pin or an RT-safe GMI accessor.
    double px, py;

    // out is a HAL pin's data-pointer SLOT, not the data cell: HAL repoints the
    // slot when the pin is linked to a signal with net, so it is dereferenced
    // twice at access time. NULL until the test wires one up.
    stmak_hal_bit_t **out;

    // calls counts cycles, so the test can tell "the function ran and found
    // nothing" from "the function never ran".
    volatile long calls;
    volatile long last_period;
} rtft_scene_t;

// rtft_check is the cyclic function: bounding-box pre-check plus
// point-in-polygon, no allocation, bounded loops, no calls out.
void rtft_check(void *arg, long period) STMAK_NONBLOCKING;

// rtft_check_fp is rtft_check's ADDRESS, published as a real symbol so cgo can
// reach it: cgo turns `C.rtft_check` into its own call wrapper, whose address
// means nothing to HAL, and it cannot take the address of a C function at all.
// Defining the pointer in the .c file rather than the cgo preamble also keeps
// the address-taking inside the translation unit -Wfunction-effects checks.
extern const stmak_hal_funct_t rtft_check_fp;

// --- init-time helpers (non-RT) --------------------------------------------
//
// They carve and fill the block Go allocated. Alignment is why the carving is
// done here rather than with Go pointer arithmetic: C knows the offsets of its
// own types.

size_t rtft_size(int npolys, int npoints);
void   rtft_layout(void *blk, int npolys, int npoints);
void   rtft_set_point(rtft_scene_t *s, int idx, double x, double y);
void   rtft_set_poly(rtft_scene_t *s, int poly, int first, int n);

#endif // RTFUNCTTEST_RT_H
