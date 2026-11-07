// rtbridgetest_rt.c — the RT bridge test function. A real translation unit, so
// "make rt-effects-check" verifies it and, through it, the bridge headers'
// STMAK_NONBLOCKING claims.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#include "rtbridgetest_rt.h"

static int pins_differ(const rbt_pins_t *a, const rbt_pins_t *b) STMAK_NONBLOCKING;
static int pins_differ(const rbt_pins_t *a, const rbt_pins_t *b)
{
    return a->B != b->B || a->S != b->S || a->U != b->U || a->F != b->F;
}

void rbt_cycle(void *arg, long period)
{
    rbt_t *r = (rbt_t *)arg;
    rbt_pins_t in = {0};
    int fresh;

    (void)period;

    stmak_pinmap_in(r->in, &in);

    const rbt_go_frame_t *g = (const rbt_go_frame_t *)stmak_tribuf_read(r->to_rt, &fresh);
    stmak_pinmap_out(r->out, &g->Out);

    r->cycle++;
    rbt_rt_frame_t *f = (rbt_rt_frame_t *)stmak_tribuf_write_slot(r->to_go);
    f->Cycle = r->cycle;
    f->GoSeq = g->Seq;
    f->In    = in;
    stmak_tribuf_publish(r->to_go);

    stmak_doorbell_tick(r->bell);
    int changed = !r->primed || pins_differ(&in, &r->last_in) || g->Seq != r->last_seq;
    r->primed   = 1;
    r->last_in  = in;
    r->last_seq = g->Seq;
    if (changed)
        stmak_doorbell_ring(r->bell);
}

const stmak_hal_funct_t rbt_cycle_fp = rbt_cycle;
