// pnp_deadzone_rt.h — dead-zone clearance, evaluated in the servo cycle.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// One pin per configured DEADZONE_FILE says whether the machine point is clear
// of that drawing's zones. It answers a question the route planner cannot:
// planning keeps the head OUT of a zone, but nothing retracts a head that is
// already inside one, and a fixture that closes around the machine — a sphere,
// a door, a press — has to know the portal has left before it moves.
//
// The predicate is the Go one, ported verbatim: pkg/pnproute's
// insideAnyObstacleExcept, a bounding-box pre-check plus point-in-polygon. What
// changed is only its freshness — from a 10 ms poll of a motstat snapshot to a
// value recomputed every servo cycle — and the geometry it walks is the same
// offset zones the planner routes with, assembled once at load time
// (docs/dev/GOMOD_RT_DESIGN.md §6).
//
// All coordinates are the internal millimetres of PNPTASK_DESIGN.md D23, which
// is also what motmod works in, so the position needs no conversion.

#ifndef PNP_DEADZONE_RT_H
#define PNP_DEADZONE_RT_H

#include <stddef.h>

#include "stmak_hal.h"
#include "stmak_rt_check.h"

typedef struct {
    double x, y;
} pnp_dz_point_t;

// pnp_dz_poly_t is one offset dead zone. It carries its own bounding box
// because that is where the cyclic function spends most of its time NOT doing
// work: four comparisons reject a zone the machine is nowhere near, and the box
// is computed once at load.
typedef struct {
    int                   n;
    const pnp_dz_point_t *pts;
    double                minx, miny, maxx, maxy;
} pnp_dz_poly_t;

// pnp_dz_scene_t is one drawing: the zones of one DEADZONE_FILE, plus the pin
// that publishes the answer for it.
typedef struct {
    int                  n;
    const pnp_dz_poly_t *polys;

    // free_pin is the HAL pin's data-pointer SLOT (hal.Pin.RTDataPtr), not the
    // data cell: HAL repoints the slot when the pin is netted, so it is
    // dereferenced twice, every cycle.
    stmak_hal_bit_t **free_pin;
} pnp_dz_scene_t;

typedef struct {
    int                   n;       // one scene per DEADZONE_FILE, in INI order
    const pnp_dz_scene_t *scenes;

    // mot is the motmod callback table (const mot_callbacks_t *), or NULL.
    //
    // It is the one field that changes after the structure is built, because
    // the lookup can only happen in Start — the provider may be loaded on a
    // later HAL line — while HAL threads are already running by then. So it is
    // the one field with explicit atomics: released by pnp_dz_set_mot,
    // acquired by the cyclic function. Everything else here is immutable once
    // the threads run, which is why nothing else needs any.
    void *mot;

    // Init-time bookkeeping: where the flat arrays start inside this same
    // allocation. Used only by the setters below, never by the cyclic function.
    pnp_dz_scene_t *scene_base;
    pnp_dz_poly_t  *poly_base;
    pnp_dz_point_t *point_base;
} pnp_dz_t;

// pnp_dz_check is the cyclic function. It must run AFTER motion-controller in
// the servo thread, so it sees this cycle's position rather than the previous
// one; one cycle of staleness is harmless, but the ordering should be a
// deliberate line in the HAL file rather than an accident of where it was typed.
void pnp_dz_check(void *arg, long period) STMAK_NONBLOCKING;

// pnp_dz_check_fp is pnp_dz_check's address, as a real symbol — cgo cannot take
// the address of a C function. See hal.CFunct.
extern const stmak_hal_funct_t pnp_dz_check_fp;

// --- init-time helpers (non-RT) --------------------------------------------

size_t pnp_dz_size(int nscenes, int npolys, int npoints);
void   pnp_dz_layout(void *blk, int nscenes, int npolys, int npoints);
void   pnp_dz_set_point(pnp_dz_t *d, int idx, double x, double y);
void   pnp_dz_set_poly(pnp_dz_t *d, int poly, int first, int n);
void   pnp_dz_set_scene(pnp_dz_t *d, int scene, int first_poly, int npolys,
                        void *free_pin_slot);

// pnp_dz_set_mot publishes the motmod callback table with release semantics.
// Passing NULL retracts it, which makes every pin read "not clear".
void pnp_dz_set_mot(pnp_dz_t *d, const void *cbs);

#endif // PNP_DEADZONE_RT_H
