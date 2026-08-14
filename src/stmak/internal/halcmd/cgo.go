// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package halcmd

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include -I${SRCDIR}/../../pkg/cmodule
#cgo LDFLAGS: -ldl

#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <sys/wait.h>
#include <unistd.h>
#include <stdio.h>
#include <errno.h>
#include <ctype.h>
#include <fnmatch.h>
#include <fcntl.h>
#include <spawn.h>
#include <dlfcn.h>
#include <stdatomic.h>
#include <stdarg.h>
#include <time.h>
#include <limits.h>
#include "config.h"
#include "rtapi.h"
#include "hal.h"
#include "hal_priv.h"
#include "stmak_log.h"

extern char **environ;

// Helper to convert hal_type_t to int for Go
static inline int get_hal_type(hal_type_t t) { return (int)t; }

// Helper to convert hal_pin_dir_t to int for Go
static inline int get_hal_dir(hal_pin_dir_t d) { return (int)d; }

// hal_shim_list_comps walks the HAL component list and writes each component
// name as a null-terminated string into buf (strings are concatenated).
// Returns the number of components found, or a negative errno value on error.
// Returns -ENOSPC if the buffer is too small to hold all names.
static int hal_shim_list_comps(char *buf, int buf_size) {
    void *next;
    hal_comp_t *comp;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) {
        return -EINVAL;
    }

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->comp_list_ptr;
    while (next != 0) {
        comp = (hal_comp_t *)SHMPTR(next);
        name_len = (int)strlen(comp->name) + 1; // include null terminator
        if (pos + name_len > buf_size) {
            rtapi_mutex_give(&(hal_data->mutex));
            return -ENOSPC;
        }
        memcpy(buf + pos, comp->name, name_len);
        pos += name_len;
        count++;
        next = comp->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// HAL_SHIM_MAX_COMPS is the maximum number of components tracked by the shim
// helpers. 256 exceeds any realistic LinuxCNC machine configuration.
#define HAL_SHIM_MAX_COMPS 256

// hal_shim_report states why a shim is about to fail, to the log and into the
// caller's buffer — the same contract as hal_lib's hal_report (see the
// HAL_ERRLEN block in hal.h), for the refusals that happen here rather than in
// hal_lib.  The shims below resolve names and parse values themselves, so those
// failures never reach hal_lib and would otherwise come back as a bare errno.
//
// err may be NULL, in which case only the log line is produced.
static void hal_shim_report(char *err, int errlen, const char *fmt, ...)
    __attribute__((format(printf, 3, 4)));

static void hal_shim_report(char *err, int errlen, const char *fmt, ...) {
    char line[HAL_ERRLEN];
    va_list ap;

    va_start(ap, fmt);
    vsnprintf(line, sizeof(line), fmt, ap);
    va_end(ap);

    rtapi_print_msg(RTAPI_MSG_ERR, "HAL: ERROR: %s\n", line);

    if (err != NULL && errlen > 0) {
        strncpy(err, line, (size_t)errlen - 1);
        err[errlen - 1] = '\0';
    }
}

// ===== RTAPI message routing through stmak_log ring =====
// RT threads push messages into the stmak_log ring buffer.  The Go drain
// goroutine reads them and handles stdout/stderr output + subscriber fan-out.

// Forward declaration — defined in uspace_common.h (linked via hallib).
typedef void(*rtapi_msg_handler_t)(msg_level_t level, const char *fmt, va_list ap);
extern void rtapi_set_msg_handler(rtapi_msg_handler_t handler);

static stmak_log_t rt_log;  // initialized by hal_shim_set_log_ring

static stmak_log_level_t rtapi_level_to_stmak(msg_level_t level) {
    switch (level) {
    case RTAPI_MSG_DBG:  return STMAK_LOG_DEBUG;
    case RTAPI_MSG_INFO:
    case RTAPI_MSG_ALL:  return STMAK_LOG_INFO;
    case RTAPI_MSG_WARN: return STMAK_LOG_WARN;
    case RTAPI_MSG_ERR:  return STMAK_LOG_ERROR;
    default:             return STMAK_LOG_INFO;
    }
}

static void rt_msg_handler(msg_level_t level, const char *fmt, va_list ap) {
    if (rt_log.ring) {
        stmak_log_emit(&rt_log, rtapi_level_to_stmak(level), "rtapi", fmt, ap);
    }
}

// hal_shim_set_log_ring sets the stmak_log ring for the RTAPI message handler.
// Must be called before hal_shim_rtapi_app_init().
static void hal_shim_set_log_ring(stmak_log_ring_t *ring) {
    rt_log.ring = ring;
}

// hal_shim_rtapi_app_init performs the in-process equivalent of rtapi_app's
// master() startup: sets up the message handler and calls halpr_rtapi_app_main().
static int hal_shim_rtapi_app_init(void) {
    int result;
    rtapi_set_msg_handler(rt_msg_handler);

    result = halpr_rtapi_app_main();
    if (result != 0) {
        return result;
    }
    return 0;
}

// hal_shim_rtapi_app_cleanup performs the in-process equivalent of rtapi_app's
// master() cleanup: calls halpr_rtapi_app_exit().
static void hal_shim_rtapi_app_cleanup(void) {
    halpr_rtapi_app_exit();
}

// hal_shim_unload_all unloads all HAL components:
//   - Userspace components: send SIGTERM to their owning process
//   - Realtime components: unload via direct rtapi_dlclose (in-process)
// The component identified by except_id is skipped (pass 0 to not skip any).
// Returns 0 on success, or a negative errno value on error.
static int hal_shim_unload_all(int except_id) {
    void *next;
    hal_comp_t *comp;
    pid_t ourpid = getpid();

    if (hal_data == NULL) {
        return -EINVAL;
    }

    // Phase 1: send SIGTERM to userspace components
    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->comp_list_ptr;
    while (next != 0) {
        comp = (hal_comp_t *)SHMPTR(next);
        if (comp->pid != 0 && comp->pid != ourpid) {
            if (comp->comp_id != except_id) {
                kill(abs(comp->pid), SIGTERM);
            }
        }
        next = comp->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));

    // Phase 2: Realtime components are unloaded by the cmod infrastructure.

    return 0;
}

// hal_shim_lock_dl_handle locks the PT_LOAD segments of a single dlopen handle.
static void hal_shim_lock_dl_handle(void *handle) {
    rtapi_lock_dl_handle(handle);
}

// hal_shim_unlock_dl_handle unlocks the PT_LOAD segments of a single dlopen handle.
static void hal_shim_unlock_dl_handle(void *handle) {
    rtapi_unlock_dl_handle(handle);
}

// ===== 1a. Simple wrapper shims =====

// hal_shim_newsig wraps hal_signal_new(name, type)
static int hal_shim_newsig(const char *name, int type, char *err, int errlen) {
    return hal_signal_new_ex(name, (hal_type_t)type, err, errlen);
}

// hal_shim_delsig wraps hal_signal_delete(name)
static int hal_shim_delsig(const char *name, char *err, int errlen) {
    return hal_signal_delete_ex(name, err, errlen);
}

// hal_shim_retain sets HAL_SIGFLAG_RETAIN on a signal.
static int hal_shim_retain(const char *name, char *err, int errlen) {
    hal_sig_t *sig;
    if (hal_data == NULL) {
        hal_shim_report(err, errlen, "retain called before init");
        return -EINVAL;
    }
    rtapi_mutex_get(&(hal_data->mutex));
    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen, "signal '%s' not found", name);
        return -EINVAL;
    }
    if (sig->writers > 0) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen,
            "signal '%s' has %d writer(s); only an unwritten signal can be "
            "retained", name, sig->writers);
        return -EINVAL;
    }
    sig->flags |= HAL_SIGFLAG_RETAIN;
    rtapi_mutex_give(&(hal_data->mutex));
    return 0;
}

// hal_shim_unretain clears HAL_SIGFLAG_RETAIN on a signal.
static int hal_shim_unretain(const char *name, char *err, int errlen) {
    hal_sig_t *sig;
    if (hal_data == NULL) {
        hal_shim_report(err, errlen, "unretain called before init");
        return -EINVAL;
    }
    rtapi_mutex_get(&(hal_data->mutex));
    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen, "signal '%s' not found", name);
        return -EINVAL;
    }
    sig->flags &= ~HAL_SIGFLAG_RETAIN;
    rtapi_mutex_give(&(hal_data->mutex));
    return 0;
}

// hal_shim_linkps wraps hal_link(pin, sig)
static int hal_shim_linkps(const char *pin, const char *sig,
                           char *err, int errlen) {
    return hal_link_ex(pin, sig, err, errlen);
}

// hal_shim_unlinkp wraps hal_unlink(pin)
static int hal_shim_unlinkp(const char *pin, char *err, int errlen) {
    return hal_unlink_ex(pin, err, errlen);
}

