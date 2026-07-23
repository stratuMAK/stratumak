/********************************************************************
 * Description: IoControl_v2.c
 *           IO controller (v2 abort/fault-handshake protocol): drives the
 *           HAL estop/tool/coolant/lube pins.
 *
 *   Built as a C plugin (.so) loaded by gomc-server via:
 *
 *       load iov2
 *
 *   The launcher calls New (constructor + HAL init), Start (resolve
 *   tooltable), Stop (signal shutdown), and Destroy (release resources)
 *   in that order during its lifecycle. There is NO NML transport and NO
 *   main loop thread: milltask invokes the emcio GMI callbacks directly
 *   through cgo function pointers, and all HAL reads/writes happen inside
 *   those synchronous callbacks (the tool-change/prepare handshakes and
 *   the emc-abort-ack handshake busy-wait on their HAL pins).
 *
 *   Derived from a work by Fred Proctor & Will Shackleford
 *
 * Author:
 * License: GPL Version 2
 * Copyright (c) 2004 All rights reserved.
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de> — cmod port
 *
 *  ENABLE logic:  this module exports three HAL pins related to ENABLE.
 *  The first is emc-enable-in.  It is an input from the HAL, when FALSE,
 *  EMC will go into the STOPPED state (regardless of the state of
 *  the other two pins).  When it goes TRUE, EMC will go into the
 *  ESTOP_RESET state (also known as READY).
 *
 *  The second HAL pin is an output to the HAL.  It is controlled by
 *  the NML messages ESTOP_ON and ESTOP_OFF, which normally result from
 *  user actions at the GUI.  For the simplest system, loop user-enable-out
 *  back to emc-enable-in in the HAL.  The GUI controls user-enable-out, and EMC
 *  responds to that once it is looped back.
 *
 *  If external ESTOP inputs are desired, they can be
 *  used in a classicladder rung, in series with user-enable-out.
 *  It will look like this:
 *
 *  -----|UEO|-----|EEST|--+--|EEI|--+--(EEI)----
 *                         |         |
 *                         +--|URE|--+
 *  UEO=user-enable-out
 *  EEST=external ESTOP circuitry
 *  EEI=machine is enabled
 *  URE=user request enable
 *
 *  This will work like this: EMC will be enabled (by EEI, emc-enabled-in),
 *  only if UEO, EEST are closed when URE gets strobed.
 *  If any of UEO (user requested stop) or EEST (external estop) have been
 *  opened, then EEI will open as well.
 *  After restoring normal condition (UEO and EEST closed), an additional
 *  URE (user-request-enable) is needed, this is either sent by the GUI
 *  (using the EMC_AUX_ESTOP_RESET NML message), or by a hardware button
 *  connected to the ladder driving URE.
 *
 *  NML messages are sent usually from the user hitting F1 on the GUI.
 *
 *   Derived from a work by Fred Proctor & Will Shackleford
 *
 * Author:
 * License: GPL Version 2
 * System: Linux
 *
 * Copyright (c) 2004 All rights reserved.
 *
 * Last change: Michael Haberler 1/2011 - rework & add v2 protocol support
 *              Ported to cmod plugin API
 ********************************************************************/

#include <stdio.h>
#include <sys/types.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>
#include <ctype.h>
#include <stdatomic.h>
#include <pthread.h>

#include "iocontrol_stat.h"

#include "gomc/pkg/cmodule/gomc_env.h"
#include "gomc/generated/gmi/emcio/emcio_api.h"
#include "gomc/generated/gmi/tooltable/tooltable_api.h"

#define UNEXPECTED_MSG fprintf(stderr,"UNEXPECTED %s %d\n",__FILE__,__LINE__);

typedef enum {
    V1 = 1,
    V2 = 2
} version_t;

// extend EMC_IO_ABORT_REASON_ENUM from emc.hh
enum {
    EMC_ABORT_BY_TOOLCHANGER_FAULT = EMC_ABORT_USER + 1
};

// iocontrol states. Reflected in state pin
typedef  enum {
    ST_IDLE = 0,
    ST_PREPARING = 1,
    ST_START_CHANGE = 2, // V2 only
    ST_CHANGING = 3,
    ST_WAIT_FOR_ABORT_ACK = 4, // V2 only
} iostate_t;

