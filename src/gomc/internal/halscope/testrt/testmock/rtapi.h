/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPL Version 2
 */
/* rtapi.h — Mock for halscope unit tests */
#ifndef RTAPI_H
#define RTAPI_H
/* All needed types are in hal_mock.h (included via hal.h) */

/* The RT-effect annotations are NOT mocked: rtapi_rt_check.h is a contract
 * header shared with the generated GMI headers, and it already degrades to
 * empty defines off clang-20+. Including the real one keeps halscope_rt.h's
 * RTAPI_NONBLOCKING meaningful when the suite is built with a clang that
 * supports -Wfunction-effects, instead of silently stubbing the check out. */
#include "rtapi/rtapi_rt_check.h"

/* RT-safe allocator (rtapi/core/rtapi_core.h). The contract the cmod
 * environment provides is "returns zeroed memory, freeable"; under test that
 * is libc calloc/free, which is what the uspace implementation reduces to
 * anyway. Kept here rather than linked so the suite stays free of RTAPI. */
#include <stdlib.h>

static inline void *rtapi_calloc(size_t size) { return calloc(1, size); }
static inline void rtapi_free(void *p) { free(p); }

#endif
