// rtbridgetest_rt.h — a cyclic function that uses the whole RT bridge (triple
// buffers both ways, an In and an Out pin map, the doorbell), for pkg/hal's
// tests. The shape is the one pnptask's cycle function takes
// (docs/dev/PNPTASK_RT_BOUNDARY.md §4.1), in miniature.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// Field names are capitalized so the Go tests, in another package, can read
// them through the cgo types.

#ifndef RTBRIDGETEST_RT_H
#define RTBRIDGETEST_RT_H

#include <stdint.h>

#include "stmak_hal.h"
#include "stmak_rt_check.h"
#include "stmak_tribuf.h"
#include "stmak_pinmap.h"
#include "stmak_doorbell.h"

// One value per pin type: the In map fills it from pins, the Out map writes
// pins from it.
typedef struct {
    uint8_t  B;
    uint8_t  pad_[3];
    int32_t  S;
    uint32_t U;
    uint32_t pad2_;
    double   F;
} rbt_pins_t;

// Go -> RT.
typedef struct {
    uint32_t   Seq;     // Go's commit number
    uint32_t   pad_;
    rbt_pins_t Out;     // what the Out map writes
} rbt_go_frame_t;

// RT -> Go.
typedef struct {
    uint32_t   Cycle;   // runs of the function
    uint32_t   GoSeq;   // the Go frame's Seq this cycle saw
    rbt_pins_t In;      // what the In map read
} rbt_rt_frame_t;

typedef struct {
    stmak_tribuf_t   *to_rt;    // rbt_go_frame_t, Go writes
    stmak_tribuf_t   *to_go;    // rbt_rt_frame_t, RT writes
    stmak_pinmap_t   *in;       // pins -> rbt_pins_t
    stmak_pinmap_t   *out;      // rbt_pins_t -> pins
    stmak_doorbell_t *bell;

    uint32_t   cycle;
    uint32_t   last_seq;
    int        primed;
    rbt_pins_t last_in;
} rbt_t;

// rbt_cycle, per run: pins in, Go's frame, pins out from it, publish to Go,
// tick, and ring when an input or Go's frame changed.
void rbt_cycle(void *arg, long period) STMAK_NONBLOCKING;

extern const stmak_hal_funct_t rbt_cycle_fp;

#endif // RTBRIDGETEST_RT_H
