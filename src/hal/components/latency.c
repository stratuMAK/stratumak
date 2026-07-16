//
// latency.c - RT scheduling-latency / jitter measurement (cmod).
//
// Each instance is addf'd to one HAL thread.  Its RT function reads a
// monotonic clock every invocation, computes the scheduling latency
// (actual interval - nominal period), maintains running min/max/worst-case
// scalars on HAL pins, and pushes the raw per-cycle latency into a
// lock-free ring for a non-RT consumer (histogram / history - added in a
// later phase) to drain.
//
// Threads are dynamic: load one instance per thread you want to watch.
//
//   loadrt latency [depth=N]        # N = ring capacity in samples
//   addf   latency <thread>
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//

#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <errno.h>

#include "gomc_env.h"
#include "latency_ring.h"

// Default ring capacity (samples).  ~64k int32 ~ 256 KiB; at a 1 kHz servo
// thread that is ~65 s of backlog tolerance before the drainer must catch
// up, far more than any non-RT scheduling hiccup.  Rounded up to a power of
// two in New().
#define DEFAULT_DEPTH   65536
#define MAX_DEPTH       (1u << 24)   // 16M samples (64 MiB) cap; bounds the
                                     // power-of-two rounding so it can't overflow

// ---------------------------------------------------------------------------
// Per-instance state
// ---------------------------------------------------------------------------

typedef struct {
    // HAL pins (values in nanoseconds unless noted).
    volatile int32_t  *interval;    // OUT: last measured interval (now - last)
    volatile int32_t  *latency;     // OUT: last latency (interval - period), signed
    volatile int32_t  *lat_min;     // OUT: minimum latency seen
    volatile int32_t  *lat_max;     // OUT: maximum latency seen
    volatile int32_t  *max_jitter;  // OUT: worst-case |latency| seen (headline)
    volatile uint32_t *samples;     // OUT: number of samples measured
    volatile uint32_t *period_ns;   // OUT: nominal thread period
    volatile unsigned *reset;       // IN:  while true, clear stats + re-baseline

    // RT-private running state (not exposed as pins).
    const gomc_rtapi_t *rtapi;      // cached for get_time() in the RT path
    int64_t  last;                  // previous timestamp
    int      have_last;             // false until the first timestamp is taken
    int32_t  min_v;                 // running min latency
    int32_t  max_v;                 // running max latency
    int32_t  jit_v;                 // running worst-case |latency|
    uint32_t count;                 // running sample count

    // Raw per-cycle latency samples; drained by a non-RT consumer later.
    lat_ring_t ring;
} latency_inst_t;

typedef struct {
    cmod_env_t     env;
    cmod_t         mod;
    int            comp_id;
    char           name[64];
    latency_inst_t inst;
} latency_priv_t;

// ---------------------------------------------------------------------------
// Forward declarations
// ---------------------------------------------------------------------------

static void latency_funct(void *arg, long period);
static void latency_destroy(cmod_t *self);

// Saturate a 64-bit nanosecond value into the int32 range used by the pins,
// so a multi-second stall (interval > ~2.147 s) cannot wrap the statistics.
static inline int32_t clamp_i32(int64_t v) {
    if (v > INT32_MAX) return INT32_MAX;
    if (v < INT32_MIN) return INT32_MIN;
    return (int32_t)v;
}

// ---------------------------------------------------------------------------
// RT function - measure the scheduling interval and update stats.
// ---------------------------------------------------------------------------

static void latency_funct(void *arg, long period) {
    latency_inst_t *inst = (latency_inst_t *)arg;
    int64_t now = inst->rtapi->get_time(inst->rtapi->ctx);

    *(inst->period_ns) = (uint32_t)period;

    // Reset: clear the running stats and re-baseline the clock.  Held level
    // by the client (matches timedelta); accumulation resumes once cleared.
    if (*(inst->reset)) {
        inst->min_v = 0;
        inst->max_v = 0;
        inst->jit_v = 0;
        inst->count = 0;
        *(inst->interval)   = 0;
        *(inst->latency)    = 0;
        *(inst->lat_min)    = 0;
        *(inst->lat_max)    = 0;
        *(inst->max_jitter) = 0;
        *(inst->samples)    = 0;
        inst->last = now;
        inst->have_last = 1;
        return;
    }

    if (inst->have_last) {
        // Compute in 64-bit, then saturate into the int32 pins so a stall
        // longer than ~2.147 s cannot wrap the interval/latency/jitter.
        int64_t interval64 = now - inst->last;
        int64_t lat64 = interval64 - (int64_t)period;
        int32_t interval = clamp_i32(interval64);
        int32_t lat = clamp_i32(lat64);
        int32_t ajit = clamp_i32(lat64 < 0 ? -lat64 : lat64);

        if (inst->count == 0 || lat < inst->min_v) inst->min_v = lat;
        if (inst->count == 0 || lat > inst->max_v) inst->max_v = lat;
        if (ajit > inst->jit_v) inst->jit_v = ajit;
        inst->count++;

        *(inst->interval)   = interval;
        *(inst->latency)    = lat;
        *(inst->lat_min)    = inst->min_v;
        *(inst->lat_max)    = inst->max_v;
        *(inst->max_jitter) = inst->jit_v;
        *(inst->samples)    = inst->count;

        lat_ring_push(&inst->ring, lat);
    }

    inst->last = now;
    inst->have_last = 1;
}

