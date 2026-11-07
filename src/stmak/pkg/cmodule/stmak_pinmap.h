// stmak_pinmap.h — copy a set of HAL pins into a frame and back, from a
// realtime function (docs/dev/PNPTASK_RT_BOUNDARY.md §3.2).
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: LGPL Version 2
//
// A pin map is a flat table of (pin slot, frame offset, type), built once at
// init and walked by the realtime function. stmak_pinmap_in copies every pin of
// an input map into the frame, stmak_pinmap_out copies the frame into every pin
// of an output map. A function that does all its pin I/O through one map each
// way reads all its inputs at one point of the thread and changes all its
// outputs at one point: no reader of its outputs can see half an update, and
// nothing it computes can mix two samples of its inputs.
//
// The entries are deliberately the shape of a transfer-plan entry (offset,
// type, direction; stmak-concepts HAL_SPECIFICATION.md). When the transfer
// plans land they replace the map and the frames stay.
//
// Frame field types per pin type:
//
//   STMAK_PINMAP_BIT    uint8_t   0 or 1
//   STMAK_PINMAP_FLOAT  double
//   STMAK_PINMAP_S32    int32_t
//   STMAK_PINMAP_U32    uint32_t
//
// A bit cell is accessed as one byte, which is what hal.h's hal_bit_t (volatile
// bool) is. The slot is a pin's data-pointer slot (hal.Pin.RTDataPtr): HAL
// repoints it when the pin is netted, so it is dereferenced on every copy,
// never cached.
//
// The block is one allocation of stmak_pinmap_size(cap) bytes; init and add are
// non-RT, before the function runs. The pins must outlive the map's use: the
// launcher's unload barrier (GOMOD_RT_DESIGN.md §5) is what guarantees it.

#ifndef STMAK_PINMAP_H
#define STMAK_PINMAP_H

#include <stddef.h>
#include <stdint.h>

#include "stmak_rt_check.h"

#ifdef __cplusplus
extern "C" {
#endif

// The values of stmak_hal_type_t (and hal.h's hal_type_t).
#define STMAK_PINMAP_BIT   1
#define STMAK_PINMAP_FLOAT 2
#define STMAK_PINMAP_S32   3
#define STMAK_PINMAP_U32   4

typedef struct {
    void *volatile *slot;   // the pin's data-pointer slot
    uint32_t        ofs;    // byte offset of the field in the frame
    uint32_t        type;   // STMAK_PINMAP_*
} stmak_pinmap_entry_t;

typedef struct {
    uint32_t             n;     // entries in use
    uint32_t             cap;   // entries allocated
    stmak_pinmap_entry_t e[];
} stmak_pinmap_t;

// stmak_pinmap_size is the size of the block for cap entries. Non-RT.
static inline size_t stmak_pinmap_size(uint32_t cap)
{
    return sizeof(stmak_pinmap_t) + (size_t)cap * sizeof(stmak_pinmap_entry_t);
}

// stmak_pinmap_init sets up a zeroed block for cap entries. Non-RT.
static inline void stmak_pinmap_init(stmak_pinmap_t *m, uint32_t cap)
{
    m->n   = 0;
    m->cap = cap;
}

// stmak_pinmap_add appends one entry; -1 when the map is full or the type is
// not one of STMAK_PINMAP_*. Non-RT, before the function runs. Range and
// alignment of ofs are the builder's to check (pkg/hal's PinMap does).
static inline int stmak_pinmap_add(stmak_pinmap_t *m, void *slot, uint32_t ofs, uint32_t type)
{
    if (m->n >= m->cap || type < STMAK_PINMAP_BIT || type > STMAK_PINMAP_U32)
        return -1;
    m->e[m->n].slot = (void *volatile *)slot;
    m->e[m->n].ofs  = ofs;
    m->e[m->n].type = type;
    m->n++;
    return 0;
}

// stmak_pinmap_in copies every pin of the map into frame.
static inline void stmak_pinmap_in(const stmak_pinmap_t *m, void *frame) STMAK_NONBLOCKING;
static inline void stmak_pinmap_in(const stmak_pinmap_t *m, void *frame)
{
    unsigned char *f = (unsigned char *)frame;
    for (uint32_t i = 0; i < m->n; i++) {
        const stmak_pinmap_entry_t *e = &m->e[i];
        const volatile void *d = *e->slot;
        switch (e->type) {
        case STMAK_PINMAP_BIT:
            *(uint8_t *)(f + e->ofs) = *(const volatile uint8_t *)d ? 1 : 0;
            break;
        case STMAK_PINMAP_FLOAT:
            *(double *)(f + e->ofs) = *(const volatile double *)d;
            break;
        case STMAK_PINMAP_S32:
            *(int32_t *)(f + e->ofs) = *(const volatile int32_t *)d;
            break;
        case STMAK_PINMAP_U32:
            *(uint32_t *)(f + e->ofs) = *(const volatile uint32_t *)d;
            break;
        default:
            break;
        }
    }
}

// stmak_pinmap_out copies frame into every pin of the map.
static inline void stmak_pinmap_out(const stmak_pinmap_t *m, const void *frame) STMAK_NONBLOCKING;
static inline void stmak_pinmap_out(const stmak_pinmap_t *m, const void *frame)
{
    const unsigned char *f = (const unsigned char *)frame;
    for (uint32_t i = 0; i < m->n; i++) {
        const stmak_pinmap_entry_t *e = &m->e[i];
        volatile void *d = *e->slot;
        switch (e->type) {
        case STMAK_PINMAP_BIT:
            *(volatile uint8_t *)d = *(const uint8_t *)(f + e->ofs) ? 1 : 0;
            break;
        case STMAK_PINMAP_FLOAT:
            *(volatile double *)d = *(const double *)(f + e->ofs);
            break;
        case STMAK_PINMAP_S32:
            *(volatile int32_t *)d = *(const int32_t *)(f + e->ofs);
            break;
        case STMAK_PINMAP_U32:
            *(volatile uint32_t *)d = *(const uint32_t *)(f + e->ofs);
            break;
        default:
            break;
        }
    }
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_PINMAP_H
