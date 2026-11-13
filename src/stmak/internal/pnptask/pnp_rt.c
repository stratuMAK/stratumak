// pnp_rt.c — pnptask's cycle function. A real translation unit, so
// "make rt-effects-check" verifies it; see pnp_rt.h for what it does.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#include "pnp_rt.h"

static int rose(uint8_t now, uint8_t before) STMAK_NONBLOCKING;
static int rose(uint8_t now, uint8_t before)
{
    return now && !before;
}

// count_reset folds one station's two reset edges into its counter: the
// latest wins, both in one cycle is a conflict.
static void count_reset(pnp_rt_edge_reset_t *e, int set, int clear,
                        int64_t set_value, int64_t clear_value) STMAK_NONBLOCKING;
static void count_reset(pnp_rt_edge_reset_t *e, int set, int clear,
                        int64_t set_value, int64_t clear_value)
{
    if (set && clear) {
        e->conflicts++;
    } else if (set || clear) {
        e->value = set ? set_value : clear_value;
        e->count++;
    }
}

// pnp_rt_handshakes is §4.4 for one cycle (pnp_rt.h, the handshakes): end what
// Go has finished, clear the latch on a reset, latch what Go raised, accept a
// new request. In that order: a job's busy falls in the cycle its outcome
// latches, a reset only clears what was latched before it, and a request is
// judged against the state this cycle ends with.
static void pnp_rt_handshakes(pnp_rt_t *r, const pnp_rt_in_t *in,
                              const pnp_rt_out_t *go) STMAK_NONBLOCKING;
static void pnp_rt_handshakes(pnp_rt_t *r, const pnp_rt_in_t *in,
                              const pnp_rt_out_t *go)
{
    const pnp_rt_in_t *p = &r->prev;

    if (r->busy) {
        if (r->busy_park ? go->park_done == r->park_seq : go->job_done == r->job.seq)
            r->busy = 0;
    }

    if (r->primed && rose(in->error_reset, p->error_reset) && r->latch.set) {
        r->latch.set = 0;
        r->latch.id = 0;
        r->latch.station = 0;
        r->latch.missing = 0;
        r->edges.error_reset++;
    }

    if (go->raise_seq != r->raise_seen) {
        r->raise_seen = go->raise_seq;
        if (!r->latch.set) {
            r->latch.set = 1;
            r->latch.id = go->raise_id;
            r->latch.station = go->raise_station;
            r->latch.missing = go->raise_missing;
        }
    }

    if (!r->primed || r->busy || r->latch.set)
        return;
    if (rose(in->start_job, p->start_job)) {
        r->job.seq++;
        r->job.origin_id = in->origin_id;
        r->job.dest_id = in->dest_id;
        r->job.process_step = in->process_step;
        r->job.deadzone_select = in->deadzone_select;
        r->job.stay_near = in->stay_near;
        r->busy = 1;
        r->busy_park = 0;
    } else if (!in->estop_on && rose(in->park, p->park)) {
        r->park_seq++;
        r->busy = 1;
        r->busy_park = 1;
    }
}

// pnp_rt_scene_and_watch is the scene lock and the lift watch for one cycle
// (pnp_rt.h). in->mot is this cycle's snapshot.
static void pnp_rt_scene_and_watch(pnp_rt_t *r, const pnp_rt_in_t *in,
                                   const pnp_rt_out_t *go) STMAK_NONBLOCKING;
static void pnp_rt_scene_and_watch(pnp_rt_t *r, const pnp_rt_in_t *in,
                                   const pnp_rt_out_t *go)
{
    if (!go->scene_req)
        r->scene_granted = 0;
    else if (!r->scene_granted && !in->scene_hold)
        r->scene_granted = 1;

    if (go->watch_seq != r->watch_fired && in->mot_ok && in->mot.id > go->watch_id)
        r->watch_fired = go->watch_seq;
}