typedef struct iocontrol_str {
    gomc_hal_bit_t *user_enable_out;        /* output, TRUE when EMC wants stop */
    gomc_hal_bit_t *emc_enable_in;        /* input, TRUE on any external stop */
    gomc_hal_bit_t *user_request_enable;        /* output, used to reset ENABLE latch */
    gomc_hal_bit_t *coolant_mist;        /* coolant mist output pin */
    gomc_hal_bit_t *coolant_flood;        /* coolant flood output pin */
    gomc_hal_bit_t *lube;                /* lube output pin */
    gomc_hal_bit_t *lube_level;        /* lube level input pin */

    // the following pins are needed for toolchanging
    //tool-prepare
    gomc_hal_bit_t *tool_prepare;        /* output, pin that notifies HAL it needs to prepare a tool */
    gomc_hal_s32_t *tool_prep_pocket;/* output, pin that holds the P word from the tool table entry matching the tool to be prepared,
                                   only valid when tool-prepare=TRUE */
    gomc_hal_s32_t *tool_prep_index; /* output, internal index (idx) of prepped tool above */
    gomc_hal_s32_t *tool_prep_number;/* output, pin that holds the tool number to be prepared, only valid when tool-prepare=TRUE */
    gomc_hal_s32_t *tool_number;     /* output, pin that holds the tool number currently in the spindle */
    gomc_hal_bit_t *tool_prepared;        /* input, pin that notifies that the tool has been prepared */
    //tool-change
    gomc_hal_bit_t *tool_change;        /* output, notifies a tool-change should happen (emc should be in the tool-change position) */
    gomc_hal_bit_t *tool_changed;        /* input, notifies tool has been changed */

    // v2 protocol
    // iocontrolv2 -> toolchanger
    gomc_hal_bit_t *emc_abort;         /* output, signals emc-originated abort to toolchanger */
    gomc_hal_bit_t *emc_abort_ack;         /* input, handshake line to acknowledge abort_tool_change */
    gomc_hal_s32_t *emc_reason;             /* output, convey cause for EMC-originated abort to toolchanger. */

    // toolchanger -> iocontrolv2
    gomc_hal_bit_t *toolchanger_fault;        /* input, toolchanger signals fault */
    gomc_hal_bit_t *toolchanger_fault_ack;        /* handshake line for above signal */
    gomc_hal_s32_t *toolchanger_reason;         /* input, convey reason code for toolchanger-originated fault */

    gomc_hal_bit_t *start_change;              /* signal begin of M6 cycle */
    gomc_hal_bit_t *start_change_ack;          /* acknowledge line for start_change */

    // other:
    gomc_hal_bit_t *toolchanger_faulted;         /* output. signals toolchanger-fault line has toggled */
    gomc_hal_bit_t *toolchanger_clear_fault;        /* input. resets TC fault condition. */
    gomc_hal_s32_t *state;                         /* output. Internal state for debugging */
} iocontrol_str;

// iocontrol_module holds all per-instance state.  Allocated in New(),
// freed in Destroy().
typedef struct iocontrol_module {
    cmod_t base;                        // must be first — contains lifecycle vtable
    const cmod_env_t *env;              // launcher-provided environment
    char name[64];                      // instance name (HAL component name)

    // HAL
    int comp_id;
    iocontrol_str *hal_data;

    // Cached IO status (used by GMI callbacks)
    iocontrol_stat_t emcioStatus;

    // Configuration (read from INI via env->get_ini in New)
    int debug;
    double io_cycle_time;
    int random_toolchanger;
    int proto;
    int support_start_change;

    // tooltable GMI instance name (resolved at New time)
    char tooltable_instance[64];

    // tooltable API pointer (looked up once in Start)
    const tooltable_callbacks_t *tt;

    // Runtime state
    int toolchanger_reason;  // last fault reason read from toolchanger

    // Shutdown flag (checked by blocking GMI callbacks)
    atomic_int done;

    // Serialises the iocontrol state machine across the goroutines that reach
    // it concurrently: the sequencer (tool prepare/change loops), the command
    // path (io_abort, estop, coolant) and the monitor's status poll
    // (gmi_get_status -> poll_inputs). The busy-wait loops MUST release it
    // across their usleep so the poll can reap handshakes and an abort can land.
    pthread_mutex_t io_mtx;

    // GMI callback table (persists for lifetime of module)
    emcio_callbacks_t emcio_cb;
} iocontrol_module;

// predicates for testing toolchanger fault conditions
#define TC_FAULT  (*(m->hal_data->toolchanger_faulted))
#define TC_HARDFAULT (TC_FAULT && (m->toolchanger_reason <= 0))
#define TC_SOFTFAULT (TC_FAULT && (m->toolchanger_reason > 0))

// iniLoad reads configuration from the launcher's parsed INI via env callbacks.
static int iniLoad(iocontrol_module *m)
{
    const cmod_env_t *env = m->env;
    const char *val;

    val = env->ini->get(env->ini->ctx, "EMC", "DEBUG");
    if (val) {
        if (1 != sscanf(val, "%i", &m->debug)) {
            m->debug = 0;
        }
    } else {
        m->debug = 0;
    }

    val = env->ini->get(env->ini->ctx, "EMCIO", "CYCLE_TIME");
    if (val) {
        double temp = m->io_cycle_time;
        if (1 != sscanf(val, "%lf", &m->io_cycle_time)) {
            m->io_cycle_time = temp;
            gomc_log_warnf(m->env->log, m->name, "invalid [EMCIO] CYCLE_TIME (%s); using default %f",
                        val, m->io_cycle_time);
        }
    }

    val = env->ini->get(env->ini->ctx, "EMCIO", "PROTOCOL_VERSION");
    if (val) {
        sscanf(val, "%i", &m->proto);
    }
    gomc_log_debugf(m->env->log, m->name, "%s: [EMCIO] using v%d protocol", m->name, m->proto);

    val = env->ini->get(env->ini->ctx, "EMCIO", "RANDOM_TOOLCHANGER");
    if (val) {
        m->random_toolchanger = atoi(val);
    }

    return 0;
}