// hal_shim_addf wraps hal_add_funct_to_thread(funct, thread, position)
static int hal_shim_addf(const char *funct, const char *thread, int position,
                         char *err, int errlen) {
    return hal_add_funct_to_thread_ex(funct, thread, position, err, errlen);
}

// hal_shim_delf wraps hal_del_funct_from_thread(funct, thread)
static int hal_shim_delf(const char *funct, const char *thread,
                         char *err, int errlen) {
    return hal_del_funct_from_thread_ex(funct, thread, err, errlen);
}

// hal_shim_set_lock wraps hal_set_lock(lock_type)
static int hal_shim_set_lock(unsigned char lock_type, char *err, int errlen) {
    return hal_set_lock_ex(lock_type, err, errlen);
}

// hal_shim_get_lock wraps hal_get_lock()
static unsigned char hal_shim_get_lock(void) {
    return hal_get_lock();
}

// hal_shim_pin_alias wraps hal_pin_alias(pin_name, alias).
// alias may be NULL to remove an alias.
static int hal_shim_pin_alias(const char *pin_name, const char *alias,
                              char *err, int errlen) {
    return hal_pin_alias_ex(pin_name, alias, err, errlen);
}

// hal_shim_param_alias wraps hal_param_alias(param_name, alias).
static int hal_shim_param_alias(const char *param_name, const char *alias,
                                char *err, int errlen) {
    return hal_param_alias_ex(param_name, alias, err, errlen);
}

// ===== 1b. Value access helper functions =====

// hal_shim_write_value writes a value string to a HAL data location.
// This mirrors the set_common() logic from halcmd_commands.cc.
// Assumes the HAL mutex is already held by the caller.
static int hal_shim_write_value(hal_type_t type, void *d_ptr, const char *value,
                                char *err, int errlen) {
    double fval;
    long lval;
    unsigned long ulval;
    char *cp;

    switch (type) {
    case HAL_BIT:
        if (strcmp("1", value) == 0 || strcasecmp("TRUE", value) == 0) {
            *(hal_bit_t *)d_ptr = 1;
        } else if (strcmp("0", value) == 0 || strcasecmp("FALSE", value) == 0) {
            *(hal_bit_t *)d_ptr = 0;
        } else {
            hal_shim_report(err, errlen,
                "value '%s' is not a valid bit value (use 1, 0, TRUE or FALSE)",
                value);
            return -EINVAL;
        }
        break;
    case HAL_FLOAT:
        cp = (char *)value;
        fval = strtod(value, &cp);
        if (*cp != '\0' && !isspace((unsigned char)*cp)) {
            hal_shim_report(err, errlen, "value '%s' is not a valid float", value);
            return -EINVAL;
        }
        *(hal_float_t *)d_ptr = (hal_float_t)fval;
        break;
    case HAL_S32:
        cp = (char *)value;
        lval = strtol(value, &cp, 0);
        if (*cp != '\0' && !isspace((unsigned char)*cp)) {
            hal_shim_report(err, errlen, "value '%s' is not a valid s32", value);
            return -EINVAL;
        }
        *(hal_s32_t *)d_ptr = (hal_s32_t)lval;
        break;
    case HAL_U32:
        cp = (char *)value;
        ulval = strtoul(value, &cp, 0);
        if (*cp != '\0' && !isspace((unsigned char)*cp)) {
            hal_shim_report(err, errlen, "value '%s' is not a valid u32", value);
            return -EINVAL;
        }
        *(hal_u32_t *)d_ptr = (hal_u32_t)ulval;
        break;
    case HAL_PORT:
        cp = (char *)value;
        ulval = strtoul(value, &cp, 0);
        if (*cp != '\0' && !isspace((unsigned char)*cp)) {
            hal_shim_report(err, errlen,
                "value '%s' is not a valid port buffer size", value);
            return -EINVAL;
        }
        if ((*((hal_port_t *)d_ptr) != 0) && (hal_port_buffer_size(*((hal_port_t *)d_ptr)) > 0)) {
            hal_shim_report(err, errlen,
                "port buffer is already allocated; its size cannot be changed");
            return -EINVAL;
        }
        *((hal_port_t *)d_ptr) = hal_port_alloc(ulval);
        break;
    default:
        hal_shim_report(err, errlen, "cannot write a value of HAL type %d",
            (int)type);
        return -EINVAL;
    }
    return 0;
}

// hal_shim_format_value formats a HAL data value as a string into buf.
// Returns 0 on success, -EINVAL for unknown types.
// Mirrors the data_value2() logic from halcmd_commands.cc.
static int hal_shim_format_value(hal_type_t type, void *d_ptr, char *buf, int buf_size) {
    switch (type) {
    case HAL_BIT:
        snprintf(buf, buf_size, "%s", (*(hal_bit_t *)d_ptr) ? "TRUE" : "FALSE");
        break;
    case HAL_FLOAT:
        snprintf(buf, buf_size, "%.7g", (double)(*(hal_float_t *)d_ptr));
        break;
    case HAL_S32:
        snprintf(buf, buf_size, "%ld", (long)(*(hal_s32_t *)d_ptr));
        break;
    case HAL_U32:
        snprintf(buf, buf_size, "%lu", (unsigned long)(*(hal_u32_t *)d_ptr));
        break;
    default:
        snprintf(buf, buf_size, "unknown");
        return -EINVAL;
    }
    return 0;
}

// ===== 1b. Shmem access shims =====

// hal_shim_setp sets the value of a pin or parameter by name.
// Tries pin first, then parameter. Mirrors halcmd's do_setp_cmd logic.
// Returns 0 on success, negative errno on error.
static int hal_shim_setp(const char *name, const char *value,
                         char *err, int errlen) {
    hal_pin_t *pin;
    hal_param_t *param;
    hal_type_t type;
    void *d_ptr;
    int retval;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    param = halpr_find_param_by_name(name);
    if (param) {
        if (param->dir == HAL_RO) {
            rtapi_mutex_give(&(hal_data->mutex));
            hal_shim_report(err, errlen, "parameter '%s' is read-only", name);
            return -EPERM;
        }
        type = param->type;
        d_ptr = SHMPTR(param->data_ptr);
        retval = hal_shim_write_value(type, d_ptr, value, err, errlen);
        rtapi_mutex_give(&(hal_data->mutex));
        return retval;
    }

    pin = halpr_find_pin_by_name(name);
    if (pin) {
        if (pin->dir == HAL_OUT) {
            rtapi_mutex_give(&(hal_data->mutex));
            hal_shim_report(err, errlen,
                "pin '%s' is an output pin; its value is set by its owning "
                "component", name);
            return -EPERM;
        }
        if (pin->signal != 0) {
            const char *signame = ((hal_sig_t *)SHMPTR(pin->signal))->name;
            rtapi_mutex_give(&(hal_data->mutex));
            hal_shim_report(err, errlen,
                "pin '%s' is linked to signal '%s'; set the signal with sets, "
                "or unlink the pin first", name, signame);
            return -EBUSY;
        }
        type = pin->type;
        d_ptr = (void *)&pin->dummysig;
        retval = hal_shim_write_value(type, d_ptr, value, err, errlen);
        rtapi_mutex_give(&(hal_data->mutex));
        return retval;
    }

    rtapi_mutex_give(&(hal_data->mutex));
    hal_shim_report(err, errlen, "no pin or parameter named '%s'", name);
    return -ENOENT;
}

// hal_shim_getp gets the value of a pin or parameter as a string.
// Writes the value into buf (max buf_size bytes).
// Returns 0 on success, negative errno on error.
static int hal_shim_getp(const char *name, char *buf, int buf_size,
                         char *err, int errlen) {
    hal_pin_t *pin;
    hal_sig_t *sig;
    hal_param_t *param;
    hal_type_t type;
    void *d_ptr;
    int retval;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    param = halpr_find_param_by_name(name);
    if (param) {
        type = param->type;
        d_ptr = SHMPTR(param->data_ptr);
        retval = hal_shim_format_value(type, d_ptr, buf, buf_size);
        rtapi_mutex_give(&(hal_data->mutex));
        return retval;
    }

    pin = halpr_find_pin_by_name(name);
    if (pin) {
        type = pin->type;
        if (pin->signal != 0) {
            sig = (hal_sig_t *)SHMPTR(pin->signal);
            d_ptr = SHMPTR(sig->data_ptr);
        } else {
            d_ptr = (void *)&pin->dummysig;
        }
        retval = hal_shim_format_value(type, d_ptr, buf, buf_size);
        rtapi_mutex_give(&(hal_data->mutex));
        return retval;
    }

    rtapi_mutex_give(&(hal_data->mutex));
    hal_shim_report(err, errlen, "no pin or parameter named '%s'", name);
    return -ENOENT;
}

