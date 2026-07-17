// latency_history.h - bounded server-side ring of downsampled latency buckets.
//
// The non-RT drainer folds every sample into a fixed-width wall-clock bucket
// (min / max / sum / count); when a bucket's window elapses it is pushed into
// a ring that keeps the most recent buckets.  This gives a single,
// client-independent time series for the plot: every client sees the same
// data, late-connecting clients get the retained history, and the visible
// range is just a window into the ring.
//
// Not thread-safe: the owner serialises the drainer's writes against the API
// readers with its own mutex (the same one that guards the histogram).
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#ifndef LATENCY_HISTORY_H
#define LATENCY_HISTORY_H

#include <stdint.h>

typedef struct {
    int64_t  t_ms;    // bucket start time, ms since the instance epoch
    int32_t  min;     // min latency in the bucket (ns)
    int32_t  max;     // max latency in the bucket (ns)
    double   sum;     // sum of latencies (mean = sum / count)
    uint32_t count;   // samples in the bucket
} hbucket_t;

typedef struct {
    hbucket_t *buf;         // `capacity` closed buckets (caller-allocated)
    int        capacity;
    int        count;       // number of valid buckets (<= capacity)
    int        head;        // ring index of the oldest valid bucket
    int64_t    epoch_ns;    // reference time for the relative timestamps
    int32_t    bucket_ms;   // bucket width

    // The currently-accumulating (open) bucket.
    int64_t  cur_start_ns;
    int32_t  cur_min, cur_max;
    double   cur_sum;
    uint32_t cur_count;
    int      cur_open;
} history_t;

static inline void history_init(history_t *h, hbucket_t *buf, int capacity,
                                int32_t bucket_ms, int64_t epoch_ns) {
    h->buf = buf;
    h->capacity = capacity;
    h->count = 0;
    h->head = 0;
    h->epoch_ns = epoch_ns;
    h->bucket_ms = bucket_ms;
    h->cur_open = 0;
    h->cur_min = h->cur_max = 0;
    h->cur_sum = 0;
    h->cur_count = 0;
    h->cur_start_ns = 0;
}

// Clear the retained buckets and the open accumulator, and re-base the epoch
// to now so the time axis restarts at 0 after a reset.  The epoch lives on the
// server, so every client still sees the same timeline.
static inline void history_reset(history_t *h, int64_t now_ns) {
    h->count = 0;
    h->head = 0;
    h->cur_open = 0;
    h->cur_min = h->cur_max = 0;
    h->cur_sum = 0;
    h->cur_count = 0;
    h->epoch_ns = now_ns;
}

// Push the open bucket into the ring (dropping the oldest if full).
static inline void history_flush(history_t *h) {
    if (!h->cur_open || h->cur_count == 0) { h->cur_open = 0; return; }
    int idx;
    if (h->count < h->capacity) {
        idx = (h->head + h->count) % h->capacity;
        h->count++;
    } else {
        idx = h->head;
        h->head = (h->head + 1) % h->capacity;  // overwrite oldest
    }
    hbucket_t *b = &h->buf[idx];
    b->t_ms = (h->cur_start_ns - h->epoch_ns) / 1000000;
    b->min = h->cur_min;
    b->max = h->cur_max;
    b->sum = h->cur_sum;
    b->count = h->cur_count;
    h->cur_open = 0;
}

// Fold one sample (measured near wall time now_ns) into the open bucket.
static inline void history_add(history_t *h, int32_t lat, int64_t now_ns) {
    if (!h->cur_open) {
        h->cur_open = 1;
        h->cur_start_ns = now_ns;
        h->cur_min = lat;
        h->cur_max = lat;
        h->cur_sum = 0;
        h->cur_count = 0;
    }
    if (lat < h->cur_min) h->cur_min = lat;
    if (lat > h->cur_max) h->cur_max = lat;
    h->cur_sum += lat;
    h->cur_count++;
}

// Close the open bucket once its window has elapsed.  Call every drain
// iteration (also when idle) so buckets close on time.
static inline void history_tick(history_t *h, int64_t now_ns) {
    if (h->cur_open && now_ns - h->cur_start_ns >= (int64_t)h->bucket_ms * 1000000)
        history_flush(h);
}

// t_ms of the newest bucket (or the open one), or -1 if empty.
static inline int64_t history_newest_ms(const history_t *h) {
    if (h->cur_open)
        return (h->cur_start_ns - h->epoch_ns) / 1000000;
    if (h->count > 0) {
        int idx = (h->head + h->count - 1) % h->capacity;
        return h->buf[idx].t_ms;
    }
    return -1;
}

#endif // LATENCY_HISTORY_H
