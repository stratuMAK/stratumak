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
// fake_fed_t is one call of the trajectory intake (mot v4) as the fake
// received it.
typedef struct {
    int              kind;      // 0 term cond, 1 line, 2 circle
    int32_t          cond;
    double           tolerance;
    mot_line_seg_t   line;
    mot_circle_seg_t circle;
} fake_fed_t;

#define FAKE_FED_CAP 64

typedef struct {
    double x, y;
    int    ok;
    int    has_snap;
    mot_snapshot_t snap;

    // The trajectory intake: what was fed since the last take, and a segment
    // id to refuse with a status (refuse_status 0: none).
    fake_fed_t fed[FAKE_FED_CAP];
    int        n_fed, lost;
    int32_t    refuse_id, refuse_status;
} fake_mot_ctx_t;

static void fake_note(fake_mot_ctx_t *c, const fake_fed_t *f) {
    if (c->n_fed < FAKE_FED_CAP)
        c->fed[c->n_fed++] = *f;
    else
        c->lost++;
}

static int32_t fake_traj_set_term_cond(void *ctx, int32_t cond, double tolerance) {
    fake_fed_t f;
    memset(&f, 0, sizeof(f));
    f.kind = 0;
    f.cond = cond;
    f.tolerance = tolerance;
    fake_note((fake_mot_ctx_t *)ctx, &f);
    return 0;
}

static int32_t fake_traj_add_line(void *ctx, const mot_line_seg_t *seg) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)ctx;
    if (c->refuse_status != 0 && seg->id == c->refuse_id)
        return c->refuse_status;
    fake_fed_t f;
    memset(&f, 0, sizeof(f));
    f.kind = 1;
    f.line = *seg;
    fake_note(c, &f);
    return 0;
}

static int32_t fake_traj_add_circle(void *ctx, const mot_circle_seg_t *seg) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)ctx;
    if (c->refuse_status != 0 && seg->id == c->refuse_id)
        return c->refuse_status;
    fake_fed_t f;
    memset(&f, 0, sizeof(f));
    f.kind = 2;
    f.circle = *seg;
    fake_note(c, &f);
    return 0;
}

// fake_mot_enable_traj fills in the trajectory intake, as a mot version 4
// provider has it.
static void fake_mot_enable_traj(mot_callbacks_t *cb) {
    cb->traj_set_term_cond = fake_traj_set_term_cond;
    cb->traj_add_line = fake_traj_add_line;
    cb->traj_add_circle = fake_traj_add_circle;
}

static void fake_mot_refuse(mot_callbacks_t *cb, int32_t id, int32_t status) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)cb->ctx;
    c->refuse_id = id;
    c->refuse_status = status;
}

// fake_mot_take copies what was fed since the last take into out (up to
// FAKE_FED_CAP) and clears it; it returns the count, and the calls lost to a
// full buffer in *lost.
static int fake_mot_take(mot_callbacks_t *cb, fake_fed_t *out, int *lost) {
    fake_mot_ctx_t *c = (fake_mot_ctx_t *)cb->ctx;
    int n = c->n_fed;
    memcpy(out, c->fed, (size_t)n * sizeof(fake_fed_t));
    *lost = c->lost;
    c->n_fed = 0;
    c->lost = 0;
    return n;
}

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

import (
	"fmt"
	"unsafe"
)

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

// Fed is one call of the trajectory intake (mot version 4) the fake took:
// a term cond, or a line or circle segment with what motctl's set_line and
// set_circle carry.
type Fed struct {
	Kind      FedKind
	Cond      int32
	Tolerance float64

	Pos            [9]float64 // x y z a b c u v w
	Center, Normal [3]float64 // circle only
	ID, MotionType int32
	Turn           int32
	Vel, IniMaxvel float64
	Acc, FeedMm    float64
}

// FedKind tells the three calls apart.
type FedKind int

const (
	FedTermCond FedKind = iota
	FedLine
	FedCircle
)

// EnableTraj makes the fake a mot version 4 provider: it takes segments
// through traj_set_term_cond, traj_add_line and traj_add_circle and records
// them until Take. Without it the three slots stay NULL.
func (f *FakeMot) EnableTraj() { C.fake_mot_enable_traj(f.cb) }

// Refuse makes the fake refuse the segment with this id, returning status
// (an EMCMOT_COMMAND_* code); status 0 refuses none.
func (f *FakeMot) Refuse(id, status int32) { C.fake_mot_refuse(f.cb, C.int32_t(id), C.int32_t(status)) }

// Take returns what was fed since the last Take, oldest first. The fake holds
// 64 calls between two takes; more is an error, as a test that takes after
// every cycle can never see it.
func (f *FakeMot) Take() ([]Fed, error) {
	var buf [C.FAKE_FED_CAP]C.fake_fed_t
	var lost C.int
	n := int(C.fake_mot_take(f.cb, &buf[0], &lost))
	out := make([]Fed, n)
	for i := 0; i < n; i++ {
		b := &buf[i]
		o := &out[i]
		o.Kind = FedKind(b.kind)
		switch o.Kind {
		case FedTermCond:
			o.Cond, o.Tolerance = int32(b.cond), float64(b.tolerance)
		case FedLine:
			l := &b.line
			o.Pos = pose9(&l.pos)
			o.ID, o.MotionType, o.Turn = int32(l.id), int32(l.motion_type), int32(l.turn)
			o.Vel, o.IniMaxvel, o.Acc, o.FeedMm = float64(l.vel), float64(l.ini_maxvel), float64(l.acc), float64(l.feed_mm_per_min)
		case FedCircle:
			c := &b.circle
			o.Pos = pose9(&c.pos)
			o.Center = [3]float64{float64(c.center.x), float64(c.center.y), float64(c.center.z)}
			o.Normal = [3]float64{float64(c.normal.x), float64(c.normal.y), float64(c.normal.z)}
			o.ID, o.MotionType, o.Turn = int32(c.id), int32(c.motion_type), int32(c.turn)
			o.Vel, o.IniMaxvel, o.Acc, o.FeedMm = float64(c.vel), float64(c.ini_maxvel), float64(c.acc), float64(c.feed_mm_per_min)
		}
	}
	if lost != 0 {
		return out, fmt.Errorf("dztest: %d trajectory calls lost (more than %d between two takes)", int(lost), C.FAKE_FED_CAP)
	}
	return out, nil
}

func pose9(p *C.mot_pose_t) [9]float64 {
	return [9]float64{float64(p.x), float64(p.y), float64(p.z), float64(p.a), float64(p.b),
		float64(p.c), float64(p.u), float64(p.v), float64(p.w)}
}