/********************************************************************
*
* Description: iocontrol_hal_init()
*
* Side Effects: Exports HAL pins.
*
* Called By: New()
********************************************************************/
static int iocontrol_hal_init(iocontrol_module *m)
{
    int retval;

    /* STEP 1: initialise the hal component */
    m->comp_id = m->env->hal->init(m->env->hal->ctx, m->name, m->env->dl_handle, GOMC_HAL_COMP_USER);
    if (m->comp_id < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: hal_init() failed");
        return -1;
    }

    /* STEP 2: allocate shared memory for iocontrol data */
    m->hal_data = (iocontrol_str *) m->env->hal->malloc(m->env->hal->ctx, sizeof(iocontrol_str));
    if (m->hal_data == 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: hal_malloc() failed");
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }

    /* STEP 3a: export the out-pin(s) */

    // user-enable-out
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->user_enable_out), m->comp_id,
                              "%s.user-enable-out", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin user-enable-out export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // user-request-enable
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->user_request_enable), m->comp_id,
                             "%s.user-request-enable", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin user-request-enable export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // coolant-flood
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->coolant_flood), m->comp_id,
                         "%s.coolant-flood", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin coolant-flood export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // coolant-mist
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->coolant_mist), m->comp_id,
                              "%s.coolant-mist", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin coolant-mist export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // lube
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->lube), m->comp_id,
                              "%s.lube", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin lube export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-number
    retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_number), m->comp_id,
                              "%s.tool-number", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-number export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-prep-number
    retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_prep_number), m->comp_id,
                              "%s.tool-prep-number", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-prep-number export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-prep-pocket
    retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_prep_pocket), m->comp_id,
                              "%s.tool-prep-pocket", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-prep-pocket export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-prep-index (idx)
    retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_prep_index), m->comp_id,
                              "%s.tool-prep-index", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-prep-index export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-prepare
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_prepare), m->comp_id,
                              "%s.tool-prepare", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-prepare export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-prepared
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->tool_prepared), m->comp_id,
                              "%s.tool-prepared", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-prepared export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-change
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->tool_change), m->comp_id,
                              "%s.tool-change", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-change export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // tool-changed
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->tool_changed), m->comp_id,
                        "%s.tool-changed", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin tool-changed export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }

    /* STEP 3b: export the in-pin(s) */

    // emc-enable-in
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->emc_enable_in), m->comp_id,
                             "%s.emc-enable-in", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin emc-enable-in export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }
    // lube_level
    retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->lube_level), m->comp_id,
                             "%s.lube_level", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin lube_level export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }

    // state pin (present in all protocol versions)
    retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->state), m->comp_id,
                              "%s.state", m->name);
    if (retval < 0) {
        gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin state export failed with err=%i",
                        m->name, retval);
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        return -1;
    }

    // v2 protocol pins
    if (m->proto > V1) {
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->emc_abort), m->comp_id,
                                  "%s.emc-abort", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin emc-abort export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->emc_abort_ack), m->comp_id,
                                  "%s.emc-abort-ack", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin emc-abort-ack export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->emc_reason), m->comp_id,
                                  "%s.emc-reason", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin emc-reason export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->toolchanger_fault), m->comp_id,
                                  "%s.toolchanger-fault", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin toolchanger-fault export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->toolchanger_fault_ack), m->comp_id,
                                  "%s.toolchanger-fault-ack", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin toolchanger-fault-ack export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_s32_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->toolchanger_reason), m->comp_id,
                                  "%s.toolchanger-reason", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin toolchanger-reason export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->toolchanger_faulted), m->comp_id,
                                  "%s.toolchanger-faulted", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin toolchanger-faulted export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->toolchanger_clear_fault), m->comp_id,
                                  "%s.toolchanger-clear-fault", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin toolchanger-clear-fault export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_OUT, &(m->hal_data->start_change), m->comp_id,
                                  "%s.start-change", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin start-change export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
        retval = gomc_hal_pin_bit_newf(m->env->hal, GOMC_HAL_IN, &(m->hal_data->start_change_ack), m->comp_id,
                                  "%s.start-change-ack", m->name);
        if (retval < 0) {
            gomc_log_errorf(m->env->log, m->name, "IOV2: ERROR: %s pin start-change-ack export failed with err=%i",
                            m->name, retval);
            m->env->hal->exit(m->env->hal->ctx, m->comp_id);
            return -1;
        }
    }

    gomc_log_debugf(m->env->log, m->name, "%s: iocontrol_hal_init() complete", m->name);
    m->env->hal->ready(m->env->hal->ctx, m->comp_id);
    return 0;
}


/********************************************************************
 *
 * Description: hal_init_pins(void)
 *
 * Side Effects: Sets HAL pins default values.
 *
 * Called By: New, EMC_IO_INIT handler
 ********************************************************************/
static void hal_init_pins(iocontrol_module *m)
{
    iocontrol_str *d = m->hal_data;
    *(d->user_enable_out) = 0;
    *(d->user_request_enable) = 0;
    *(d->coolant_mist) = 0;
    *(d->coolant_flood) = 0;
    *(d->lube) = 0;
    *(d->tool_prepare) = 0;
    *(d->tool_prep_number) = 0;
    *(d->tool_prep_pocket) = 0;
    *(d->tool_prep_index) = 0;
    *(d->tool_change) = 0;

    *(d->state) = ST_IDLE;

    if (m->proto > V1) {
        // v2 protocol output pins
        *(d->emc_abort) = 0;
        *(d->emc_reason) = 0;
        *(d->toolchanger_fault_ack) = 0;
        *(d->toolchanger_faulted) = 0;
        *(d->start_change) = 0;
    }
}

// tt_get_tool / tt_put_tool wrap the tooltable calls, which report a storage
// failure through their rc (tooltable.gmi is @rc_error).  Before that the
// payload came back by value with nowhere to put an error, so a broken tool
// table was indistinguishable from an unstored tool — and on the tool-change
// path that means driving the spindle with zeroed offsets.  An unstored slot
// is still the empty entry with rc 0; only a real failure returns -1.
//
// The tool table is addressed by SLOT (idx), exactly as 2.9's tooldata is:
// idx 0 is the spindle slot, idx 1..N the table proper.  Everything below
// therefore speaks in idx, and converts a tool number to a slot through
// tt_find_idx (2.9's tooldata_find_index_for_tool) at the boundary.
static int tt_get_tool(iocontrol_module *m, int32_t idx,
                       tooltable_tool_entry_t *entry)
{
    memset(entry, 0, sizeof(*entry));
    if (m->tt->get_tool(m->tt->ctx, idx, entry) != 0) {
        memset(entry, 0, sizeof(*entry));
        gomc_log_errorf(m->env->log, m->name,
            "IOV2: ERROR: tool table read failed for slot %d", idx);
        return -1;
    }
    return 0;
}

