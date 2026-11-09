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
//   2. motmod's state of this cycle goes into the input frame (mot's
//      status_get_snapshot), so Go sees motion and pins from the same cycle,
//      and the lift watch below judges this cycle's segment;
//   3. Go's latest output frame is read;
//   4. the handshakes run (step 5): a job or park Go has finished ends (busy
//      falls), an error-reset edge clears the latch, a fault Go raised
//      latches, a start-job or park edge is accepted (busy rises, the job's
//      parameters are latched from this same sample); then the scene lock and
//      the lift watch (step 6);
//   5. the rising edges of the other command inputs are counted
//      (pnp_rt_edges_t);
//   6. the output pins are written from Go's frame, with the pins RT owns
//      (busy, the error latch, picker.N.missing, cycle, scene-busy, and a
//      fired lift watch's release override) put in: one pin map, so they all
//      change together and no reader can see half an update;
//   7. the dead-zone check runs (pnp_deadzone_rt.h);
//   8. the input frame is published to Go, and the doorbell's cycle counter
//      ticks (Go reads it to know a frame postdates a motion command); the
//      doorbell rings when something Go acts on changed, or Go's wake time
//      came (step 7, the change counter below).
//
// The frames have fixed-size arrays; config.go refuses more stations, pickers
// or axes than they hold. A field no pin is bound to stays 0 in every slot.

#ifndef PNP_RT_H
#define PNP_RT_H

#include <stddef.h>
#include <stdint.h>

#include "stmak_hal.h"
#include "stmak_rt_check.h"
#include "stmak_tribuf.h"
#include "stmak_pinmap.h"
#include "stmak_doorbell.h"
#include "mot_api.h"
#include "pnp_deadzone_rt.h"

#define PNP_RT_MAX_PICKERS 2
#define PNP_RT_MAX_TRAYS   16
#define PNP_RT_MAX_PROCS   16
#define PNP_RT_MAX_AXES    9

// --- RT -> Go: the edges, counted at servo rate --------------------------------
//
// Each command input's rising edge, counted in the cycle it happens in and
// only when the state allows it then (press-time gating, PNPTASK_RT_BOUNDARY.md
// §4.3): machine-on not under estop, home neither under estop nor while busy,
// error-reset only with an error latched (and that edge clears it, §4.4), the
// manual picker presses neither under estop nor in auto. Go keeps the last
// count it took and acts on the difference, so a pulse of one servo cycle is
// seen and none is lost between two Go wakes. start-job and park are not
// counted here: they are the handshakes (pnp_rt_job_t, park_seq).
//
// The first run only primes: an input already high then is no edge (Go adds
// machine-on's boot edge itself, as it always did).
//
// The station resets are one counter each with the value of the latest one:
// set-full carries the tray's step of that cycle, set-empty -1 (a proc
// station: 1 has material, 0 empty). The latest wins; both in one cycle is a
// contradiction and counts as a conflict instead.

typedef struct {
    uint32_t open, close;
} pnp_rt_edge_picker_t;

typedef struct {
    int64_t  value;
    uint32_t count, conflicts;
} pnp_rt_edge_reset_t;

typedef struct {
    uint32_t machine_on, home, error_reset;
    pnp_rt_edge_picker_t picker[PNP_RT_MAX_PICKERS];
    pnp_rt_edge_reset_t  tray[PNP_RT_MAX_TRAYS];
    pnp_rt_edge_reset_t  proc[PNP_RT_MAX_PROCS];
} pnp_rt_edges_t;

// --- the handshakes (§4.4) ------------------------------------------------------
//
// Job: a start-job rising edge, with busy low and no error latched, is
// accepted in its own cycle: busy rises, seq counts it, and the parameters are
// latched from that same sample. Go runs the job and ends it by setting
// job_done to seq in its frame, with the outcome (a raise or none) and the
// model in the same commit; busy falls in the cycle that frame is applied. An
// edge while busy or latched is ignored -- the requester sees error, or busy,
// in the same frame. Park is the same handshake on the park edge (not under
// estop), counted in park_seq and ended by park_done.
//
// Error latch: Go raises by counting raise_seq with id, station and the
// missing mask in its frame. The first fault wins: a raise while latched is
// dropped (raise_seen still takes it). An error-reset edge with the latch set
// clears it, all of it in one cycle; it is applied before a raise of the same
// frame, so a reset never clears a fault it cannot have seen.