// pnp_rt_feed_one hands motmod one record (pnp_rt.h, the leg feed) and returns
// motmod's status, or 1 when the provider has no intake for its kind (a record
// of an unknown kind counts as refused).
static int32_t pnp_rt_feed_one(const mot_callbacks_t *mot, const pnp_rt_feed_rec_t *rec) STMAK_NONBLOCKING;
static int32_t pnp_rt_feed_one(const mot_callbacks_t *mot, const pnp_rt_feed_rec_t *rec)
{
    switch (rec->kind) {
    case PNP_FEED_TERM_COND:
        return mot->traj_set_term_cond(mot->ctx, rec->term_cond, rec->tolerance);
    case PNP_FEED_LINE: {
        mot_line_seg_t seg;
        seg.pos = rec->pos;
        seg.id = rec->id;
        seg.motion_type = rec->motion_type;
        seg.turn = rec->turn;
        seg.vel = rec->vel;
        seg.ini_maxvel = rec->ini_maxvel;
        seg.acc = rec->acc;
        seg.feed_mm_per_min = rec->feed_mm_per_min;
        return mot->traj_add_line(mot->ctx, &seg);
    }
    case PNP_FEED_CIRCLE: {
        mot_circle_seg_t seg;
        seg.pos = rec->pos;
        seg.center = rec->center;
        seg.normal = rec->normal;
        seg.id = rec->id;
        seg.motion_type = rec->motion_type;
        seg.turn = rec->turn;
        seg.vel = rec->vel;
        seg.ini_maxvel = rec->ini_maxvel;
        seg.acc = rec->acc;
        seg.feed_mm_per_min = rec->feed_mm_per_min;
        return mot->traj_add_circle(mot->ctx, &seg);
    }
    default:
        return 1;
    }
}

// pnp_rt_feed is the leg feed for one run (pnp_rt.h): skip what a cancel
// voided, skip everything under estop, hold behind a refusal until the loop
// cancels, else feed one record below feed_end.
static void pnp_rt_feed(pnp_rt_t *r, const pnp_rt_in_t *in, const pnp_rt_out_t *go,
                        const mot_callbacks_t *mot) STMAK_NONBLOCKING;
static void pnp_rt_feed(pnp_rt_t *r, const pnp_rt_in_t *in, const pnp_rt_out_t *go,
                        const mot_callbacks_t *mot)
{
    if ((int32_t)(go->feed_begin - r->fed) > 0)
        r->fed = go->feed_begin;
    if (r->feed_hold && (int32_t)(go->feed_begin - r->hold_at) >= 0)
        r->feed_hold = 0;
    if (in->estop_on) {
        if ((int32_t)(go->feed_end - r->fed) > 0)
            r->fed = go->feed_end;
        return;
    }
    if (r->feed_hold || (int32_t)(go->feed_end - r->fed) <= 0 || r->feed == NULL)
        return;
    // A truth test on each slot, as for status_get_snapshot (a provider
    // older than mot version 4 has none of them).
    if (mot == NULL || !mot->traj_set_term_cond || !mot->traj_add_line || !mot->traj_add_circle)
        return;

    const pnp_rt_feed_rec_t *rec = &r->feed[r->fed % PNP_RT_FEED_CAP];
    int32_t status = pnp_rt_feed_one(mot, rec);
    r->fed++;
    if (status != 0) {
        r->feed_err_seq++;
        r->feed_err_id = rec->id;
        r->feed_err_status = status;
        r->feed_hold = 1;
        r->hold_at = r->fed;
    }
}

// pnp_rt_stall is the stall check (§7.2): rings outstanding and the loop's
// taken count standing still for task_timeout latch TASK_STALLED. What counts
// is that taken moves, not that it has caught up: a loop that takes every ring
// while the next one is already out (an input that changes every cycle) is
// running. While the stall lasts the fault comes back the cycle after an
// error-reset, so no request is accepted for a loop that is not there to run
// it. The function leaves motion alone -- motmod finishes what is queued and
// stands -- and the latch is what the PLC's error path already reacts to.
static void pnp_rt_stall(pnp_rt_t *r, const pnp_rt_out_t *go, long period) STMAK_NONBLOCKING;
static void pnp_rt_stall(pnp_rt_t *r, const pnp_rt_out_t *go, long period)
{
    int took = go->taken != r->taken_seen;

    r->taken_seen = go->taken;
    if (go->task_timeout == 0 || r->change == go->taken || took) {
        r->stall_ns = 0;
        return;
    }
    r->stall_ns += (uint64_t)period;
    if (r->stall_ns >= go->task_timeout && !r->latch.set) {
        r->latch.set = 1;
        r->latch.id = PNP_RT_TASK_STALLED;
        r->latch.station = 0;
        r->latch.missing = 0;
    }
}