// hal_shim_sets sets the value of a signal by name.
// Returns 0 on success, negative errno on error.
static int hal_shim_sets(const char *name, const char *value,
                         char *err, int errlen) {
    hal_sig_t *sig;
    hal_type_t type;
    void *d_ptr;
    int retval;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen, "signal '%s' not found", name);
        return -ENOENT;
    }

    if (sig->type != HAL_PORT && sig->writers > 0) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen,
            "signal '%s' is driven by %d output pin(s); its value cannot be set",
            name, sig->writers);
        return -EBUSY;
    }

    type = sig->type;
    d_ptr = SHMPTR(sig->data_ptr);
    retval = hal_shim_write_value(type, d_ptr, value, err, errlen);
    rtapi_mutex_give(&(hal_data->mutex));
    return retval;
}

// hal_shim_gets gets the value of a signal as a string.
// Writes the value into buf (max buf_size bytes).
// Returns 0 on success, negative errno on error.
static int hal_shim_gets(const char *name, char *buf, int buf_size,
                         char *err, int errlen) {
    hal_sig_t *sig;
    hal_type_t type;
    void *d_ptr;
    int retval;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen, "signal '%s' not found", name);
        return -ENOENT;
    }

    type = sig->type;
    d_ptr = SHMPTR(sig->data_ptr);
    retval = hal_shim_format_value(type, d_ptr, buf, buf_size);
    rtapi_mutex_give(&(hal_data->mutex));
    return retval;
}

// hal_shim_ptype returns the HAL type (as int) of a pin or parameter by name.
// Returns the hal_type_t value on success, negative errno on error.
static int hal_shim_ptype(const char *name, char *err, int errlen) {
    hal_pin_t *pin;
    hal_param_t *param;
    int type;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    param = halpr_find_param_by_name(name);
    if (param) {
        type = (int)param->type;
        rtapi_mutex_give(&(hal_data->mutex));
        return type;
    }

    pin = halpr_find_pin_by_name(name);
    if (pin) {
        type = (int)pin->type;
        rtapi_mutex_give(&(hal_data->mutex));
        return type;
    }

    rtapi_mutex_give(&(hal_data->mutex));
    hal_shim_report(err, errlen, "no pin or parameter named '%s'", name);
    return -ENOENT;
}

// hal_shim_stype returns the HAL type (as int) of a signal by name.
// Returns the hal_type_t value on success, negative errno on error.
static int hal_shim_stype(const char *name, char *err, int errlen) {
    hal_sig_t *sig;
    int type;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));

    sig = halpr_find_sig_by_name(name);
    if (sig == NULL) {
        rtapi_mutex_give(&(hal_data->mutex));
        hal_shim_report(err, errlen, "signal '%s' not found", name);
        return -ENOENT;
    }

    type = (int)sig->type;
    rtapi_mutex_give(&(hal_data->mutex));
    return type;
}

// ===== 1c. Net command shim =====

// HAL_SHIM_MAX_PINS is the maximum number of pins accepted by hal_shim_net.
#define HAL_SHIM_MAX_PINS 64

// hal_shim_net implements the "net" command.
// sig_name is the signal name. pin_names is a null-separated list of pin names.
// num_pins is the number of pins.
// Arrow tokens must be stripped by the Go caller before calling this function.
// Returns 0 on success, negative errno on error.
static int hal_shim_net(const char *sig_name, const char *pin_names, int num_pins,
                        char *err, int errlen) {
    hal_sig_t *sig;
    hal_type_t sig_type;
    int i, retval = 0;
    const char *pins[HAL_SHIM_MAX_PINS];
    const char *p;

    if (hal_data == NULL) return -EINVAL;
    if (num_pins <= 0 || num_pins > HAL_SHIM_MAX_PINS) {
        hal_shim_report(err, errlen, "signal '%s': %d pins given; expected 1..%d",
            sig_name, num_pins, HAL_SHIM_MAX_PINS);
        return -EINVAL;
    }

    // Decode null-separated pin names
    p = pin_names;
    for (i = 0; i < num_pins; i++) {
        pins[i] = p;
        p += strlen(p) + 1;
    }

    rtapi_mutex_get(&(hal_data->mutex));
    sig = halpr_find_sig_by_name(sig_name);

    if (!sig) {
        // Create signal with the type of the first pin
        hal_pin_t *pin = halpr_find_pin_by_name(pins[0]);
        if (!pin) {
            rtapi_mutex_give(&(hal_data->mutex));
            hal_shim_report(err, errlen,
                "signal '%s': pin '%s' not found, and it is the pin the new "
                "signal would take its type from", sig_name, pins[0]);
            return -ENOENT;
        }
        sig_type = pin->type;
        rtapi_mutex_give(&(hal_data->mutex));
        retval = hal_signal_new_ex(sig_name, sig_type, err, errlen);
    } else {
        rtapi_mutex_give(&(hal_data->mutex));
    }

    if (retval != 0) return retval;

    // Link each pin to the signal
    for (i = 0; i < num_pins && retval == 0; i++) {
        retval = hal_link_ex(pins[i], sig_name, err, errlen);
    }

    return retval;
}

// ===== 1d. Process management shims =====

// hal_shim_newinst creates a new instance of a HAL component.
static int hal_shim_newinst(const char *type, const char *name, const char *arg) {
    hal_comp_t *comp = halpr_find_comp_by_name((char *)type);
    if (!comp) {
        rtapi_print_msg(RTAPI_MSG_ERR,
            "newinst: component %s not found\n", type);
        return -ENOENT;
    }
    if (!arg) arg = "";
    return comp->make((char *)name, (char *)arg);
}

// ===== 1e. Query/list shims =====

