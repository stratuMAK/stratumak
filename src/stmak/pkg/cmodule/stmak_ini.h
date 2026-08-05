/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_ini.h — INI configuration access API for stratuMAK C modules.
//
// Provides read-only access to the parsed INI file through callbacks
// into the Go launcher.  Returned strings have arena lifetime — they
// remain valid until the module's Destroy() is called.
//
// Usage:
//   const char *val = env->ini->get(env->ini->ctx, "DISPLAY", "PROGRAM_PREFIX");
//   const char *path = env->ini->source_file(env->ini->ctx);
//
//   // Multi-value lookup:
//   int count = 0;
//   const char **vals = env->ini->get_all(env->ini->ctx, "HAL", "HALFILE", &count);
//   for (int i = 0; i < count; i++) { use(vals[i]); }
//
//   // Typed helpers with defaults:
//   double vel = stmak_ini_get_double(env->ini, "TRAJ", "MAX_VELOCITY", 1.0);
//   int debug  = stmak_ini_get_int(env->ini, "EMC", "DEBUG", 0);

#ifndef STMAK_INI_H
#define STMAK_INI_H

#include <stdlib.h>
#include <string.h>

#ifdef __cplusplus
extern "C" {
#endif

// stmak_ini_t — INI configuration access callbacks.
//
// ctx:          opaque pointer passed as first argument to each callback.
// get:          returns the value for section/key, or NULL if not found.
//               The returned string is valid until Destroy().
// get_all:      returns all values for section/key as a NULL-terminated array.
//               Both the array and the strings have arena lifetime (valid until
//               Destroy()).  *out_count receives the number of values.
//               Returns NULL with *out_count == 0 if the key is not present.
// source_file:  returns the absolute path of the INI file, or "" when the
//               launcher runs without one.  Never NULL; valid until Destroy().
//
// The launcher may run with no INI file at all (halrun mode).  In that case
// get() returns NULL and get_all() returns NULL with *out_count == 0 for every
// lookup — i.e. no-INI is indistinguishable from "key not found", so code
// written against the documented contract needs no special case.
typedef struct {
    void *ctx;
    const char*  (*get)(void *ctx, const char *section, const char *key);
    const char** (*get_all)(void *ctx, const char *section, const char *key,
                            int *out_count);
    const char*  (*source_file)(void *ctx);
} stmak_ini_t;

// ---------------------------------------------------------------------------
// Typed convenience helpers — pure client-side, no CGO crossing.
// ---------------------------------------------------------------------------

// stmak_ini_get_int returns the INI value parsed as an integer (via strtol),
// or default_val if the key is missing or not a valid integer.
static inline int
stmak_ini_get_int(const stmak_ini_t *ini, const char *section, const char *key,
                 int default_val) {
    const char *val = ini->get(ini->ctx, section, key);
    if (!val) return default_val;
    char *end;
    long v = strtol(val, &end, 0);
    if (end == val || *end != '\0') return default_val;
    return (int)v;
}

// stmak_ini_get_double returns the INI value parsed as a double (via strtod),
// or default_val if the key is missing or not a valid number.
static inline double
stmak_ini_get_double(const stmak_ini_t *ini, const char *section, const char *key,
                    double default_val) {
    const char *val = ini->get(ini->ctx, section, key);
    if (!val) return default_val;
    char *end;
    double v = strtod(val, &end);
    if (end == val) return default_val;
    return v;
}

// stmak_ini_get_bool returns the INI value parsed as a boolean.
// Recognises "1", "TRUE", "YES" (case-insensitive) as true(1),
// "0", "FALSE", "NO" as false(0).  Returns default_val otherwise.
static inline int
stmak_ini_get_bool(const stmak_ini_t *ini, const char *section, const char *key,
                  int default_val) {
    const char *val = ini->get(ini->ctx, section, key);
    if (!val) return default_val;
    if (!strcmp(val, "1") || !strcasecmp(val, "TRUE") || !strcasecmp(val, "YES"))
        return 1;
    if (!strcmp(val, "0") || !strcasecmp(val, "FALSE") || !strcasecmp(val, "NO"))
        return 0;
    return default_val;
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_INI_H