// pnp_rt_changed builds this cycle's signature of what the loop acts on
// (pnp_rt_sig_t) and reports whether it differs from the last cycle's.
static int pnp_rt_changed(pnp_rt_t *r, const pnp_rt_in_t *in, const pnp_rt_out_t *go) STMAK_NONBLOCKING;
static int pnp_rt_changed(pnp_rt_t *r, const pnp_rt_in_t *in, const pnp_rt_out_t *go)
{
    pnp_rt_sig_t *g = &r->sig;
    const uint8_t *src = (const uint8_t *)in + offsetof(pnp_rt_in_t, jog_speed);

    for (size_t i = 0; i < PNP_RT_SIG_INPUTS; i++)
        g->inputs[i] = src[i];
    g->edges = in->edges;
    g->job = in->job;
    g->latch = in->latch;
    g->park_seq = in->park_seq;
    g->raise_seen = in->raise_seen;
    g->watch_fired = in->watch_fired;
    g->busy = in->busy;
    g->busy_park = in->busy_park;
    g->scene_granted = in->scene_granted;
    g->mot_ok = in->mot_ok;
    g->command_num_echo = in->mot.command_num_echo;
    g->command_status = in->mot.command_status;
    g->motion_state = in->mot.motion_state;
    g->enabled = in->mot.enabled;
    g->inpos = in->mot.inpos;
    g->queue_depth = in->mot.queue_depth;
    g->id = in->mot.id;
    g->carte_pos_cmd_ok = in->mot.carte_pos_cmd_ok;
    g->carte_pos_fb_ok = in->mot.carte_pos_fb_ok;
    g->joints_homed = in->mot.joints_homed;
    g->joints_homing = in->mot.joints_homing;
    g->feed_err_seq = in->feed_err_seq;
    g->feed_through = in->fed == go->feed_end;

    const uint8_t *a = (const uint8_t *)&r->sig;
    uint8_t       *b = (uint8_t *)&r->sig_prev;
    int changed = 0;
    for (size_t i = 0; i < sizeof(pnp_rt_sig_t); i++) {
        if (a[i] != b[i]) {
            changed = 1;
            b[i] = a[i];
        }
    }
    return changed;
}

// pnp_rt_edges counts this cycle's rising edges of the other command inputs
// (pnp_rt.h, the edges block), keeps this cycle's levels for the next, and
// hands the counts to the frame.
static void pnp_rt_edges(pnp_rt_t *r, pnp_rt_in_t *in) STMAK_NONBLOCKING;
static void pnp_rt_edges(pnp_rt_t *r, pnp_rt_in_t *in)
{
    const pnp_rt_in_t *p = &r->prev;
    pnp_rt_edges_t    *e = &r->edges;

    if (r->primed) {
        int armed  = !in->estop_on;
        int manual = armed && !in->auto_enable;

        if (armed && rose(in->machine_on, p->machine_on))
            e->machine_on++;
        if (armed && !r->busy && rose(in->home, p->home))
            e->home++;
        for (int i = 0; i < r->n_pickers; i++) {
            if (manual && rose(in->picker[i].manual_open, p->picker[i].manual_open))
                e->picker[i].open++;
            if (manual && rose(in->picker[i].manual_close, p->picker[i].manual_close))
                e->picker[i].close++;
        }
        for (int i = 0; i < r->n_trays; i++)
            count_reset(&e->tray[i],
                        rose(in->tray[i].set_full, p->tray[i].set_full),
                        rose(in->tray[i].set_empty, p->tray[i].set_empty),
                        (int64_t)in->tray[i].step, -1);
        for (int i = 0; i < r->n_procs; i++)
            count_reset(&e->proc[i],
                        rose(in->proc[i].set_has_material, p->proc[i].set_has_material),
                        rose(in->proc[i].set_empty, p->proc[i].set_empty),
                        1, 0);
    }
    r->primed = 1;

    r->prev.machine_on  = in->machine_on;
    r->prev.home        = in->home;
    r->prev.park        = in->park;
    r->prev.start_job   = in->start_job;
    r->prev.error_reset = in->error_reset;
    for (int i = 0; i < r->n_pickers; i++)
        r->prev.picker[i] = in->picker[i];
    for (int i = 0; i < r->n_trays; i++)
        r->prev.tray[i] = in->tray[i];
    for (int i = 0; i < r->n_procs; i++)
        r->prev.proc[i] = in->proc[i];

    in->edges = *e;
}

