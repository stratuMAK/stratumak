// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/config"
	"github.com/sittner/linuxcnc/src/gomc/internal/halrest"
)

const (
	// defaultRESTAddr is the default listen address for the REST API server.
	// Can be overridden via [GMC]REST_ADDR in the INI file.
	defaultRESTAddr = "127.0.0.1:5080"
)

// resolveRESTAddr returns the REST API listen address, in precedence order:
// the GMC_REST_ADDR environment variable, then [GMC]REST_ADDR in the INI, then
// the compiled default (127.0.0.1:5080).  The env override lets the test
// harness run several gomc-server instances on distinct ports in parallel
// without editing per-config REST_ADDR.
func (l *Launcher) resolveRESTAddr() string {
	if v := os.Getenv("GMC_REST_ADDR"); v != "" {
		return v
	}
	if l.ini != nil {
		if v := l.ini.Get("GMC", "REST_ADDR"); v != "" {
			return v
		}
	}
	return defaultRESTAddr
}

// createAPIServer creates the API server instance and sets it as the
// default. Called early in startup so that stream_server registrations
// from cmod plugins can find it. Does not start listening.
func (l *Launcher) createAPIServer() {
	addr := l.resolveRESTAddr()

	reg := apiserver.DefaultRegistry()
	if reg == nil {
		l.logger.Warn("no API registry available, API server not created")
		return
	}

	srv := apiserver.NewServer(reg, addr)
	srv.SetLogger(l.logger)
	srv.SetWSOriginPatterns(l.resolveWSOriginPatterns())

	l.apiMu.Lock()
	l.apiServer = srv
	l.apiMu.Unlock()

	apiserver.SetDefaultServer(srv)
}

// apiServerRef returns the current API server (nil if none), read under apiMu.
func (l *Launcher) apiServerRef() *apiserver.Server {
	l.apiMu.Lock()
	defer l.apiMu.Unlock()
	return l.apiServer
}

// resolveWSOriginPatterns returns the WebSocket Origin allow-list, from the
// GMC_REST_ORIGINS environment variable, else [GMC]REST_ORIGINS in the INI
// (both comma-separated). Empty means same-origin only — the secure default
// that blocks cross-site WebSocket hijacking. Set it to the HMI host(s), or to
// "*" to allow any origin (opt-in, insecure).
func (l *Launcher) resolveWSOriginPatterns() []string {
	raw := os.Getenv("GMC_REST_ORIGINS")
	if raw == "" && l.ini != nil {
		raw = l.ini.Get("GMC", "REST_ORIGINS")
	}
	if raw == "" {
		return nil
	}
	var patterns []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// startAPIServer starts the REST API server in the background.
// The listen address is read from [GMC]REST_ADDR in the INI file,
// defaulting to "127.0.0.1:5080".
func (l *Launcher) startAPIServer() {
	if l.apiServerRef() == nil {
		l.createAPIServer()
	}
	// Everything below configures and serves this one instance; a concurrent
	// stopAPIServer() nils the field, so hold the reference in a local rather
	// than re-reading it (that is also what the serve goroutine closes over).
	srv := l.apiServerRef()
	if srv == nil {
		return
	}

	// Add WebSocket watch endpoint if a watch registry is available
	watchReg := apiserver.DefaultWatchRegistry()
	if watchReg == nil {
		watchReg = apiserver.NewWatchRegistry()
		apiserver.SetDefaultWatchRegistry(watchReg)
	}
	srv.AddWatchEndpoint(watchReg)

	// Register halcmd watch functions (live pin/signal value streaming).
	// [HAL]WATCH_INTERVAL overrides the default 100ms push rate.
	watchInterval := time.Duration(0)
	if l.ini != nil {
		if ms := l.ini.Get("HAL", "WATCH_INTERVAL"); ms != "" {
			if d, err := time.ParseDuration(ms); err == nil {
				watchInterval = d
			}
		}
	}
	halrest.RegisterWatch(watchReg, watchInterval)

	// Serve web applications from share/gomc/webapp/<app>/
	if config.EMC2WebAppDir != "" {
		l.logger.Info("configuring web apps", "dir", config.EMC2WebAppDir)
		srv.AddWebApps(config.EMC2WebAppDir)
	} else {
		l.logger.Warn("EMC2WebAppDir not set, web apps disabled")
	}

	addr := l.resolveRESTAddr()
	srv.SetAddr(addr)

	go func() {
		l.logger.Info("starting REST API server", "addr", addr)
		if err := srv.ListenAndServe(); err != nil {
			// http.ErrServerClosed is expected on graceful shutdown
			if err.Error() != "http: Server closed" {
				// FATAL: without the REST/WS server every client (GUIs, gmi,
				// halcmd remote, test drivers) is locked out while HAL keeps
				// running — a headless zombie. Observed with a stale instance
				// still holding the port ("bind: address already in use"):
				// the next server came up headless and its test driver hung
				// forever. Exit through the ordered shutdown path instead.
				l.logger.Error("REST API server failed — shutting down", "error", err)
				l.fail(fmt.Errorf("REST API server: %w", err))
			}
		}
	}()
}

// stopAPIServer gracefully shuts down the REST API server.
func (l *Launcher) stopAPIServer() {
	// Take the server out of the field first, so a second caller (or a future
	// restart path) cannot shut the same instance down twice; the 2s Shutdown
	// then runs without apiMu held.
	l.apiMu.Lock()
	srv := l.apiServer
	l.apiServer = nil
	l.apiMu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		l.logger.Debug("REST API server shutdown error", "error", err)
	}
}