static int tt_put_tool(iocontrol_module *m, int32_t idx,
                       const tooltable_tool_entry_t *entry)
{
    tooltable_put_tool_result_t res;
    memset(&res, 0, sizeof(res));
    // iocontrol is an unconditional writer: it acts on a physical tool change
    // that has already happened, so there is nothing to reconcile and nothing
    // to refuse. Clear the optimistic-concurrency stamp — a slot read carries
    // the stamp of ITS row, and copying a tool into the spindle slot would
    // otherwise be checked against the spindle's stamp and always refused.
    tooltable_tool_entry_t e = *entry;
    e.updated = 0;
    if (m->tt->put_tool(m->tt->ctx, idx, &e, &res) != 0 || !res.ok) {
        gomc_log_errorf(m->env->log, m->name,
            "IOV2: ERROR: tool table write failed for slot %d", idx);
        return -1;
    }
    return 0;
}

// tt_find_idx resolves a tool number to its slot.  Returns -1 when the tool
// is not in the table (a normal answer), or -2 when the lookup itself failed
// — an unreadable tool table must not be mistaken for an absent tool, which
// on the change path means driving the spindle with zeroed offsets.
static int tt_find_idx(iocontrol_module *m, int32_t toolno)
{
    tooltable_index_result_t res;
    memset(&res, 0, sizeof(res));
    if (m->tt->find_index_for_tool(m->tt->ctx, toolno, &res) != 0) {
        gomc_log_errorf(m->env->log, m->name,
            "IOV2: ERROR: tool table lookup failed for tool %d", toolno);
        return -2;
    }
    return res.idx;
}

// load_tool moves table slot idx into the spindle slot, mirroring 2.9's
// load_tool(idx) (ioControl.cc:541).
//
//   RANDOM     the spindle and the target slot swap, and the two rows swap
//              carousel pockets with them: the outgoing tool takes the
//              target's pocket, the incoming tool takes pocket 0.
//   NON-RANDOM idx 0 is the "unload" handshake and resets the spindle slot;
//              any other slot is copied to slot 0 VERBATIM — the copy keeps
//              the tool's own pocketno, because on a non-random changer the
//              pocket is the tool's home in the carousel and slot 0 is only
//              ever a copy of what is currently mounted.
static void load_tool(iocontrol_module *m, int idx) {
    if(m->random_toolchanger) {
        // Swap the tools between the desired slot and the spindle slot.
        tooltable_tool_entry_t tzero, tpocket;
        if (tt_get_tool(m, 0, &tzero) != 0
            || tt_get_tool(m, idx, &tpocket) != 0) {
            UNEXPECTED_MSG; return;
        }

        // spindle --> pocket (the slot named by idx)
        tzero.pocketno = tpocket.pocketno;
        if (tt_put_tool(m, idx, &tzero) != 0) {
            UNEXPECTED_MSG; return;
        }

        // pocket --> spindle (slot 0)
        tpocket.pocketno = 0;
        if (tt_put_tool(m, 0, &tpocket) != 0) {
            UNEXPECTED_MSG; return;
        }
    } else if(idx == 0) {
        // on non-random tool-changers, asking for slot 0 is the secret
        // handshake for "unload the tool from the spindle"
        tooltable_tool_entry_t empty;
        memset(&empty, 0, sizeof(empty));
        empty.toolno   = 0; // nonrandom unload tool from spindle
        empty.pocketno = 0;
        if (tt_put_tool(m, 0, &empty) != 0) {
            UNEXPECTED_MSG; return;
        }
    } else {
        // just copy the desired tool to the spindle slot
        tooltable_tool_entry_t tdata;
        if (tt_get_tool(m, idx, &tdata) != 0) {
            UNEXPECTED_MSG; return;
        }
        if (tt_put_tool(m, 0, &tdata) != 0) {
            UNEXPECTED_MSG; return;
        }
    }
} // load_tool()

static void reload_tool_number(iocontrol_module *m, int toolno) {
    if(m->random_toolchanger) return; // doesn't need special handling here
    if(toolno <= 0) return;
    int idx = tt_find_idx(m, toolno);
    if (idx > 0) {
        load_tool(m, idx);
    }
}

/********************************************************************
* GMI emcio callbacks — called by milltask via function pointers.
* These implement the v2 protocol with abort/fault handshake.
********************************************************************/

// poll_inputs — the non-blocking, async half of 2.9's read_inputs().
//
// 2.9 iocontrol was a free-running loop that serviced these handshakes every
// cycle; gomc has no such loop, so this runs on the monitor's periodic status
// poll (see gmi_get_status). It latches the toolchanger-fault line, services
// the fault-ack / clear-fault handshakes, and reaps the emc-abort ->
// emc-abort-ack handshake — so no abort/estop/tool path ever has to block
// waiting for the toolchanger to respond. Caller MUST hold m->io_mtx.
static void poll_inputs(iocontrol_module *m)
{
    iocontrol_str *d = m->hal_data;
    if (m->proto <= V1)
        return;

    // Latch a toolchanger fault and raise the ack line (2.9 read_inputs).
    if (*(d->toolchanger_fault)) {
        m->toolchanger_reason = *(d->toolchanger_reason);
        *(d->toolchanger_fault_ack) = 1;
        *(d->toolchanger_faulted) = 1;
    } else {
        *(d->toolchanger_fault_ack) = 0;
    }
    // Honour a clear-fault request (2.9 clears unconditionally after latching).
    if (*(d->toolchanger_clear_fault)) {
        *(d->toolchanger_faulted) = 0;
        m->toolchanger_reason = 0;
    }
    // Reap an in-progress emc-abort once the toolchanger acknowledges it
    // (2.9 read_inputs: emc_abort && emc_abort_ack -> deassert, force IDLE).
    if (*(d->emc_abort) && *(d->emc_abort_ack)) {
        *(d->emc_abort) = 0;
        *(d->emc_reason) = 0;
        *(d->state) = ST_IDLE;
    }
}

