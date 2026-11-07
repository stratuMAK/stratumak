// pnp_rt.h — pnptask's cycle function: every HAL pin of the module is read and
// written here, in the servo thread, at one point (docs/dev/PNPTASK_RT_BOUNDARY.md
// §4.1, step 2).
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// Per run:
//
//   1. the input pins are copied into the write slot of the RT -> Go buffer
//      (one pin map, so all of them from one moment of the thread);
//   2. Go's latest output frame is read and copied to the output pins (one pin
//      map, so they all change together: no reader can see half an update);
//   3. a start-job clear Go asked for is applied (start-job is an IO pin the
//      requester raises, so it cannot be in the output map);
//   4. the dead-zone check runs (pnp_deadzone_rt.h);
//   5. the input frame is published to Go.
//
// The frames have fixed-size arrays; config.go refuses more stations, pickers
// or axes than they hold. A field no pin is bound to stays 0 in every slot.

#ifndef PNP_RT_H
#define PNP_RT_H

#include <stdint.h>

#include "stmak_hal.h"
#include "stmak_rt_check.h"
#include "stmak_tribuf.h"
#include "stmak_pinmap.h"
#include "pnp_deadzone_rt.h"

#define PNP_RT_MAX_PICKERS 2
#define PNP_RT_MAX_TRAYS   16
#define PNP_RT_MAX_PROCS   16
#define PNP_RT_MAX_AXES    9

// --- RT -> Go: the inputs of one servo cycle --------------------------------

typedef struct {
    uint8_t manual_open, manual_close, opened, closed;
} pnp_rt_in_picker_t;

typedef struct {
    double   z_offset;
    uint32_t tray_id, step;
    uint8_t  set_full, set_empty;
    uint8_t  pad_[6];
} pnp_rt_in_tray_t;

typedef struct {
    double  z_offset;
    uint8_t busy, released, set_has_material, set_empty;
    uint8_t pad_[4];
} pnp_rt_in_proc_t;

typedef struct {
    uint8_t pos, neg;
} pnp_rt_in_jog_t;

typedef struct {
    uint32_t cycle;            // runs of the cycle function; 0 = never ran
    uint32_t go_seq;           // the Go frame this run applied

    double   jog_speed;
    uint32_t process_step, origin_id, dest_id, deadzone_select;

    uint8_t  estop_on, machine_on, auto_enable, wait_abort;
    uint8_t  park, home, error_reset, start_job;
    uint8_t  stay_near, scene_hold;
    uint8_t  pad_[6];

    pnp_rt_in_picker_t picker[PNP_RT_MAX_PICKERS];
    pnp_rt_in_jog_t    jog[PNP_RT_MAX_AXES];
    uint8_t            pad2_[6];
    pnp_rt_in_tray_t   tray[PNP_RT_MAX_TRAYS];
    pnp_rt_in_proc_t   proc[PNP_RT_MAX_PROCS];
} pnp_rt_in_t;

// --- Go -> RT: the outputs of one Go commit ----------------------------------

typedef struct {
    double   pos_x, pos_y, pos_x_mu, pos_y_mu;
    uint32_t origin_id;
    uint8_t  close, missing, holds;
    uint8_t  pad_;
} pnp_rt_out_picker_t;

typedef struct {
    uint32_t count;
    uint8_t  empty, avail, full;
    uint8_t  pad_;
} pnp_rt_out_tray_t;

typedef struct {
    uint8_t has_material, release;
} pnp_rt_out_proc_t;

typedef struct {
    uint32_t seq;              // Go's commit number
    uint32_t start_job_clears; // a change clears the start-job pin (step 3)

    double   plan_time;
    uint32_t cycle_count, error_id, error_station;

    uint8_t  machine_is_on, homed, parked, busy;
    uint8_t  scene_busy, error;
    uint8_t  pad_[6];

    pnp_rt_out_picker_t picker[PNP_RT_MAX_PICKERS];
    pnp_rt_out_tray_t   tray[PNP_RT_MAX_TRAYS];
    pnp_rt_out_proc_t   proc[PNP_RT_MAX_PROCS];
} pnp_rt_out_t;

// --- the function's arg block -----------------------------------------------

typedef struct {
    stmak_tribuf_t *to_go;     // pnp_rt_in_t, RT writes
    stmak_tribuf_t *to_rt;     // pnp_rt_out_t, Go writes
    stmak_pinmap_t *in;        // input pins -> pnp_rt_in_t
    stmak_pinmap_t *out;       // pnp_rt_out_t -> output pins
    pnp_dz_t       *dz;        // the dead-zone check's block

    // start-job's data-pointer slot: written only to clear it (step 3), as
    // one byte, the size of hal.h's hal_bit_t.
    void *volatile *start_job;

    uint32_t cycle;
    uint32_t start_job_clears; // the last count applied
} pnp_rt_t;

void pnp_rt_cycle(void *arg, long period) STMAK_NONBLOCKING;

// pnp_rt_cycle's address, as a real symbol (see hal.CFunct).
extern const stmak_hal_funct_t pnp_rt_cycle_fp;

#endif // PNP_RT_H