// hal_shim_list_pins lists pin names matching pattern.
// Writes null-separated names into buf. Returns count or negative errno.
static int hal_shim_list_pins(const char *pattern, char *buf, int buf_size) {
    hal_pin_t *pin;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->pin_list_ptr;
    while (next != 0) {
        pin = (hal_pin_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, pin->name, 0) == 0) {
            name_len = (int)strlen(pin->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, pin->name, name_len);
            pos += name_len;
            count++;
        }
        next = pin->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_list_sigs lists signal names matching pattern.
static int hal_shim_list_sigs(const char *pattern, char *buf, int buf_size) {
    hal_sig_t *sig;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->sig_list_ptr;
    while (next != 0) {
        sig = (hal_sig_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, sig->name, 0) == 0) {
            name_len = (int)strlen(sig->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, sig->name, name_len);
            pos += name_len;
            count++;
        }
        next = sig->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_list_retain_sigs lists signal names that have the HAL_SIGFLAG_RETAIN
// flag set and whose name matches pattern (NULL or empty string matches all).
// This mirrors halcmd's do_list_cmd "retain" special case.
static int hal_shim_list_retain_sigs(const char *pattern, char *buf, int buf_size) {
    hal_sig_t *sig;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->sig_list_ptr;
    while (next != 0) {
        sig = (hal_sig_t *)SHMPTR(next);
        if ((sig->flags & HAL_SIGFLAG_RETAIN) &&
            (pattern == NULL || *pattern == '\0' ||
             fnmatch(pattern, sig->name, 0) == 0)) {
            name_len = (int)strlen(sig->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, sig->name, name_len);
            pos += name_len;
            count++;
        }
        next = sig->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_list_params lists parameter names matching pattern.
static int hal_shim_list_params(const char *pattern, char *buf, int buf_size) {
    hal_param_t *param;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->param_list_ptr;
    while (next != 0) {
        param = (hal_param_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, param->name, 0) == 0) {
            name_len = (int)strlen(param->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, param->name, name_len);
            pos += name_len;
            count++;
        }
        next = param->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_list_functs lists function names matching pattern.
static int hal_shim_list_functs(const char *pattern, char *buf, int buf_size) {
    hal_funct_t *funct;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->funct_list_ptr;
    while (next != 0) {
        funct = (hal_funct_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, funct->name, 0) == 0) {
            name_len = (int)strlen(funct->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, funct->name, name_len);
            pos += name_len;
            count++;
        }
        next = funct->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_list_threads lists thread names matching pattern.
static int hal_shim_list_threads(const char *pattern, char *buf, int buf_size) {
    hal_thread_t *thread;
    void *next;
    int count = 0;
    int pos = 0;
    int name_len;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->thread_list_ptr;
    while (next != 0) {
        thread = (hal_thread_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, thread->name, 0) == 0) {
            name_len = (int)strlen(thread->name) + 1;
            if (pos + name_len > buf_size) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            memcpy(buf + pos, thread->name, name_len);
            pos += name_len;
            count++;
        }
        next = thread->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// ===== 1f. Show/status/save/debug shim structs and helpers =====

// HAL_SHIM_MAX_ITEMS is the initial result-array capacity for hal_shim_show_*
// functions.  If the real count exceeds this the shim returns -ENOSPC and the
// Go caller retries with a doubled capacity (up to showMaxCap).
#define HAL_SHIM_MAX_ITEMS 1024

// HAL_SHIM_MAX_TH_FNCTS is the maximum number of functions per thread stored
// in hal_shim_thread_info_t.funct_names.  In practice LinuxCNC threads rarely
// have more than a handful of functions; 32 is well above any real-world value.
// Functions beyond this limit are silently truncated in the output.
#define HAL_SHIM_MAX_TH_FNCTS 32

typedef struct {
    char name[HAL_NAME_LEN + 1];
    int  comp_id;
    int  type_;  // component_type_t: 0=user, 1=realtime, 2=other
    int  ready;
} hal_shim_comp_info_t;

typedef struct {
    char name[HAL_NAME_LEN + 1];
    char owner[HAL_NAME_LEN + 1];
    char signal[HAL_NAME_LEN + 1]; // empty if not linked
    // value[64]: sufficient for all HAL types — "%.7g" float ≤15 chars,
    // boolean is "TRUE"/"FALSE", s32/u32 ≤12 decimal digits.
    char value[64];
    int  type_;  // hal_type_t
    int  dir;    // hal_pin_dir_t
    int  has_writer; // 1 if linked signal has writers > 0
} hal_shim_pin_info_t;

typedef struct {
    char name[HAL_NAME_LEN + 1];
    char owner[HAL_NAME_LEN + 1];
    // value[64]: sufficient for all HAL types (see hal_shim_pin_info_t).
    char value[64];
    int  type_;  // hal_type_t
    int  dir;    // hal_param_dir_t
} hal_shim_param_info_t;

typedef struct {
    char name[HAL_NAME_LEN + 1];
    // value[64]: sufficient for all HAL types (see hal_shim_pin_info_t).
    char value[64];
    int  type_;    // hal_type_t
    int  readers;
    int  writers;
    int  bidirs;
} hal_shim_sig_info_t;

typedef struct {
    char name[HAL_NAME_LEN + 1];
    char owner[HAL_NAME_LEN + 1];
    int  users;
    int  fp;
    long maxtime;
} hal_shim_funct_info_t;

typedef struct {
    char name[HAL_NAME_LEN + 1];
    long period;   // period in nanoseconds
    int  running;  // non-zero if threads are started
    int  fp;
    int  nfuncts;
    char funct_names[HAL_SHIM_MAX_TH_FNCTS][HAL_NAME_LEN + 1];
} hal_shim_thread_info_t;

typedef struct {
    int shmem_avail;
    int lock;
} hal_shim_status_t;

// hal_shim_show_comps fills arr with up to max_items components matching pattern.
// Returns the number filled, or negative errno on error.
static int hal_shim_show_comps(const char *pattern, hal_shim_comp_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_comp_t *comp;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->comp_list_ptr;
    while (next != 0) {
        comp = (hal_comp_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, comp->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", comp->name);
            arr[count].comp_id = comp->comp_id;
            arr[count].type_   = (int)comp->type;
            arr[count].ready   = comp->ready;
            count++;
        }
        next = comp->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_show_pins fills arr with up to max_items pins matching pattern.
static int hal_shim_show_pins(const char *pattern, hal_shim_pin_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_pin_t  *pin;
    hal_sig_t  *sig;
    hal_comp_t *comp;
    void       *d_ptr;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->pin_list_ptr;
    while (next != 0) {
        pin = (hal_pin_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, pin->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", pin->name);
            arr[count].type_ = (int)pin->type;
            arr[count].dir  = (int)pin->dir;

            if (pin->owner_ptr != 0) {
                comp = (hal_comp_t *)SHMPTR(pin->owner_ptr);
                snprintf(arr[count].owner, sizeof(arr[count].owner), "%s", comp->name);
            } else {
                arr[count].owner[0] = '\0';
            }

            if (pin->signal != 0) {
                sig = (hal_sig_t *)SHMPTR(pin->signal);
                snprintf(arr[count].signal, sizeof(arr[count].signal), "%s", sig->name);
                arr[count].has_writer = (sig->writers > 0) ? 1 : 0;
                d_ptr = SHMPTR(sig->data_ptr);
            } else {
                arr[count].signal[0] = '\0';
                arr[count].has_writer = 0;
                d_ptr = (void *)&pin->dummysig;
            }
            hal_shim_format_value(pin->type, d_ptr,
                                  arr[count].value, sizeof(arr[count].value));
            count++;
        }
        next = pin->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_show_params fills arr with up to max_items parameters matching pattern.
static int hal_shim_show_params(const char *pattern, hal_shim_param_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_param_t *param;
    hal_comp_t  *comp;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->param_list_ptr;
    while (next != 0) {
        param = (hal_param_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, param->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", param->name);
            arr[count].type_ = (int)param->type;
            arr[count].dir  = (int)param->dir;

            if (param->owner_ptr != 0) {
                comp = (hal_comp_t *)SHMPTR(param->owner_ptr);
                snprintf(arr[count].owner, sizeof(arr[count].owner), "%s", comp->name);
            } else {
                arr[count].owner[0] = '\0';
            }
            hal_shim_format_value(param->type, SHMPTR(param->data_ptr),
                                  arr[count].value, sizeof(arr[count].value));
            count++;
        }
        next = param->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_show_sigs fills arr with up to max_items signals matching pattern.
static int hal_shim_show_sigs(const char *pattern, hal_shim_sig_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_sig_t *sig;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->sig_list_ptr;
    while (next != 0) {
        sig = (hal_sig_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, sig->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", sig->name);
            arr[count].type_   = (int)sig->type;
            arr[count].readers = sig->readers;
            arr[count].writers = sig->writers;
            arr[count].bidirs  = sig->bidirs;
            hal_shim_format_value(sig->type, SHMPTR(sig->data_ptr),
                                  arr[count].value, sizeof(arr[count].value));
            count++;
        }
        next = sig->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_show_functs fills arr with up to max_items functions matching pattern.
static int hal_shim_show_functs(const char *pattern, hal_shim_funct_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_funct_t *funct;
    hal_comp_t  *comp;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->funct_list_ptr;
    while (next != 0) {
        funct = (hal_funct_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, funct->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", funct->name);
            arr[count].users = funct->users;
            arr[count].fp = funct->uses_fp;
            arr[count].maxtime = (long)funct->maxtime;
            if (funct->owner_ptr != 0) {
                comp = (hal_comp_t *)SHMPTR(funct->owner_ptr);
                snprintf(arr[count].owner, sizeof(arr[count].owner), "%s", comp->name);
            } else {
                arr[count].owner[0] = '\0';
            }
            count++;
        }
        next = funct->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_show_threads fills arr with up to max_items threads matching pattern.
static int hal_shim_show_threads(const char *pattern, hal_shim_thread_info_t *arr, int max_items) {
    void *next; int count = 0;
    hal_thread_t      *tptr;
    hal_list_t        *list_root, *list_entry;
    hal_funct_entry_t *fentry;
    hal_funct_t       *funct;

    if (hal_data == NULL) return -EINVAL;

    rtapi_mutex_get(&(hal_data->mutex));
    next = hal_data->thread_list_ptr;
    while (next != 0) {
        tptr = (hal_thread_t *)SHMPTR(next);
        if (pattern == NULL || *pattern == '\0' ||
            fnmatch(pattern, tptr->name, 0) == 0) {
            if (count >= max_items) {
                rtapi_mutex_give(&(hal_data->mutex));
                return -ENOSPC;
            }
            snprintf(arr[count].name, sizeof(arr[count].name), "%s", tptr->name);
            arr[count].period  = tptr->period;
            arr[count].running = hal_data->threads_running;
            arr[count].fp      = tptr->uses_fp;
            arr[count].nfuncts = 0;

            list_root  = &(tptr->funct_list);
            list_entry = list_next(list_root);
            while (list_entry != list_root &&
                   arr[count].nfuncts < HAL_SHIM_MAX_TH_FNCTS) {
                fentry = (hal_funct_entry_t *)list_entry;
                funct  = (hal_funct_t *)SHMPTR(fentry->funct_ptr);
                snprintf(arr[count].funct_names[arr[count].nfuncts],
                         HAL_NAME_LEN + 1, "%s", funct->name);
                arr[count].nfuncts++;
                list_entry = list_next(list_entry);
            }
            count++;
        }
        next = tptr->next_ptr;
    }
    rtapi_mutex_give(&(hal_data->mutex));
    return count;
}

// hal_shim_status reads HAL shared-memory status into *st.
// Returns 0 on success or -EINVAL if hal_data is NULL.
static int hal_shim_status(hal_shim_status_t *st) {
    if (hal_data == NULL) return -EINVAL;
    rtapi_mutex_get(&(hal_data->mutex));
    st->shmem_avail = (int)hal_data->shmem_avail;
    st->lock        = (int)hal_data->lock;
    rtapi_mutex_give(&(hal_data->mutex));
    return 0;
}

// buf_write_line copies line into buf[*pos] as a null-terminated entry and
// advances *pos.  Returns 0 on success or -ENOSPC if the buffer is full.
static int buf_write_line(char *buf, int *pos, int buf_size, const char *line) {
    int len = (int)strlen(line);
    if (*pos + len + 1 > buf_size) return -ENOSPC;
    memcpy(buf + *pos, line, len);
    *pos += len;
    buf[(*pos)++] = '\0';
    return 0;
}

// hal_shim_save serializes the current HAL state as halcmd command strings.
// type selects what to save: "all", "allu", "comp", "alias", "sig", "signal",
// "sigu", "link", "linka", "net", "neta", "netl", "netla", "netal", "param",
// "parameter", "thread".  Lines are written null-separated into buf.
// Returns the number of lines written, or a negative errno value on error.
static int hal_shim_save(const char *type, char *buf, int buf_size) {
    int pos   = 0;
    int count = 0;
    void *next;
    char tmp[2048]; // large enough for any single halcmd line

    if (hal_data == NULL) return -EINVAL;
    if (type == NULL || *type == '\0') type = "all";

    int do_comps   = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"comp")==0);
    int do_aliases = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"alias")==0);
    int do_sigs    = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"sig")==0 || strcmp(type,"signal")==0 ||
                      strcmp(type,"sigu")==0);
    int do_nets    = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"net")==0 || strcmp(type,"neta")==0 ||
                      strcmp(type,"netl")==0 || strcmp(type,"netla")==0 ||
                      strcmp(type,"netal")==0);
    int do_links   = (strcmp(type,"link")==0 || strcmp(type,"linka")==0);
    int do_params  = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"param")==0 || strcmp(type,"parameter")==0);
    int do_threads = (strcmp(type,"all")==0 || strcmp(type,"allu")==0 ||
                      strcmp(type,"thread")==0);

    if (!do_comps && !do_aliases && !do_sigs && !do_nets &&
        !do_links && !do_params && !do_threads) {
        return -EINVAL;
    }

#define SAVE_LINE(fmt, ...) do { \
    snprintf(tmp, sizeof(tmp), fmt, ##__VA_ARGS__); \
    if (buf_write_line(buf, &pos, buf_size, tmp) != 0) return -ENOSPC; \
    count++; \
} while (0)

    // save realtime components (informational only — loaded via cmod system)
    if (do_comps) {
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->comp_list_ptr;
        while (next != 0) {
            hal_comp_t *comp = (hal_comp_t *)SHMPTR(next);
            if (comp->pid == 0) {
                SAVE_LINE("# component %s (loaded by cmod)", comp->name);
            }
            next = comp->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

    // save aliases
    if (do_aliases) {
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->pin_list_ptr;
        while (next != 0) {
            hal_pin_t *pin = (hal_pin_t *)SHMPTR(next);
            if (pin->oldname != 0) {
                hal_oldname_t *oldname = (hal_oldname_t *)SHMPTR(pin->oldname);
                SAVE_LINE("alias pin %s %s", oldname->name, pin->name);
            }
            next = pin->next_ptr;
        }
        next = hal_data->param_list_ptr;
        while (next != 0) {
            hal_param_t *param = (hal_param_t *)SHMPTR(next);
            if (param->oldname != 0) {
                hal_oldname_t *oldname = (hal_oldname_t *)SHMPTR(param->oldname);
                SAVE_LINE("alias param %s %s", oldname->name, param->name);
            }
            next = param->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

    // save signals (newsig lines)
    if (do_sigs) {
        int only_unlinked = (strcmp(type,"sigu") == 0 ||
                             strcmp(type,"allu") == 0);
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->sig_list_ptr;
        while (next != 0) {
            hal_sig_t *sig = (hal_sig_t *)SHMPTR(next);
            if (only_unlinked && (sig->readers || sig->writers)) {
                next = sig->next_ptr;
                continue;
            }
            const char *type_name;
            switch (sig->type) {
            case HAL_BIT:   type_name = "bit";   break;
            case HAL_FLOAT: type_name = "float"; break;
            case HAL_S32:   type_name = "s32";   break;
            case HAL_U32:   type_name = "u32";   break;
            default:        type_name = "unknown"; break;
            }
            SAVE_LINE("newsig %s %s", sig->name, type_name);
            next = sig->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

    // save nets
    if (do_nets) {
        int net_size = HAL_NAME_LEN * 64 + 64;
        char *net_line = (char *)malloc(net_size);
        if (!net_line) return -ENOMEM;
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->sig_list_ptr;
        while (next != 0) {
            hal_sig_t *sig = (hal_sig_t *)SHMPTR(next);
            hal_pin_t *pin = halpr_find_pin_by_sig(sig, 0);
            if (pin) {
                // net <signame> <pins...>
                int net_pos = 0;
                int wr = snprintf(net_line, net_size, "net %s", sig->name);
                if (wr > 0 && wr < net_size) net_pos = wr;
                pin = halpr_find_pin_by_sig(sig, 0);
                while (pin != 0 && net_pos < net_size - 1) {
                    wr = snprintf(net_line + net_pos, net_size - net_pos,
                                  " %s", pin->name);
                    if (wr > 0 && wr < net_size - net_pos) net_pos += wr;
                    pin = halpr_find_pin_by_sig(sig, pin);
                }
                if (buf_write_line(buf, &pos, buf_size, net_line) != 0) {
                    rtapi_mutex_give(&(hal_data->mutex));
                    free(net_line);
                    return -ENOSPC;
                }
                count++;
            }
            next = sig->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
        free(net_line);
    }

    // save links (linkps lines)
    if (do_links) {
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->pin_list_ptr;
        while (next != 0) {
            hal_pin_t *pin = (hal_pin_t *)SHMPTR(next);
            if (pin->signal != 0) {
                hal_sig_t *sig = (hal_sig_t *)SHMPTR(pin->signal);
                SAVE_LINE("linkps %s %s", pin->name, sig->name);
            }
            next = pin->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

    // save writable parameter values
    if (do_params) {
        char val_buf[64];
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->param_list_ptr;
        while (next != 0) {
            hal_param_t *param = (hal_param_t *)SHMPTR(next);
            if (param->dir != HAL_RO) {
                hal_shim_format_value(param->type, SHMPTR(param->data_ptr),
                                      val_buf, sizeof(val_buf));
                SAVE_LINE("setp %s %s", param->name, val_buf);
            }
            next = param->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

    // save thread function assignments
    if (do_threads) {
        rtapi_mutex_get(&(hal_data->mutex));
        next = hal_data->thread_list_ptr;
        while (next != 0) {
            hal_thread_t *tptr = (hal_thread_t *)SHMPTR(next);
            hal_list_t *list_root  = &(tptr->funct_list);
            hal_list_t *list_entry = list_next(list_root);
            while (list_entry != list_root) {
                hal_funct_entry_t *fentry = (hal_funct_entry_t *)list_entry;
                hal_funct_t *funct = (hal_funct_t *)SHMPTR(fentry->funct_ptr);
                SAVE_LINE("addf %s %s", funct->name, tptr->name);
                list_entry = list_next(list_entry);
            }
            next = tptr->next_ptr;
        }
        rtapi_mutex_give(&(hal_data->mutex));
    }

#undef SAVE_LINE
    return count;
}

// hal_shim_set_exact implements the classic halcmd
// "setexact_for_test_suite_only" command.  It used to ask HAL to pretend the
// requested base period was achievable exactly instead of rounding thread
// periods to the value RTAPI reported, which made test sample counts
// deterministic.  Thread periods are no longer rounded at all — every period is
// exact — so this is now a no-op, accepted (rather than removed) so the test
// configurations that issue it keep working unchanged.
static int hal_shim_set_exact(void) {
    if (hal_data == NULL) {
        return -EINVAL;
    }
    return 0;
}
*/
import "C"
import (
	"bytes"
	"fmt"
	"unsafe"

	hal "github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// SetExact enables exact_base_period mode in HAL, mirroring the classic halcmd
// "setexact_for_test_suite_only" command used by the test suite.  It must be
// called before any thread is created.  Returns an error if a base period has
// already been established.
func SetExact() error {
	rc := int(C.hal_shim_set_exact())
	return halError(rc, "setexact", "")
}

// halErr is the buffer a HAL call writes the reason it failed into.
//
// Every wrapper below declares one and passes it to the _ex entry point, so the
// reason comes back through the call that produced it rather than being
// recovered afterwards from a log or a per-thread slot. That keeps the wrappers
// free of any ordering, locking or thread-affinity requirement: the buffer
// belongs to the call, and concurrent callers cannot see each other's.
type halErr struct{ buf [C.HAL_ERRLEN]C.char }

// ptr and length are what the _ex entry points take.
func (e *halErr) ptr() *C.char  { return &e.buf[0] }
func (e *halErr) length() C.int { return C.int(len(e.buf)) }

// String returns the reason, or "" if the call did not set one. The buffer is
// zeroed by Go on declaration and left untouched on success, so an empty string
// unambiguously means "nothing reported".
func (e *halErr) String() string { return C.GoString(&e.buf[0]) }

// halError translates a HAL C error code and the reason that came back with it
// into a Go error. Returns nil if the code is 0 (success).
// Error codes are negative errno values as returned by HAL/RTAPI functions.
//
// op names the halcmd command the caller issued — "newthread", "setp", "net" —
// not the C function that produced the code. The error text is what an operator
// reads, and it should name the command they typed rather than an internal
// symbol they cannot act on. Wrappers shared by several commands (linkps and
// linksp, alias and unalias, lock and unlock) take op as a parameter instead of
// hardcoding one of the two. The few calls with no command behind them at all —
// module unload cleanup, thread-cycle sync, RT app init — keep the C name,
// because inventing a verb for them would be worse than naming the function.
//
// detail is hal_lib's or the shim's own explanation ("duplicate thread name
// loop1"); it becomes Error.Detail. The generic per-code text stays in
// Error.Message because several hal.Err* sentinels share a code and are told
// apart by message, so replacing it would silently break errors.Is().
func halError(code int, op string, detail string) error {
	return hal.CodeError(op, code, detail)
}

// halCreateThreadCPU wraps hal_create_thread_cpu() to create a single HAL
// realtime thread with explicit CPU affinity.
// cpu=-1 means no affinity.
func halCreateThreadCPU(name string, periodNs int64, usesFP int, cpu int) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_create_thread_cpu_ex(cName, C.ulong(periodNs), C.int(usesFP), C.int(cpu), e.ptr(), e.length())
	if int(ret) < 0 {
		return halError(int(ret), "newthread", e.String())
	}
	return nil
}

// halThreadDelete wraps hal_thread_delete() to delete a HAL realtime thread by name.
func halThreadDelete(name string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_thread_delete_ex(cName, e.ptr(), e.length())
	return halError(int(ret), "delthread", e.String())
}

// halStartThreads wraps hal_start_threads() to start all HAL realtime threads.
func halStartThreads() error {
	var e halErr
	ret := C.hal_start_threads_ex(e.ptr(), e.length())
	return halError(int(ret), "start", e.String())
}

// halStopThreads wraps hal_stop_threads() to stop all HAL realtime threads.
func halStopThreads() error {
	var e halErr
	ret := C.hal_stop_threads_ex(e.ptr(), e.length())
	return halError(int(ret), "stop", e.String())
}

// halDelFunctsByComp removes all functions owned by comp_id from all threads.
func halDelFunctsByComp(compID int) (int, error) {
	var e halErr
	ret := C.hal_del_functs_by_comp_ex(C.int(compID), e.ptr(), e.length())
	if ret < 0 {
		return 0, halError(int(ret), "hal_del_functs_by_comp", e.String())
	}
	return int(ret), nil
}

// halWaitCycleAdvance waits for every thread to complete a full cycle.
func halWaitCycleAdvance() error {
	ret := C.hal_wait_cycle_advance()
	return halError(int(ret), "hal_wait_cycle_advance", "")
}

// halFindCompID returns the comp_id for a named component, or 0 if not found.
func halFindCompID(name string) int {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	comp := C.halpr_find_comp_by_name(cName)
	if comp == nil {
		return 0
	}
	return int(comp.comp_id)
}

// halListComponents wraps hal_shim_list_comps() to return all HAL component names.
// Returns a slice of component name strings, or an error on failure.
// halFnmatch reports whether name matches the shell glob pattern using libc
// fnmatch — the exact matcher the C list shims use for pin/sig/param/funct/
// thread/comp (cgo.go: fnmatch(pattern, ->name, 0)). The `comp` list type
// enumerates components in Go, so it routes its filtering through here to stay
// consistent with the other list types rather than diverging on Go's path.Match
// glob dialect (which special-cases '/' and errors on a malformed pattern).
func halFnmatch(pattern, name string) bool {
	cp := C.CString(pattern)
	defer C.free(unsafe.Pointer(cp))
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	return C.fnmatch(cp, cn, 0) == 0
}

func halListComponents() ([]string, error) {
	// Allocate a buffer sized for HAL_SHIM_MAX_COMPS components, each with a
	// name up to HAL_NAME_LEN+1 bytes (HAL_NAME_LEN is 127, defined in hal.h).
	bufSize := int(C.HAL_SHIM_MAX_COMPS) * (int(C.HAL_NAME_LEN) + 1)
	buf := make([]byte, bufSize)

	ret := C.hal_shim_list_comps((*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.int(bufSize))
	if ret < 0 {
		return nil, halError(int(ret), "list", "")
	}
	if ret == 0 {
		return []string{}, nil
	}

	// Scan only the portion of the buffer that was actually written.
	end := 0
	remaining := int(ret)
	for end < len(buf) && remaining > 0 {
		if buf[end] == 0 {
			remaining--
		}
		end++
	}

	parts := bytes.Split(buf[:end], []byte{0})
	names := make([]string, 0, int(ret))
	for _, p := range parts {
		if len(p) > 0 {
			names = append(names, string(p))
		}
	}
	return names, nil
}

// halUnloadAll wraps hal_shim_unload_all() to exit all HAL components except
// the one identified by exceptID.
func halUnloadAll(exceptID int) error {
	ret := C.hal_shim_unload_all(C.int(exceptID))
	return halError(int(ret), "unload", "")
}

// halLockDLHandle locks the PT_LOAD segments of a single dlopen handle
// into memory, preventing page faults during RT execution.
func halLockDLHandle(handle unsafe.Pointer) {
	C.hal_shim_lock_dl_handle(handle)
}

// halUnlockDLHandle unlocks the PT_LOAD segments of a single dlopen handle.
func halUnlockDLHandle(handle unsafe.Pointer) {
	C.hal_shim_unlock_dl_handle(handle)
}

// ===== Go wrappers for 1a simple shims =====

// halNewSig wraps hal_shim_newsig() to create a new HAL signal.
func halNewSig(name string, halType hal.PinType) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_newsig(cName, C.int(halType), e.ptr(), e.length())
	return halError(int(ret), "newsig", e.String())
}

// halDelSig wraps hal_shim_delsig() to delete a HAL signal.
func halDelSig(name string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_delsig(cName, e.ptr(), e.length())
	return halError(int(ret), "delsig", e.String())
}

// halRetain sets the HAL_SIGFLAG_RETAIN flag on a signal.
func halRetain(name string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_retain(cName, e.ptr(), e.length())
	return halError(int(ret), "retain", e.String())
}

// halUnretain clears the HAL_SIGFLAG_RETAIN flag on a signal.
func halUnretain(name string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_unretain(cName, e.ptr(), e.length())
	return halError(int(ret), "unretain", e.String())
}

// halLinkPS wraps hal_shim_linkps() to link a pin to a signal.
// op is the halcmd verb this call is serving ("linkps" or "linksp"), which
// differ only in argument order and share this wrapper.
func halLinkPS(pin, sig, op string) error {
	var e halErr
	cPin := C.CString(pin)
	defer C.free(unsafe.Pointer(cPin))
	cSig := C.CString(sig)
	defer C.free(unsafe.Pointer(cSig))
	ret := C.hal_shim_linkps(cPin, cSig, e.ptr(), e.length())
	return halError(int(ret), op, e.String())
}

// halUnlinkP wraps hal_shim_unlinkp() to unlink a pin from its signal.
func halUnlinkP(pin string) error {
	var e halErr
	cPin := C.CString(pin)
	defer C.free(unsafe.Pointer(cPin))
	ret := C.hal_shim_unlinkp(cPin, e.ptr(), e.length())
	return halError(int(ret), "unlinkp", e.String())
}

// halAddF wraps hal_shim_addf() to add a function to a thread.
func halAddF(funct, thread string, pos int) error {
	var e halErr
	cFunct := C.CString(funct)
	defer C.free(unsafe.Pointer(cFunct))
	cThread := C.CString(thread)
	defer C.free(unsafe.Pointer(cThread))
	ret := C.hal_shim_addf(cFunct, cThread, C.int(pos), e.ptr(), e.length())
	return halError(int(ret), "addf", e.String())
}

// halDelF wraps hal_shim_delf() to remove a function from a thread.
func halDelF(funct, thread string) error {
	var e halErr
	cFunct := C.CString(funct)
	defer C.free(unsafe.Pointer(cFunct))
	cThread := C.CString(thread)
	defer C.free(unsafe.Pointer(cThread))
	ret := C.hal_shim_delf(cFunct, cThread, e.ptr(), e.length())
	return halError(int(ret), "delf", e.String())
}

// halSetLock wraps hal_shim_set_lock() to set the HAL lock level.
func halSetLock(lockType int, op string) error {
	var e halErr
	ret := C.hal_shim_set_lock(C.uchar(lockType), e.ptr(), e.length())
	return halError(int(ret), op, e.String())
}

// halGetLock wraps hal_shim_get_lock() to get the current HAL lock level.
func halGetLock() int {
	return int(C.hal_shim_get_lock())
}

// halPinAlias wraps hal_shim_pin_alias() to set or clear a pin alias.
// Pass an empty alias to remove the existing alias.
func halPinAlias(pinName, alias, op string) error {
	var e halErr
	cPin := C.CString(pinName)
	defer C.free(unsafe.Pointer(cPin))
	var cAlias *C.char
	if alias != "" {
		cAlias = C.CString(alias)
		defer C.free(unsafe.Pointer(cAlias))
	}
	ret := C.hal_shim_pin_alias(cPin, cAlias, e.ptr(), e.length())
	return halError(int(ret), op, e.String())
}

// halParamAlias wraps hal_shim_param_alias() to set or clear a parameter alias.
// Pass an empty alias to remove the existing alias.
func halParamAlias(paramName, alias, op string) error {
	var e halErr
	cParam := C.CString(paramName)
	defer C.free(unsafe.Pointer(cParam))
	var cAlias *C.char
	if alias != "" {
		cAlias = C.CString(alias)
		defer C.free(unsafe.Pointer(cAlias))
	}
	ret := C.hal_shim_param_alias(cParam, cAlias, e.ptr(), e.length())
	return halError(int(ret), op, e.String())
}

// halAlias creates an alias for a pin or parameter.
// kind must be "pin" or "param".
func halAlias(kind, name, alias string) error {
	switch kind {
	case "pin":
		return halPinAlias(name, alias, "alias")
	case "param":
		return halParamAlias(name, alias, "alias")
	default:
		return fmt.Errorf("alias: unknown kind %q: must be \"pin\" or \"param\"", kind)
	}
}

// halUnAlias removes an alias from a pin or parameter.
// kind must be "pin" or "param".
func halUnAlias(kind, name string) error {
	switch kind {
	case "pin":
		return halPinAlias(name, "", "unalias")
	case "param":
		return halParamAlias(name, "", "unalias")
	default:
		return fmt.Errorf("unalias: unknown kind %q: must be \"pin\" or \"param\"", kind)
	}
}

// ===== Go wrappers for 1b shmem access shims =====

// halSetP wraps hal_shim_setp() to set a pin or parameter value by name.
func halSetP(name, value string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))
	ret := C.hal_shim_setp(cName, cValue, e.ptr(), e.length())
	return halError(int(ret), "setp", e.String())
}

// halGetP wraps hal_shim_getp() to get a pin or parameter value as a string.
func halGetP(name string) (string, error) {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	buf := make([]byte, 256)
	ret := C.hal_shim_getp(cName, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.int(len(buf)), e.ptr(), e.length())
	if ret < 0 {
		return "", halError(int(ret), "getp", e.String())
	}
	return C.GoString((*C.char)(unsafe.Pointer(unsafe.SliceData(buf)))), nil
}

// halSetS wraps hal_shim_sets() to set a signal value by name.
func halSetS(name, value string) error {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))
	ret := C.hal_shim_sets(cName, cValue, e.ptr(), e.length())
	return halError(int(ret), "sets", e.String())
}

// halGetS wraps hal_shim_gets() to get a signal value as a string.
func halGetS(name string) (string, error) {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	buf := make([]byte, 256)
	ret := C.hal_shim_gets(cName, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.int(len(buf)), e.ptr(), e.length())
	if ret < 0 {
		return "", halError(int(ret), "gets", e.String())
	}
	return C.GoString((*C.char)(unsafe.Pointer(unsafe.SliceData(buf)))), nil
}

// halPType wraps hal_shim_ptype() to get the type of a pin or parameter.
func halPType(name string) (hal.PinType, error) {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_ptype(cName, e.ptr(), e.length())
	if ret < 0 {
		return 0, halError(int(ret), "ptype", e.String())
	}
	return hal.PinType(ret), nil
}

// halSType wraps hal_shim_stype() to get the type of a signal.
func halSType(name string) (hal.PinType, error) {
	var e halErr
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	ret := C.hal_shim_stype(cName, e.ptr(), e.length())
	if ret < 0 {
		return 0, halError(int(ret), "stype", e.String())
	}
	return hal.PinType(ret), nil
}

// ===== Go wrapper for 1c net shim =====

// halNet wraps hal_shim_net() to connect pins to a signal.
// pinNames must not include arrow tokens (=>, <=, <=>).
func halNet(sigName string, pinNames []string) error {
	var e halErr
	if len(pinNames) == 0 {
		return halError(-22, "net", e.String())
	}
	cSig := C.CString(sigName)
	defer C.free(unsafe.Pointer(cSig))

	// Build null-separated pin names buffer
	var buf []byte
	for _, pin := range pinNames {
		buf = append(buf, []byte(pin)...)
		buf = append(buf, 0)
	}

	ret := C.hal_shim_net(cSig, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.int(len(pinNames)), e.ptr(), e.length())
	return halError(int(ret), "net", e.String())
}

// ===== Go wrappers for 1d process management shims =====

// halNewInst wraps hal_shim_newinst() to create a new component instance.
func halNewInst(compType, name, arg string) error {
	cType := C.CString(compType)
	defer C.free(unsafe.Pointer(cType))
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cArg := C.CString(arg)
	defer C.free(unsafe.Pointer(cArg))
	ret := C.hal_shim_newinst(cType, cName, cArg)
	return halError(int(ret), "newinst", "")
}

// halRtapiAppInit wraps hal_shim_rtapi_app_init() — initializes HAL shared
// memory.  Must be called before hal_init().
func halRtapiAppInit() error {
	ret := C.hal_shim_rtapi_app_init()
	return halError(int(ret), "hal_shim_rtapi_app_init", "")
}

// halSetLogRing sets the stmak_log ring for the RTAPI message handler.
// Must be called before halRtapiAppInit().
func halSetLogRing(ring unsafe.Pointer) {
	C.hal_shim_set_log_ring((*C.stmak_log_ring_t)(ring))
}

// halClearMsgHandler sets the RTAPI message handler to NULL so that
// subsequent rtapi_print_msg calls are silently discarded.  Must be
// called before the log ring is destroyed.
func halClearMsgHandler() {
	C.rtapi_set_msg_handler(nil)
}

// halRtapiInitializeApp wraps rtapi_initialize_app() — idempotently sets up
// RT rlimits, mlockall(MCL_CURRENT), signal handlers, and io privileges.
// Safe to call multiple times (guarded internally by a once flag).
func halRtapiInitializeApp() {
	C.rtapi_initialize_app()
}

// halRtapiAppCleanup wraps hal_shim_rtapi_app_cleanup() — tears down HAL
// threads and releases shared memory.
// Must be called after all components are unloaded and before hal_exit().
func halRtapiAppCleanup() {
	C.hal_shim_rtapi_app_cleanup()
}

// ===== Go wrappers for 1e list shims =====

// listShimFn is a generic helper type for the hal_shim_list_* family.
type listShimFn func(pattern *C.char, buf *C.char, size C.int) C.int

func halListGeneric(pattern string, shimFn listShimFn) ([]string, error) {
	bufSize := C.int(C.HAL_SHIM_MAX_COMPS * (C.HAL_NAME_LEN + 1))
	buf := make([]byte, int(bufSize))

	var cPattern *C.char
	if pattern != "" {
		cPattern = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPattern))
	}

	ret := shimFn(cPattern, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), bufSize)
	if ret < 0 {
		return nil, halError(int(ret), "list", "")
	}
	if ret == 0 {
		return []string{}, nil
	}

	// Count nulls to find the written portion of the buffer
	end := 0
	remaining := int(ret)
	for end < len(buf) && remaining > 0 {
		if buf[end] == 0 {
			remaining--
		}
		end++
	}

	parts := bytes.Split(buf[:end], []byte{0})
	names := make([]string, 0, int(ret))
	for _, p := range parts {
		if len(p) > 0 {
			names = append(names, string(p))
		}
	}
	return names, nil
}

// halListPins returns pin names matching the given pattern (may be empty for all).
func halListPins(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_pins(p, buf, size)
	})
}

// halListSigs returns signal names matching the given pattern.
func halListSigs(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_sigs(p, buf, size)
	})
}

// halListRetainSigs returns names of retain-flagged signals matching the given pattern.
func halListRetainSigs(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_retain_sigs(p, buf, size)
	})
}

// halListParams returns parameter names matching the given pattern.
func halListParams(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_params(p, buf, size)
	})
}

// halListFuncts returns function names matching the given pattern.
func halListFuncts(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_functs(p, buf, size)
	})
}

// halListThreads returns thread names matching the given pattern.
func halListThreads(pattern string) ([]string, error) {
	return halListGeneric(pattern, func(p *C.char, buf *C.char, size C.int) C.int {
		return C.hal_shim_list_threads(p, buf, size)
	})
}

// ===== Go wrappers for 1f show/status/save/debug shims =====

// cHalTypeName converts a C hal_type_t integer to a Go type name string.
func cHalTypeName(t C.int) string {
	switch hal.PinType(t) {
	case hal.TypeBit:
		return "bit"
	case hal.TypeFloat:
		return "float"
	case hal.TypeS32:
		return "s32"
	case hal.TypeU32:
		return "u32"
	default:
		return "unknown"
	}
}

// cPinDirName converts a C hal_pin_dir_t integer to a direction string.
func cPinDirName(d C.int) string {
	switch hal.Direction(d) {
	case hal.In:
		return "IN"
	case hal.Out:
		return "OUT"
	case hal.IO:
		return "IO"
	default:
		return "unknown"
	}
}

// cParamDirName converts a C hal_param_dir_t integer to a direction string.
func cParamDirName(d C.int) string {
	switch d {
	case 64: // HAL_RO
		return "RO"
	case 192: // HAL_RW = HAL_RO | HAL_WO
		return "RW"
	default:
		return "unknown"
	}
}

// cCompTypeName converts a C component_type_t integer to a type string.
func cCompTypeName(t C.int) string {
	switch t {
	case 0: // COMPONENT_TYPE_USER
		return "user"
	case 1: // COMPONENT_TYPE_REALTIME
		return "realtime"
	default:
		return "other"
	}
}

// lockLevelName converts a HAL lock bitmask to a human-readable string.
func lockLevelName(lock C.int) string {
	switch lock {
	case 0:
		return "none"
	case 1:
		return "load"
	case 2:
		return "config"
	case 3:
		return "tune"
	case 4:
		return "params"
	case 8:
		return "run"
	case 255:
		return "all"
	default:
		return fmt.Sprintf("0x%02x", int(lock))
	}
}

// showMaxCap is the upper bound for the number of items any halShow* call
// will allocate before giving up.  The initial attempt uses HAL_SHIM_MAX_ITEMS;
// on -ENOSPC the capacity is doubled each time up to this limit.
const showMaxCap = 65536

// saveBufMax is the upper bound in bytes for the hal_shim_save output buffer.
// The initial attempt uses 64 KiB; on -ENOSPC the buffer is doubled up to this limit.
const saveBufMax = 4 * 1024 * 1024 // 4 MiB

// halShowComps returns structured information about all components matching pattern.
func halShowComps(pattern string) ([]CompInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_comp_info_t, cap)
		n := C.hal_shim_show_comps(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]CompInfo, int(n))
		for i := range result {
			result[i] = CompInfo{
				Name: C.GoString(&arr[i].name[0]),
				ID:   int(arr[i].comp_id),
				Type: cCompTypeName(arr[i].type_),
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_comps: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halShowPins returns structured information about all pins matching pattern.
func halShowPins(pattern string) ([]PinInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_pin_info_t, cap)
		n := C.hal_shim_show_pins(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]PinInfo, int(n))
		for i := range result {
			result[i] = PinInfo{
				Name:      C.GoString(&arr[i].name[0]),
				Type:      cHalTypeName(arr[i].type_),
				Direction: cPinDirName(arr[i].dir),
				Value:     C.GoString(&arr[i].value[0]),
				Signal:    C.GoString(&arr[i].signal[0]),
				Owner:     C.GoString(&arr[i].owner[0]),
				HasWriter: arr[i].has_writer != 0,
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_pins: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halShowParams returns structured information about all parameters matching pattern.
func halShowParams(pattern string) ([]ParamInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_param_info_t, cap)
		n := C.hal_shim_show_params(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]ParamInfo, int(n))
		for i := range result {
			result[i] = ParamInfo{
				Name:      C.GoString(&arr[i].name[0]),
				Type:      cHalTypeName(arr[i].type_),
				Direction: cParamDirName(arr[i].dir),
				Value:     C.GoString(&arr[i].value[0]),
				Owner:     C.GoString(&arr[i].owner[0]),
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_params: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halShowSigs returns structured information about all signals matching pattern.
func halShowSigs(pattern string) ([]SigInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_sig_info_t, cap)
		n := C.hal_shim_show_sigs(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]SigInfo, int(n))
		for i := range result {
			result[i] = SigInfo{
				Name:  C.GoString(&arr[i].name[0]),
				Type:  cHalTypeName(arr[i].type_),
				Value: C.GoString(&arr[i].value[0]),
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_sigs: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halShowFuncts returns structured information about all functions matching pattern.
func halShowFuncts(pattern string) ([]FunctInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_funct_info_t, cap)
		n := C.hal_shim_show_functs(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]FunctInfo, int(n))
		for i := range result {
			result[i] = FunctInfo{
				Name:    C.GoString(&arr[i].name[0]),
				Owner:   C.GoString(&arr[i].owner[0]),
				Users:   int32(arr[i].users),
				FP:      arr[i].fp != 0,
				MaxTime: int64(arr[i].maxtime),
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_functs: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halShowThreads returns structured information about all threads matching pattern.
func halShowThreads(pattern string) ([]ThreadInfo, error) {
	var cPat *C.char
	if pattern != "" {
		cPat = C.CString(pattern)
		defer C.free(unsafe.Pointer(cPat))
	}

	for cap := int(C.HAL_SHIM_MAX_ITEMS); cap <= showMaxCap; cap *= 2 {
		arr := make([]C.hal_shim_thread_info_t, cap)
		n := C.hal_shim_show_threads(cPat, &arr[0], C.int(cap))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "show", "")
		}
		result := make([]ThreadInfo, int(n))
		for i := range result {
			nf := int(arr[i].nfuncts)
			functs := make([]string, nf)
			for j := 0; j < nf; j++ {
				functs[j] = C.GoString(&arr[i].funct_names[j][0])
			}
			result[i] = ThreadInfo{
				Name:    C.GoString(&arr[i].name[0]),
				Period:  int64(arr[i].period),
				FP:      arr[i].fp != 0,
				Running: arr[i].running != 0,
				Functs:  functs,
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("hal_shim_show_threads: result set exceeds maximum capacity (%d items)", showMaxCap)
}

// halStatus returns HAL shared-memory status information.
func halStatus() (*StatusInfo, error) {
	var st C.hal_shim_status_t
	ret := C.hal_shim_status(&st)
	if ret < 0 {
		return nil, halError(int(ret), "status", "")
	}
	return &StatusInfo{
		ShmemFree: int(st.shmem_avail),
		LockLevel: lockLevelName(st.lock),
	}, nil
}

// halSave serializes current HAL state as halcmd command strings.
// type selects what to save (see hal_shim_save for valid types).
// The output buffer starts at 64 KiB and doubles on -ENOSPC up to saveBufMax.
func halSave(saveType string) ([]string, error) {
	cType := C.CString(saveType)
	defer C.free(unsafe.Pointer(cType))

	for bufSize := 65536; bufSize <= saveBufMax; bufSize *= 2 {
		buf := make([]byte, bufSize)
		n := C.hal_shim_save(cType, (*C.char)(unsafe.Pointer(unsafe.SliceData(buf))), C.int(bufSize))
		if n < 0 {
			if int(n) == -int(C.ENOSPC) {
				continue
			}
			return nil, halError(int(n), "save", "")
		}
		if n == 0 {
			return []string{}, nil
		}

		// Parse null-separated lines.
		end := 0
		remaining := int(n)
		for end < len(buf) && remaining > 0 {
			if buf[end] == 0 {
				remaining--
			}
			end++
		}

		parts := bytes.Split(buf[:end], []byte{0})
		lines := make([]string, 0, int(n))
		for _, p := range parts {
			if len(p) > 0 {
				lines = append(lines, string(p))
			}
		}
		return lines, nil
	}
	return nil, fmt.Errorf("hal_shim_save: output exceeds maximum buffer size (%d bytes)", saveBufMax)
}

// rtapiIsRealtime wraps rtapi_is_realtime(). Returns true if the process is
// running with POSIX realtime scheduling (SCHED_FIFO).
func rtapiIsRealtime() bool {
	return C.rtapi_is_realtime() != 0
}
