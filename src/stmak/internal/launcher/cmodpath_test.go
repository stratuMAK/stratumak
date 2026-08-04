// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// withCModDirs points the config package at temporary cmod directories for one
// test. The variables are normally set once by -ldflags and never written.
func withCModDirs(t *testing.T, shipped, local string) {
	t.Helper()
	oldShipped, oldState := config.EMC2CmodDir, config.EMC2StateDir
	t.Cleanup(func() {
		config.EMC2CmodDir, config.EMC2StateDir = oldShipped, oldState
	})
	config.EMC2CmodDir = shipped
	// LocalCModDir is derived from the state directory, so this is how a
	// packaged layout is expressed; "" gives the run-in-place one.
	config.EMC2StateDir = local
}

// touchSO creates an empty .so so the resolver's existence checks see it.
// Nothing dlopens these — resolution happens before any of that.
func touchSO(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name+".so")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestResolveCModuleFindsLocalAndShipped covers the ordinary cases: a module in
// either directory resolves to that directory, and one in neither is not an
// error — the caller goes on to try the name as a Go module.
func TestResolveCModuleFindsLocalAndShipped(t *testing.T) {
	base := t.TempDir()
	shipped := filepath.Join(base, "usr", "cmod")
	state := filepath.Join(base, "var")
	withCModDirs(t, shipped, state)
	local := config.LocalCModDir()

	wantShipped := touchSO(t, shipped, "packaged")
	wantLocal := touchSO(t, local, "homegrown")

	for _, tc := range []struct {
		name string
		want string
	}{
		{"packaged", wantShipped},
		{"homegrown", wantLocal},
		// The .so suffix is optional in HAL files and must not change which
		// file is found.
		{"homegrown.so", wantLocal},
	} {
		got, found, err := resolveCModule(tc.name)
		if err != nil {
			t.Errorf("resolveCModule(%q): unexpected error: %v", tc.name, err)
			continue
		}
		if !found {
			t.Errorf("resolveCModule(%q): not found, want %s", tc.name, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveCModule(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	_, found, err := resolveCModule("nowhere")
	if err != nil {
		t.Errorf("resolveCModule of an unknown name must not error (it may be a Go module): %v", err)
	}
	if found {
		t.Error("resolveCModule found a module that does not exist")
	}
}

// TestResolveCModuleRefusesCollision is the ruling this search path was built
// around. Letting the local directory win would mean a stale local .so
// silently shadowing a shipped fix, which is diagnosable only by knowing to
// look — so a name in both places is refused, and the message says where both
// copies are.
func TestResolveCModuleRefusesCollision(t *testing.T) {
	base := t.TempDir()
	shipped := filepath.Join(base, "usr", "cmod")
	state := filepath.Join(base, "var")
	withCModDirs(t, shipped, state)

	shippedPath := touchSO(t, shipped, "contested")
	localPath := touchSO(t, config.LocalCModDir(), "contested")

	got, found, err := resolveCModule("contested")
	if err == nil {
		t.Fatalf("resolveCModule resolved a contested name to %q instead of refusing", got)
	}
	if found {
		t.Error("a refused resolution must not also report found")
	}
	for _, want := range []string{shippedPath, localPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s so it can be acted on; got: %v", want, err)
		}
	}
}

// TestResolveCModuleExplicitPathBypassesSearch: naming a path outright is the
// documented way to load a specific module, and remains the escape hatch for
// deliberately overriding a shipped one.
func TestResolveCModuleExplicitPathBypassesSearch(t *testing.T) {
	base := t.TempDir()
	shipped := filepath.Join(base, "usr", "cmod")
	state := filepath.Join(base, "var")
	withCModDirs(t, shipped, state)

	// Contested by name, so a search would refuse; an explicit path must not.
	touchSO(t, shipped, "contested")
	explicit := touchSO(t, config.LocalCModDir(), "contested")

	got, found, err := resolveCModule(explicit)
	if err != nil {
		t.Fatalf("an explicit path must not go through the search: %v", err)
	}
	if !found || got != explicit {
		t.Errorf("resolveCModule(%q) = (%q, %v), want the path itself", explicit, got, found)
	}
}

// TestResolveCModuleRefusesGoModuleShadow: resolveCModule is consulted before
// the Go registry on both bare-name load paths (the boot IterLoads loop and
// loadModuleNamed), so without this refusal a local cmod would silently win
// over a same-named module compiled into the server. The refusal must name
// both providers so it can be acted on; the explicit-path form stays the
// deliberate override.
func TestResolveCModuleRefusesGoModuleShadow(t *testing.T) {
	base := t.TempDir()
	withCModDirs(t, filepath.Join(base, "usr", "cmod"), filepath.Join(base, "var"))

	// Registered once per test process (there is no unregister); the name is
	// unique to this test.
	stmak.RegisterModule("shadowprobe", func(*inifile.IniFile, *slog.Logger, string, []string) (stmak.Module, error) {
		return nil, nil
	})
	localPath := touchSO(t, config.LocalCModDir(), "shadowprobe")

	for _, load := range []string{"shadowprobe", "shadowprobe.so"} {
		got, found, err := resolveCModule(load)
		if err == nil {
			t.Fatalf("resolveCModule(%q) = %q: a cmod shadowing a compiled-in Go module must be refused", load, got)
		}
		if found {
			t.Error("a refused resolution must not also report found")
		}
		for _, want := range []string{localPath, "compiled into this server"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name %q so it can be acted on; got: %v", want, err)
			}
		}
	}

	// Naming the path outright is the documented deliberate override, and must
	// keep resolving even while the bare name is refused.
	got, found, err := resolveCModule(localPath)
	if err != nil || !found || got != localPath {
		t.Errorf("resolveCModule(%q) = (%q, %v, %v), want the path itself", localPath, got, found, err)
	}
}

// TestCModuleSearchPathRunInPlace: a run-in-place tree has one cmod directory
// and no state directory, so nothing can collide and the search must not
// invent a second entry (an empty one would resolve names to "/<name>.so").
func TestCModuleSearchPathRunInPlace(t *testing.T) {
	withCModDirs(t, "/rip/cmod", "")

	dirs := cModuleSearchPath()
	if len(dirs) != 1 || dirs[0] != "/rip/cmod" {
		t.Fatalf("cModuleSearchPath() = %v, want just the tree's own cmod dir", dirs)
	}
}
