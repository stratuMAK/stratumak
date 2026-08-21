// pnp_deadzone_rt.c — the pnptask dead-zone clearance function.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// A real translation unit, not a cgo preamble, because that is what
// "make rt-effects-check" compiles with -Wfunction-effects — see
// docs/dev/GOMOD_RT_DESIGN.md §4 and the header for what this computes.

#include <stdatomic.h>

#include "pnp_deadzone_rt.h"
#include "mot_api.h"

// --- the cyclic function ---------------------------------------------------

// mot_load reads the published callback table. Acquire pairs with the release
// in pnp_dz_set_mot, so a table seen here is fully constructed.
static inline const mot_callbacks_t *mot_load(pnp_dz_t *d) STMAK_NONBLOCKING;
static inline const mot_callbacks_t *mot_load(pnp_dz_t *d)
{
    return (const mot_callbacks_t *)atomic_load_explicit(
        (void *_Atomic *)&d->mot, memory_order_acquire);
}

// point_in_polygon is the crossing-number test of pkg/pnproute's geom.go,
// ported unchanged: no allocation, bounded loops, no calls out.
static int point_in_polygon(double px, double py,
                            const pnp_dz_point_t *poly, int n) STMAK_NONBLOCKING;
static int point_in_polygon(double px, double py,
                            const pnp_dz_point_t *poly, int n)
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

// scene_clear is pnproute's Planner.Clear: outside every offset zone. It is
// deliberately not CheckPoint's twin — the outer limit says where the machine
// may be SENT, and a machine parked at a limit is a bad target but perfectly
// clear of the obstacles, which is the physical question being asked here.
static int scene_clear(const pnp_dz_scene_t *s, double px, double py) STMAK_NONBLOCKING;
static int scene_clear(const pnp_dz_scene_t *s, double px, double py)
{
    for (int i = 0; i < s->n; i++) {
        const pnp_dz_poly_t *p = &s->polys[i];
        if (px < p->minx || px > p->maxx || py < p->miny || py > p->maxy)
            continue;
        if (point_in_polygon(px, py, p->pts, p->n))
            return 0;
    }
    return 1;
}

void pnp_dz_check(void *arg, long period)
{
    pnp_dz_t *d = (pnp_dz_t *)arg;
    const mot_callbacks_t *mot = mot_load(d);
    mot_pose_t pos;
    int have_pos = 0;

    (void)period;

    // The position comes from motmod's RT-side interface, which already ran the
    // forward kinematics this cycle — not from motstat, whose accessors take
    // the reader mutex and copy the whole status struct however hot-path they
    // look. The non-zero return is carte_pos_fb_ok saying the feedback is
    // meaningless (joints not all homed), and a meaningless position must NOT
    // be turned into a "clear": an interlock that fails has to fail towards
    // "the head might be in there".
    if (mot != NULL)
        have_pos = (mot->status_get_carte_pos_fb(mot->ctx, &pos) == 0);

    for (int i = 0; i < d->n; i++) {
        const pnp_dz_scene_t *s = &d->scenes[i];
        int clear = have_pos ? scene_clear(s, pos.x, pos.y) : 0;
        if (s->free_pin != NULL)
            **s->free_pin = clear ? 1 : 0;
    }
}

// The address hal.ExportFunct is handed.
const stmak_hal_funct_t pnp_dz_check_fp = pnp_dz_check;

// --- init-time helpers -----------------------------------------------------

size_t pnp_dz_size(int nscenes, int npolys, int npoints)
{
    return sizeof(pnp_dz_t)
         + (size_t)nscenes * sizeof(pnp_dz_scene_t)
         + (size_t)npolys  * sizeof(pnp_dz_poly_t)
         + (size_t)npoints * sizeof(pnp_dz_point_t);
}

// One allocation, laid out header-then-scenes-then-polys-then-points, so a
// single hal.RTFree releases the lot. Every element type is double-aligned and
// the header ends on a double boundary, so the packing needs no padding.
void pnp_dz_layout(void *blk, int nscenes, int npolys, int npoints)
{
    pnp_dz_t      *d = (pnp_dz_t *)blk;
    unsigned char *p = (unsigned char *)blk + sizeof(pnp_dz_t);

    (void)npoints;

    d->scene_base = (pnp_dz_scene_t *)p;
    p += (size_t)nscenes * sizeof(pnp_dz_scene_t);
    d->poly_base = (pnp_dz_poly_t *)p;
    p += (size_t)npolys * sizeof(pnp_dz_poly_t);
    d->point_base = (pnp_dz_point_t *)p;

    d->n      = nscenes;
    d->scenes = d->scene_base;
    d->mot    = NULL;
}

void pnp_dz_set_point(pnp_dz_t *d, int idx, double x, double y)
{
    d->point_base[idx].x = x;
    d->point_base[idx].y = y;
}

// pnp_dz_set_poly points polygon `poly` at points [first, first+n) and derives
// its bounding box from them — the precomputation the cyclic function relies on.
// The points must already be set.
void pnp_dz_set_poly(pnp_dz_t *d, int poly, int first, int n)
{
    pnp_dz_poly_t  *p   = &d->poly_base[poly];
    pnp_dz_point_t *pts = &d->point_base[first];

    p->n    = n;
    p->pts  = pts;
    p->minx = p->maxx = pts[0].x;
    p->miny = p->maxy = pts[0].y;
    for (int i = 1; i < n; i++) {
        if (pts[i].x < p->minx) p->minx = pts[i].x;
        if (pts[i].x > p->maxx) p->maxx = pts[i].x;
        if (pts[i].y < p->miny) p->miny = pts[i].y;
        if (pts[i].y > p->maxy) p->maxy = pts[i].y;
    }
}

void pnp_dz_set_scene(pnp_dz_t *d, int scene, int first_poly, int npolys,
                      void *free_pin_slot)
{
    pnp_dz_scene_t *s = &d->scene_base[scene];

    s->n        = npolys;
    s->polys    = &d->poly_base[first_poly];
    s->free_pin = (stmak_hal_bit_t **)free_pin_slot;
}

void pnp_dz_set_mot(pnp_dz_t *d, const void *cbs)
{
    atomic_store_explicit((void *_Atomic *)&d->mot, (void *)cbs,
                          memory_order_release);
}
