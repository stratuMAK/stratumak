// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// stalenessFixture builds a state directory and an installed source tree,
// stamps the sources with installedVersion, and tells the config package that
// this binary was built from builtVersion. It returns whatever WarnIfStale
// logged.
//
// localBuild selects the case under test: a real file at
// $STATE/bin/stmakd is a server somebody rebuilt, a symlink is the
// package's own.
func stalenessFixture(t *testing.T, builtVersion, installedVersion string, localBuild bool) string {
	t.Helper()

	base := t.TempDir()
	state := filepath.Join(base, "var")
	sources := filepath.Join(base, "usr", "stmak")

	oldState, oldStmak, oldVer := config.EMC2StateDir, config.EMC2StmakDir, config.EMC2Version
	t.Cleanup(func() {
		config.EMC2StateDir, config.EMC2StmakDir, config.EMC2Version = oldState, oldStmak, oldVer
	})
	config.EMC2StateDir = state
	config.EMC2StmakDir = sources
	config.EMC2Version = builtVersion
	t.Setenv(config.StmakDirEnv, "")

	if err := os.MkdirAll(filepath.Join(state, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if installedVersion != "" {
		p := filepath.Join(sources, stmakVersionFile)
		if err := os.WriteFile(p, []byte(installedVersion+"\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	server := filepath.Join(state, "bin", "stmakd")
	if localBuild {
		if err := os.WriteFile(server, []byte("ELF"), 0o755); err != nil {
			t.Fatalf("writing %s: %v", server, err)
		}
	} else if err := os.Symlink("/usr/libexec/stratumak/stmakd", server); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var buf bytes.Buffer
	WarnIfStale(slog.New(slog.NewTextHandler(&buf, nil)))
	return buf.String()
}

// TestWarnIfStaleReportsAnOldLocalBuild is the case the stamp exists for: a
// server rebuilt against a previous release's stmak, still running after the
// package was upgraded around it.
func TestWarnIfStaleReportsAnOldLocalBuild(t *testing.T) {
	out := stalenessFixture(t, "0.1.0", "0.2.0", true)

	if out == "" {
		t.Fatal("a locally built server older than the installed sources must be reported")
	}
	for _, want := range []string{"0.1.0", "0.2.0", "modcompile rebuild"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning must mention %q so it can be acted on; got: %s", want, out)
		}
	}
}

// TestWarnIfStaleSilentCases: everything that is not the case above must say
// nothing at all. A startup warning that fires on a healthy system is one
// operators learn to ignore, which costs the real warning its meaning.
func TestWarnIfStaleSilentCases(t *testing.T) {
	t.Run("packaged server, no local build", func(t *testing.T) {
		// The symlink means an upgrade took effect the moment dpkg unpacked
		// it, whatever the versions happen to say.
		if out := stalenessFixture(t, "0.1.0", "0.2.0", false); out != "" {
			t.Errorf("a server that is still the packaged one is never stale; got: %s", out)
		}
	})

	t.Run("local build matching the sources", func(t *testing.T) {
		if out := stalenessFixture(t, "0.2.0", "0.2.0", true); out != "" {
			t.Errorf("an up-to-date local build must be silent; got: %s", out)
		}
	})

	t.Run("no version stamp installed", func(t *testing.T) {
		// An installation from before the stamp existed. Nothing can be
		// concluded, so nothing is claimed.
		if out := stalenessFixture(t, "0.1.0", "", true); out != "" {
			t.Errorf("without a stamp there is nothing to compare; got: %s", out)
		}
	})

	t.Run("run-in-place tree", func(t *testing.T) {
		oldState := config.EMC2StateDir
		t.Cleanup(func() { config.EMC2StateDir = oldState })
		config.EMC2StateDir = ""

		var buf bytes.Buffer
		WarnIfStale(slog.New(slog.NewTextHandler(&buf, nil)))
		if buf.Len() != 0 {
			t.Errorf("a tree whose binary and sources are rebuilt together cannot be stale; got: %s", buf.String())
		}
	})
}
