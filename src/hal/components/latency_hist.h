// latency_hist.h - symmetric latency histogram with grow-to-fit autoscaling.
//
// Accumulated entirely on the non-RT drainer thread (never in RT), so the
// autoscaling is free to re-bin.  Bins are symmetric around zero latency:
// bin i covers [(i - nbins/2)*width, (i - nbins/2 + 1)*width).  When a
// sample falls outside the covered range the bin width is doubled and the
// existing counts are merged pairwise into the central half (a coarsen),
// repeatedly, until the sample fits or the width hits its cap.  Coarsening
// only ever reduces resolution, so it is safe without keeping raw samples,
// and the served histogram always contains the data seen so far.
//
// nbins is fixed and must be a multiple of 4 (so nbins/2 and nbins/4 are
// integers and the pairwise merge is exact); the caller supplies the bin
// storage.  Not thread-safe: the owner serialises hist_add/hist_reset
// against the API readers with its own mutex.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#ifndef LATENCY_HIST_H
#define LATENCY_HIST_H

#include <stdint.h>

#define HIST_MAX_BINS 512

typedef struct {
    uint32_t *bins;        // nbins counts (caller-allocated)
    int       nbins;       // fixed bin count (multiple of 4, <= HIST_MAX_BINS)
    int32_t   width;       // current bin width (ns), grows via coarsening
    int32_t   init_width;  // starting bin width, restored on reset
    int32_t   max_width;   // width cap: (nbins/2)*max_width stays < INT32_MAX
    uint32_t  underflow;   // samples below the covered range
    uint32_t  overflow;    // samples above the covered range
    int64_t   sum;         // running sum of latencies (for mean)
    double    sumsq;       // running sum of squares (for stddev)
    int64_t   n;           // binned sample count
} hist_t;

// Initialise over caller-provided bin storage (nbins uint32 slots).
static inline void hist_init(hist_t *h, uint32_t *bins, int nbins,
                             int32_t init_width) {
    h->bins = bins;
    h->nbins = nbins;
    h->init_width = init_width;
    h->width = init_width;
    h->max_width = INT32_MAX / (nbins / 2);
    h->underflow = 0;
    h->overflow = 0;
    h->sum = 0;
    h->sumsq = 0;
    h->n = 0;
    for (int i = 0; i < nbins; i++) h->bins[i] = 0;
}

// Clear all counts and restore the initial (finest) bin width.
static inline void hist_reset(hist_t *h) {
    h->width = h->init_width;
    h->underflow = 0;
    h->overflow = 0;
    h->sum = 0;
    h->sumsq = 0;
    h->n = 0;
    for (int i = 0; i < h->nbins; i++) h->bins[i] = 0;
}

// Double the bin width, merging each adjacent pair of old bins into one new
// bin in the central half; the outer quarters become free space for the
// now-wider range.  All existing counts are preserved (no data dropped).
static inline void hist_coarsen(hist_t *h) {
    int N = h->nbins;
    uint32_t tmp[HIST_MAX_BINS];
    for (int i = 0; i < N; i++) tmp[i] = 0;
    for (int k = -N / 4; k < N / 4; k++)
        tmp[N / 2 + k] = h->bins[N / 2 + 2 * k] + h->bins[N / 2 + 2 * k + 1];
    for (int i = 0; i < N; i++) h->bins[i] = tmp[i];
    h->width *= 2;
}

// Add one latency sample, growing the bin width as needed to contain it.
static inline void hist_add(hist_t *h, int32_t lat) {
    // Grow until the sample fits the covered range, or the width is capped.
    while (h->width < h->max_width) {
        int64_t half = (int64_t)(h->nbins / 2) * h->width;
        if (lat >= -half && lat < half) break;
        hist_coarsen(h);
    }
    // Floor-divide into a bin (integer / truncates toward zero, so bias
    // negatives down by one when there is a remainder).
    int32_t q = lat / h->width;
    if (lat % h->width != 0 && lat < 0) q -= 1;
    int idx = q + h->nbins / 2;
    if (idx < 0) h->underflow++;
    else if (idx >= h->nbins) h->overflow++;
    else h->bins[idx]++;

    h->sum += lat;
    h->sumsq += (double)lat * (double)lat;
    h->n++;
}

// Left edge (ns) of bins[0] for the current width.
static inline int32_t hist_base(const hist_t *h) {
    return -(h->nbins / 2) * h->width;
}

#endif // LATENCY_HIST_H
