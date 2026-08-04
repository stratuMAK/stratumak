// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package launcher — cmodules.go handles loading, lifecycle management of
// C plugin .so files loaded via the "load" HAL command.
//
// C plugins are resolved from EMC2_CMOD_DIR (bare names) and loaded via
// dlopen/dlsym.  They share the same lifecycle as Go plugins:
//
//	New(env, name, args) → Start() → Stop() → Destroy()
//
// The launcher provides INI access, logging, HAL and RTAPI to C plugins
// via the stmak sub-API callback structs in cmod_env_t, so plugins never
// parse config files or link liblinuxcnchal.so directly.
package launcher

/*
#cgo CFLAGS: -I${SRCDIR}/../../pkg/cmodule -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#cgo LDFLAGS: -ldl

#include <dlfcn.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include "stmak_env.h"
#include "hal.h"
#include "rtapi.h"

// --- API registry callbacks (forward-declared, implemented in Go via //export) ---

extern int stmak_api_register_cb(void *ctx, char *api_name, int version,
                                char *instance_name, void *callbacks);
extern void *stmak_api_get_cb(void *ctx, char *api_name, int version,
                             char *instance_name);
extern int stmak_watch_push_cb(void *ctx, char *api_name, char *instance_name,
                              char *func_name, void *data, int data_len);
extern void stmak_record_consumer_cb(void *ctx, char *consumer_instance,
                                    char *api_name, char *provider_instance);

// --- RT module handle tracking ---
//
// Modules that call hal_init() with STMAK_HAL_COMP_REALTIME have their
// dl_handle recorded here.  The Go side batch-locks / unlocks these
// before starting / after stopping HAL threads.  Works for both cmod
// and gomod — the interception point is stmak_hal_init_cb which every
// module's hal->init() delegates to.

static void **rt_dl_handles = NULL;
static int    rt_dl_count   = 0;
static int    rt_dl_cap     = 0;

static int rt_dl_handles_add(void *handle) {
    for (int i = 0; i < rt_dl_count; i++)
        if (rt_dl_handles[i] == handle) return 0;  // deduplicate
    if (rt_dl_count >= rt_dl_cap) {
        int new_cap = rt_dl_cap ? rt_dl_cap * 2 : 8;
        void **grown = realloc(rt_dl_handles, new_cap * sizeof(void *));
        if (!grown)
            return -1;  // keep the old array intact; the caller reports it
        rt_dl_handles = grown;
        rt_dl_cap = new_cap;
    }
    rt_dl_handles[rt_dl_count++] = handle;
    return 0;
}

static void rt_dl_handles_remove(void *handle) {
    for (int i = 0; i < rt_dl_count; i++) {
        if (rt_dl_handles[i] == handle) {
            rt_dl_handles[i] = rt_dl_handles[--rt_dl_count];
            return;
        }
    }
}

static void rt_dl_handles_free(void) {
    free(rt_dl_handles);
    rt_dl_handles = NULL;
    rt_dl_count = 0;
    rt_dl_cap = 0;
}

static int rt_dl_handles_len(void) { return rt_dl_count; }
static void *rt_dl_handles_get(int i) { return rt_dl_handles[i]; }

// --- Pass-through HAL callbacks (delegate to liblinuxcnchal.so) ---

static int stmak_hal_init_cb(void *ctx, const char *name, void *dl_handle, int type) {
    if (type == STMAK_HAL_COMP_REALTIME && dl_handle) {
        // An RT module whose handle cannot be tracked would never have its
        // PT_LOAD segments locked; refuse the init rather than silently
        // degrade realtime.
        if (rt_dl_handles_add(dl_handle) != 0)
            return -ENOMEM;
    }
    return hal_init_ex(name, dl_handle, (component_type_t)type);
}

static void stmak_hal_exit_cb(void *ctx, int comp_id) {
    hal_exit(comp_id);
}

static int stmak_hal_ready_cb(void *ctx, int comp_id) {
    return hal_ready(comp_id);
}

static void *stmak_hal_malloc_cb(void *ctx, long size) {
    return hal_malloc(size);
}

static int stmak_hal_pin_new_cb(void *ctx, const char *name, int type, int dir,
                               void **data_ptr_addr, int comp_id) {
    return hal_pin_new(name, (hal_type_t)type, (hal_pin_dir_t)dir,
                       data_ptr_addr, comp_id);
}

static int stmak_hal_param_new_cb(void *ctx, const char *name, int type, int dir,
                                 void *data_addr, int comp_id) {
    return hal_param_new(name, (hal_type_t)type, (hal_param_dir_t)dir,
                         data_addr, comp_id);
}

static int stmak_hal_pin_alias_cb(void *ctx, const char *pin_name, const char *alias) {
    return hal_pin_alias(pin_name, alias);
}

static int stmak_hal_param_alias_cb(void *ctx, const char *param_name, const char *alias) {
    return hal_param_alias(param_name, alias);
}

static int stmak_hal_export_funct_cb(void *ctx, const char *name,
                                    stmak_hal_funct_t funct,
                                    void *arg, int uses_fp, int reentrant,
                                    int comp_id) {
    return hal_export_funct(name, funct, arg, uses_fp, reentrant, comp_id);
}

// --- Pass-through RTAPI callbacks ---

static void *stmak_rtapi_calloc_cb(void *ctx, size_t size) {
    return rtapi_calloc(size);
}

static void *stmak_rtapi_realloc_cb(void *ctx, void *ptr, size_t size) {
    return rtapi_realloc(ptr, size);
}

static void stmak_rtapi_free_cb(void *ctx, void *ptr) {
    rtapi_free(ptr);
}

static int64_t stmak_rtapi_get_time_cb(void *ctx) STMAK_NONBLOCKING {
    return (int64_t)rtapi_get_time();
}

static void stmak_rtapi_delay_cb(void *ctx, long nsec) STMAK_NONBLOCKING {
    rtapi_delay(nsec);
}

static long stmak_rtapi_delay_max_cb(void *ctx) STMAK_NONBLOCKING {
    return rtapi_delay_max();
}

static int64_t stmak_rtapi_pll_get_reference_cb(void *ctx) STMAK_NONBLOCKING {
    return (int64_t)rtapi_task_pll_get_reference();
}

static int stmak_rtapi_pll_set_correction_cb(void *ctx, long value) STMAK_NONBLOCKING {
    return rtapi_task_pll_set_correction(value);
}

static int stmak_rtapi_task_self_cb(void *ctx) STMAK_NONBLOCKING {
    return rtapi_task_self();
}

// --- INI callbacks (forward-declared, implemented in Go via //export) ---
// ctx is uintptr_t, not void*: it carries a cgo.Handle integer, and receiving
// that in a Go unsafe.Pointer parameter puts a non-address value in a
// GC-scanned pointer slot ("invalid pointer found on stack" on a stack scan).

extern char* stmak_ini_get(uintptr_t ctx, char *section, char *key);
extern char** stmak_ini_get_all(uintptr_t ctx, char *section, char *key, int *out_count);
extern char* stmak_ini_source_file(uintptr_t ctx);

// --- Path resolution (forward-declared, implemented in Go) ---

extern char* stmak_path_resolve(uintptr_t ctx, char *name, int mode, char **err_out);

// --- Log subscribe/unsubscribe (forward-declared, implemented in Go) ---

extern stmak_log_sub_t* stmak_log_subscribe_cb(uintptr_t ctx, stmak_log_level_t min_level);
extern void stmak_log_unsubscribe_cb(uintptr_t ctx, stmak_log_sub_t *sub);

// --- Env initialisation helpers ---

static void stmak_log_init(stmak_log_t *log, stmak_log_ring_t *ring, void *ctx) {
    log->ring        = ring;
    log->subscribe   = (stmak_log_sub_t*(*)(void*,stmak_log_level_t))stmak_log_subscribe_cb;
    log->unsubscribe = (void(*)(void*,stmak_log_sub_t*))stmak_log_unsubscribe_cb;
    log->ctx         = ctx;
}

static void stmak_ini_init(stmak_ini_t *ini, void *ctx) {
    ini->ctx         = ctx;
    ini->get         = (const char*(*)(void*,const char*,const char*))stmak_ini_get;
    ini->get_all     = (const char**(*)(void*,const char*,const char*,int*))stmak_ini_get_all;
    ini->source_file = (const char*(*)(void*))stmak_ini_source_file;
}

static void stmak_path_init(stmak_path_t *path, void *ctx) {
    path->ctx     = ctx;
    path->resolve = (const char*(*)(void*,const char*,stmak_path_mode_t,const char**))stmak_path_resolve;
}

static void stmak_hal_init_struct(stmak_hal_t *hal) {
    hal->ctx          = NULL;
    hal->init         = stmak_hal_init_cb;
    hal->exit         = stmak_hal_exit_cb;
    hal->ready        = stmak_hal_ready_cb;
    hal->malloc       = stmak_hal_malloc_cb;
    hal->pin_new      = stmak_hal_pin_new_cb;
    hal->param_new    = stmak_hal_param_new_cb;
    hal->pin_alias    = stmak_hal_pin_alias_cb;
    hal->param_alias  = stmak_hal_param_alias_cb;
    hal->export_funct = stmak_hal_export_funct_cb;
}

static void stmak_rtapi_init_struct(stmak_rtapi_t *rtapi) {
    rtapi->ctx                = NULL;
    rtapi->calloc             = stmak_rtapi_calloc_cb;
    rtapi->realloc            = stmak_rtapi_realloc_cb;
    rtapi->free               = stmak_rtapi_free_cb;
    rtapi->get_time           = stmak_rtapi_get_time_cb;
    rtapi->delay              = stmak_rtapi_delay_cb;
    rtapi->delay_max          = stmak_rtapi_delay_max_cb;
    rtapi->pll_get_reference  = stmak_rtapi_pll_get_reference_cb;
    rtapi->pll_set_correction = stmak_rtapi_pll_set_correction_cb;
    rtapi->task_self          = stmak_rtapi_task_self_cb;
}

static void stmak_api_init_struct(stmak_api_t *api) {
    api->ctx              = NULL;
    api->register_api     = (int(*)(void*,const char*,int,const char*,const void*))stmak_api_register_cb;
    api->get_api          = (const void*(*)(void*,const char*,int,const char*))stmak_api_get_cb;
    api->push_watch       = (int(*)(void*,const char*,const char*,const char*,const void*,int))stmak_watch_push_cb;
    api->record_consumer  = (void(*)(void*,const char*,const char*,const char*))stmak_record_consumer_cb;
}

// log_ctx/ini_ctx are cgo.Handles (opaque integers) passed as uintptr_t rather
// than void* so the Go side never converts a uintptr to unsafe.Pointer (bad
// pointer arithmetic under -d=checkptr / -race); cast into the void* ctx fields
// via stmak_log_init/stmak_ini_init below.
static cmod_env_t *stmak_env_create(stmak_log_ring_t *ring, uintptr_t log_ctx,
                                   uintptr_t ini_ctx, void *dl_handle) {
    cmod_env_t *env = (cmod_env_t *)calloc(1, sizeof(cmod_env_t));
    if (!env) return NULL;

    stmak_log_t *log = (stmak_log_t *)calloc(1, sizeof(stmak_log_t));
    stmak_ini_t *ini = (stmak_ini_t *)calloc(1, sizeof(stmak_ini_t));
    stmak_path_t *path = (stmak_path_t *)calloc(1, sizeof(stmak_path_t));
    stmak_hal_t *hal = (stmak_hal_t *)calloc(1, sizeof(stmak_hal_t));
    stmak_rtapi_t *rtapi = (stmak_rtapi_t *)calloc(1, sizeof(stmak_rtapi_t));
    stmak_api_t *api = (stmak_api_t *)calloc(1, sizeof(stmak_api_t));

    if (!log || !ini || !path || !hal || !rtapi || !api) {
        free(log); free(ini); free(path); free(hal); free(rtapi); free(api); free(env);
        return NULL;
    }

    stmak_log_init(log, ring, (void *)log_ctx);
    stmak_ini_init(ini, (void *)ini_ctx);
    stmak_path_init(path, (void *)ini_ctx);
    stmak_hal_init_struct(hal);
    stmak_rtapi_init_struct(rtapi);
    stmak_api_init_struct(api);

    env->dl_handle = dl_handle;
    env->log       = log;
    env->ini       = ini;
    env->path      = path;
    env->hal       = hal;
    env->rtapi     = rtapi;
    env->api       = api;

    return env;
}

static void stmak_env_destroy(cmod_env_t *env) {
    if (!env) return;
    free((void *)env->log);
    free((void *)env->ini);
    free((void *)env->path);
    free((void *)env->hal);
    free((void *)env->rtapi);
    free((void *)env->api);
    free(env);
}

// cmod lifecycle call wrappers.
static int cmod_call_new(cmod_new_fn fn, const cmod_env_t *env,
                         const char *name, int argc, const char **argv,
                         cmod_t **out) {
    return fn(env, name, argc, argv, out);
}

static int cmod_call_init(cmod_t *m) {
    if (!m->Init) return 0;
    return m->Init(m);
}

static int cmod_call_start(cmod_t *m) {
    if (!m->Start) return 0;
    return m->Start(m);
}

static void cmod_call_stop(cmod_t *m) {
    if (!m->Stop) return;
    m->Stop(m);
}

static void cmod_call_destroy(cmod_t *m) {
    if (!m->Destroy) return;
    m->Destroy(m);
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/cgo"
	"strings"
	"syscall"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
	halcmd "github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// cModule holds a loaded C plugin module.
type cModule struct {
	handle  *C.void // dlopen handle
	mod     *C.cmod_t
	env     *C.cmod_env_t // stmak env (freed in destroyCModules)
	hCtx    cgo.Handle    // Go↔C handle for the Launcher pointer
	name    string
	started bool // true after Start() has been called
}

// cModuleSearchPath returns the directories a bare C module name is looked up
// in: the locally built modules first, then the ones the package ships.
//
// The order is presentational only — a name found in both is refused, not
// resolved (see resolveCModule) — but it is the order the diagnostics list.
func cModuleSearchPath() []string {
	var dirs []string
	if d := config.LocalCModDir(); d != "" {
		dirs = append(dirs, d)
	}
	if config.EMC2CmodDir != "" {
		dirs = append(dirs, config.EMC2CmodDir)
	}
	return dirs
}

// resolveCModule resolves a C module name or path to an absolute .so path.
// If the name contains a '/' it is treated as a path and used as-is.
// Otherwise the bare module name is looked up along cModuleSearchPath.
//
// found is false when no such .so exists anywhere on the search path, which is
// not an error: the caller then tries the name as a Go module.
//
// A name that exists in more than one directory IS an error. The alternative —
// letting the first directory win — means a locally built module silently
// shadows a shipped one of the same name, and a stale local .so masking a
// packaged fix is miserable to diagnose from the symptoms. Deliberate
// overriding is still available, by naming the path outright.
//
// The same reasoning covers a cmod whose name a compiled-in Go module also
// answers to: resolveCModule is consulted before the Go registry on every
// bare-name load path, so without the refusal the .so would win silently.
func resolveCModule(name string) (path string, found bool, err error) {
	if strings.Contains(name, "/") {
		return name, cModuleExists(name), nil
	}
	base := strings.TrimSuffix(name, ".so") + ".so"

	var hits []string
	for _, dir := range cModuleSearchPath() {
		p := filepath.Join(dir, base)
		if cModuleExists(p) {
			hits = append(hits, p)
		}
	}

	switch len(hits) {
	case 0:
		// The path is meaningless when found is false, but naming the first
		// search directory keeps "not found" messages concrete.
		if dirs := cModuleSearchPath(); len(dirs) > 0 {
			return filepath.Join(dirs[0], base), false, nil
		}
		return base, false, nil
	case 1:
		// A cmod may not shadow a Go module compiled into this server: both
		// load paths (boot and runtime) try the cmod resolution first, so the
		// .so would win and the compiled-in module would sit there unreached —
		// diagnosable only by knowing to look. Deliberate overriding stays
		// available, by naming the path outright.
		if stmak.HasModule(strings.TrimSuffix(name, ".so")) {
			return "", false, fmt.Errorf(
				"module %q is provided by both the C module %s and a Go module compiled into this server. "+
					"Remove the C module you did not mean, or load it deliberately by its full path",
				name, hits[0])
		}
		return hits[0], true, nil
	default:
		return "", false, fmt.Errorf(
			"the C module %q is provided by more than one directory: %s. "+
				"Remove the one you did not mean, or load the one you did by its full path",
			name, strings.Join(hits, " and "))
	}
}

// cModuleExists checks whether a .so file exists at the given path.
func cModuleExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkCModuleABI refuses a dlopened module whose cmod ABI is not this
// launcher's.
//
// The check has to happen here, between dlopen and the first call into the
// module: from New() onwards the module is reading cmod_env_t at whatever
// offsets its header described, and if those are not the offsets this binary
// wrote there is nothing left to detect — the symbols resolve, the call
// succeeds, and the module acts on the wrong memory.
//
// A missing symbol is a mismatch, not a pass. It means the module was compiled
// against a header from before the stamp existed, which is exactly the skew
// this is here to catch.
func checkCModuleABI(handle unsafe.Pointer, path string) error {
	symName := C.CString("cmod_abi_version")
	defer C.free(unsafe.Pointer(symName))

	// dlerror() is sticky; clear it so the lookup's own result is read.
	C.dlerror()
	sym := C.dlsym(handle, symName)
	if sym == nil {
		return fmt.Errorf(
			"load C plugin %q: no cmod ABI version; it was built against stmak headers "+
				"older than this server (expected ABI %d). Rebuild it with modcompile",
			path, C.CMOD_ABI_VERSION)
	}

	got := uint32(*(*C.uint)(sym))
	if got != uint32(C.CMOD_ABI_VERSION) {
		return fmt.Errorf(
			"load C plugin %q: cmod ABI %d, but this server provides %d. "+
				"The two were built from different stmak sources; rebuild whichever is older",
			path, got, C.CMOD_ABI_VERSION)
	}
	return nil
}

// moduleLogHint is appended to a module load/init/start failure. A cmod reports
// why it refused through its own logging, which reaches the server log with the
// module's name on it; the launcher only ever sees the int it returned. Rather
// than reconstruct the reason from the log stream and risk attributing an
// unrelated message, point the operator at where the real one already is.
const moduleLogHint = " (the module logs the reason it refused; see the server log)"

// loadCPlugin loads a C plugin .so via dlopen, looks up the "New" symbol,
// builds the cmod_env_t with stmak sub-API callbacks, calls the factory, and
// appends the module to l.cModules.
//
// The factory is expected to create and fully initialize the module (including
// HAL component/pin creation) before returning.
func (l *Launcher) loadCPlugin(path string, name string, args []string) error {
	l.logger.Info("loading C plugin", "path", path, "name", name)

	// Ensure the shared log ring is created and draining.
	l.ensureLogRing()

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	handle := C.dlopen(cpath, C.RTLD_NOW)
	if handle == nil {
		return fmt.Errorf("load C plugin %q: dlopen: %s", path, C.GoString(C.dlerror()))
	}

	if err := checkCModuleABI(handle, path); err != nil {
		C.dlclose(handle)
		return err
	}

	symName := C.CString("New")
	defer C.free(unsafe.Pointer(symName))

	sym := C.dlsym(handle, symName)
	if sym == nil {
		C.dlclose(handle)
		return fmt.Errorf("load C plugin %q: missing \"New\" symbol: %s", path, C.GoString(C.dlerror()))
	}

	factory := C.cmod_new_fn(sym)

	// Build the stmak environment with all sub-API callbacks.
	cm := &cModule{
		handle: (*C.void)(handle),
		name:   name,
	}

	// Pass the handle as an integer (C.uintptr_t), not unsafe.Pointer(uintptr(hCtx)):
	// a cgo.Handle is a uintptr and uintptr->unsafe.Pointer is bad pointer
	// arithmetic under -d=checkptr (enabled by -race).
	hCtx := cgo.NewHandle(l)
	cCtx := C.uintptr_t(hCtx)
	env := C.stmak_env_create(l.logRing.ring, cCtx, cCtx, handle)
	if env == nil {
		hCtx.Delete()
		C.dlclose(handle)
		return fmt.Errorf("load C plugin %q: failed to allocate cmod_env_t", path)
	}
	cm.env = env

	// Convert args to C strings.  Keep them alive for the full module
	// lifecycle so C code can hold pointers into name/argv safely.
	// Append arena strings under arenaMu (short critical section, released
	// before the cgo cmod_call_new below, whose stmak_ini_get* callbacks re-take
	// arenaMu on this same goroutine — see the arenaMu contract on Launcher).
	cName := C.CString(name)
	l.arenaAppend(unsafe.Pointer(cName))

	argc := C.int(len(args))
	var argv **C.char
	if len(args) > 0 {
		cargs := make([]*C.char, len(args))
		for i, a := range args {
			cargs[i] = C.CString(a)
			l.arenaAppend(unsafe.Pointer(cargs[i]))
		}
		argv = &cargs[0]
	}

	var mod *C.cmod_t
	rc := C.cmod_call_new(factory, env, cName, argc, argv, &mod)
	if rc != 0 {
		hCtx.Delete()
		C.stmak_env_destroy(env)
		C.dlclose(handle)
		return fmt.Errorf("load C plugin %q: factory returned error code %d"+moduleLogHint,
			path, int(rc))
	}
	cm.mod = mod

	cm.hCtx = hCtx
	l.cModules = append(l.cModules, cm)
	l.logger.Debug("C plugin loaded and initialized", "path", path, "name", name)

	return nil
}

// runtimeLoadModule loads a cmod plugin at runtime (called from the halcmd
// "load" REST endpoint).  It resolves the path, calls loadCPlugin, Init,
// and Start so the module is fully operational when the call returns.
func (l *Launcher) runtimeLoadModule(module string, args []string) error {
	return l.loadModuleNamed(module, "", args)
}

// loadModuleNamed loads a single module (cmod plugin or Go module) at runtime
// and immediately runs its Init and Start phases so it is fully operational
// on return.  This is the sequential load path used by both the REST "load"
// command (via runtimeLoadModule) and the one-shot halrun executor.
//
// instanceName overrides the instance/component name; pass "" to derive the
// default (the module basename).  The explicit-name form supports HAL-file
// syntax like "load abs <abs.0>", where the instance must be named "abs.0".
func (l *Launcher) loadModuleNamed(module, instanceName string, args []string) error {
	// Serialize the whole load against concurrent REST load/unload and against
	// shutdown (modMu is held across the cgo cmod_call_* calls — safe, no
	// //export callback takes modMu). This also makes the cModules[len-1] /
	// goModules[len-1] reads below correct under concurrency.
	l.modMu.Lock()
	defer l.modMu.Unlock()
	if l.shuttingDown {
		return fmt.Errorf("cannot load %q: shutting down: %w", module, syscall.ESHUTDOWN)
	}

	path, found, err := resolveCModule(module)
	if err != nil {
		return err
	}
	if !found {
		// Try as a Go module — load and start immediately.
		name := instanceName
		if name == "" {
			name = module
		}
		if err := l.loadGoModule(module, name, args); err != nil {
			return err
		}
		gm := l.goModules[len(l.goModules)-1]
		return gm.mod.Start()
	}

	// Use the module basename (without .so) as the instance name when not
	// explicitly provided.
	name := instanceName
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".so")
	}

	if err := l.loadCPlugin(path, name, args); err != nil {
		return err
	}

	// The newly loaded module is the last one appended.
	cm := l.cModules[len(l.cModules)-1]

	// Init phase — look up other modules' APIs.
	rc := C.cmod_call_init(cm.mod)
	if rc != 0 {
		return fmt.Errorf("init of C module %q returned error code %d"+moduleLogHint,
			name, int(rc))
	}

	// Start phase — begin operation.
	rc = C.cmod_call_start(cm.mod)
	if rc != 0 {
		return fmt.Errorf("start of C module %q returned error code %d"+moduleLogHint,
			name, int(rc))
	}
	cm.started = true

	l.logger.Info("runtime-loaded C module", "name", name, "path", path)
	return nil
}

// initCModules calls Init() on all loaded C plugin modules in load order.
// Init() runs after all modules' New() have completed (all APIs registered)
// but before HAL wiring commands and Start().  Modules use Init() to look up
// other modules' APIs and perform cross-module initialization.
func (l *Launcher) initCModules() error {
	for _, cm := range l.cModules {
		rc := C.cmod_call_init(cm.mod)
		if rc != 0 {
			return fmt.Errorf("init of C module %q returned error code %d"+moduleLogHint,
				cm.name, int(rc))
		}
	}
	return nil
}

// startCModules calls Start() on all loaded C plugin modules that have not
// already been started.
func (l *Launcher) startCModules() error {
	for _, cm := range l.cModules {
		if cm.started {
			continue
		}
		rc := C.cmod_call_start(cm.mod)
		if rc != 0 {
			return fmt.Errorf("start of C module %q returned error code %d"+moduleLogHint,
				cm.name, int(rc))
		}
		cm.started = true
	}
	return nil
}

// lockRTModules locks the PT_LOAD segments of all module .so files that
// registered at least one STMAK_HAL_COMP_REALTIME component.  The set of
// handles is maintained by stmak_hal_init_cb (C side) and covers both
// cmod and gomod .so files.
func (l *Launcher) lockRTModules() {
	n := int(C.rt_dl_handles_len())
	for i := 0; i < n; i++ {
		halcmd.LockDLHandle(C.rt_dl_handles_get(C.int(i)))
	}
}

// unlockRTModules unlocks the PT_LOAD segments locked by lockRTModules.
func (l *Launcher) unlockRTModules() {
	n := int(C.rt_dl_handles_len())
	for i := 0; i < n; i++ {
		halcmd.UnlockDLHandle(C.rt_dl_handles_get(C.int(i)))
	}
}

// stopCModules calls Stop() on all loaded C plugin modules in reverse order.
func (l *Launcher) stopCModules() {
	l.modMu.Lock()
	snapshot := l.cModules
	l.modMu.Unlock()
	for i := len(snapshot) - 1; i >= 0; i-- {
		cm := snapshot[i]
		// Only stop modules that were actually started. On a partial-startup
		// failure (startCModules returns mid-loop) later modules are loaded but
		// never started; calling stop on them violates the plugin's
		// start-before-stop contract and can crash. Mirrors unload.go's guard
		// and startCModules's own started check.
		if !cm.started {
			continue
		}
		C.cmod_call_stop(cm.mod)
	}
}

// arenaAppend records a C allocation in cModArena for later free in
// destroyCModules. It takes arenaMu only for the append and MUST NOT be called
// while holding arenaMu across a cgo call (see the arenaMu contract on
// Launcher). Safe to call re-entrantly from the stmak_ini_get* //export
// callbacks during a load.
func (l *Launcher) arenaAppend(p unsafe.Pointer) {
	l.arenaMu.Lock()
	l.cModArena = append(l.cModArena, p)
	l.arenaMu.Unlock()
}

// destroyCModules calls Destroy() on all loaded C plugin modules in reverse
// order, unlocks and closes the dlopen handles, and frees all arena-tracked
// strings and stmak env structs.  The log ring is NOT destroyed here — it
// remains active so that later cleanup steps (destroyGoModules, UnloadAll)
// can still emit log messages.  See doCleanup() for ring teardown.
func (l *Launcher) destroyCModules() {
	l.unlockRTModules()
	// Snapshot and clear the live slice under modMu so a straggler REST unload
	// (should already be blocked by shuttingDown) can never double-destroy.
	l.modMu.Lock()
	snapshot := l.cModules
	l.cModules = nil
	l.modMu.Unlock()
	for i := len(snapshot) - 1; i >= 0; i-- {
		cm := snapshot[i]
		C.cmod_call_destroy(cm.mod)
		if cm.env != nil {
			C.stmak_env_destroy(cm.env)
			cm.env = nil
		}
		if cm.handle != nil {
			C.dlclose(unsafe.Pointer(cm.handle))
		}
		cm.hCtx.Delete()
	}
	// Free arena strings after all modules have been destroyed.
	l.arenaMu.Lock()
	for _, p := range l.cModArena {
		C.free(p)
	}
	l.cModArena = nil
	l.arenaMu.Unlock()
	// Free the RT handle tracking array.
	C.rt_dl_handles_free()
}

// cmodStop calls Stop() on a single cmod.
func cmodStop(cm *cModule) {
	C.cmod_call_stop(cm.mod)
}

// cmodDestroy calls Destroy() on a single cmod.
func cmodDestroy(cm *cModule) {
	C.cmod_call_destroy(cm.mod)
}

// cmodDestroyEnv frees the stmak env struct.
func cmodDestroyEnv(cm *cModule) {
	if cm.env != nil {
		C.stmak_env_destroy(cm.env)
		cm.env = nil
	}
}

// cmodDlclose closes the dlopen handle.
func cmodDlclose(cm *cModule) {
	if cm.handle != nil {
		C.rt_dl_handles_remove(unsafe.Pointer(cm.handle))
		C.dlclose(unsafe.Pointer(cm.handle))
	}
}
