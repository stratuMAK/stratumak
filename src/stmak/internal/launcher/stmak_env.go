// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package launcher — stmak_env.go provides the Go-side log ring drain loop
// and INI callback implementations for the stratuMAK C plugin environment.
//
// All C helper functions (pass-through HAL/RTAPI callbacks, env allocation,
// ring buffer read) live in the cgo preamble of cmodules.go.  This file
// contains only pure Go code.
package launcher

/*
#include "../../pkg/cmodule/stmak_log.h"
#include <string.h>

// stmak_sub_ring_write writes one message to a subscriber's ring.
// Returns 0 on success, -1 if the ring is full (message dropped).
static int stmak_sub_ring_write(stmak_log_ring_t *ring,
                               uint32_t level, int64_t ts,
                               const char *component, const char *msg) {
    uint32_t pos = __atomic_fetch_add(&ring->write_pos, 1, __ATOMIC_RELAXED);
    uint32_t idx = pos & STMAK_LOG_RING_MASK;
    stmak_log_slot_t *slot = &ring->slots[idx];

    uint32_t expected = 0;
    if (!__atomic_compare_exchange_n(&slot->seq, &expected, 1, 0,
                                     __ATOMIC_ACQUIRE, __ATOMIC_RELAXED)) {
        return -1;  // ring full
    }

    slot->level = level;
    slot->timestamp_ns = ts;
    memcpy(slot->component, component, STMAK_LOG_COMPONENT_LEN);
    memcpy(slot->msg, msg, STMAK_LOG_MSG_LEN);

    __atomic_store_n(&slot->seq, pos + 1, __ATOMIC_RELEASE);
    return 0;
}
*/
import "C"

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/cgo"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// stmakLogRing wraps a C-allocated stmak_log_ring_t and provides the Go-side
// drain loop that forwards log entries to the slog logger.
type stmakLogRing struct {
	ring    *C.stmak_log_ring_t
	readPos uint32
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// Subscriber fan-out: drain loop copies messages to per-subscriber rings.
	subsMu sync.Mutex
	subs   []*C.stmak_log_sub_t
}

// newStmakLogRing allocates and returns a new log ring.
func newStmakLogRing() *stmakLogRing {
	return &stmakLogRing{
		ring: C.stmak_ring_create(),
	}
}

// startDrain starts a goroutine that continuously drains log entries from the
// ring buffer and forwards them to the given slog.Logger.
func (r *stmakLogRing) startDrain(logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.drainLoop(ctx, logger)
}

// stopDrain signals the drain goroutine to stop and waits for it to finish.
// Performs a final drain to flush any remaining messages.
func (r *stmakLogRing) stopDrain(logger *slog.Logger) {
	if r.cancel != nil {
		r.cancel()
		r.wg.Wait()
	}
	// Final flush — drain any messages that arrived after cancel.
	r.drainAll(logger)
}

// destroy frees the C-allocated ring buffer.
func (r *stmakLogRing) destroy() {
	if r.ring != nil {
		C.stmak_ring_destroy(r.ring)
		r.ring = nil
	}
}

func (r *stmakLogRing) drainLoop(ctx context.Context, logger *slog.Logger) {
	defer r.wg.Done()
	for {
		n := r.drainAll(logger)
		if n == 0 {
			// No messages — check if we should exit.
			select {
			case <-ctx.Done():
				return
			default:
				// Brief sleep to avoid busy-spinning.  1ms is fast enough
				// for log display but gentle on CPU.
				time.Sleep(1 * time.Millisecond)
			}
		}
	}
}

