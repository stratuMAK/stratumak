// stmak_doorbell.h — how a realtime function tells non-RT code that something
// changed (docs/dev/PNPTASK_RT_BOUNDARY.md §3.4).
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: LGPL Version 2
//
// The servo thread makes no syscalls, so it cannot wake a thread waiting on a
// futex or an eventfd. What it can do is count: `cycle` advances every time the
// function runs, `change` every time it published something its non-RT side
// should act on. The non-RT side polls the two words (pkg/hal's Doorbell.Wait,
// one atomic load per poll) and wakes its worker only when `change` moved, or
// reports the function stalled when `cycle` stands still.
//
// What counts as a change is the function's business. Ring AFTER publishing
// the frame the change is in: the release here then orders the frame before the
// counter, and a reader that sees the new count reads the new frame.
//
// Plain words accessed through the __atomic builtins, so cgo can translate the
// struct and Go can load them with sync/atomic.

#ifndef STMAK_DOORBELL_H
#define STMAK_DOORBELL_H

#include <stdint.h>

#include "stmak_rt_check.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint32_t change;   // bumped by stmak_doorbell_ring
    uint32_t cycle;    // bumped by stmak_doorbell_tick
} stmak_doorbell_t;

// stmak_doorbell_tick counts one run of the function.
static inline void stmak_doorbell_tick(stmak_doorbell_t *d) STMAK_NONBLOCKING;
static inline void stmak_doorbell_tick(stmak_doorbell_t *d)
{
    __atomic_fetch_add(&d->cycle, 1, __ATOMIC_RELEASE);
}

// stmak_doorbell_ring announces a change, after its frame was published.
static inline void stmak_doorbell_ring(stmak_doorbell_t *d) STMAK_NONBLOCKING;
static inline void stmak_doorbell_ring(stmak_doorbell_t *d)
{
    __atomic_fetch_add(&d->change, 1, __ATOMIC_RELEASE);
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_DOORBELL_H
