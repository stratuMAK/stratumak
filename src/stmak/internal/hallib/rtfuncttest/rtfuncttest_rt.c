// rtfuncttest_rt.c — the cyclic function pkg/hal's ExportFunct test registers.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// This file is a real translation unit, not a cgo preamble, and that is the
// whole point: "make rt-effects-check" compiles it with -Wfunction-effects, so
// the STMAK_NONBLOCKING claim on rtft_check is verified rather than asserted. A
// preamble is compiled by cgo's own invocation and no checker ever sees it.
//
// The check reaches this file through scripts/rt-effects-check.sh, whose Go
// module section globs every *_rt.c under src/stmak — so a new one cannot be
// added without being covered.

#include "rtfuncttest_rt.h"

// --- the cyclic function ---------------------------------------------------

// point_in_polygon is the standard crossing-number test, ported verbatim from
// pkg/pnproute's geom.go. It allocates nothing, loops a bounded number of
// times, and calls nothing out — which is what lets it be nonblocking without
// a trust boundary.
static int point_in_polygon(double px, double py,
                            const rtft_point_t *poly, int n) STMAK_NONBLOCKING;
static int point_in_polygon(double px, double py,
                            const rtft_point_t *poly, int n)
{
    int inside = 0;
    for (int i = 0, j = n - 1; i < n; j = i, i++) {
        double yi = poly[i].y, yj = poly[j].y;
        double xi = poly[i].x, xj = poly[j].x;
        if ((yi > py) != (yj > py)) {
            double xcross = (xj - xi) * (py - yi) / (yj - yi) + xi;
            if (px < xcross)
                inside = !inside;
        }
    }
    return inside;
}

void rtft_check(void *arg, long period)
{
    rtft_scene_t *s = (rtft_scene_t *)arg;
    int inside = 0;

    s->calls++;
    s->last_period = period;

    for (int i = 0; i < s->n; i++) {
        const rtft_poly_t *p = &s->polys[i];
        if (s->px < p->minx || s->px > p->maxx ||
            s->py < p->miny || s->py > p->maxy)
            continue;
        if (point_in_polygon(s->px, s->py, p->pts, p->n)) {
            inside = 1;
            break;
        }
    }

    // Two dereferences: HAL repoints the slot when the pin is netted, so the
    // inner pointer must be read every cycle.
    if (s->out)
        **s->out = inside ? 1 : 0;
}

// The address hal.ExportFunct is handed. See the header for why it is a symbol
// rather than something cgo derives from the function name.
const stmak_hal_funct_t rtft_check_fp = rtft_check;

// --- init-time helpers -----------------------------------------------------

size_t rtft_size(int npolys, int npoints)
{
    return sizeof(rtft_scene_t)
         + (size_t)npolys  * sizeof(rtft_poly_t)
         + (size_t)npoints * sizeof(rtft_point_t);
}

// polys_of / points_of recover the two arrays that follow the header in the
// single block. Both element types are double-aligned and the header ends on a
// double boundary, so the packing needs no padding of its own.
static rtft_poly_t *polys_of(rtft_scene_t *s)
{
    return (rtft_poly_t *)((unsigned char *)s + sizeof(rtft_scene_t));
}

static rtft_point_t *points_of(rtft_scene_t *s, int npolys)
{
    return (rtft_point_t *)((unsigned char *)polys_of(s)
                            + (size_t)npolys * sizeof(rtft_poly_t));
}

void rtft_layout(void *blk, int npolys, int npoints)
{
    rtft_scene_t *s = (rtft_scene_t *)blk;
    (void)npoints;
    s->n     = npolys;
    s->polys = polys_of(s);
}

void rtft_set_point(rtft_scene_t *s, int idx, double x, double y)
{
    rtft_point_t *pts = points_of(s, s->n);
    pts[idx].x = x;
    pts[idx].y = y;
}

// rtft_set_poly points polygon `poly` at points [first, first+n) and derives
// its bounding box from them, so the cyclic function never computes one.
void rtft_set_poly(rtft_scene_t *s, int poly, int first, int n)
{
    rtft_poly_t  *p   = &polys_of(s)[poly];
    rtft_point_t *pts = &points_of(s, s->n)[first];

    p->n   = n;
    p->pts = pts;
    if (n <= 0) {
        // Mirror the Go-side len(poly) > 0 guard: an empty polygon gets an
        // inverted box that rejects every query instead of reading pts[-].
        p->minx = p->miny = 1.0;
        p->maxx = p->maxy = -1.0;
        return;
    }
    p->minx = p->maxx = pts[0].x;
    p->miny = p->maxy = pts[0].y;
    for (int i = 1; i < n; i++) {
        if (pts[i].x < p->minx) p->minx = pts[i].x;
        if (pts[i].x > p->maxx) p->maxx = pts[i].x;
        if (pts[i].y < p->miny) p->miny = pts[i].y;
        if (pts[i].y > p->maxy) p->maxy = pts[i].y;
    }
}
