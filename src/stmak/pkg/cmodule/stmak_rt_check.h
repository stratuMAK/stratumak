// stmak_rt_check.h — realtime function-effect annotations for stratuMAK C modules.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: LGPL Version 2
//
// STMAK_NONBLOCKING marks a function (or function-pointer type) as safe for
// the hard-RT path: no allocation, no locking, no blocking calls.  It mirrors
// RTAPI_NONBLOCKING in rtapi.h (this header set is self-contained for
// external modules, so the definition is duplicated — keep in sync).  A
// third copy of the clang version gate (STMAK_API_NONBLOCKING) is emitted
// into generated GMI headers by gmicompile (cgen/server.go) — update it
// alongside this one.
//
// Verification is done by clang's function-effects analysis: the opt-in
// "make rt-effects-check" target compiles the RT translation units with
// -Wfunction-effects (clang >= 20; the clang 19 implementation of the
// analysis is broken).  On gcc and older clang everything here expands to
// nothing, so production builds are unaffected.
//
// STMAK_NONBLOCKING_TRUSTED_BEGIN/END wrap a function *definition* that is
// declared STMAK_NONBLOCKING but cannot be verified by the compiler, e.g.
// because it calls a libc or vendor-library primitive that is non-blocking
// in practice.  Every use is a trust boundary of the RT path and must carry
// a justification comment.

#ifndef STMAK_RT_CHECK_H
#define STMAK_RT_CHECK_H

#if defined(__clang__) && (__clang_major__ >= 20) && defined(__has_attribute)
#if __has_attribute(nonblocking)
#define STMAK_NONBLOCKING __attribute__((nonblocking))
#define STMAK_NONBLOCKING_TRUSTED_BEGIN \
    _Pragma("clang diagnostic push") \
    _Pragma("clang diagnostic ignored \"-Wfunction-effects\"")
#define STMAK_NONBLOCKING_TRUSTED_END \
    _Pragma("clang diagnostic pop")
#endif
#endif
#ifndef STMAK_NONBLOCKING
#define STMAK_NONBLOCKING
#define STMAK_NONBLOCKING_TRUSTED_BEGIN
#define STMAK_NONBLOCKING_TRUSTED_END
#endif

#endif // STMAK_RT_CHECK_H
