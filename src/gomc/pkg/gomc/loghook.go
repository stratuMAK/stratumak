// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package gomc

import (
	"log/slog"
	"sync"
)

// LogErrorFunc is called when a C module emits an ERROR-level log message.
// component is the module instance name (e.g. "motmod"), msg is the text.
type LogErrorFunc func(component, msg string)

// logHook pairs a registered callback with the id used to unregister it.
type logHook struct {
	id uint64
	fn LogErrorFunc
}

var (
	hookMu     sync.RWMutex
	logHooks   []logHook
	nextHookID uint64
)

// OnLogError registers a callback invoked for every ERROR-level log message from
// C modules, and returns an unregister function that removes it. Used by
// milltask to forward motion error messages (like "joint N following error") to
// the operator message list, matching the old reportError() → error buffer path.
//
// A module that registers a hook MUST call the returned unregister function when
// it is torn down (Stop/Destroy). The registry is a process-global with no owner;
// without unregistering, a destroyed module's hook keeps firing on later errors
// — during shutdown the log ring's final flush runs after Go modules are
// destroyed, so a stale hook would call into a freed module. The unregister
// function is idempotent (safe to call more than once).
func OnLogError(fn LogErrorFunc) (unregister func()) {
	hookMu.Lock()
	defer hookMu.Unlock()
	id := nextHookID
	nextHookID++
	logHooks = append(logHooks, logHook{id: id, fn: fn})
	return func() { removeLogHook(id) }
}

// removeLogHook removes the hook with the given id, preserving the order of the
// rest. A no-op if it was already removed.
func removeLogHook(id uint64) {
	hookMu.Lock()
	defer hookMu.Unlock()
	for i := range logHooks {
		if logHooks[i].id == id {
			logHooks = append(logHooks[:i], logHooks[i+1:]...)
			return
		}
	}
}

// NotifyLogError is called by the launcher's log drain loop for ERROR-level
// messages. It fans out to all registered hooks.
//
// It runs synchronously on the drain goroutine, which has no recover of its own,
// so a panic in any hook would otherwise take down the whole process (and the
// log drain with it). Each hook call is therefore isolated: a panicking hook is
// recovered and reported, and the remaining hooks still run.
func NotifyLogError(component, msg string) {
	hookMu.RLock()
	defer hookMu.RUnlock()
	for _, h := range logHooks {
		callLogHook(h.fn, component, msg)
	}
}

// callLogHook invokes one hook with panic isolation so a buggy or racing hook
// cannot kill the drain goroutine.
func callLogHook(fn LogErrorFunc, component, msg string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("gomc: log-error hook panicked",
				"panic", r, "component", component, "msg", msg)
		}
	}()
	fn(component, msg)
}

// resetLogHooks clears all registered hooks. Test-only: even with the unregister
// path, tests that register hooks want to start from a clean slate.
func resetLogHooks() {
	hookMu.Lock()
	defer hookMu.Unlock()
	logHooks = nil
}
