/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPL Version 2
 */
// task_interp_shim.h — C declarations for the interpreter shim functions.
// This header is included by interp.go via cgo.
//
// Named for the task, not just for what it is, because emc/rs274ngc has its
// own interp_shim.h declaring a different set of types for the preview
// interpreter. The two used to share both a basename and an include guard: in
// a flat include directory the wrong one wins silently, and the symptom is
// this package failing on types that sit in the file next to it.

#ifndef TASK_INTERP_SHIM_H
#define TASK_INTERP_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// --- INI accessor callback struct ---
// Provides INI values to the interpreter without it needing to parse files.
// The caller (Go milltask) owns the ctx and implements the callbacks.
// Namespace resolution is handled by the caller — the interpreter just asks
// for section/key and gets the (possibly namespace-overridden) value back.
typedef struct {
    void *ctx;
    // Get the first value for section/key.  Returns NULL if not found.
    // The returned string is valid until the next call to get/get_nth on the
    // same accessor (caller may use a single reusable buffer).
    const char* (*get)(void *ctx, const char *section, const char *key);
    // Get the n-th value (1-based) for section/key (for repeated keys like REMAP).
    // Returns NULL when there is no n-th occurrence.
    const char* (*get_nth)(void *ctx, const char *section, const char *key, int n);
} interp_ini_accessor_t;

// Interpreter lifecycle
void *interp_new(void);
void *interp_from_lib(const char *shlib);
void interp_delete(void *handle);

// Configuration and initialization
int interp_ini_load(void *handle, const char *inifile);
int interp_ini_load_accessor(void *handle, const interp_ini_accessor_t *accessor);
int interp_init(void *handle);

// Set the INI accessor for runtime INI parameter lookups (#<_ini[SEC]KEY>).
// Must be called before init() if runtime INI access is desired.
void interp_set_ini_accessor(void *handle, const interp_ini_accessor_t *accessor);

// File operations
int interp_open(void *handle, const char *filename);
int interp_close(void *handle);
int interp_reset(void *handle);
int interp_exit(void *handle);

// Read/Execute
int interp_read(void *handle);
int interp_read_string(void *handle, const char *line);
int interp_execute(void *handle);
int interp_execute_string(void *handle, const char *line);
int interp_execute_string_lineno(void *handle, const char *line, int line_number);
int interp_synch(void *handle);

// State queries
int interp_line(void *handle);
int interp_sequence_number(void *handle);
int interp_call_level(void *handle);
int interp_on_abort(void *handle, int reason, const char *message);
int interp_state_tag_size(void);
int interp_restore_from_tag(void *handle, const void *tag_bytes);
/* Decodes a packed state_tag_t into TASK_STAT-style active G-/M-code and
 * settings arrays (Interp::active_modes) — how 2.9 task derives the status
 * codes of the EXECUTING motion segment from its tag. Arrays must hold
 * ACTIVE_G_CODES / ACTIVE_M_CODES / ACTIVE_SETTINGS entries and should be
 * pre-filled with defaults (active_modes leaves some entries untouched).
 * Returns 0 on success, nonzero when the tag is invalid. */
int interp_active_modes_from_tag(void *handle, const void *tag_bytes,
                                 int *g_codes, int *m_codes, double *settings);
/* Copies the packed state_tag_t an update_tag callback points at (the GMI
 * boundary delivers the pointer as an integer) into dst — done in C so Go
 * never converts a bare integer to a pointer (vet unsafeptr). */
void interp_copy_state_tag(unsigned long long src, void *dst);

// String queries
const char *interp_error_text(void *handle, int errcode, char *buf, size_t buflen);
const char *interp_line_text(void *handle, char *buf, size_t buflen);
const char *interp_file_name(void *handle, char *buf, size_t buflen);
const char *interp_command(void *handle, char *buf, size_t buflen);

// Configuration
void interp_set_loglevel(void *handle, int level);
void interp_set_loop_on_main_m99(void *handle, int state);
void interp_set_task_mode(void *handle, int mode);

// Canon callback wiring
struct canon_callbacks;
typedef struct canon_callbacks canon_callbacks_t;
void interp_set_canon_callbacks(void *handle, const canon_callbacks_t *cb);

// M-code handler registration (M100-M199)
// Register a user-defined function slot in the interpreter.
// The slot will call the provided function pointer when M(100+idx) is encountered.
void interp_set_user_defined_function(void *handle, int idx,
    void (*fn)(int num, double arg1, double arg2));

// Active G/M codes and settings
// These copy the interpreter's internal arrays into caller-provided buffers.
void interp_active_g_codes(void *handle, int *gcodes, int max_len);
void interp_active_m_codes(void *handle, int *mcodes, int max_len);
void interp_active_settings(void *handle, double *settings, int max_len);

// Parameter I/O backend.
// Set before init() to override the default file-based persistence.
struct interp_param_io_t;
void interp_set_param_io(void *handle, const struct interp_param_io_t *io);

// --- Interpreter state inspection ---
// Read-only views of interpreter state that has no other C-callable route.
// These mirror emc/rs274ngc/interp_inspection.hh, which exists for exactly
// this purpose ("shim functions to access interp internal data generically
// ... so that backend changes don't break the tests as often") and is linked
// into librs274 already.  Used by the Go interpreter unit tests to assert on
// offset/parameter state that is invisible from the canon stream.

// Axis selector for the interp_current_* accessors below.
enum interp_axis {
    INTERP_AXIS_X = 0,
    INTERP_AXIS_Y,
    INTERP_AXIS_Z,
    INTERP_AXIS_A,
    INTERP_AXIS_B,
    INTERP_AXIS_C,
    INTERP_AXIS_COUNT
};

// Read a numbered parameter (#index), e.g. 5211 for the G92 X offset.
// Returns 0 for an out-of-range index or a non-Interp handle.
double interp_get_parameter(void *handle, int index);

// Current length units as a CANON_UNITS value: INCHES=1, MM=2, CM=3.
// Returns -1 for a non-Interp handle.
int interp_length_units(void *handle);

// Active coordinate system index: 1=G54, 2=G55 ... 9=G59.3.
// Returns -1 for a non-Interp handle.
int interp_origin_index(void *handle);

// Current position, G5x work offset and G92 axis offset for one axis, in the
// interpreter's internal units.  Return 0 for an out-of-range axis or a
// non-Interp handle.
double interp_current_position(void *handle, int axis);
double interp_current_work_offset(void *handle, int axis);
double interp_current_axis_offset(void *handle, int axis);

#ifdef __cplusplus
}
#endif

#endif // TASK_INTERP_SHIM_H
