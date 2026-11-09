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

    // 1. Inputs, straight into the frame Go will read. Only bound fields are
    //    written; the others are 0 in every slot.
    pnp_rt_in_t *in = (pnp_rt_in_t *)stmak_tribuf_write_slot(r->to_go);
    stmak_pinmap_in(r->in, in);

    // 2. Go's latest frame.
    const pnp_rt_out_t *go = (const pnp_rt_out_t *)stmak_tribuf_read(r->to_rt, &fresh);

    // 3., 4. The handshakes, then the other edges (home is gated on the busy
    //    this cycle ends with).
    pnp_rt_handshakes(r, in, go);
    pnp_rt_edges(r, in);

    // 5. The outputs, all at once: Go's frame with RT's pins put in.
    pnp_rt_out_t *o = &r->composed;
    *o = *go;
    o->cycle = r->cycle;
    o->busy = (uint8_t)r->busy;
    o->error = r->latch.set;
    o->error_id = r->latch.id;
    o->error_station = r->latch.station;
    for (int i = 0; i < r->n_pickers; i++)
        o->picker[i].missing = (uint8_t)((r->latch.missing >> i) & 1u);
    stmak_pinmap_out(r->out, o);

    // 6. The dead-zone check, as it has always run.
    if (r->dz != NULL)
        pnp_dz_check(r->dz, period);

    // 7. Motion, this cycle (the function runs after motion-controller).
    const mot_callbacks_t *mot = r->dz != NULL ? (const mot_callbacks_t *)pnp_dz_mot(r->dz) : NULL;
    in->mot_ok = 0;
    // A truth test, not "!= NULL": comparing the nonblocking-typed pointer
    // with (void *)0 reads to -Wfunction-effects as adding the attribute.
    if (mot != NULL && mot->status_get_snapshot &&
        mot->status_get_snapshot(mot->ctx, &in->mot) == 0)
        in->mot_ok = 1;

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
    stmak_tribuf_publish(r->to_go);
    if (r->bell != NULL)
        stmak_doorbell_tick(r->bell);
}

const stmak_hal_funct_t pnp_rt_cycle_fp = pnp_rt_cycle;
