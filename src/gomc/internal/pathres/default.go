// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"fmt"
	"sync"
)

// Go module factories receive only (ini, logger, name, args) — they never see
// the HAL library path — so the resolver is published here by the launcher
// during startup, the same way the API registry is.  C modules reach the same
// resolver through env->path->resolve().
var (
	defaultMu sync.RWMutex
	def       *Resolver
)

// SetDefault publishes the process-wide resolver.  The launcher calls this
// once, before any module is loaded.
func SetDefault(r *Resolver) {
	defaultMu.Lock()
	def = r
	defaultMu.Unlock()
}

// Default returns the process-wide resolver, or nil if none was published.
func Default() *Resolver {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return def
}

// Resolve locates name with the process-wide resolver.
//
// It fails rather than falling back to an unchecked path: a module that
// resolves configuration paths must not silently lose containment because the
// resolver was not installed.
func Resolve(name string, mode Mode) (string, error) {
	r := Default()
	if r == nil {
		return "", fmt.Errorf("path resolver: not initialised (cannot resolve %q)", name)
	}
	return r.Resolve(name, mode)
}
