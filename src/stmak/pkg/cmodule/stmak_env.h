/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_env.h — Combined environment header for stratuMAK C modules.
//
// This is the new top-level header that replaces cmodule.h for modules
// migrating to the stratuMAK API.  It composes the four sub-API headers
// (log, ini, hal, rtapi) into a single cmod_env_t structure.
//
// Module lifecycle remains the same:
//   New(env, name, argc, argv) → Start() → Stop() → Destroy()
//
// The launcher guarantees:
//   - Strings returned by ini->get(), ini->source_file() and path->resolve()
//     remain valid until Destroy() is called on the module that requested
//     them (arena).
//   - The env pointer and all sub-API pointers remain valid from New()
//     through Destroy().
//   - hal and rtapi pointers may be NULL for modules that don't need them.
//     Always check before use.
//
// For C++ plugins: declare the New symbol as extern "C".

#ifndef STMAK_ENV_H
#define STMAK_ENV_H

#include "stmak_ini.h"
#include "stmak_hal.h"
#include "stmak_rtapi.h"
#include "stmak_log.h"
#include "stmak_api.h"
#include "stmak_path.h"

#ifdef __cplusplus
extern "C" {
#endif

// ---------------------------------------------------------------------------
// ABI version
// ---------------------------------------------------------------------------

// CMOD_ABI_VERSION identifies the layout of everything below: cmod_env_t,
// cmod_t, the sub-API structures they point at, and the calling conventions
// between them.  Bump it in any change that a module compiled against the
// previous header would not survive -- a field added to or moved within
// cmod_env_t or a sub-API struct, a changed callback signature, a changed
// lifetime guarantee.  Purely additive changes at the END of a structure that
// old modules never read are the one thing that does not need a bump.
//
// Why it exists: a locally rebuilt stmakd can be older than the cmods
// installed beside it (see EXTERNAL_MODULE_INSTALL_DESIGN.md section 4.4).
// Without a stamp, dlopening a cmod built for a different cmod_env_t is
// undefined behaviour with nothing to detect it at load time -- the symbols
// all resolve, and the module reads the wrong offsets.  The launcher reads
// this symbol before it calls New() and refuses a module that disagrees.
#define CMOD_ABI_VERSION 1

// Every module carries the stamp automatically, by including this header:
// the definition is emitted into each translation unit rather than left for a
// module to remember to declare.
//
// weak, because a module built from several translation units would otherwise
// have several definitions of it and fail to link; the linker keeps one.
// default visibility, because the launcher looks it up with dlsym().
// used, so the compiler keeps it even though nothing in the module refers to
// it.
// Not const, which is the one surprising part: at namespace scope C++ gives a
// const object internal linkage, and the extern "C" block above does not undo
// that -- g++ rejects it outright ("weak declaration must be public") and the
// symbol would not be there for dlsym() to find. Nothing writes it.
#ifndef CMOD_ABI_VERSION_SYMBOL_DEFINED
#define CMOD_ABI_VERSION_SYMBOL_DEFINED
__attribute__((weak, used, visibility("default")))
unsigned int cmod_abi_version = CMOD_ABI_VERSION;
#endif

// ---------------------------------------------------------------------------
// cmod_env_t — the launcher-provided environment passed to New().
// ---------------------------------------------------------------------------

typedef struct {
    // dlopen() handle of the .so file that contains this plugin.
    // Plugins that create RT HAL components should pass this to
    // hal->init() so the launcher can lock the .so's memory pages.
    void *dl_handle;

    // Sub-API pointers — always non-NULL for log and ini.
    // hal and rtapi may be NULL for pure userspace / non-HAL modules.
    const stmak_log_t   *log;
    const stmak_ini_t   *ini;
    const stmak_hal_t   *hal;    // NULL if module has no HAL component
    const stmak_rtapi_t *rtapi;  // NULL if module needs no RT services
    const stmak_api_t   *api;    // NULL if module does not use dynamic APIs
    // Configuration path resolution.  Always non-NULL.  Any path that comes
    // from configuration (an argument, a filename inside a config file, an INI
    // value) must go through path->resolve() before it is opened — see
    // stmak_path.h.
    const stmak_path_t  *path;
} cmod_env_t;

// ---------------------------------------------------------------------------
// cmod_t — module instance returned by the constructor.
// ---------------------------------------------------------------------------

typedef struct cmod {
    int  (*Init)(struct cmod *self);
    int  (*Start)(struct cmod *self);
    void (*Stop)(struct cmod *self);
    void (*Destroy)(struct cmod *self);
    void *priv;
} cmod_t;

// ---------------------------------------------------------------------------
// cmod_new_fn — constructor signature.  Plugins export "New" with this type.
// ---------------------------------------------------------------------------

typedef int (*cmod_new_fn)(
    const cmod_env_t *env,
    const char *name,
    int argc, const char **argv,
    cmod_t **out
);

#ifdef __cplusplus
}
#endif

#endif // STMAK_ENV_H