typedef struct {
    uint32_t seq;              // jobs accepted
    uint32_t origin_id, dest_id, process_step, deadzone_select;
    uint8_t  stay_near;
    uint8_t  pad_[3];
} pnp_rt_job_t;

typedef struct {
    uint32_t id, station;
    uint8_t  set;
    uint8_t  missing;          // bit per picker
    uint8_t  pad_[2];
} pnp_rt_latch_t;

// Scene lock (D45, §4.4): Go holds scene_req while it wants to start a leg
// into the movable region. The lock is granted in a cycle where the request
// is high and scene-hold is low, and scene-busy rises in that same cycle --
// atomic in the cycle cam.sphere reads. Hold and request in the same cycle:
// hold wins. Granted, it stays until the request drops, whatever hold does
// meanwhile (the other side waits for scene-busy low). Go dispatches only
// once the frame shows the grant.
//
// Lift watch (D53, §4.4): Go arms it by counting watch_seq with a segment id
// and the process stations whose release it covers. In the first cycle whose
// motion snapshot runs a segment beyond the id, the watch fires: watch_fired
// takes the seq, and from that cycle on those stations' release pins are held
// low, until Go clears the mask or arms the next watch. Go learns of it from
// watch_fired and runs its hook, which drops release itself.

// The change counter (step 7, §6.1): the control loop sleeps until the doorbell
// rings. It rings when anything the loop acts on is different from the last
// cycle -- an input, an edge count, the handshake and latch state, the scene
// grant, a fired watch, and the motion snapshot but for its positions and
// velocity, which move every cycle while the head travels -- and in the cycle
// servo time reaches the loop's wake_at (a deadline, or the idle wake).
// `change` counts the rings. The loop says which count it has acted on
// (taken); a ring it leaves untaken for task_timeout latches TASK_STALLED
// (§7.2).
//
// The teach pins (picker.N.pos-x/-y and -mu) are the function's too: the
// snapshot's feedback plus the picker's offset, every cycle.

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
    uint64_t servo_ns;         // servo time: the periods of every run so far
    uint32_t change;           // the change count, with this frame's ring
    uint32_t pad5_;

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

    pnp_rt_edges_t     edges;   // counted since the function started

    pnp_rt_job_t       job;       // the last job accepted
    pnp_rt_latch_t     latch;     // the error latch as on the pins
    uint32_t           park_seq;  // parks accepted
    uint32_t           raise_seen; // Go's last raise_seq this function took
    uint8_t            busy;      // a job or park accepted and not ended
    uint8_t            busy_park; // ... and it is a park
    uint8_t            scene_granted; // the scene lock is Go's (scene-busy)
    uint8_t            pad4_[1];
    uint32_t           watch_fired;   // the lift watch seq that fired

    mot_snapshot_t     mot;     // motmod's state this cycle, valid with mot_ok
    uint8_t            mot_ok;
    uint8_t            pad3_[7];
} pnp_rt_in_t;

// --- Go -> RT: the outputs of one Go commit ----------------------------------

typedef struct {
    double   pos_x, pos_y, pos_x_mu, pos_y_mu;   // RT
    double   x_offset, y_offset;                 // the picker's offset params
    uint32_t origin_id;
    uint8_t  close, holds;
    uint8_t  missing;          // RT
    uint8_t  pad_;
} pnp_rt_out_picker_t;

typedef struct {
    uint32_t count, resets;
    uint8_t  empty, avail, full;
    uint8_t  pad_[5];
} pnp_rt_out_tray_t;

typedef struct {
    uint32_t resets;
    uint8_t  has_material;
    uint8_t  release;          // Go's, held low by a fired lift watch (RT)
    uint8_t  pad_[2];
} pnp_rt_out_proc_t;