void pnp_rt_cycle(void *arg, long period)
{
    pnp_rt_t *r = (pnp_rt_t *)arg;
    int fresh;

    r->cycle++;
    r->servo_ns += (uint64_t)period;

    // 1. Inputs, straight into the frame Go will read. Only bound fields are
    //    written; the others are 0 in every slot.
    pnp_rt_in_t *in = (pnp_rt_in_t *)stmak_tribuf_write_slot(r->to_go);
    stmak_pinmap_in(r->in, in);

    // 2. Motion, this cycle (the function runs after motion-controller).
    const mot_callbacks_t *mot = r->dz != NULL ? (const mot_callbacks_t *)pnp_dz_mot(r->dz) : NULL;
    in->mot_ok = 0;
    // A truth test, not "!= NULL": comparing the nonblocking-typed pointer
    // with (void *)0 reads to -Wfunction-effects as adding the attribute.
    if (mot != NULL && mot->status_get_snapshot &&
        mot->status_get_snapshot(mot->ctx, &in->mot) == 0)
        in->mot_ok = 1;

    // 3. Go's latest frame.
    const pnp_rt_out_t *go = (const pnp_rt_out_t *)stmak_tribuf_read(r->to_rt, &fresh);

    // 4., 5. The stall check, the handshakes, the scene lock and the lift
    //    watch, then the other edges (home is gated on the busy this cycle
    //    ends with).
    pnp_rt_stall(r, go, period);
    pnp_rt_handshakes(r, in, go);
    pnp_rt_scene_and_watch(r, in, go);
    pnp_rt_edges(r, in);

    // 5a. The leg feed. The frame says what the snapshot above has seen:
    //    a record fed now is motion-controller's in the next cycle, so it
    //    counts from the next frame on.
    in->fed = r->fed;
    pnp_rt_feed(r, in, go, mot);
    in->feed_err_seq    = r->feed_err_seq;
    in->feed_err_id     = r->feed_err_id;
    in->feed_err_status = r->feed_err_status;

    // 6. The outputs, all at once: Go's frame with RT's pins put in.
    pnp_rt_out_t *o = &r->composed;
    *o = *go;
    o->cycle = r->cycle;
    o->busy = (uint8_t)r->busy;
    o->error = r->latch.set;
    o->error_id = r->latch.id;
    o->error_station = r->latch.station;
    for (int i = 0; i < r->n_pickers; i++)
        o->picker[i].missing = (uint8_t)((r->latch.missing >> i) & 1u);
    o->scene_busy = (uint8_t)r->scene_granted;
    for (int i = 0; i < r->n_pickers; i++) {
        if (in->mot_ok) {
            r->pos_x[i] = in->mot.carte_pos_fb.x + go->picker[i].x_offset;
            r->pos_y[i] = in->mot.carte_pos_fb.y + go->picker[i].y_offset;
        }
        o->picker[i].pos_x = r->pos_x[i];
        o->picker[i].pos_y = r->pos_y[i];
        o->picker[i].pos_x_mu = r->pos_x[i] * go->linear_units;
        o->picker[i].pos_y_mu = r->pos_y[i] * go->linear_units;
    }
    if (r->watch_fired == go->watch_seq && go->watch_release != 0) {
        for (int i = 0; i < r->n_procs; i++)
            if (go->watch_release & (1u << i))
                o->proc[i].release = 0;
    }
    stmak_pinmap_out(r->out, o);

    // 7. The dead-zone check, as it has always run.
    if (r->dz != NULL)
        pnp_dz_check(r->dz, period);

    // 8. Publish, then count the run: a cycle count Go reads means that frame
    //    is out.
    in->cycle      = r->cycle;
    in->go_seq     = go->seq;
    in->job        = r->job;
    in->latch      = r->latch;
    in->park_seq   = r->park_seq;
    in->raise_seen = r->raise_seen;
    in->busy       = (uint8_t)r->busy;
    in->busy_park  = (uint8_t)r->busy_park;
    in->scene_granted = (uint8_t)r->scene_granted;
    in->watch_fired   = r->watch_fired;
    in->servo_ns      = r->servo_ns;

    // The change counter: what the loop acts on changed, or its wake time came
    // (once per wake_at).
    int ring = pnp_rt_changed(r, in, go);
    if (ring)
        r->sig_changes++;
    in->sig_changes = r->sig_changes;
    if (go->wake_at != 0 && r->servo_ns >= go->wake_at && r->wake_rung != go->wake_at) {
        r->wake_rung = go->wake_at;
        ring = 1;
    }
    if (ring)
        r->change++;
    in->change = r->change;

    stmak_tribuf_publish(r->to_go);
    if (r->bell != NULL) {
        if (ring)
            stmak_doorbell_ring(r->bell);
        stmak_doorbell_tick(r->bell);
    }
}

const stmak_hal_funct_t pnp_rt_cycle_fp = pnp_rt_cycle;