// ---------------------------------------------------------------------------
// Destroy
// ---------------------------------------------------------------------------

static void latency_destroy(cmod_t *self) {
    latency_priv_t *priv = (latency_priv_t *)self->priv;
    const gomc_rtapi_t *rtapi = priv->env.rtapi;
    if (priv->comp_id > 0)
        priv->env.hal->exit(priv->env.hal->ctx, priv->comp_id);
    rtapi->free(rtapi->ctx, priv->inst.ring.buf);
    rtapi->free(rtapi->ctx, priv);
}

// ---------------------------------------------------------------------------
// New - constructor
// ---------------------------------------------------------------------------

int New(const cmod_env_t *env, const char *name,
        int argc, const char **argv, cmod_t **out)
{
    int retval;
    int depth = DEFAULT_DEPTH;

    for (int i = 0; i < argc; i++) {
        if (strncmp(argv[i], "depth=", 6) == 0) {
            int d = atoi(argv[i] + 6);
            if (d > 0) depth = d;
        }
    }
    if (depth > (int)MAX_DEPTH) depth = MAX_DEPTH;

    if (!env->hal) {
        gomc_log_errorf(env->log, name, "HAL API not available");
        return -EINVAL;
    }
    if (!env->rtapi) {
        gomc_log_errorf(env->log, name, "RTAPI not available (need get_time)");
        return -EINVAL;
    }

    // Round the requested depth up to a power of two (the ring masks).
    uint32_t cap = 1;
    while (cap < (uint32_t)depth) cap <<= 1;

    // RT-locked allocations: this component measures RT latency, so its own
    // working set must not page-fault mid-cycle.  rtapi->calloc is mlock'd
    // and pre-faulted (unlike plain calloc).
    latency_priv_t *priv = env->rtapi->calloc(env->rtapi->ctx, sizeof(*priv));
    if (!priv) return -ENOMEM;
    priv->env = *env;
    snprintf(priv->name, sizeof(priv->name), "%s", name);

    latency_inst_t *inst = &priv->inst;
    inst->rtapi = env->rtapi;  // valid for the module lifetime (env contract)

    int32_t *ringbuf = env->rtapi->calloc(env->rtapi->ctx,
                                          (size_t)cap * sizeof(int32_t));
    if (!ringbuf) {
        env->rtapi->free(env->rtapi->ctx, priv);
        return -ENOMEM;
    }
    if (lat_ring_init(&inst->ring, ringbuf, cap) != 0) {
        gomc_log_errorf(env->log, name, "ring init failed (cap=%u)", cap);
        env->rtapi->free(env->rtapi->ctx, ringbuf);
        env->rtapi->free(env->rtapi->ctx, priv);
        return -EINVAL;
    }

    priv->comp_id = env->hal->init(env->hal->ctx, name, env->dl_handle,
                                   GOMC_HAL_COMP_REALTIME);
    if (priv->comp_id < 0) {
        gomc_log_errorf(env->log, name, "hal_init failed");
        env->rtapi->free(env->rtapi->ctx, ringbuf);
        env->rtapi->free(env->rtapi->ctx, priv);
        return -1;
    }

    // Create pins.
    if ((retval = gomc_hal_pin_s32_newf(env->hal, GOMC_HAL_OUT,
            &inst->interval,   priv->comp_id, "%s.interval",   name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_s32_newf(env->hal, GOMC_HAL_OUT,
            &inst->latency,    priv->comp_id, "%s.latency",    name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_s32_newf(env->hal, GOMC_HAL_OUT,
            &inst->lat_min,    priv->comp_id, "%s.min",        name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_s32_newf(env->hal, GOMC_HAL_OUT,
            &inst->lat_max,    priv->comp_id, "%s.max",        name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_s32_newf(env->hal, GOMC_HAL_OUT,
            &inst->max_jitter, priv->comp_id, "%s.max-jitter", name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_u32_newf(env->hal, GOMC_HAL_OUT,
            &inst->samples,    priv->comp_id, "%s.samples",    name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_u32_newf(env->hal, GOMC_HAL_OUT,
            &inst->period_ns,  priv->comp_id, "%s.period",     name)) != 0) goto fail;
    if ((retval = gomc_hal_pin_bit_newf(env->hal, GOMC_HAL_IN,
            &inst->reset,      priv->comp_id, "%s.reset",      name)) != 0) goto fail;

    // Export the RT function (integer-only -> no FP, may run on a nofp thread).
    retval = env->hal->export_funct(env->hal->ctx, name,
                                    latency_funct, inst, 0, 0, priv->comp_id);
    if (retval < 0) {
        gomc_log_errorf(env->log, name, "function export failed");
        goto fail;
    }

    env->hal->ready(env->hal->ctx, priv->comp_id);

    priv->mod.priv = priv;
    priv->mod.Init = NULL;
    priv->mod.Start = NULL;
    priv->mod.Stop = NULL;
    priv->mod.Destroy = latency_destroy;

    *out = &priv->mod;
    return 0;

fail:
    env->hal->exit(env->hal->ctx, priv->comp_id);
    env->rtapi->free(env->rtapi->ctx, ringbuf);
    env->rtapi->free(env->rtapi->ctx, priv);
    return retval;
}
