//
// latency.c - RT scheduling-latency / jitter measurement (cmod).
//
// Each instance is addf'd to one HAL thread.  Its RT function reads a
// monotonic clock every invocation, computes the scheduling latency
// (actual interval - nominal period), maintains running min/max/worst-case
// scalars on HAL pins, and pushes the raw per-cycle latency into a
// lock-free ring.  A non-RT drainer thread empties the ring into server-side
// aggregates - a histogram, running statistics, and a downsampled time-series
// ring for the plot - exposed over a generated GMI REST API (get_status,
// get_histogram, get_history, reset).  Because the aggregates live in the cmod
// and accumulate continuously, they survive client disconnects, are identical
// for every client, and are complete for late-connecting clients.
//
// Threads are dynamic: load one instance per thread you want to watch.  The
// instance name comes from the loader's angle-bracket name list; without it
// every load defaults to the module basename and a second load collides with
// "duplicate component name".  Each instance registers its own GMI API under
// its name, so the UI can enumerate the threads from the registry.
//
//   loadrt latency <NAME> [depth=N] [bins=M] [binsize=W]
//   addf   NAME THREAD
//
// e.g. one instance per thread, with their own settings:
//
//   loadrt latency <lat.base>  binsize=200
//   loadrt latency <lat.servo> binsize=1000
//   addf   lat.base  base-thread
//   addf   lat.servo servo-thread
//
// Note an instance cannot be named after the thread it watches: HAL gives both
// a `.time` pin, so the names would collide ("duplicate variable").
//
// (A single load may also name several instances at once: <a,b,c> - they then
// share the same arguments.)
//
//   depth   ring capacity in samples (default 65536)
//   bins    histogram bin count (default 256, rounded to a multiple of 4)
//   binsize initial histogram bin width in ns (default 1000; autoscales)
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//

#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <errno.h>
#include <math.h>
#include <pthread.h>
#include <unistd.h>

#include "gomc_env.h"
#include "latency_api.h"
#include "latency_ring.h"
#include "latency_hist.h"
#include "latency_history.h"

#define DEFAULT_DEPTH    65536
#define MAX_DEPTH        (1u << 24)   // 16M samples (64 MiB) cap
#define DEFAULT_BINS     256
#define DEFAULT_BINSIZE  1000         // initial histogram bin width (ns) = 1 us
#define DRAIN_BATCH      4096         // samples copied per drain iteration
#define DRAIN_POLL_US    20000        // sleep when the ring is empty (20 ms)
#define HISTORY_BUCKET_MS 100         // plot-history bucket width
#define HISTORY_BUCKETS   6000        // retained buckets (100 ms x 6000 = 10 min)

// ---------------------------------------------------------------------------
// Per-instance state
// ---------------------------------------------------------------------------

// RT-facing state (arg to the RT function).
typedef struct {
    // HAL pins (values in nanoseconds unless noted).
    volatile int32_t  *interval;    // OUT: last measured interval (now - last)
    volatile int32_t  *latency;     // OUT: last latency (interval - period)
    volatile int32_t  *lat_min;     // OUT: minimum latency seen
    volatile int32_t  *lat_max;     // OUT: maximum latency seen
    volatile int32_t  *max_jitter;  // OUT: worst-case |latency| seen (headline)
    volatile uint32_t *samples;     // OUT: number of samples measured
    volatile uint32_t *period_ns;   // OUT: nominal thread period
    volatile unsigned *reset;       // IN:  while true, clear stats + re-baseline

    const gomc_rtapi_t *rtapi;      // cached for get_time() in the RT path
    int64_t  last;                  // previous timestamp
    int      have_last;             // false until the first timestamp is taken
    int32_t  min_v;                 // running min latency
    int32_t  max_v;                 // running max latency
    int32_t  jit_v;                 // running worst-case |latency|
    uint32_t count;                 // running sample count

    uint32_t reset_gen;             // bumped by API reset() / reset-pin edge; watched by RT + drainer
    uint32_t rt_gen_seen;           // RT's last-observed reset_gen
    unsigned reset_prev;            // previous reset-pin level (for edge detect)

    lat_ring_t ring;                // raw per-cycle latency samples (SPSC)
} latency_inst_t;