static int32_t gmi_io_abort(void *ctx, int32_t reason)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;

    gomc_log_debugf(m->env->log, m->name, "gmi_io_abort reason=%d", reason);

    pthread_mutex_lock(&m->io_mtx);
    m->emcioStatus.coolant.mist = 0;
    m->emcioStatus.coolant.flood = 0;
    *(d->coolant_mist) = 0;
    *(d->coolant_flood) = 0;

    if (m->proto > V1) {
        // Assert the abort to the toolchanger BEFORE clearing the request
        // lines (2.9 EMC_TOOL_ABORT race fix). This is an abort/estop/teardown
        // path: do NOT block on the ack here — poll_inputs() reaps the
        // emc-abort -> emc-abort-ack handshake on the monitor status poll.
        *(d->emc_reason) = reason;
        *(d->emc_abort) = 1;
    }
    *(d->tool_change) = 0;   // abort an in-progress tool change
    *(d->tool_prepare) = 0;  // abort an in-progress tool prepare
    *(d->start_change) = 0;
    *(d->state) = (m->proto > V1) ? ST_WAIT_FOR_ABORT_ACK : ST_IDLE;
    pthread_mutex_unlock(&m->io_mtx);
    return 0;
}

static int32_t gmi_set_debug(void *ctx, int32_t debug)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    gomc_log_debugf(m->env->log, m->name, "gmi_set_debug debug=%d", debug);
    m->debug = debug;
    return 0;
}

static int32_t gmi_estop_on(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    gomc_log_debugf(m->env->log, m->name, "gmi_estop_on");
    *(m->hal_data->user_enable_out) = 0;
    hal_init_pins(m);
    return 0;
}

static int32_t gmi_estop_off(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    gomc_log_debugf(m->env->log, m->name, "gmi_estop_off");
    *(m->hal_data->user_enable_out) = 1;
    *(m->hal_data->user_request_enable) = 1;
    return 0;
}

static int32_t gmi_coolant_mist_on(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.coolant.mist = 1;
    *(m->hal_data->coolant_mist) = 1;
    return 0;
}

static int32_t gmi_coolant_mist_off(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.coolant.mist = 0;
    *(m->hal_data->coolant_mist) = 0;
    return 0;
}

static int32_t gmi_coolant_flood_on(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.coolant.flood = 1;
    *(m->hal_data->coolant_flood) = 1;
    return 0;
}

static int32_t gmi_coolant_flood_off(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.coolant.flood = 0;
    *(m->hal_data->coolant_flood) = 0;
    return 0;
}

static int32_t gmi_lube_on(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.lube.on = 1;
    *(m->hal_data->lube) = 1;
    return 0;
}

static int32_t gmi_lube_off(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    m->emcioStatus.lube.on = 0;
    *(m->hal_data->lube) = 0;
    return 0;
}

static int32_t gmi_tool_prepare(void *ctx, int32_t toolno)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;

    // pocketPrepped is an IDX, as it is in 2.9 — the slot the prepped tool
    // lives in, which is also what the tool-prep-index HAL pin carries.
    int idx = tt_find_idx(m, toolno);
    if (idx < 0) {  // -2 lookup failed, -1 tool not in the table
        m->emcioStatus.tool.pocketPrepped = -1;
        return -1;
    }

    tooltable_tool_entry_t tdata;
    if (tt_get_tool(m, idx, &tdata) != 0) {
        m->emcioStatus.tool.pocketPrepped = -1;
        return -1;
    }

    gomc_log_debugf(m->env->log, m->name, "gmi_tool_prepare tool=%d idx=%d pocket=%d",
                    toolno, idx, tdata.pocketno);

    *(d->tool_prep_index) = idx;  // any type of changer

    if (m->random_toolchanger) {
        *(d->tool_prep_number) = tdata.toolno;
        if (idx == 0) {
            // It doesn't make sense to prep the spindle pocket: the tool is
            // already there. Complete without raising tool-prepare.
            m->emcioStatus.tool.pocketPrepped = 0;
            *(d->tool_prep_pocket) = 0;
            return 0;
        }
        *(d->tool_prep_pocket) = tdata.pocketno;
    } else {
        if (idx == 0) {
            // Non-random T0: the "unload" handshake. 2.9 publishes
            // pocketPrepped=0 up front but still runs the external prepare.
            m->emcioStatus.tool.pocketPrepped = 0;
            *(d->tool_prep_number) = 0;
            *(d->tool_prep_pocket) = 0;
        } else {
            *(d->tool_prep_number) = tdata.toolno;
            *(d->tool_prep_pocket) = tdata.pocketno;
        }
    }

    // v2: warn if toolchanger is faulted — next M6 will abort
    if ((m->proto > V1) && *(d->toolchanger_faulted)) {
        gomc_log_debugf(m->env->log, m->name,
            "prepare: toolchanger faulted (reason=%d), next M6 will %s",
            m->toolchanger_reason,
            m->toolchanger_reason > 0 ? "set fault code and reason" : "abort program");
    }

    // Signal HAL and wait for tool-prepared. poll_inputs() (monitor status
    // poll) owns the toolchanger-fault latch; this loop only completes on the
    // prepared ack, or bails when an abort clears tool_prepare.
    *(d->tool_prepare) = 1;
    *(d->state) = ST_PREPARING;
    while (!m->done) {
        pthread_mutex_lock(&m->io_mtx);
        // Abort: gmi_io_abort cleared tool_prepare (2.9: abort clears the
        // request line and the wait ends). Leave state as io_abort set it.
        if (!*(d->tool_prepare)) {
            pthread_mutex_unlock(&m->io_mtx);
            return -1;
        }
        if (*(d->tool_prepared)) {
            m->emcioStatus.tool.pocketPrepped = idx;
            *(d->tool_prepare) = 0;
            *(d->state) = ST_IDLE;
            pthread_mutex_unlock(&m->io_mtx);
            return 0;
        }
        pthread_mutex_unlock(&m->io_mtx);
        usleep((useconds_t)(m->io_cycle_time * 1e6));
    }
    return -1;  // shutdown
}