func (r *stmakLogRing) drainAll(logger *slog.Logger) int {
	var (
		level C.uint32_t
		ts    C.int64_t
	)
	compBuf := make([]byte, C.STMAK_LOG_COMPONENT_LEN)
	msgBuf := make([]byte, C.STMAK_LOG_MSG_LEN)

	count := 0
	for {
		ok := C.stmak_ring_try_read(
			r.ring, C.uint32_t(r.readPos),
			&level, &ts,
			(*C.char)(unsafe.Pointer(&compBuf[0])),
			(*C.char)(unsafe.Pointer(&msgBuf[0])),
		)
		if ok == 0 {
			break
		}
		r.readPos++
		count++

		// Fan-out to subscribers whose level filter matches.
		r.fanOut(level, ts, compBuf, msgBuf)

		component := cStringFromBytes(compBuf)
		msg := cStringFromBytes(msgBuf)
		tsNano := int64(ts)

		// Notify Go-level error hooks (e.g. milltask operator messages).
		if int(level) >= 3 { // STMAK_LOG_ERROR
			stmak.NotifyLogError(component, msg)
		}

		logLevel := slog.LevelInfo
		switch int(level) {
		case 0: // STMAK_LOG_DEBUG
			logLevel = slog.LevelDebug
		case 1: // STMAK_LOG_INFO
			logLevel = slog.LevelInfo
		case 2: // STMAK_LOG_WARN
			logLevel = slog.LevelWarn
		case 3: // STMAK_LOG_ERROR
			logLevel = slog.LevelError
		}

		// Convert C-side wall clock timestamp to Go time.
		logTime := time.Unix(0, tsNano)
		if !logger.Handler().Enabled(context.Background(), logLevel) {
			continue
		}
		record := slog.NewRecord(logTime, logLevel, msg, 0)
		record.AddAttrs(slog.String("component", component))
		_ = logger.Handler().Handle(context.Background(), record)
	}
	return count
}

// fanOut copies a log message to all subscriber rings whose level filter matches.
func (r *stmakLogRing) fanOut(level C.uint32_t, ts C.int64_t, comp, msg []byte) {
	r.subsMu.Lock()
	defer r.subsMu.Unlock()

	for _, sub := range r.subs {
		if level < sub.min_level {
			continue
		}
		C.stmak_sub_ring_write(sub.ring,
			level, ts,
			(*C.char)(unsafe.Pointer(&comp[0])),
			(*C.char)(unsafe.Pointer(&msg[0])))
	}
}

// subscribe creates a new subscription with a per-subscriber ring buffer.
func (r *stmakLogRing) subscribe(minLevel C.stmak_log_level_t) *C.stmak_log_sub_t {
	sub := (*C.stmak_log_sub_t)(C.calloc(1, C.size_t(unsafe.Sizeof(C.stmak_log_sub_t{}))))
	if sub == nil {
		return nil
	}
	sub.ring = C.stmak_ring_create()
	if sub.ring == nil {
		C.free(unsafe.Pointer(sub))
		return nil
	}
	sub.read_pos = 0
	sub.min_level = C.uint32_t(minLevel)

	r.subsMu.Lock()
	r.subs = append(r.subs, sub)
	r.subsMu.Unlock()
	return sub
}

// unsubscribe removes a subscription and frees its resources.
func (r *stmakLogRing) unsubscribe(sub *C.stmak_log_sub_t) {
	r.subsMu.Lock()
	for i, s := range r.subs {
		if s == sub {
			r.subs = append(r.subs[:i], r.subs[i+1:]...)
			break
		}
	}
	r.subsMu.Unlock()

	if sub.ring != nil {
		C.stmak_ring_destroy(sub.ring)
	}
	C.free(unsafe.Pointer(sub))
}

// --- Log subscribe/unsubscribe callbacks (exported to C) ---

//export stmak_log_subscribe_cb
func stmak_log_subscribe_cb(ctx C.uintptr_t, minLevel C.stmak_log_level_t) *C.stmak_log_sub_t {
	l := cgo.Handle(ctx).Value().(*Launcher)
	return l.logRing.subscribe(minLevel)
}

//export stmak_log_unsubscribe_cb
func stmak_log_unsubscribe_cb(ctx C.uintptr_t, sub *C.stmak_log_sub_t) {
	l := cgo.Handle(ctx).Value().(*Launcher)
	l.logRing.unsubscribe(sub)
}

