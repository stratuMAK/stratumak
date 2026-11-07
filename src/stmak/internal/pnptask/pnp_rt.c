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

// pnp_rt_edges counts this cycle's rising edges (pnp_rt.h, the edges block)
// and hands the counts to the frame.
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
        if (armed && rose(in->home, p->home))
            e->home++;
        if (armed && rose(in->park, p->park))
            e->park++;
        if (rose(in->error_reset, p->error_reset))
            e->error_reset++;
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

    // 1. Inputs, straight into the frame Go will read. Only bound fields are
    //    written; the others are 0 in every slot.
    pnp_rt_in_t *in = (pnp_rt_in_t *)stmak_tribuf_write_slot(r->to_go);
    stmak_pinmap_in(r->in, in);
    pnp_rt_edges(r, in);

    // 2. Go's outputs, all at once.
    const pnp_rt_out_t *out = (const pnp_rt_out_t *)stmak_tribuf_read(r->to_rt, &fresh);
    stmak_pinmap_out(r->out, out);

    // 3. A start-job clear, in the same cycle as the outputs that came with it
    //    (a job's busy falling with its outcome). Written only on a change, so
    //    the requester's raise is never overwritten otherwise.
    if (out->start_job_clears != r->start_job_clears) {
        r->start_job_clears = out->start_job_clears;
        if (r->start_job != NULL)
            *(volatile uint8_t *)*r->start_job = 0;
    }

    // 4. The dead-zone check, as it has always run.
    if (r->dz != NULL)
        pnp_dz_check(r->dz, period);

    // 5. Motion, this cycle (the function runs after motion-controller).
    const mot_callbacks_t *mot = r->dz != NULL ? (const mot_callbacks_t *)pnp_dz_mot(r->dz) : NULL;
    in->mot_ok = 0;
    // A truth test, not "!= NULL": comparing the nonblocking-typed pointer
    // with (void *)0 reads to -Wfunction-effects as adding the attribute.
    if (mot != NULL && mot->status_get_snapshot &&
        mot->status_get_snapshot(mot->ctx, &in->mot) == 0)
        in->mot_ok = 1;

    // 6. Publish, then count the run: a cycle count Go reads means that frame
    //    is out.
    r->cycle++;
    in->cycle  = r->cycle;
    in->go_seq = out->seq;
    stmak_tribuf_publish(r->to_go);
    if (r->bell != NULL)
        stmak_doorbell_tick(r->bell);
}

const stmak_hal_funct_t pnp_rt_cycle_fp = pnp_rt_cycle;
