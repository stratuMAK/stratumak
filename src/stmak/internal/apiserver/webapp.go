// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// AddWebApps registers static file handlers for each web application
// found under the given directories. Each subdirectory becomes an app served
// at /app/<name>/. A root handler at / lists available apps.
//
// The directories are searched in order: the system's own apps first, then a
// machine configuration's ([SERVER]WEBAPP_DIR). A name already taken by an
// earlier directory is refused with an error naming both — the configuration
// cannot replace a system app, and two apps of one name would otherwise
// depend on registration order. Called once: the root handler can be
// registered only once.
//
// SPA fallback: requests under /app/<name>/ that don't match a real
// file are served index.html so client-side routing works.
func (s *Server) AddWebApps(webappDirs ...string) {
	var apps []string
	appDirs := map[string]string{}
	configured := false

	for _, webappDir := range webappDirs {
		if webappDir == "" {
			continue
		}
		configured = true

		entries, err := os.ReadDir(webappDir)
		if err != nil {
			s.logger.Warn("cannot read webapp directory", "dir", webappDir, "error", err)
			continue
		}

		var found bool
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			found = true
			name := e.Name()
			appDir := filepath.Join(webappDir, name)
			if prev, dup := appDirs[name]; dup {
				s.logger.Error("webapp name already taken, not serving it",
					"name", name, "dir", appDir, "served", prev)
				continue
			}
			appDirs[name] = appDir
			s.addWebApp(name, appDir)
			apps = append(apps, name)
		}
		if !found {
			s.logger.Warn("no webapp subdirectories found", "dir", webappDir)
		}
	}

	if !configured {
		s.logger.Warn("no webapp directory configured (EMC2WebAppDir is empty)")
		return
	}

	// Root handler: list available apps as simple HTML links.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><head><title>STMAK Web Apps</title></head><body>\n"))
		_, _ = w.Write([]byte("<h1>STMAK Web Applications</h1><ul>\n"))
		for _, name := range apps {
			_, _ = w.Write([]byte(`<li><a href="/app/` + name + `/">` + name + `</a></li>` + "\n"))
		}
		_, _ = w.Write([]byte("</ul></body></html>\n"))
	})
}

// addWebApp serves one app directory at /app/<name>/.
func (s *Server) addWebApp(name, appDir string) {
	prefix := "/app/" + name + "/"

	// SPA-aware file server: serve real files, fall back to index.html.
	fs := http.Dir(appDir)
	fileServer := http.FileServer(fs)
	s.mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		// Strip the prefix to get the path within the app directory.
		relPath := strings.TrimPrefix(r.URL.Path, prefix)
		if relPath == "" {
			relPath = "index.html"
		}

		// Check if the file exists. If not, serve index.html (SPA fallback).
		//
		// It has to be served directly, not by rewriting the URL to
		// .../index.html: http.FileServer redirects any request whose path
		// ends in "index.html" back to "./", which made every deep link
		// (e.g. /app/hmi/settings/network on a hard refresh) bounce between
		// the rewrite and the redirect until the client gave up.
		if _, err := os.Stat(filepath.Join(appDir, relPath)); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(appDir, "index.html"))
			return
		}

		http.StripPrefix(prefix, fileServer).ServeHTTP(w, r)
	})

	s.logger.Info("registered webapp", "name", name, "prefix", prefix, "dir", appDir)
}
