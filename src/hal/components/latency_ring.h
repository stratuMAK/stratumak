// latency_ring.h - lock-free SPSC ring of int32 latency samples.
//
// One RT producer (the latency RT function) pushes one sample per cycle;
// one non-RT consumer (the drainer thread, added in a later phase) reads
// them.  Producer and consumer live in the same process, so a release /
// acquire fence pair (via __sync_synchronize) is all that is needed - no
// locks, and the producer side stays RT-safe (nonblocking).
//
// The write cursor is free-running and wraps at 2^32; unsigned subtraction
// (wp - rp) yields the correct count across the wrap.  Capacity is a power
// of two so slot selection is a mask, not a modulo.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#ifndef LATENCY_RING_H
#define LATENCY_RING_H

#include <stdint.h>

typedef struct {
    int32_t  *buf;                // `capacity` slots, caller-allocated (RT-locked)
    uint32_t  capacity;           // power of two
    uint32_t  mask;               // capacity - 1
    volatile uint32_t write_pos;  // producer cursor (free-running, wraps)
    uint32_t  read_pos;           // consumer cursor (single consumer)
    uint32_t  dropped;            // samples lost to overrun (bumped by consumer)
} lat_ring_t;

// Initialise the ring over a caller-provided buffer of `capacity` int32
// slots.  `capacity` must be a non-zero power of two.  Returns 0 on
// success, -1 on bad arguments.
static inline int lat_ring_init(lat_ring_t *r, int32_t *buf, uint32_t capacity) {
    if (!buf || capacity == 0 || (capacity & (capacity - 1)) != 0)
        return -1;
    r->buf = buf;
    r->capacity = capacity;
    r->mask = capacity - 1;
    r->write_pos = 0;
    r->read_pos = 0;
    r->dropped = 0;
    return 0;
}

// Producer (RT, nonblocking): store one sample.  Never blocks; if the
// consumer has fallen a full lap behind, the oldest unread slot is
// overwritten - the loss is accounted by the consumer on its next read.
static inline void lat_ring_push(lat_ring_t *r, int32_t v) {
    r->buf[r->write_pos & r->mask] = v;
    __sync_synchronize();   // publish the slot before advancing the cursor
    r->write_pos++;
}

// Consumer (non-RT): copy up to `max` available samples into out[],
// advancing the read cursor.  If the producer lapped the consumer, the
// oldest overwritten samples are skipped and counted in r->dropped.
// Returns the number of samples copied.  (Used by the drainer thread in a
// later phase; unused in the RT-only phase.)
//
// Note: like the sampler.c ring, under sustained overrun a slot near the
// write frontier may be read while the producer overwrites it, yielding an
// occasional torn sample.  This is acceptable for statistical binning and
// only occurs when the consumer is already dropping data.
static inline uint32_t lat_ring_read(lat_ring_t *r, int32_t *out, uint32_t max) {
    uint32_t wp = r->write_pos;
    __sync_synchronize();   // observe the cursor before reading the slots it covers
    uint32_t rp = r->read_pos;
    uint32_t avail = wp - rp;            // correct across the 2^32 wrap
    if (avail > r->capacity) {           // producer lapped us: skip to oldest kept
        r->dropped += avail - r->capacity;
        rp = wp - r->capacity;
        avail = r->capacity;
    }
    uint32_t n = avail < max ? avail : max;
    for (uint32_t i = 0; i < n; i++)
        out[i] = r->buf[(rp + i) & r->mask];
    r->read_pos = rp + n;
    return n;
}

#endif // LATENCY_RING_H
