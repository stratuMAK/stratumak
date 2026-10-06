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
#include <stdlib.h>
#include <string.h>

// stmak_ring_dropped copies the producers' per-severity drop counters.
static void stmak_ring_dropped(stmak_log_ring_t *ring, uint32_t *out) {
    for (int i = 0; i < STMAK_LOG_NUM_SEVERITIES; i++) {
        out[i] = __atomic_load_n(&ring->dropped[i], __ATOMIC_RELAXED);
    }
}

// stmak_ring_fill is how many positions the producers are ahead of the
// consumer.  For tests.
static uint32_t stmak_ring_fill(stmak_log_ring_t *ring) {
    return __atomic_load_n(&ring->write_pos, __ATOMIC_RELAXED) -
           __atomic_load_n(&ring->read_pos, __ATOMIC_RELAXED);
}

// stmak_ring_emit_str pushes one message the way a C module does -- the
// test's producer, since a Go test file cannot call C.  Returns 0 when the
// message was enqueued, 1 when the ring's floor filtered it, -1 when it was
// dropped.
static int stmak_ring_emit_str(stmak_log_ring_t *ring, uint32_t level,
                               const char *component, const char *msg) {
    stmak_log_t log = { .ring = ring };
    if (!stmak_ring_wants(ring, level)) return 1;
    uint32_t *d = &ring->dropped[stmak_log_drop_index(level)];
    uint32_t before = __atomic_load_n(d, __ATOMIC_RELAXED);
    stmak_logf(&log, component, level, "%s", msg);
    return __atomic_load_n(d, __ATOMIC_RELAXED) == before ? 0 : -1;
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
	ring *C.stmak_log_ring_t
	// drainMu serialises drainAll. The drain goroutine is not the only
	// caller: the launcher flushes synchronously on a module load failure
	// (drainLogRingNow) so the module's own explanation lands next to the
	// error. Without this the two race on the ring's read position, and two
	// readers at the same position deliver one message twice and skip the
	// next.
	drainMu sync.Mutex
	// batch is where a drain copies messages out of the ring before it
	// formats and writes any of them, so the slots are back with the
	// producers at memcpy speed however slow the sink is.
	batch []C.stmak_log_slot_t
	// dropped mirrors the ring's producer-side counters as of the last
	// drain, so a change can be reported once rather than the count
	// re-logged.
	dropped [C.STMAK_LOG_NUM_SEVERITIES]uint32
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// Subscriber fan-out: drain loop copies messages to per-subscriber rings.
	subsMu sync.Mutex
	subs   []*C.stmak_log_sub_t
}

// drainBatch is how many messages one C call copies out of the ring.  256
// slots are 67 KB of Go memory, allocated once.
const drainBatch = 256

