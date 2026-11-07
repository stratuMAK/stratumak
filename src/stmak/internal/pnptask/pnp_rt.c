// pnp_rt.c — pnptask's cycle function. A real translation unit, so
// "make rt-effects-check" verifies it; see pnp_rt.h for what it does.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#include "pnp_rt.h"

void pnp_rt_cycle(void *arg, long period)
{
    pnp_rt_t *r = (pnp_rt_t *)arg;
    int fresh;

    // 1. Inputs, straight into the frame Go will read. Only bound fields are
    //    written; the others are 0 in every slot.
    pnp_rt_in_t *in = (pnp_rt_in_t *)stmak_tribuf_write_slot(r->to_go);
    stmak_pinmap_in(r->in, in);

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

    // 5. Publish.
    r->cycle++;
    in->cycle  = r->cycle;
    in->go_seq = out->seq;
    stmak_tribuf_publish(r->to_go);
}

const stmak_hal_funct_t pnp_rt_cycle_fp = pnp_rt_cycle;