typedef struct {
    cmod_env_t     env;
    cmod_t         mod;
    int            comp_id;
    char           name[64];
    latency_inst_t inst;

    // Non-RT server side (drainer thread + API callbacks).
    hist_t          hist;           // histogram aggregate, guarded by `lock`
    uint32_t       *hist_bins;      // bin storage for `hist`
    int32_t         default_binsize; // bin width restored when autoscale is re-enabled
    history_t       history;        // plot time-series ring, guarded by `lock`
    hbucket_t      *hist_ring;      // bucket storage for `history`
    pthread_mutex_t lock;           // serialises drainer writes vs API reads
    pthread_t       io_tid;         // drainer thread
    volatile int    io_stop;        // exit request for the drainer
    int             io_running;     // drainer was started (join guard)
    uint32_t        drain_gen_seen; // drainer's last-observed reset_gen

    latency_callbacks_t cb;         // GMI callbacks (ctx = this priv)
} latency_priv_t;

// ---------------------------------------------------------------------------
// Forward declarations
// ---------------------------------------------------------------------------

static void latency_funct(void *arg, long period);
static void *drain_thread(void *arg);
static void latency_destroy(cmod_t *self);

static latency_latency_status_t    gmi_latency_get_status(void *ctx);
static latency_latency_histogram_t gmi_latency_get_histogram(void *ctx);
static latency_latency_history_t   gmi_latency_get_history(void *ctx, int32_t seconds);
static bool                        gmi_latency_configure(void *ctx, int32_t binWidthNs);
static bool                        gmi_latency_reset(void *ctx);

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

    // A rising edge on the HAL `reset` pin is routed through reset_gen, the
    // same generation counter the GMI reset() action bumps.  Both reset paths
    // therefore clear the RT scalars here AND the drainer's histogram (which
    // watches reset_gen), keeping the two views consistent.
    unsigned rpin = *(inst->reset);
    if (rpin && !inst->reset_prev)
        __atomic_fetch_add(&inst->reset_gen, 1, __ATOMIC_RELAXED);
    inst->reset_prev = rpin;

    uint32_t gen = __atomic_load_n(&inst->reset_gen, __ATOMIC_RELAXED);
    if (gen != inst->rt_gen_seen) {
        inst->rt_gen_seen = gen;
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
// Drainer thread - empty the ring into the histogram/statistics.
// ---------------------------------------------------------------------------

static void *drain_thread(void *arg) {
    latency_priv_t *priv = (latency_priv_t *)arg;
    latency_inst_t *inst = &priv->inst;
    int32_t batch[DRAIN_BATCH];

    while (!priv->io_stop) {
        // One timestamp per iteration: samples are drained within a drain
        // interval (20 ms) of being produced, which is well inside the 100 ms
        // history bucket.
        int64_t now = inst->rtapi->get_time(inst->rtapi->ctx);

        // React to a reset: drop any backlog still in the ring and clear the
        // aggregates, so pre-reset samples don't leak into the fresh histogram.
        // The history epoch re-bases to now, so the plot's time axis restarts
        // at 0 for every client.
        uint32_t gen = __atomic_load_n(&inst->reset_gen, __ATOMIC_RELAXED);
        if (gen != priv->drain_gen_seen) {
            priv->drain_gen_seen = gen;
            inst->ring.read_pos = inst->ring.write_pos;  // discard backlog
            inst->ring.dropped = 0;
            pthread_mutex_lock(&priv->lock);
            hist_reset(&priv->hist);
            history_reset(&priv->history, now);
            pthread_mutex_unlock(&priv->lock);
        }

        uint32_t n = lat_ring_read(&inst->ring, batch, DRAIN_BATCH);

        pthread_mutex_lock(&priv->lock);
        for (uint32_t i = 0; i < n; i++) {
            hist_add(&priv->hist, batch[i]);
            history_add(&priv->history, batch[i], now);
        }
        history_tick(&priv->history, now);   // also when idle, so buckets close
        pthread_mutex_unlock(&priv->lock);

        if (n < DRAIN_BATCH)
            usleep(DRAIN_POLL_US);   // ring drained; back off
    }
    return NULL;
}

// ---------------------------------------------------------------------------
// GMI callbacks (run on server request / watch threads).
// ---------------------------------------------------------------------------

// RT-owned scalar pins are read directly (word-sized, naturally atomic);
// drainer-owned aggregate fields are read under the mutex.
static latency_latency_status_t gmi_latency_get_status(void *ctx) {
    latency_priv_t *priv = (latency_priv_t *)ctx;
    latency_inst_t *inst = &priv->inst;
    latency_latency_status_t s;
    memset(&s, 0, sizeof(s));

    s.name        = priv->name;
    s.periodNs    = (int64_t)(uint32_t)*(inst->period_ns);
    s.samples     = (int64_t)(uint32_t)*(inst->samples);
    s.lastNs      = *(inst->latency);
    s.minNs       = *(inst->lat_min);
    s.maxNs       = *(inst->lat_max);
    s.maxJitterNs = *(inst->max_jitter);
    s.dropped     = inst->ring.dropped;   // relaxed scalar read (like the pins above)

    pthread_mutex_lock(&priv->lock);
    int64_t n   = priv->hist.n;
    double  sum = (double)priv->hist.sum;
    double  ssq = priv->hist.sumsq;
    s.binWidthNs = priv->hist.width;
    pthread_mutex_unlock(&priv->lock);

    double mean = n > 0 ? sum / (double)n : 0.0;
    double var  = n > 1 ? ssq / (double)n - mean * mean : 0.0;
    if (var < 0.0) var = 0.0;   // guard against fp round-off
    s.meanNs   = mean;
    s.stddevNs = sqrt(var);
    return s;
}

// Returns a snapshot of the histogram.  The bin counts are copied to the
// stack under the lock (so the drainer isn't blocked by the allocator), then
// the returned `bins` buffer is malloc'd outside the lock; the cgo bridge
// copies it into a Go slice and frees it with C.free.
static latency_latency_histogram_t gmi_latency_get_histogram(void *ctx) {
    latency_priv_t *priv = (latency_priv_t *)ctx;
    latency_latency_histogram_t h;
    uint32_t snap[HIST_MAX_BINS];
    int nb;
    memset(&h, 0, sizeof(h));

    pthread_mutex_lock(&priv->lock);
    nb = priv->hist.nbins;
    memcpy(snap, priv->hist.bins, (size_t)nb * sizeof(uint32_t));
    h.binWidthNs = priv->hist.width;
    h.baseNs     = hist_base(&priv->hist);
    h.underflow  = priv->hist.underflow;
    h.overflow   = priv->hist.overflow;
    h.samples    = priv->hist.n;
    h.autoscale  = priv->hist.autoscale != 0;
    pthread_mutex_unlock(&priv->lock);

    uint32_t *bins = (uint32_t *)malloc((size_t)nb * sizeof(uint32_t));
    if (bins) {
        memcpy(bins, snap, (size_t)nb * sizeof(uint32_t));
        h.bins = bins;
        h.bins_len = (size_t)nb;
        h.binCount = nb;
    }
    return h;
}

// Returns the retained plot history, newest-last.  `seconds` > 0 limits the
// window to that many seconds back from the newest bucket; 0 returns all that
// is retained.  The point array is malloc'd here; the cgo bridge copies it
// into a Go slice and frees it with C.free.
static latency_latency_history_t gmi_latency_get_history(void *ctx, int32_t seconds) {
    latency_priv_t *priv = (latency_priv_t *)ctx;
    latency_latency_history_t out;
    memset(&out, 0, sizeof(out));

    pthread_mutex_lock(&priv->lock);
    history_t *H = &priv->history;
    out.bucketMs = H->bucket_ms;

    int64_t cutoff = -1;
    if (seconds > 0) {
        int64_t newest = history_newest_ms(H);
        if (newest >= 0) cutoff = newest - (int64_t)seconds * 1000;
    }

    if (H->count > 0) {
        latency_latency_hist_point_t *pts =
            (latency_latency_hist_point_t *)malloc((size_t)H->count * sizeof(*pts));
        if (pts) {
            size_t m = 0;
            for (int i = 0; i < H->count; i++) {
                const hbucket_t *b = &H->buf[(H->head + i) % H->capacity];
                if (cutoff >= 0 && b->t_ms < cutoff) continue;
                pts[m].tMs = b->t_ms;
                pts[m].minNs = b->min;
                pts[m].maxNs = b->max;
                pts[m].meanNs = b->count ? b->sum / (double)b->count : 0.0;
                pts[m].count = (int32_t)b->count;
                m++;
            }
            out.points = pts;
            out.points_len = m;
        }
    }
    pthread_mutex_unlock(&priv->lock);
    return out;
}

// Pin the histogram bin width, disabling autoscale; binWidthNs <= 0 restores
// autoscale at the instance's configured default.  The histogram is cleared
// either way: past counts cannot be re-binned to a different width.
static bool gmi_latency_configure(void *ctx, int32_t binWidthNs) {
    latency_priv_t *priv = (latency_priv_t *)ctx;

    pthread_mutex_lock(&priv->lock);
    if (binWidthNs > 0) {
        if (binWidthNs > priv->hist.max_width) binWidthNs = priv->hist.max_width;
        priv->hist.init_width = binWidthNs;
        priv->hist.autoscale = 0;
    } else {
        priv->hist.init_width = priv->default_binsize;
        priv->hist.autoscale = 1;
    }
    hist_reset(&priv->hist);   // restores width = init_width and clears counts
    pthread_mutex_unlock(&priv->lock);
    return true;
}

// Reset both the RT scalars and the server-side aggregate.  Bumping reset_gen
// signals the RT function (clears pins) and the drainer (clears histogram +
// drops backlog); neither is touched directly here.
static bool gmi_latency_reset(void *ctx) {
    latency_priv_t *priv = (latency_priv_t *)ctx;
    __atomic_fetch_add(&priv->inst.reset_gen, 1, __ATOMIC_RELAXED);
    return true;
}

// ---------------------------------------------------------------------------
// Destroy
// ---------------------------------------------------------------------------

static void latency_destroy(cmod_t *self) {
    latency_priv_t *priv = (latency_priv_t *)self->priv;
    const gomc_rtapi_t *rtapi = priv->env.rtapi;

    if (priv->io_running) {
        priv->io_stop = 1;
        pthread_join(priv->io_tid, NULL);
    }
    if (priv->comp_id > 0)
        priv->env.hal->exit(priv->env.hal->ctx, priv->comp_id);
    pthread_mutex_destroy(&priv->lock);
    free(priv->hist_bins);
    free(priv->hist_ring);
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
    int nbins = DEFAULT_BINS;
    int binsize = DEFAULT_BINSIZE;

    for (int i = 0; i < argc; i++) {
        if (strncmp(argv[i], "depth=", 6) == 0) {
            int d = atoi(argv[i] + 6);
            if (d > 0) depth = d;
        } else if (strncmp(argv[i], "bins=", 5) == 0) {
            int b = atoi(argv[i] + 5);
            if (b > 0) nbins = b;
        } else if (strncmp(argv[i], "binsize=", 8) == 0) {
            int w = atoi(argv[i] + 8);
            if (w > 0) binsize = w;
        }
    }
    if (depth > (int)MAX_DEPTH) depth = MAX_DEPTH;
    if (nbins < 16) nbins = 16;
    if (nbins > HIST_MAX_BINS) nbins = HIST_MAX_BINS;
    nbins &= ~3;   // multiple of 4 (required by the histogram merge)

    if (!env->hal) {
        gomc_log_errorf(env->log, name, "HAL API not available");
        return -EINVAL;
    }
    if (!env->rtapi) {
        gomc_log_errorf(env->log, name, "RTAPI not available (need get_time)");
        return -EINVAL;
    }
    if (!env->api) {
        gomc_log_errorf(env->log, name, "API registry not available");
        return -EINVAL;
    }

    // Round the requested depth up to a power of two (the ring masks).
    uint32_t cap = 1;
    while (cap < (uint32_t)depth) cap <<= 1;

    // RT-locked allocations: this component measures RT latency, so its own
    // RT working set must not page-fault mid-cycle.  rtapi->calloc is mlock'd.
    latency_priv_t *priv = env->rtapi->calloc(env->rtapi->ctx, sizeof(*priv));
    if (!priv) return -ENOMEM;
    priv->env = *env;
    snprintf(priv->name, sizeof(priv->name), "%s", name);

    latency_inst_t *inst = &priv->inst;
    inst->rtapi = env->rtapi;   // valid for the module lifetime (env contract)

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

    // Histogram storage is drainer/API-only (non-RT) - plain calloc is fine.
    priv->hist_bins = calloc((size_t)nbins, sizeof(uint32_t));
    if (!priv->hist_bins) {
        env->rtapi->free(env->rtapi->ctx, ringbuf);
        env->rtapi->free(env->rtapi->ctx, priv);
        return -ENOMEM;
    }
    hist_init(&priv->hist, priv->hist_bins, nbins, binsize);
    priv->default_binsize = binsize;   // restored when autoscale is re-enabled

    // Plot history ring (also drainer/API-only).
    priv->hist_ring = calloc(HISTORY_BUCKETS, sizeof(hbucket_t));
    if (!priv->hist_ring) {
        free(priv->hist_bins);
        env->rtapi->free(env->rtapi->ctx, ringbuf);
        env->rtapi->free(env->rtapi->ctx, priv);
        return -ENOMEM;
    }
    history_init(&priv->history, priv->hist_ring, HISTORY_BUCKETS,
                 HISTORY_BUCKET_MS, env->rtapi->get_time(env->rtapi->ctx));

    pthread_mutex_init(&priv->lock, NULL);

    priv->comp_id = env->hal->init(env->hal->ctx, name, env->dl_handle,
                                   GOMC_HAL_COMP_REALTIME);
    if (priv->comp_id < 0) {
        gomc_log_errorf(env->log, name, "hal_init failed");
        retval = -1;
        goto fail;
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

    // Start the non-RT drainer.
    priv->io_stop = 0;
    if (pthread_create(&priv->io_tid, NULL, drain_thread, priv) != 0) {
        gomc_log_errorf(env->log, name, "pthread_create failed");
        retval = -1;
        goto fail;
    }
    priv->io_running = 1;

    // Register the GMI API last, so a failure above never leaves callbacks
    // pointing at a priv we are about to free (there is no unregister path).
    priv->cb = (latency_callbacks_t)GMI_LATENCY_CALLBACKS;
    priv->cb.ctx = priv;
    retval = latency_api_register(env->api, name, &priv->cb);
    if (retval != 0) {
        gomc_log_errorf(env->log, name, "api register failed: %d", retval);
        goto fail;
    }

    priv->mod.priv = priv;
    priv->mod.Init = NULL;
    priv->mod.Start = NULL;
    priv->mod.Stop = NULL;
    priv->mod.Destroy = latency_destroy;

    *out = &priv->mod;
    return 0;

fail:
    if (priv->io_running) {
        priv->io_stop = 1;
        pthread_join(priv->io_tid, NULL);
    }
    if (priv->comp_id > 0)
        env->hal->exit(env->hal->ctx, priv->comp_id);
    pthread_mutex_destroy(&priv->lock);
    free(priv->hist_bins);
    free(priv->hist_ring);
    env->rtapi->free(env->rtapi->ctx, ringbuf);
    env->rtapi->free(env->rtapi->ctx, priv);
    return retval;
}
