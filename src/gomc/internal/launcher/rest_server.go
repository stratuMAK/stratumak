// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
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

	// defaultRESTMaxConnections caps concurrently accepted HTTP connections;
	// [GMC]REST_MAX_CONNECTIONS overrides it (0 = unlimited).
	defaultRESTMaxConnections = 256
	// defaultRESTMaxWSConnections caps concurrent WebSocket connections across
	// the watch and stream endpoints; [GMC]REST_MAX_WS_CONNECTIONS overrides it
	// (0 = unlimited). Kept below the overall cap on purpose — a WebSocket is a
	// hijacked HTTP connection and holds an accept slot for its whole life.
	defaultRESTMaxWSConnections = 64
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

// resolveConnLimit reads a connection cap from the GMC_<key> environment
// variable, else [GMC]<key> in the INI, else def. Zero means "unlimited" and is
// an explicit opt-out; an unparsable or negative value is a configuration
// mistake, so it is logged and the default is used rather than silently
// disabling the cap.
func (l *Launcher) resolveConnLimit(key string, def int) int {
	raw := os.Getenv("GMC_" + key)
	if raw == "" && l.ini != nil {
		raw = l.ini.Get("GMC", key)
	}
	if raw = strings.TrimSpace(raw); raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		l.logger.Warn("invalid connection limit, using the default",
			"key", key, "value", raw, "default", def)
		return def
	}
	return n
}

// createAPIServer creates the API server instance, binds its listen address and
// sets it as the default. Called early in startup so that stream_server
// registrations from cmod plugins can find it. It does not accept requests yet
// — startAPIServer serves the listener opened here.
//
// The bind happens here, not at serve time, because it is the one part of
// bringing the API up that can fail for a reason outside this process: another
// instance, or anything else, already holding the address. Binding late meant
// discovering that *after* realtime was running, motmod was loaded, HAL threads
// were started and the interpreter was initialised — a fully started machine,
// torn straight back down (the exit status was right, the cost was not). Bound
// here, a taken address stops startup before realtime is touched.
//
// Between this bind and the Serve in startAPIServer the socket is listening but
// unaccepted: a client that connects early waits in the backlog instead of
// being refused, and no request is answered before the API is actually ready.
func (l *Launcher) createAPIServer() error {
	addr := l.resolveRESTAddr()

	reg := apiserver.DefaultRegistry()
	if reg == nil {
		l.logger.Warn("no API registry available, API server not created")
		return nil
	}

	srv := apiserver.NewServer(reg, addr)
	srv.SetLogger(l.logger)
	srv.SetWSOriginPatterns(l.resolveWSOriginPatterns())

	// Connection caps (finding N9). Both are blast-radius limits, not security
	// controls — see internal/apiserver/limits.go for why there are two and why
	// the defaults are generous. 0 disables either cap explicitly.
	maxConns := l.resolveConnLimit("REST_MAX_CONNECTIONS", defaultRESTMaxConnections)
	maxWS := l.resolveConnLimit("REST_MAX_WS_CONNECTIONS", defaultRESTMaxWSConnections)
	if maxConns > 0 && maxWS >= maxConns {
		// WebSocket connections are hijacked HTTP connections and so consume
		// accept slots too: a WS cap at or above the overall cap lets watch
		// clients starve plain REST of connections entirely.
		l.logger.Warn("REST_MAX_WS_CONNECTIONS >= REST_MAX_CONNECTIONS — WebSocket clients can starve REST",
			"ws", maxWS, "total", maxConns)
	}
	srv.SetMaxConnections(maxConns)
	srv.SetMaxWSConnections(maxWS)
	l.logger.Info("REST connection limits", "max_connections", maxConns, "max_ws_connections", maxWS)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("REST API server: %w", err)
	}
	// Report the bound address, which is what a client must connect to: with a
	// ":0" address (an ephemeral port, used to run several instances at once)
	// the configured string does not name it.
	l.logger.Info("REST API address bound", "addr", ln.Addr().String())

	l.apiMu.Lock()
	l.apiServer = srv
	l.apiListener = ln
	l.apiMu.Unlock()

	apiserver.SetDefaultServer(srv)
	return nil
}

// apiListenerRef returns the bound listener (nil if none), read under apiMu.
func (l *Launcher) apiListenerRef() net.Listener {
	l.apiMu.Lock()
	defer l.apiMu.Unlock()
	return l.apiListener
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
		if err := l.createAPIServer(); err != nil {
			// Reached only if startup skipped createAPIServer; the bind error
			// is fatal here for the same reason it is fatal below.
			l.logger.Error("REST API server failed to bind — shutting down", "error", err)
			l.fail(err)
			return
		}
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

	// Serve the listener bound in createAPIServer. ListenAndServe is the
	// fallback for a caller that installed a server without going through it.
	ln := l.apiListenerRef()
	if ln != nil {
		addr = ln.Addr().String()
	}

	go func() {
		l.logger.Info("starting REST API server", "addr", addr)
		serve := srv.ListenAndServe
		if ln != nil {
			serve = func() error { return srv.Serve(ln) }
		}
		err := serve()
		switch {
		case err == nil || errors.Is(err, http.ErrServerClosed):
			// Graceful shutdown through http.Server.Shutdown/Close.
		case l.shutdownRequested():
			// Serving stopped while the machine was already going down, so
			// there is nothing left to escalate: cleanup closes shutdownCh
			// before it touches the server (doCleanup step 0), and it closes
			// the pre-bound listener itself so a never-served bind cannot
			// outlive the launcher. net/http only translates the resulting
			// accept failure into ErrServerClosed when its own Shutdown is
			// what closed the listener, so on every clean stop this arrives
			// as a raw "use of closed network connection" — which used to be
			// logged as a fatal error and recorded in fatalErr, making an
			// orderly SIGTERM look like a crash.
			l.logger.Debug("REST API server stopped", "reason", err)
		default:
			// FATAL: without the REST/WS server every client (GUIs, gmi,
			// halcmd remote, test drivers) is locked out while HAL keeps
			// running — a headless zombie. Observed with a stale instance
			// still holding the port ("bind: address already in use"):
			// the next server came up headless and its test driver hung
			// forever. Exit through the ordered shutdown path instead.
			l.logger.Error("REST API server failed — shutting down", "error", err)
			l.fail(fmt.Errorf("REST API server: %w", err))
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
	ln := l.apiListener
	l.apiServer = nil
	l.apiListener = nil
	l.apiMu.Unlock()
	// Shutdown only knows about listeners the server is serving, so a bind that
	// startup never got as far as serving (an earlier step failed) has to be
	// closed here or the socket outlives the launcher. Serve() closes it
	// itself, which makes this second Close a harmless already-closed error.
	if ln != nil {
		_ = ln.Close()
	}
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		l.logger.Debug("REST API server shutdown error", "error", err)
	}
}
