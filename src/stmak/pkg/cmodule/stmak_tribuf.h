// stmak_tribuf.h — single-producer single-consumer triple buffer between a
// realtime function and non-RT code (docs/dev/PNPTASK_RT_BOUNDARY.md §3.1).
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: LGPL Version 2
//
// One writer and one reader exchange whole frames through three slots. Both
// sides are wait-free: one atomic exchange each, no retry, no lock. The reader
// always gets the latest COMPLETE frame the writer published, never a mix of
// two. Frames the reader was too slow to see are skipped, which is the point:
// anything that must not be lost travels as a counter inside the frame.
//
// It is motmod's status buffer (cnc/motion/motion_struct.h) made generic, and
// keeps the lesson learned there: the writer tags the index it publishes with
// STMAK_TRIBUF_DIRTY, and the reader swaps only when the tag is set. Without
// it, a reader polling faster than the writer publishes is handed its own
// previous slot back and sees time go backwards (observed in motmod as motion
// ids going backwards). Only the reader stores an untagged index, so a tag
// observed set cannot vanish before the reader's exchange.
//
// Either side may be the realtime one; both directions use the same buffer.
// The block is one allocation (rtapi_calloc, hal.RTCalloc) of
// stmak_tribuf_size(frame_size) bytes, set up once with stmak_tribuf_init
// before either side touches it.
//
// Rules for the two sides:
//
//   - The writer writes a WHOLE frame into stmak_tribuf_write_slot() before
//     stmak_tribuf_publish(). The slot holds a frame from two publishes ago,
//     not the last one, so a partial update would publish stale fields.
//   - The reader treats what stmak_tribuf_read() returns as valid until its
//     next read. Before the writer's first publish it is the zeroed slot; a
//     frame should carry its own counter so "never published" is visible.
//   - One writer, one reader. Two non-RT threads on the same side must be
//     serialized by their owner (pkg/hal's TriBuf does).
//
// The fields are plain integers accessed through the __atomic builtins rather
// than _Atomic members, so cgo can translate the struct. The slots start at a
// cache line boundary relative to the block and the three indices live on
// separate lines, so the two sides do not share a line they write.

#ifndef STMAK_TRIBUF_H
#define STMAK_TRIBUF_H

#include <stddef.h>
#include <stdint.h>

#include "stmak_rt_check.h"

#ifdef __cplusplus
extern "C" {
#endif

#define STMAK_TRIBUF_SLOTS 3
#define STMAK_TRIBUF_DIRTY 4             // bit above the 2-bit slot index
#define STMAK_TRIBUF_IDX(v) ((v) & 3)    // strip the dirty tag
#define STMAK_TRIBUF_LINE 64             // cache line

typedef struct {
    // Line 0: the exchange word and the read-only geometry.
    int32_t  middle;        // slot index | STMAK_TRIBUF_DIRTY, __atomic only
    uint32_t frame_size;    // bytes, as requested
    uint32_t slot_stride;   // frame_size rounded up to a cache line
    uint32_t magic;         // STMAK_TRIBUF_MAGIC once initialized
    uint8_t  pad0[STMAK_TRIBUF_LINE - 16];
    // Line 1: writer-private.
    int32_t  write_idx;
    uint8_t  pad1[STMAK_TRIBUF_LINE - 4];
    // Line 2: reader-private.
    int32_t  read_idx;
    uint8_t  pad2[STMAK_TRIBUF_LINE - 4];
    // The three slots follow.
} stmak_tribuf_t;

#define STMAK_TRIBUF_MAGIC 0x54524942u  // "TRIB"

static inline uint32_t stmak_tribuf_stride(uint32_t frame_size)
{
    return (frame_size + STMAK_TRIBUF_LINE - 1) & ~(uint32_t)(STMAK_TRIBUF_LINE - 1);
}

// stmak_tribuf_size is the size of the block to allocate for frames of
// frame_size bytes. Non-RT.
static inline size_t stmak_tribuf_size(uint32_t frame_size)
{
    return sizeof(stmak_tribuf_t) + (size_t)STMAK_TRIBUF_SLOTS * stmak_tribuf_stride(frame_size);
}

// stmak_tribuf_init sets up a zeroed block. Non-RT, before either side runs.
static inline void stmak_tribuf_init(stmak_tribuf_t *b, uint32_t frame_size)
{
    b->frame_size  = frame_size;
    b->slot_stride = stmak_tribuf_stride(frame_size);
    b->write_idx   = 0;
    b->read_idx    = 2;
    b->magic       = STMAK_TRIBUF_MAGIC;
    __atomic_store_n(&b->middle, 1, __ATOMIC_RELEASE);
}

static inline void *stmak_tribuf_slot(stmak_tribuf_t *b, int idx) STMAK_NONBLOCKING;
static inline void *stmak_tribuf_slot(stmak_tribuf_t *b, int idx)
{
    return (unsigned char *)b + sizeof(stmak_tribuf_t) + (size_t)idx * b->slot_stride;
}

// stmak_tribuf_write_slot is the writer's slot: fill all of it, then publish.
static inline void *stmak_tribuf_write_slot(stmak_tribuf_t *b) STMAK_NONBLOCKING;
static inline void *stmak_tribuf_write_slot(stmak_tribuf_t *b)
{
    return stmak_tribuf_slot(b, b->write_idx);
}

// stmak_tribuf_publish hands the write slot to the reader and takes the
// exchange slot as the next write slot. Release on the frame's contents.
static inline void stmak_tribuf_publish(stmak_tribuf_t *b) STMAK_NONBLOCKING;
static inline void stmak_tribuf_publish(stmak_tribuf_t *b)
{
    int32_t prev = __atomic_exchange_n(&b->middle, b->write_idx | STMAK_TRIBUF_DIRTY,
                                       __ATOMIC_ACQ_REL);
    b->write_idx = STMAK_TRIBUF_IDX(prev);
}

// stmak_tribuf_read returns the latest published frame. *fresh is 1 when the
// writer published since the previous read (the returned frame is new), 0 when
// it is the same frame as last time. Acquire on the frame's contents.
static inline const void *stmak_tribuf_read(stmak_tribuf_t *b, int *fresh) STMAK_NONBLOCKING;
static inline const void *stmak_tribuf_read(stmak_tribuf_t *b, int *fresh)
{
    *fresh = 0;
    if (__atomic_load_n(&b->middle, __ATOMIC_ACQUIRE) & STMAK_TRIBUF_DIRTY) {
        int32_t prev = __atomic_exchange_n(&b->middle, b->read_idx, __ATOMIC_ACQ_REL);
        b->read_idx = STMAK_TRIBUF_IDX(prev);
        *fresh = 1;
    }
    return stmak_tribuf_slot(b, b->read_idx);
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_TRIBUF_H