// cStringFromBytes extracts a C string from a byte slice (up to first NUL).
func cStringFromBytes(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// --- INI callback implementations (exported to C) ---

// The launcher runs without an INI file in halrun mode (`halrun -f file.hal`
// never sets l.ini), and pkg/inifile's methods dereference the receiver
// immediately — so l.ini must be checked before it is touched.  These are
// //export'ed callbacks: a panic here unwinds into a C caller and kills the
// process.  The nil check lives in the three iniX helpers below (also unit
// testable, which the cgo callbacks are not — cgo is not allowed in _test.go).
// No-INI is reported as "key not found" (NULL / count 0), which the documented
// stmak_ini.h contract and the stmak_ini_get_* helpers already handle.

// iniGet returns the INI value for section/key and whether it is present.
// A launcher with no INI reports "not present" for every key.
func (l *Launcher) iniGet(section, key string) (string, bool) {
	if l.ini == nil {
		return "", false
	}
	val := l.ini.Get(section, key)
	return val, val != ""
}

// iniGetAll returns all INI values for section/key; nil when there is no INI.
func (l *Launcher) iniGetAll(section, key string) []string {
	if l.ini == nil {
		return nil
	}
	return l.ini.GetAll(section, key)
}

// iniSourceFile returns the INI file path, or "" when there is no INI.
func (l *Launcher) iniSourceFile() string {
	if l.ini == nil {
		return ""
	}
	return l.ini.SourceFile()
}

//export stmak_ini_get
func stmak_ini_get(ctx C.uintptr_t, section, key *C.char) *C.char {
	l := cgo.Handle(ctx).Value().(*Launcher)
	val, ok := l.iniGet(C.GoString(section), C.GoString(key))
	if !ok {
		return nil
	}
	cs := C.CString(val)
	l.arenaAppend(unsafe.Pointer(cs))
	return cs
}

//export stmak_ini_source_file
func stmak_ini_source_file(ctx C.uintptr_t) *C.char {
	l := cgo.Handle(ctx).Value().(*Launcher)
	// Unlike get/get_all, this one keeps its "always a valid string" contract
	// and reports no-INI as "" — C callers may strlen/strcpy the result.
	cs := C.CString(l.iniSourceFile())
	l.arenaAppend(unsafe.Pointer(cs))
	return cs
}

//export stmak_ini_get_all
func stmak_ini_get_all(ctx C.uintptr_t, section, key *C.char, outCount *C.int) **C.char {
	l := cgo.Handle(ctx).Value().(*Launcher)
	vals := l.iniGetAll(C.GoString(section), C.GoString(key))
	n := len(vals)
	*outCount = C.int(n)
	if n == 0 {
		return nil
	}

	// Arena-allocate the pointer array (n+1 entries, NULL-terminated)
	// and each string.  All freed in destroyCModules via cModArena.
	ptrSize := unsafe.Sizeof((*C.char)(nil))
	arr := (**C.char)(C.malloc(C.size_t(uintptr(n+1) * ptrSize)))
	l.arenaAppend(unsafe.Pointer(arr))

	for i, v := range vals {
		cs := C.CString(v)
		l.arenaAppend(unsafe.Pointer(cs))
		*(**C.char)(unsafe.Add(unsafe.Pointer(arr), uintptr(i)*ptrSize)) = cs
	}
	// NULL terminator
	*(**C.char)(unsafe.Add(unsafe.Pointer(arr), uintptr(n)*ptrSize)) = nil

	return arr
}

// --- API registry callback implementations (exported to C) ---

//export stmak_api_register_cb
func stmak_api_register_cb(ctx unsafe.Pointer, apiName *C.char, version C.int,
	instanceName *C.char, callbacks unsafe.Pointer) C.int {

	reg := apiserver.DefaultRegistry()
	if reg == nil {
		slog.Error("register_api: no default registry")
		return -C.int(syscall.EINVAL)
	}

	name := C.GoString(apiName)
	ver := int(version)
	instance := C.GoString(instanceName)

	err := reg.Register(name, ver, instance, callbacks)
	if err != nil {
		slog.Error("register_api: registration failed",
			"api", name, "instance", instance, "error", err)
		switch err {
		case syscall.EEXIST:
			return -C.int(syscall.EEXIST)
		case syscall.EINVAL:
			return -C.int(syscall.EINVAL)
		default:
			return -1
		}
	}

	// If a watch factory exists for this API, create and register the WatchAPI.
	if factory := apiserver.GetWatchFactory(name); factory != nil {
		watchReg := apiserver.DefaultWatchRegistry()
		if watchReg == nil {
			watchReg = apiserver.NewWatchRegistry()
			apiserver.SetDefaultWatchRegistry(watchReg)
		}
		watchReg.Register(factory(instance, callbacks))
	}

	// If a stream server factory exists, create and register the stream endpoint.
	if factory := apiserver.GetStreamFactory(name); factory != nil {
		if srv := apiserver.DefaultServer(); srv != nil {
			srv.RegisterStream(name, instance, factory(instance, callbacks))
		}
	}

	return 0
}

//export stmak_api_get_cb
func stmak_api_get_cb(ctx unsafe.Pointer, apiName *C.char, version C.int,
	instanceName *C.char) unsafe.Pointer {

	reg := apiserver.DefaultRegistry()
	if reg == nil {
		slog.Error("get_api: no default registry")
		return nil
	}

	name := C.GoString(apiName)
	instance := C.GoString(instanceName)
	ver := int(version)

	cbs, err := reg.GetAPIUntracked(name, instance, ver)
	if err != nil {
		slog.Error("get_api: lookup failed",
			"api", name, "instance", instance, "version", ver, "error", err)
		return nil
	}
	return cbs
}

//export stmak_record_consumer_cb
func stmak_record_consumer_cb(ctx unsafe.Pointer, consumerInstance *C.char,
	apiName *C.char, providerInstance *C.char) {

	reg := apiserver.DefaultRegistry()
	if reg == nil {
		return
	}
	reg.RecordConsumer(C.GoString(consumerInstance), C.GoString(apiName), C.GoString(providerInstance))
}

//export stmak_watch_push_cb
func stmak_watch_push_cb(ctx unsafe.Pointer, apiName *C.char, instanceName *C.char,
	funcName *C.char, data unsafe.Pointer, dataLen C.int) C.int {

	name := C.GoString(apiName)
	instance := C.GoString(instanceName)
	fn := C.GoString(funcName)

	pw := apiserver.GetOrCreatePushWatch(name, instance, fn)
	if pw == nil {
		return -C.int(syscall.EINVAL)
	}

	if err := pw.Push(data, int(dataLen)); err != nil {
		slog.Error("push_watch: conversion failed",
			"api", name, "instance", instance, "func", fn, "error", err)
		return -1
	}
	return 0
}

// --- Path resolution callback (exported to C) ---

// pathMode maps the C stmak_path_mode_t values onto pathres.Mode.  An unknown
// value is rejected rather than silently treated as a read.
func pathMode(mode C.int) (pathres.Mode, bool) {
	switch mode {
	case 0:
		return pathres.Read, true
	case 1:
		return pathres.Write, true
	case 2:
		return pathres.Dir, true
	}
	return 0, false
}

// resolveConfigPath is the Go half of env->path->resolve().  It is split out
// so it can be unit tested; cgo is not allowed in _test.go.
//
// Returned strings are arena-allocated by the caller, so they live until the
// module is destroyed, matching the stmak_path.h contract.
func (l *Launcher) resolveConfigPath(name string, mode C.int) (string, error) {
	m, ok := pathMode(mode)
	if !ok {
		return "", fmt.Errorf("path resolver: unknown access mode %d", int(mode))
	}
	return pathres.Resolve(name, m)
}

//export stmak_path_resolve
func stmak_path_resolve(ctx C.uintptr_t, name *C.char, mode C.int, errOut **C.char) *C.char {
	l := cgo.Handle(ctx).Value().(*Launcher)

	resolved, err := l.resolveConfigPath(C.GoString(name), mode)
	if err != nil {
		// The caller logs with its own component name; hand back the reason so
		// "not found" and "outside the allowed directories" stay
		// distinguishable in the module's message.
		if errOut != nil {
			cs := C.CString(err.Error())
			l.arenaAppend(unsafe.Pointer(cs))
			*errOut = cs
		}
		return nil
	}
	if errOut != nil {
		*errOut = nil
	}
	cs := C.CString(resolved)
	l.arenaAppend(unsafe.Pointer(cs))
	return cs
}
