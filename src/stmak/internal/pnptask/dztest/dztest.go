// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package dztest drives pnptask's cyclic dead-zone check from a test.
//
// cgo is not permitted in _test.go files, and the alternative — a test-only
// Invoke on the production deadzoneRT — would put a "call the servo function
// from Go" door in the module itself. So the two things a test needs live here
// instead: a stand-in for motmod's callback table, and a direct call into the
// cyclic function.
//
// Only the pnptask test binary imports it.
package dztest

/*
#cgo CFLAGS: -I${SRCDIR}/.. -I${SRCDIR}/../../../pkg/cmodule -I${SRCDIR}/../../../generated/gmi/mot

#include <stdlib.h>
#include <string.h>

#include "pnp_deadzone_rt.h"
#include "mot_api.h"

// fake_mot_ctx_t stands in for motmod's instance: a position and the
// carte_pos_fb_ok flag that decides whether it means anything.
typedef struct {
    double x, y;
    int    ok;
    int    has_snap;
    mot_snapshot_t snap;
} fake_mot_ctx_t;

static int32_t fake_get_snapshot(void *ctx, mot_snapshot_t *s) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)ctx;
    *s = c->snap;
    return 0;
}

static void fake_mot_set_snapshot(mot_callbacks_t *cb, int32_t inpos, int32_t depth, int32_t id,
                                  double x, double y, uint32_t homed) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)cb->ctx;
    memset(&c->snap, 0, sizeof(c->snap));
    c->snap.enabled = 1;
    c->snap.inpos = inpos;
    c->snap.queue_depth = depth;
    c->snap.id = id;
    c->snap.carte_pos_fb.x = x;
    c->snap.carte_pos_fb.y = y;
    c->snap.carte_pos_fb_ok = 1;
    c->snap.num_joints = 3;
    c->snap.joints_homed = homed;
    cb->status_get_snapshot = fake_get_snapshot;
}

static int32_t fake_get_carte_pos_fb(void *ctx, mot_pose_t *pos) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)ctx;
    if (!c->ok)
        return -1;
    memset(pos, 0, sizeof(*pos));
    pos->x = c->x;
    pos->y = c->y;
    return 0;
}

// fake_mot_new builds a callback table with only status_get_carte_pos_fb
// filled in — the one entry the dead-zone check calls. Every other slot stays
// NULL, so a check that reached for anything else would fault loudly here
// rather than pass quietly.
static mot_callbacks_t *fake_mot_new(void) {
    fake_mot_ctx_t  *c  = (fake_mot_ctx_t *)calloc(1, sizeof(*c));
    mot_callbacks_t *cb = (mot_callbacks_t *)calloc(1, sizeof(*cb));
    if (!c || !cb) { free(c); free(cb); return NULL; }
    c->ok = 1;
    cb->ctx = c;
    cb->status_get_carte_pos_fb = fake_get_carte_pos_fb;
    return cb;
}

static void fake_mot_free(mot_callbacks_t *cb) {
    if (!cb) return;
    free(cb->ctx);
    free(cb);
}

static void fake_mot_set(mot_callbacks_t *cb, double x, double y, int ok) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)cb->ctx;
    c->x  = x;
    c->y  = y;
    c->ok = ok;
}

static void invoke(void *arg, long period) {
    pnp_dz_check(arg, period);
}
*/
import "C"

import "unsafe"

// FakeMot is a stand-in motmod callback table.
type FakeMot struct{ cb *C.mot_callbacks_t }

// NewFakeMot allocates one, reporting a valid feedback at the origin.
func NewFakeMot() *FakeMot { return &FakeMot{cb: C.fake_mot_new()} }

// Ptr is the pointer to hand deadzoneRT.setMot.
func (f *FakeMot) Ptr() unsafe.Pointer { return unsafe.Pointer(f.cb) }

// Set moves the reported position. ok is carte_pos_fb_ok: false means the
// forward kinematics could not be run, and the dead-zone check must then
// publish "not clear" whatever the coordinates say.
func (f *FakeMot) Set(x, y float64, ok bool) {
	c := C.int(0)
	if ok {
		c = 1
	}
	C.fake_mot_set(f.cb, C.double(x), C.double(y), c)
}

// Free releases the table. The caller must retract it from the scene first.
// SetSnapshot makes the fake serve a motion snapshot (status_get_snapshot);
// until it is called the callback is absent, as on a provider older than
// mot version 3.
func (f *FakeMot) SetSnapshot(inpos bool, depth, id int32, x, y float64, homed uint32) {
	ip := C.int32_t(0)
	if inpos {
		ip = 1
	}
	C.fake_mot_set_snapshot(f.cb, ip, C.int32_t(depth), C.int32_t(id), C.double(x), C.double(y), C.uint32_t(homed))
}

func (f *FakeMot) Free() { C.fake_mot_free(f.cb); f.cb = nil }

// Invoke runs the cyclic function once, as a HAL thread would.
func Invoke(arg unsafe.Pointer, periodNsec int64) {
	C.invoke(arg, C.long(periodNsec))
}
