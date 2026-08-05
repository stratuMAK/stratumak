/********************************************************************
* Description: motion_struct.h
*   A data structure used in only a few places
*
* Author:
* License: GPL Version 2
* System: Linux
*
* Copyright (c) 2004 All rights reserved
********************************************************************/

#ifndef MOTION_STRUCT_H
#define MOTION_STRUCT_H

#include <rtapi_mutex.h>
#include <stdatomic.h>

/* Lock-free SPSC triple buffer for status.
   Writer (RT servo thread) and a single logical consumer exchange slots
   through an atomic middle index.  The writer tags the index it publishes
   with MOTSTAT_MIDDLE_DIRTY; the reader swaps only when that flag is set
   and otherwise re-reads its own slot, which still holds the newest
   snapshot it ever took.  Without the flag an unconditional exchange hands
   the reader its own previously-returned slot back whenever it polls
   faster than the servo publishes — status then alternates between the
   two latest snapshots and motion ids go BACKWARDS (observed as stat
   readers seeing pruned/stale segment ids).  Only the reader stores an
   untagged index, so a flag observed set cannot vanish before the
   reader's exchange.  Multiple physical readers are serialized by
   reader_mtx so the buffer sees exactly one consumer. */
#define MOTSTAT_SLOTS 3
#define MOTSTAT_MIDDLE_DIRTY 4              /* bit above the 2-bit slot index */
#define MOTSTAT_MIDDLE_IDX(v) ((v) & 3)     /* strip the dirty flag */

typedef struct emcmot_status_buf_t {
    struct emcmot_status_t slots[MOTSTAT_SLOTS];
    atomic_int middle;          /* exchange slot between writer and reader */
    int write_idx;              /* writer-private slot index */
    int read_idx;               /* reader-private slot index (under reader_mtx) */
    rtapi_mutex_t reader_mtx;   /* serialize consumer access */
} emcmot_status_buf_t;

/* big comm structure, for upper memory */
    typedef struct emcmot_struct_t {
        rtapi_mutex_t command_mutex;  // Used to protect access to `command`.
        struct emcmot_command_t command;   /* struct used to pass commands/data from Task to Motion */

	struct emcmot_status_t status;	/* Legacy single status (used as writer workspace) */
	emcmot_status_buf_t status_buf;	/* Triple-buffered status for readers */
	struct emcmot_config_t config;	/* Struct used to store RT config */
	struct emcmot_internal_t internal;	/* Struct used to store RT status and debug
				   data - 2nd largest block */
    } emcmot_struct_t;


#endif // MOTION_STRUCT_H
