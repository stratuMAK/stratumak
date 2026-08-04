// task_interp_shim.cc — thin C++ wrapper exposing InterpBase virtual calls as plain C.
// This is the ONLY C++ code in the Go milltask module.
//
// Original interpreter: Copyright 2004-2006 Jeff Epler <jepler@unpythonic.net>
//                       and Chris Radek <chris@timeguy.com> (GPL v2)
// This shim: Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

#include "config.h"
#include <cstring>
#include "cnc/rs274ngc/interp_base.hh"
#include "cnc/rs274ngc/rs274ngc_interp.hh"  // Interp class, USER_DEFINED_FUNCTION_NUM
#include "cnc/rs274ngc/modal_state.hh"      // StateTag (restore_from_tag)
#include "cnc/rs274ngc/interp_inspection.hh"     // currentX() & co (state inspection)
#include "cnc/rs274ngc/interp_parameter_def.hh"  // RS274NGC_MAX_PARAMETERS

// Include the generated canon callback table header
#define CANON_API_CGO
#include "stmak/generated/gmi/canon/canon_api.h"

// Include task_interp_shim.h for the interp_ini_accessor_t typedef.
#include "task_interp_shim.h"

extern "C" {

// interp_new creates the default interpreter (rs274ngc).
void *interp_new(void) {
    return static_cast<void*>(makeInterp());
}

// interp_from_lib loads an interpreter from a shared library.
void *interp_from_lib(const char *shlib) {
    return static_cast<void*>(interp_from_shlib(shlib));
}

// interp_delete destroys an interpreter instance.
void interp_delete(void *handle) {
    delete static_cast<InterpBase*>(handle);
}

// interp_ini_load loads INI configuration.
int interp_ini_load(void *handle, const char *inifile) {
    return static_cast<InterpBase*>(handle)->ini_load(inifile);
}

// interp_init initializes the interpreter.
int interp_init(void *handle) {
    return static_cast<InterpBase*>(handle)->init();
}

// interp_open opens a G-code file for interpretation.
int interp_open(void *handle, const char *filename) {
    return static_cast<InterpBase*>(handle)->open(filename);
}

// interp_read reads the next line from the open file.
int interp_read(void *handle) {
    return static_cast<InterpBase*>(handle)->read();
}

// interp_read_string reads a line from a string.
int interp_read_string(void *handle, const char *line) {
    return static_cast<InterpBase*>(handle)->read(line);
}

// interp_execute executes the last read line.
int interp_execute(void *handle) {
    return static_cast<InterpBase*>(handle)->execute();
}

// interp_execute_string executes a string directly.
int interp_execute_string(void *handle, const char *line) {
    return static_cast<InterpBase*>(handle)->execute(line);
}

// interp_execute_string_lineno executes a string with explicit line number.
int interp_execute_string_lineno(void *handle, const char *line, int line_number) {
    return static_cast<InterpBase*>(handle)->execute(line, line_number);
}

// interp_synch synchronizes interpreter state with the machine.
int interp_synch(void *handle) {
    return static_cast<InterpBase*>(handle)->synch();
}

// interp_close closes the currently open file.
int interp_close(void *handle) {
    return static_cast<InterpBase*>(handle)->close();
}

// interp_reset resets the interpreter to initial state.
int interp_reset(void *handle) {
    return static_cast<InterpBase*>(handle)->reset();
}

// interp_exit exits the interpreter.
int interp_exit(void *handle) {
    return static_cast<InterpBase*>(handle)->exit();
}

// interp_on_abort notifies the interpreter of an abort condition.
int interp_on_abort(void *handle, int reason, const char *message) {
    return static_cast<InterpBase*>(handle)->on_abort(reason, message);
}

// interp_state_tag_size returns sizeof the packed POD state tag the canon's
// update_tag callback points at (state_tag_t).
int interp_state_tag_size(void) {
    return (int)sizeof(state_tag_t);
}

// interp_restore_from_tag rolls the interpreter's modal state back to a
// previously captured packed state_tag_t (2.9 emcTaskStateRestore →
// Interp::restore_from_tag). tag_bytes must point at interp_state_tag_size()
// bytes captured from an update_tag callback.
int interp_restore_from_tag(void *handle, const void *tag_bytes) {
    state_tag_t base;
    memcpy(&base, tag_bytes, sizeof(base));
    StateTag tag(base);
    return static_cast<InterpBase*>(handle)->restore_from_tag(tag);
}

// interp_active_modes_from_tag decodes a packed state_tag_t into the
// TASK_STAT-style active-code arrays (Interp::active_modes), the mechanism
// 2.9 task uses to report the EXECUTING segment's modal state. Returns
// nonzero when the tag is invalid (never written).
int interp_active_modes_from_tag(void *handle, const void *tag_bytes,
                                 int *g_codes, int *m_codes, double *settings) {
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (!ip)
        return -1;
    state_tag_t base;
    memcpy(&base, tag_bytes, sizeof(base));
    StateTag tag(base);
    return ip->active_modes(g_codes, m_codes, settings, tag);
}

// interp_copy_state_tag copies the packed state_tag_t an update_tag callback
// points at (delivered across the GMI boundary as an integer) into dst.
void interp_copy_state_tag(unsigned long long src, void *dst) {
    memcpy(dst, (const void *)(uintptr_t)src, sizeof(state_tag_t));
}

// interp_set_canon_callbacks sets the canon callback table.
void interp_set_canon_callbacks(void *handle, const canon_callbacks_t *cb) {
    static_cast<InterpBase*>(handle)->set_canon_callbacks(cb);
}

// interp_line returns the current line number.
int interp_line(void *handle) {
    return static_cast<InterpBase*>(handle)->line();
}

// interp_sequence_number returns the current sequence number (N-word).
int interp_sequence_number(void *handle) {
    return static_cast<InterpBase*>(handle)->sequence_number();
}

// interp_call_level returns the current subroutine call level.
int interp_call_level(void *handle) {
    return static_cast<InterpBase*>(handle)->call_level();
}

// interp_error_text retrieves the error text for a return code.
const char *interp_error_text(void *handle, int errcode, char *buf, size_t buflen) {
    return static_cast<InterpBase*>(handle)->error_text(errcode, buf, buflen);
}

// interp_line_text retrieves the current line text.
const char *interp_line_text(void *handle, char *buf, size_t buflen) {
    return static_cast<InterpBase*>(handle)->line_text(buf, buflen);
}

// interp_file_name retrieves the current file name.
const char *interp_file_name(void *handle, char *buf, size_t buflen) {
    return static_cast<InterpBase*>(handle)->file_name(buf, buflen);
}

// interp_command retrieves the current command text.
const char *interp_command(void *handle, char *buf, size_t buflen) {
    return static_cast<InterpBase*>(handle)->command(buf, buflen);
}

// interp_set_loglevel sets the interpreter log level.
void interp_set_loglevel(void *handle, int level) {
    static_cast<InterpBase*>(handle)->set_loglevel(level);
}

// interp_set_loop_on_main_m99 controls M99 loop behavior.
void interp_set_loop_on_main_m99(void *handle, int state) {
    static_cast<InterpBase*>(handle)->set_loop_on_main_m99(state != 0);
}

// interp_set_task_mode sets _setup.task_mode (1=task, 0=preview).
// When task_mode=1, save_parameters actually writes the var file.
void interp_set_task_mode(void *handle, int mode) {
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (ip) {
        ip->_setup.task_mode = mode;
    }
}

// interp_set_user_defined_function registers a function pointer for M(100+idx).
void interp_set_user_defined_function(void *handle, int idx,
    void (*fn)(int num, double arg1, double arg2)) {
    if (idx < 0 || idx >= USER_DEFINED_FUNCTION_NUM) return;
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (ip) {
        ip->_setup.user_defined_function[idx] = fn;
    }
}

// interp_active_g_codes retrieves the active G-code array.
void interp_active_g_codes(void *handle, int *gcodes, int max_len) {
    static_cast<InterpBase*>(handle)->active_g_codes(gcodes);
    (void)max_len; // array size is ACTIVE_G_CODES (17)
}

// interp_active_m_codes retrieves the active M-code array.
void interp_active_m_codes(void *handle, int *mcodes, int max_len) {
    static_cast<InterpBase*>(handle)->active_m_codes(mcodes);
    (void)max_len;
}

// interp_active_settings retrieves the active settings array.
void interp_active_settings(void *handle, double *settings, int max_len) {
    static_cast<InterpBase*>(handle)->active_settings(settings);
    (void)max_len;
}

// interp_set_ini_accessor stores the INI accessor callback struct in the
// interpreter's setup struct.  Must be called before init().
void interp_set_ini_accessor(void *handle, const interp_ini_accessor_t *accessor) {
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (ip && accessor) {
        ip->_setup.ini_accessor.ctx = accessor->ctx;
        ip->_setup.ini_accessor.get = accessor->get;
        ip->_setup.ini_accessor.get_nth = accessor->get_nth;
    }
}

// interp_ini_load_accessor loads INI config using the accessor callbacks
// instead of opening a file.  Replaces interp_ini_load() for stmak usage.
int interp_ini_load_accessor(void *handle, const interp_ini_accessor_t *accessor) {
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (!ip || !accessor) return -1;

    // Store accessor for runtime use (fetch_ini_param)
    ip->_setup.ini_accessor.ctx = accessor->ctx;
    ip->_setup.ini_accessor.get = accessor->get;
    ip->_setup.ini_accessor.get_nth = accessor->get_nth;

    // PARAMETER_FILE is optional.  With the persist-backed parameter I/O
    // backend (the stmak default) numbered parameters live in the persistence
    // service, so no .var file name is needed.  Only the opt-in
    // PARAMETER_FILE_MODE=file backend uses it, and that requirement is
    // enforced separately (task/module.go).  Record the name when present so
    // the file backend / save_parameters have it.
    const char *param_file = accessor->get(accessor->ctx, "RS274NGC", "PARAMETER_FILE");
    if (param_file && param_file[0] != '\0') {
        ip->_setup.canon.set_parameter_file_name(param_file);
    }
    return 0;
}

// interp_set_param_io sets the parameter I/O backend on the interpreter.
// Must be called before init().
void interp_set_param_io(void *handle, const interp_param_io_t *io) {
    Interp *ip = dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
    if (ip) {
        ip->set_param_io(io);
    }
}

// --- Interpreter state inspection ---

// as_interp downcasts an opaque shim handle, or returns NULL.
static Interp *as_interp(void *handle) {
    return dynamic_cast<Interp*>(static_cast<InterpBase*>(handle));
}

// interp_get_parameter reads a numbered parameter (#index).
double interp_get_parameter(void *handle, int index) {
    Interp *ip = as_interp(handle);
    if (!ip || index < 0 ||
        index >= interp_param_global::RS274NGC_MAX_PARAMETERS) {
        return 0.0;
    }
    return ip->_setup.parameters[index];
}

// interp_length_units returns _setup.length_units as a CANON_UNITS value.
int interp_length_units(void *handle) {
    Interp *ip = as_interp(handle);
    return ip ? static_cast<int>(ip->_setup.length_units) : -1;
}

// interp_origin_index returns the active coordinate system (1=G54 .. 9=G59.3).
int interp_origin_index(void *handle) {
    Interp *ip = as_interp(handle);
    return ip ? ip->_setup.origin_index : -1;
}

// The interp_inspection.hh accessors, indexed by enum interp_axis.  Going
// through that header rather than touching _setup fields directly is
// deliberate: it is the documented seam, so a field rename in setup breaks
// one shim table instead of every caller.
typedef double &(*axis_accessor_t)(setup_pointer);

static const axis_accessor_t position_accessors[INTERP_AXIS_COUNT] = {
    currentX, currentY, currentZ, currentA, currentB, currentC};

static const axis_accessor_t work_offset_accessors[INTERP_AXIS_COUNT] = {
    currentWorkOffsetX, currentWorkOffsetY, currentWorkOffsetZ,
    currentWorkOffsetA, currentWorkOffsetB, currentWorkOffsetC};

static const axis_accessor_t axis_offset_accessors[INTERP_AXIS_COUNT] = {
    currentAxisOffsetX, currentAxisOffsetY, currentAxisOffsetZ,
    currentAxisOffsetA, currentAxisOffsetB, currentAxisOffsetC};

static double axis_value(void *handle, int axis,
                         const axis_accessor_t *accessors) {
    Interp *ip = as_interp(handle);
    if (!ip || axis < 0 || axis >= INTERP_AXIS_COUNT) {
        return 0.0;
    }
    return accessors[axis](&ip->_setup);
}

double interp_current_position(void *handle, int axis) {
    return axis_value(handle, axis, position_accessors);
}

double interp_current_work_offset(void *handle, int axis) {
    return axis_value(handle, axis, work_offset_accessors);
}

double interp_current_axis_offset(void *handle, int axis) {
    return axis_value(handle, axis, axis_offset_accessors);
}

} // extern "C"