// newStmakLogRing allocates and returns a new log ring.
func newStmakLogRing() *stmakLogRing {
	return &stmakLogRing{
		ring:  C.stmak_ring_create(),
		batch: make([]C.stmak_log_slot_t, drainBatch),
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
		// The floor follows the sinks: halcmd's log level can change at
		// runtime, and a subscriber can come and go.  Four Enabled calls
		// per millisecond is nothing.
		r.setMinLevel(logger)
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
	r.drainMu.Lock()
	defer r.drainMu.Unlock()

	count := 0
	for {
		n := int(C.stmak_ring_read_batch(r.ring, &r.batch[0], drainBatch))
		if n == 0 {
			break
		}
		count += n
		for i := 0; i < n; i++ {
			r.deliver(logger, &r.batch[i])
		}
	}

	// Producers drop silently when the ring is full; say so here, once per
	// batch and by severity, so a burst that outran this loop is at least
	// visible -- and so a lost DEBUG line and a lost ERROR are not the same
	// number.  The latter is a bug in the ring's sizing, and reported as one.
	var now [C.STMAK_LOG_NUM_SEVERITIES]uint32
	C.stmak_ring_dropped(r.ring, (*C.uint32_t)(unsafe.Pointer(&now[0])))
	if now != r.dropped {
		var delta [C.STMAK_LOG_NUM_SEVERITIES]uint32
		total := uint32(0)
		for i := range now {
			delta[i] = now[i] - r.dropped[i]
			total += now[i]
		}
		level := slog.LevelWarn
		if delta[2] != 0 || delta[3] != 0 {
			level = slog.LevelError
		}
		logger.Log(context.Background(), level,
			"log ring full: C-module log messages dropped",
			"debug", delta[0], "info", delta[1], "warn", delta[2], "error", delta[3],
			"total", total)
		r.dropped = now
	}
	return count
}

// deliver hands one message copied out of the ring to the subscribers, the
// operator channel and the logger.
func (r *stmakLogRing) deliver(logger *slog.Logger, m *C.stmak_log_slot_t) {
	r.fanOut(m)

	level := uint32(m.level)
	component := C.GoString(&m.component[0])
	msg := C.GoString(&m.msg[0])

	// The level word carries a severity in its low bits and flags above
	// them, so everything here masks before comparing (stmak_log.h).
	severity := int(level) & logLevelMask

	// Operator messages are marked, not inferred from severity: audience
	// and severity are independent axes.
	if int(level)&logOperFlag != 0 {
		stmak.NotifyOperatorMessage(component, msg, severity)
	}

	logLevel := slogLevel(severity)
	if !logger.Handler().Enabled(context.Background(), logLevel) {
		return
	}
	// Convert C-side wall clock timestamp to Go time.
	record := slog.NewRecord(time.Unix(0, int64(m.timestamp_ns)), logLevel, msg, 0)
	record.AddAttrs(slog.String("component", component))
	_ = logger.Handler().Handle(context.Background(), record)
}

// slogLevel maps a ring severity onto slog's scale.
func slogLevel(severity int) slog.Level {
	switch severity {
	case 0: // STMAK_LOG_DEBUG
		return slog.LevelDebug
	case 2: // STMAK_LOG_WARN
		return slog.LevelWarn
	case 3: // STMAK_LOG_ERROR
		return slog.LevelError
	}
	return slog.LevelInfo // STMAK_LOG_INFO
}

// setMinLevel tells the producers the lowest severity anyone would see, so
// they do not spend ring slots on the rest: the logger's level, or a
// subscriber's if lower.  Operator messages bypass the floor in the ring
// itself.
func (r *stmakLogRing) setMinLevel(logger *slog.Logger) {
	min := uint32(C.STMAK_LOG_NUM_SEVERITIES) // nothing printed
	for sev := 0; sev < int(C.STMAK_LOG_NUM_SEVERITIES); sev++ {
		if logger.Handler().Enabled(context.Background(), slogLevel(sev)) {
			min = uint32(sev)
			break
		}
	}
	r.subsMu.Lock()
	for _, sub := range r.subs {
		if uint32(sub.min_level) < min {
			min = uint32(sub.min_level)
		}
	}
	r.subsMu.Unlock()
	C.stmak_ring_set_min_level(r.ring, C.uint32_t(min))
}

// emit pushes one message into the ring as a C module would.  For tests.
// Returns 1 when it was enqueued, 0 when the ring's floor filtered it, -1
// when it was dropped.
func (r *stmakLogRing) emit(level uint32, component, msg string) int {
	cc, cm := C.CString(component), C.CString(msg)
	defer C.free(unsafe.Pointer(cc))
	defer C.free(unsafe.Pointer(cm))
	switch C.stmak_ring_emit_str(r.ring, C.uint32_t(level), cc, cm) {
	case 0:
		return 1
	case 1:
		return 0
	}
	return -1
}

// size is sizeof(stmak_log_ring_t) as this package compiled it, for the
// cross-check in halcmd.SetLogRing.
func (r *stmakLogRing) size() uintptr {
	return uintptr(C.sizeof_stmak_log_ring_t)
}

// subPollMsg reads one message from a subscription's ring, "" when there is
// none.  For tests.
func subPollMsg(sub *C.stmak_log_sub_t) string {
	var m C.stmak_log_slot_t
	if C.stmak_ring_read_batch(sub.ring, &m, 1) == 0 {
		return ""
	}
	return C.GoString(&m.msg[0])
}

// fill is how far the producers are ahead of the consumer.  For tests.
func (r *stmakLogRing) fill() int {
	return int(C.stmak_ring_fill(r.ring))
}

// The level word's layout, mirroring stmak_log.h: severity in the low bits,
// flags above.
const (
	logLevelMask = 0x0f
	logOperFlag  = 0x10
)

// The ring's capacity and headroom mark, for the test (a _test.go file
// cannot see C).
const (
	C_STMAK_LOG_RING_SIZE        = C.STMAK_LOG_RING_SIZE
	C_STMAK_LOG_RING_HEADROOM_AT = C.STMAK_LOG_RING_HEADROOM_AT
)

// fanOut copies a log message to all subscriber rings.  Each ring carries
// its subscriber's level as its floor, so the filter is the push's own.
func (r *stmakLogRing) fanOut(m *C.stmak_log_slot_t) {
	r.subsMu.Lock()
	defer r.subsMu.Unlock()

	for _, sub := range r.subs {
		if C.stmak_ring_wants(sub.ring, m.level) == 0 {
			continue
		}
		C.stmak_ring_push(sub.ring, m)
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
	sub.min_level = C.uint32_t(minLevel)
	C.stmak_ring_set_min_level(sub.ring, C.uint32_t(minLevel))

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