static int32_t gmi_tool_start_change(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;

    gomc_log_debugf(m->env->log, m->name, "gmi_tool_start_change");

    if ((m->proto > V1) && m->support_start_change) {
        *(d->start_change) = 1;
        *(d->state) = ST_START_CHANGE;
        while (!m->done) {
            pthread_mutex_lock(&m->io_mtx);
            // Abort: gmi_io_abort cleared start_change — bail (leave state).
            if (!*(d->start_change)) {
                pthread_mutex_unlock(&m->io_mtx);
                return -1;
            }
            if (*(d->start_change_ack)) {
                *(d->start_change) = 0;
                *(d->state) = ST_IDLE;
                pthread_mutex_unlock(&m->io_mtx);
                return 0;
            }
            pthread_mutex_unlock(&m->io_mtx);
            usleep((useconds_t)(m->io_cycle_time * 1e6));
        }
        return -1;  // shutdown
    }
    return 0;
}

static int32_t gmi_tool_load(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;

    gomc_log_debugf(m->env->log, m->name, "gmi_tool_load loaded=%d prepped=%d",
                    m->emcioStatus.tool.toolInSpindle,
                    m->emcioStatus.tool.pocketPrepped);

    int prepped_idx = m->emcioStatus.tool.pocketPrepped;

    if (prepped_idx == -1)
        return 0;

    // It doesn't make sense to load a tool from the spindle slot.
    if (m->random_toolchanger && prepped_idx == 0)
        return 0;

    // It's not necessary to load the tool already in the spindle.
    tooltable_tool_entry_t prepped;
    if (tt_get_tool(m, prepped_idx, &prepped) != 0) {
        UNEXPECTED_MSG; return -1;
    }
    if (!m->random_toolchanger && prepped_idx > 0 &&
        m->emcioStatus.tool.toolInSpindle == prepped.toolno)
        return 0;

    // v2: toolchanger already faulted -> abort before starting the change.
    // Non-blocking: poll_inputs() reaps the emc-abort -> emc-abort-ack.
    pthread_mutex_lock(&m->io_mtx);
    if ((m->proto > V1) && *(d->toolchanger_faulted)) {
        m->toolchanger_reason = *(d->toolchanger_reason);
        *(d->emc_reason) = EMC_ABORT_BY_TOOLCHANGER_FAULT;
        *(d->emc_abort) = 1;
        *(d->state) = ST_WAIT_FOR_ABORT_ACK;
        m->emcioStatus.fault = 1;
        m->emcioStatus.reason = m->toolchanger_reason;
        pthread_mutex_unlock(&m->io_mtx);
        return -1;
    }

    // Signal HAL and wait for tool-changed. poll_inputs() (monitor status
    // poll) owns the toolchanger-fault latch; this loop reads the latched
    // toolchanger_faulted, completes on tool_changed, or bails on abort.
    *(d->tool_change) = 1;
    *(d->state) = ST_CHANGING;
    pthread_mutex_unlock(&m->io_mtx);

    while (!m->done) {
        pthread_mutex_lock(&m->io_mtx);

        // A toolchanger fault (latched by poll_inputs) aborts the change.
        if ((m->proto > V1) && *(d->toolchanger_faulted)) {
            m->toolchanger_reason = *(d->toolchanger_reason);
            *(d->tool_change) = 0;
            *(d->emc_reason) = EMC_ABORT_BY_TOOLCHANGER_FAULT;
            *(d->emc_abort) = 1;
            *(d->state) = ST_WAIT_FOR_ABORT_ACK;
            m->emcioStatus.fault = 1;
            m->emcioStatus.reason = m->toolchanger_reason;
            pthread_mutex_unlock(&m->io_mtx);
            return -1;
        }

        // Abort: gmi_io_abort cleared tool_change (2.9: abort clears the
        // request line and the wait ends). Leave state as io_abort set it.
        if (!*(d->tool_change)) {
            gomc_log_debugf(m->env->log, m->name, "gmi_tool_load aborted");
            m->emcioStatus.tool.pocketPrepped = -1;
            *(d->tool_prep_number) = 0;
            *(d->tool_prep_pocket) = 0;
            *(d->tool_prep_index) = 0;
            pthread_mutex_unlock(&m->io_mtx);
            return -1;
        }

        if (*(d->tool_changed)) {
            // Re-read the prepped slot under the lock and BEFORE load_tool:
            // a random change swaps it into the spindle, losing the tool
            // number that identifies what is now mounted.
            tooltable_tool_entry_t tdp;
            if (tt_get_tool(m, prepped_idx, &tdp) != 0) {
                pthread_mutex_unlock(&m->io_mtx);
                UNEXPECTED_MSG; return -1;
            }
            // Update the tool table DB BEFORE publishing toolInSpindle.
            // The stat watch goroutine uses toolInSpindle as a cache key;
            // if it observes the new value before the DB is updated, the
            // UI fetches stale data and caches it permanently (race).
            load_tool(m, prepped_idx);
            if (!m->random_toolchanger && prepped_idx == 0) {
                m->emcioStatus.tool.toolInSpindle = 0;
            } else {
                m->emcioStatus.tool.toolInSpindle = tdp.toolno;
            }
            *(d->tool_number) = m->emcioStatus.tool.toolInSpindle;
            m->emcioStatus.tool.pocketPrepped = -1;
            *(d->tool_prep_number) = 0;
            *(d->tool_prep_pocket) = 0;
            *(d->tool_prep_index) = 0;
            *(d->tool_change) = 0;
            *(d->state) = ST_IDLE;
            m->emcioStatus.fault = 0;
            pthread_mutex_unlock(&m->io_mtx);
            return 0;
        }

        pthread_mutex_unlock(&m->io_mtx);
        usleep((useconds_t)(m->io_cycle_time * 1e6));
    }
    return -1;  // shutdown
}