// The fields marked RT are the pins the function owns: Go never writes them,
// the function puts its own values in before the frame goes to the pins.
typedef struct {
    uint32_t seq;              // Go's commit number

    // Go's requests (§4.4)
    uint32_t job_done;         // = the job's seq: the job is over
    uint32_t park_done;        // = park_seq: the park is over
    uint32_t raise_seq;        // counts a fault to latch ...
    uint32_t raise_id, raise_station;
    uint8_t  raise_missing;    // ... with these (missing: bit per picker)
    uint8_t  scene_req;        // Go wants the scene lock
    uint8_t  pad0_[2];
    uint32_t watch_seq;        // counts a lift watch armed ...
    int32_t  watch_id;         // ... to fire past this segment ...
    uint32_t watch_release;    // ... and hold these stations' release low (bit per proc)

    uint64_t wake_at;          // servo ns to ring at, 0: none
    uint64_t task_timeout;     // ns a ring may stay untaken, 0: no check
    uint32_t taken;            // the change count Go has acted on
    uint32_t pad1_;
    double   linear_units;     // machine units per mm, for the -mu teach pins

    double   plan_time;
    uint32_t sync;             // the cycle of the input frame Go last acted on
    uint32_t cycle;            // RT
    uint32_t error_id, error_station;  // RT

    uint8_t  machine_is_on, homed, parked;
    uint8_t  busy;             // RT
    uint8_t  scene_busy;       // RT
    uint8_t  error;            // RT
    uint8_t  pad_[2];

    pnp_rt_out_picker_t picker[PNP_RT_MAX_PICKERS];
    pnp_rt_out_tray_t   tray[PNP_RT_MAX_TRAYS];
    pnp_rt_out_proc_t   proc[PNP_RT_MAX_PROCS];
} pnp_rt_out_t;

// --- the function's arg block -----------------------------------------------

// What the loop acts on, as one block to compare with the last cycle's: the
// inputs (the input frame's bound fields, from jog_speed up to the edges), the
// counters and handshake state, and the motion snapshot but for its positions
// and velocity.
#define PNP_RT_SIG_INPUTS (offsetof(pnp_rt_in_t, edges) - offsetof(pnp_rt_in_t, jog_speed))

typedef struct {
    uint8_t        inputs[PNP_RT_SIG_INPUTS];
    pnp_rt_edges_t edges;
    pnp_rt_job_t   job;
    pnp_rt_latch_t latch;
    uint32_t       park_seq, raise_seen, watch_fired;
    uint8_t        busy, busy_park, scene_granted, mot_ok;
    int32_t        command_num_echo, command_status, motion_state, enabled;
    int32_t        inpos, queue_depth, id, carte_pos_cmd_ok, carte_pos_fb_ok;
    uint32_t       joints_homed, joints_homing;
} pnp_rt_sig_t;

// TASK_STALLED: errors.go's errTaskStalled, latched by the function itself.
#define PNP_RT_TASK_STALLED 27

typedef struct {
    stmak_tribuf_t *to_go;     // pnp_rt_in_t, RT writes
    stmak_tribuf_t *to_rt;     // pnp_rt_out_t, Go writes
    stmak_pinmap_t *in;        // input pins -> pnp_rt_in_t
    stmak_pinmap_t *out;       // pnp_rt_out_t -> output pins
    pnp_dz_t       *dz;        // the dead-zone check's block, and mot
    stmak_doorbell_t *bell;    // cycle ticks after every publish

    uint32_t cycle;

    // Edge counting: how many of each the frames hold, the counts, and last
    // cycle's input levels.
    int            n_pickers, n_trays, n_procs;
    int            primed;
    pnp_rt_edges_t edges;
    pnp_rt_in_t    prev;

    // The handshakes (§4.4).
    pnp_rt_job_t   job;
    pnp_rt_latch_t latch;
    uint32_t       park_seq, raise_seen;
    int            busy, busy_park;
    int            scene_granted;
    uint32_t       watch_fired;

    // The output frame as it goes to the pins: Go's, with RT's pins put in.
    pnp_rt_out_t   composed;

    // Step 7: servo time, the change counter, the stall check, the last
    // cycle's signature of what Go acts on, the teach positions.
    uint64_t       servo_ns, wake_rung, stall_ns;
    uint32_t       change;
    int            stalled;
    pnp_rt_sig_t   sig, sig_prev;
    double         pos_x[PNP_RT_MAX_PICKERS], pos_y[PNP_RT_MAX_PICKERS];
} pnp_rt_t;

void pnp_rt_cycle(void *arg, long period) STMAK_NONBLOCKING;

// pnp_rt_cycle's address, as a real symbol (see hal.CFunct).
extern const stmak_hal_funct_t pnp_rt_cycle_fp;

#endif // PNP_RT_H
