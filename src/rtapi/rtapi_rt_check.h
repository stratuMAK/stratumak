/** rtapi_rt_check.h — realtime function-effect annotations.

    RTAPI_NONBLOCKING marks a function (or function-pointer type) as safe
    for the hard-RT path: no allocation, no locking, no blocking calls,
    no static locals.  Backed by clang's function-effects analysis:
    "make rt-effects-check" compiles the RT translation units with
    -Wfunction-effects and verifies annotated bodies transitively.
    Expands to nothing on gcc and on clang < 20 (whose analysis is
    broken), so production builds are unaffected.

    RTAPI_NONBLOCKING_TRUSTED_BEGIN/END wrap a function *definition* that
    is declared RTAPI_NONBLOCKING but cannot be verified by the compiler,
    e.g. because it calls a libc/vDSO primitive that is non-blocking in
    practice (clock_gettime).  Every use is a trust boundary of the RT
    path and must carry a justification comment.

    The cmodule header set carries an equivalent STMAK_NONBLOCKING in
    stmak_rt_check.h (self-contained for external modules), and gmicompile
    emits a third copy of the version gate (STMAK_API_NONBLOCKING, see
    gmicompile/cgen/server.go) into generated GMI headers — keep all
    three in sync.

    Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
    License: LGPL Version 2.1
*/

#ifndef RTAPI_RT_CHECK_H
#define RTAPI_RT_CHECK_H

#if defined(__clang__) && (__clang_major__ >= 20) && defined(__has_attribute)
#if __has_attribute(nonblocking)
#define RTAPI_NONBLOCKING __attribute__((nonblocking))
#define RTAPI_NONBLOCKING_TRUSTED_BEGIN \
    _Pragma("clang diagnostic push") \
    _Pragma("clang diagnostic ignored \"-Wfunction-effects\"")
#define RTAPI_NONBLOCKING_TRUSTED_END \
    _Pragma("clang diagnostic pop")
#endif
#endif
#ifndef RTAPI_NONBLOCKING
#define RTAPI_NONBLOCKING
#define RTAPI_NONBLOCKING_TRUSTED_BEGIN
#define RTAPI_NONBLOCKING_TRUSTED_END
#endif

#endif /* RTAPI_RT_CHECK_H */