static int32_t gmi_tool_unload(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    gomc_log_debugf(m->env->log, m->name, "gmi_tool_unload");
    m->emcioStatus.tool.toolInSpindle = 0;
    return 0;
}

static int32_t gmi_tool_load_table(void *ctx, const char *file)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    (void)file;
    // With tooltable, reloading is a no-op — the SQLite DB is always current.
    gomc_log_debugf(m->env->log, m->name, "gmi_tool_load_table (no-op with tooltable)");
    reload_tool_number(m, m->emcioStatus.tool.toolInSpindle);
    return 0;
}

static int32_t gmi_tool_set_offset(void *ctx,
    int32_t pocket, int32_t toolno,
    double x, double y, double z,
    double a, double b, double c,
    double u, double v, double w,
    double diameter, double frontangle, double backangle,
    int32_t orientation)
{
    iocontrol_module *m = (iocontrol_module *)ctx;

    // `pocket` is the interp's tool_table[] subscript, i.e. our slot idx —
    // see the identical note in ioControl.c.
    int32_t idx = pocket;

    gomc_log_debugf(m->env->log, m->name,
        "gmi_tool_set_offset idx=%d toolno=%d z=%lf x=%lf dia=%lf",
        idx, toolno, z, x, diameter);

    // Read-modify-write, as 2.9 does: a fresh entry drops the tool's carousel
    // pocketno and its comment, and the G10 spindle-copy call (idx 0) then
    // wrote pocketno=0 onto the real tool's row.
    tooltable_tool_entry_t entry;
    if (tt_get_tool(m, idx, &entry) != 0) {
        UNEXPECTED_MSG;
        return -1;
    }
    entry.toolno = toolno;
    entry.x_offset = x;
    entry.y_offset = y;
    entry.z_offset = z;
    entry.a_offset = a;
    entry.b_offset = b;
    entry.c_offset = c;
    entry.u_offset = u;
    entry.v_offset = v;
    entry.w_offset = w;
    entry.diameter = diameter;
    entry.frontangle = frontangle;
    entry.backangle = backangle;
    entry.orientation = orientation;

    if (tt_put_tool(m, idx, &entry) != 0) {
        UNEXPECTED_MSG;
        return -1;
    }
    return 0;
}

// M61 Q<n>. The interp resolves the tool number to a slot and sends the SLOT
// (interp_convert.cc: change_tool_number(settings->current_pocket)).
static int32_t gmi_tool_set_number(void *ctx, int32_t idx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;

    load_tool(m, idx);

    // Read the spindle slot back rather than assuming: load_tool decides what
    // ends up there (2.9 does the same re-read after load_tool).
    tooltable_tool_entry_t spindle;
    if (tt_get_tool(m, 0, &spindle) != 0) {
        UNEXPECTED_MSG;
        return -1;
    }
    m->emcioStatus.tool.toolInSpindle = spindle.toolno;
    gomc_log_debugf(m->env->log, m->name,
        "gmi_tool_set_number idx=%d new_tool=%d", idx, spindle.toolno);
    *(d->tool_number) = m->emcioStatus.tool.toolInSpindle;
    return 0;
}

static emcio_io_status_t gmi_get_status(void *ctx)
{
    iocontrol_module *m = (iocontrol_module *)ctx;
    iocontrol_str *d = m->hal_data;
    emcio_io_status_t s;
    memset(&s, 0, sizeof(s));

    // Estop is read lock-free (single HAL word) so a tool-change commit
    // holding io_mtx can never delay external-estop detection.
    s.estop = (*(d->emc_enable_in) == 0);

    pthread_mutex_lock(&m->io_mtx);
    // Service the toolchanger-fault + emc-abort handshakes on this poll
    // (the async half of 2.9's per-cycle read_inputs()).
    poll_inputs(m);

    s.lube_level = *(d->lube_level);
    s.heartbeat = m->emcioStatus.heartbeat++;
    s.status = EMCIO_DONE;
    s.reason = m->toolchanger_reason;
    s.fault = (m->proto > V1 && *(d->toolchanger_faulted)) ? 1 : 0;
    s.tool.pocket_prepped = m->emcioStatus.tool.pocketPrepped;
    s.tool.tool_in_spindle = m->emcioStatus.tool.toolInSpindle;
    s.tool.tool_from_pocket = 0;  // v2 doesn't have tool_from_pocket pin
    s.coolant.mist = m->emcioStatus.coolant.mist;
    s.coolant.flood = m->emcioStatus.coolant.flood;
    s.lube_on = m->emcioStatus.lube.on;
    s.debug = m->debug;
    pthread_mutex_unlock(&m->io_mtx);

    return s;
}

