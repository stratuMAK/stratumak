// stmak_rtapi.h — RTAPI utility functions for stmak C modules.
//
// RTAPI_NAME_LEN mirrors rtapi.h by John Kasunich and Paul Corner.
// License of rtapi.h: LGPL Version 2.1.
// Copyright (c) 2004 John Kasunich, Paul Corner.
//
// New API (callback table, inline helpers):
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: LGPL Version 2.1
//
// Provides RT-safe memory allocation (mlock + page pre-fault) and time
// functions through callbacks in stmak_rtapi_t.  Initially these delegate
// to the existing liblinuxcnchal.so / uspace_rtapi_lib.c implementation.
//
// Pure formatting functions (snprintf, vsnprintf) are NOT callbacks — they
// are simply libc on uspace targets.  This header provides stmak_snprintf()
// and stmak_vsnprintf() as thin aliases for consistency.
//
// Usage:
//   void *p = env->rtapi->calloc(env->rtapi->ctx, sizeof(my_struct));
//   int64_t now = env->rtapi->get_time(env->rtapi->ctx);
//   env->rtapi->free(env->rtapi->ctx, p);

#ifndef STMAK_RTAPI_H
#define STMAK_RTAPI_H

#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

#include "stmak_rt_check.h"

#ifdef __cplusplus
extern "C" {
#endif

// PLL functions are always available in stmak_rtapi_t.
#define STMAK_RTAPI_TASK_PLL_SUPPORT

#define STMAK_RTAPI_NAME_LEN 31

// ---------------------------------------------------------------------------
// stmak_rtapi_t — RTAPI callback table.
// ---------------------------------------------------------------------------

typedef struct {
    void *ctx;

    // RT-safe memory allocation.
    // calloc: allocates size bytes, zeroed, mlock'd and page-faulted.
    // realloc: resizes an existing allocation (unlocks old, locks new).
    // free: munlock + free.
    void *(*calloc) (void *ctx, size_t size);
    void *(*realloc)(void *ctx, void *ptr, size_t size);
    void  (*free)   (void *ctx, void *ptr);

    // Monotonic time in nanoseconds.  RT-safe (nonblocking).
    int64_t (*get_time)(void *ctx) STMAK_NONBLOCKING;

    // Busy-wait delay (nanoseconds), clamped to delay_max().  RT-safe.
    void    (*delay)(void *ctx, long nsec) STMAK_NONBLOCKING;
    long    (*delay_max)(void *ctx) STMAK_NONBLOCKING;

    // Task PLL functions for RT thread synchronisation.  RT-safe.
    int64_t (*pll_get_reference)(void *ctx) STMAK_NONBLOCKING;
    int     (*pll_set_correction)(void *ctx, long value) STMAK_NONBLOCKING;

    // Returns >= 0 if called from a RT task, < 0 otherwise.  RT-safe.
    int     (*task_self)(void *ctx) STMAK_NONBLOCKING;
} stmak_rtapi_t;

// ---------------------------------------------------------------------------
// Formatting — thin aliases for libc.  No callback needed on uspace.
// ---------------------------------------------------------------------------

#define stmak_snprintf  snprintf
#define stmak_vsnprintf vsnprintf

#ifdef __cplusplus
}
#endif

#endif // STMAK_RTAPI_H