static const emcio_callbacks_t emcio_table = {
    .ctx              = NULL,  // set at registration time
    .io_abort         = gmi_io_abort,
    .set_debug        = gmi_set_debug,
    .estop_on         = gmi_estop_on,
    .estop_off        = gmi_estop_off,
    .coolant_mist_on  = gmi_coolant_mist_on,
    .coolant_mist_off = gmi_coolant_mist_off,
    .coolant_flood_on = gmi_coolant_flood_on,
    .coolant_flood_off= gmi_coolant_flood_off,
    .lube_on          = gmi_lube_on,
    .lube_off         = gmi_lube_off,
    .tool_prepare     = gmi_tool_prepare,
    .tool_start_change= gmi_tool_start_change,
    .tool_load        = gmi_tool_load,
    .tool_unload      = gmi_tool_unload,
    .tool_load_table  = gmi_tool_load_table,
    .tool_set_offset  = gmi_tool_set_offset,
    .tool_set_number  = gmi_tool_set_number,
    .get_status       = gmi_get_status,
};

/********************************************************************
* cmod lifecycle functions
********************************************************************/

static int iocontrol_start(cmod_t *self)
{
    iocontrol_module *m = (iocontrol_module *)self->priv;

    // Look up the tooltable API (must have been loaded before us)
    m->tt = tooltable_api_get(m->env->api, m->tooltable_instance);
    if (!m->tt) {
        gomc_log_errorf(m->env->log, m->name,
            "IOV2: tooltable instance '%s' not found — "
            "ensure 'load tooltable' appears before 'load iov2' in HAL",
            m->tooltable_instance);
        return -1;
    }

    // RANDOM_TOOLCHANGER: restore the tool physically loaded in the spindle
    // from the spindle slot — 2.9 ioControl inits toolInSpindle from tooldata
    // idx 0; -1 = unknown when the slot is empty. A non-random changer starts
    // with an empty spindle (2.9: toolInSpindle = 0), because there is no
    // record of what was left mounted across a restart.
    if (m->random_toolchanger) {
        tooltable_tool_entry_t spindle;
        if (tt_get_tool(m, 0, &spindle) != 0) {
            // A tool table that cannot be read at startup must not be taken
            // for an empty one: that would silently report an empty spindle
            // and lose the tool physically loaded in it.
            gomc_log_errorf(m->env->log, m->name,
                "IOV2: ERROR: tool table read failed at startup");
            return -1;
        }
        m->emcioStatus.tool.toolInSpindle = spindle.toolno;
        *(m->hal_data->tool_number) = spindle.toolno;
    }


    m->done = 0;
    return 0;
}

static void iocontrol_stop(cmod_t *self)
{
    iocontrol_module *m = (iocontrol_module *)self->priv;
    m->done = 1;
}

static void iocontrol_destroy(cmod_t *self)
{
    iocontrol_module *m = (iocontrol_module *)self->priv;

    if (m->comp_id > 0) {
        m->env->hal->exit(m->env->hal->ctx, m->comp_id);
        m->comp_id = 0;
    }

    pthread_mutex_destroy(&m->io_mtx);

    gomc_log_warnf(m->env->log, m->name, "%s: exiting", m->name);
    free(m);
}

/********************************************************************
* New — cmod factory function.
* The launcher calls dlsym(handle, "New") to find this.
********************************************************************/
int New(const cmod_env_t *env, const char *name,
        int argc, const char **argv, cmod_t **out)
{
    iocontrol_module *m = calloc(1, sizeof(*m));
    if (!m) return -1;

    m->env = env;
    strncpy(m->name, name, sizeof(m->name) - 1);

    // Defaults
    m->io_cycle_time = 0.100;
    m->proto = V2;
    m->support_start_change = 0;
    strncpy(m->tooltable_instance, "tooltable", sizeof(m->tooltable_instance) - 1);

    // Parse arguments
    for (int t = 0; t < argc; t++) {
        if (!strcmp(argv[t], "-support-start-change") || !strcmp(argv[t], "support-start-change")) {
            m->support_start_change = 1;
        } else if (strncmp(argv[t], "tooltable_instance=", 19) == 0) {
            strncpy(m->tooltable_instance, argv[t] + 19, sizeof(m->tooltable_instance) - 1);
        }
    }

    // Read configuration from INI via launcher callbacks
    if (0 != iniLoad(m)) {
        free(m);
        return -1;
    }

    // Initialise HAL component and pins.
    if (iocontrol_hal_init(m) != 0) {
        free(m);
        return -1;
    }

    hal_init_pins(m);

    m->emcioStatus.aux.estop = 1;
    m->emcioStatus.tool.pocketPrepped = -1;
    m->emcioStatus.tool.toolInSpindle = 0;
    m->emcioStatus.coolant.mist = 0;
    m->emcioStatus.coolant.flood = 0;
    m->emcioStatus.lube.on = 0;
    m->emcioStatus.lube.level = 1;

    pthread_mutex_init(&m->io_mtx, NULL);

    // Register GMI emcio API so milltask can call us via function pointers.
    m->emcio_cb = emcio_table;
    m->emcio_cb.ctx = m;
    emcio_api_register(m->env->api, m->name, &m->emcio_cb);

    // Wire up the cmod vtable
    m->base.Start   = iocontrol_start;
    m->base.Stop    = iocontrol_stop;
    m->base.Destroy = iocontrol_destroy;
    m->base.priv    = m;

    *out = &m->base;
    return 0;
}
