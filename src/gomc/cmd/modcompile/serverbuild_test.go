// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/config"
)

// withStateDir points the config package at a temporary state directory for
// the duration of one test, and restores it afterwards. The variables are
// normally set once by -ldflags and never written, so a test that forgets to
// restore them leaks into every test that follows.
func withStateDir(t *testing.T, stateDir string) {
	t.Helper()
	oldState, oldGomc := config.EMC2StateDir, config.EMC2GomcDir
	t.Cleanup(func() {
		config.EMC2StateDir, config.EMC2GomcDir = oldState, oldGomc
	})
	config.EMC2StateDir = stateDir
	// $GOMC_DIR would take precedence over everything below and turn
	// DerivedBuild off; the build sets it, so a developer's shell may have it.
	t.Setenv(config.GomcDirEnv, "")
}

// TestDerivedBuildDiscriminator pins down when a rebuild goes through a
// derived tree and when it compiles the sources where they sit. Everything
// else in this file branches on it, and both branches are real deployments:
// getting it backwards would either write into a package-owned directory or
// build a run-in-place tree from a directory that does not exist.
func TestDerivedBuildDiscriminator(t *testing.T) {
	t.Run("packaged layout derives", func(t *testing.T) {
		withStateDir(t, "/var/lib/stratumak")
		if !config.DerivedBuild() {
			t.Error("a layout with a state directory and no $GOMC_DIR must derive its build tree")
		}
		if got, want := config.BuildTreeDir(), "/var/lib/stratumak/gomc"; got != want {
			t.Errorf("BuildTreeDir() = %q, want %q", got, want)
		}
	})

	t.Run("run-in-place does not derive", func(t *testing.T) {
		withStateDir(t, "")
		config.EMC2GomcDir = "/some/rip/tree/src/gomc"
		if config.DerivedBuild() {
			t.Error("a layout with no state directory has nothing to derive from")
		}
		if got, want := config.BuildTreeDir(), "/some/rip/tree/src/gomc"; got != want {
			t.Errorf("BuildTreeDir() = %q, want the gomc sources %q", got, want)
		}
	})

	t.Run("GOMC_DIR wins over the state directory", func(t *testing.T) {
		// This is how the build itself invokes modcompile: an installed
		// modcompile, run against the source tree. It must not divert into
		// /var, which on a build machine it has no business writing.
		withStateDir(t, "/var/lib/stratumak")
		t.Setenv(config.GomcDirEnv, "/build/src/gomc")
		if config.DerivedBuild() {
			t.Error("$GOMC_DIR names the tree outright; there is nothing to derive")
		}
		if got, want := config.BuildTreeDir(), "/build/src/gomc"; got != want {
			t.Errorf("BuildTreeDir() = %q, want %q", got, want)
		}
	})
}

// TestModuleStoreDirFollowsLayout: the store is what a rebuild derives from,
// so pointing it at the wrong place loses every registered module silently —
// the build tree would simply come out without an external/ directory.
func TestModuleStoreDirFollowsLayout(t *testing.T) {
	withStateDir(t, "/var/lib/stratumak")
	if got, want := moduleStoreDir(), "/var/lib/stratumak/modules"; got != want {
		t.Errorf("moduleStoreDir() = %q, want the registry %q", got, want)
	}

	withStateDir(t, "")
	config.EMC2GomcDir = "/rip/src/gomc"
	if got, want := moduleStoreDir(), "/rip/src/gomc/external"; got != want {
		t.Errorf("moduleStoreDir() = %q, want the in-tree external dir %q", got, want)
	}
}

// TestCModInstallDirIsNotThePackagesOwn is the point of relocating local
// cmods: a .so built here must not land among the ones dpkg shipped, where it
// would survive upgrades and shadow a module of the same name.
func TestCModInstallDirIsNotThePackagesOwn(t *testing.T) {
	oldCmod := config.EMC2CmodDir
	t.Cleanup(func() { config.EMC2CmodDir = oldCmod })
	config.EMC2CmodDir = "/usr/lib/linuxcnc/cmod"

	withStateDir(t, "/var/lib/stratumak")
	if got, want := cmodInstallDir(), "/var/lib/stratumak/cmod"; got != want {
		t.Errorf("cmodInstallDir() = %q, want %q", got, want)
	}

	withStateDir(t, "")
	if got, want := cmodInstallDir(), "/usr/lib/linuxcnc/cmod"; got != want {
		t.Errorf("cmodInstallDir() = %q, want the only cmod dir there is, %q", got, want)
	}
}

// TestSyncBuildTreeRegeneratesFromSources covers the property the whole
// derived-tree design rests on: the tree is rebuilt from the pristine sources
// plus the registry every time, so an upgrade of the sources reaches the next
// build and a module removed from the registry disappears from it.
func TestSyncBuildTreeRegeneratesFromSources(t *testing.T) {
	base := t.TempDir()
	pristine := filepath.Join(base, "usr", "gomc")
	state := filepath.Join(base, "var")

	withStateDir(t, state)
	config.EMC2GomcDir = pristine

	mkdir := func(d string) {
		t.Helper()
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	write := func(p, content string) {
		t.Helper()
		mkdir(filepath.Dir(p))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	write(filepath.Join(pristine, "go.mod.in"), "module gomc\n")
	write(filepath.Join(pristine, "pkg", "a.go"), "package a // release 1\n")
	write(filepath.Join(config.ModuleRegistryDir(), "widget", "widget.go"), "package widget\n")

	if err := syncBuildTree(); err != nil {
		t.Fatalf("syncBuildTree: %v", err)
	}

	tree := config.BuildTreeDir()
	assertContent := func(rel, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(tree, rel))
		if err != nil {
			t.Fatalf("reading %s from the build tree: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	assertContent("pkg/a.go", "package a // release 1\n")
	assertContent("external/widget/widget.go", "package widget\n")

	// An upgrade: the pristine sources change, the registry does not. The
	// same-length edit is deliberate — a mirror that compares only size would
	// call this file up to date and compile the previous release's source.
	write(filepath.Join(pristine, "pkg", "a.go"), "package a // release 2\n")
	if err := syncBuildTree(); err != nil {
		t.Fatalf("syncBuildTree after upgrade: %v", err)
	}
	assertContent("pkg/a.go", "package a // release 2\n")
	assertContent("external/widget/widget.go", "package widget\n")

	// Deregistering a module removes it from the next tree, rather than
	// leaving it to be compiled in forever.
	if err := os.RemoveAll(filepath.Join(config.ModuleRegistryDir(), "widget")); err != nil {
		t.Fatalf("removing the registry entry: %v", err)
	}
	if err := syncBuildTree(); err != nil {
		t.Fatalf("syncBuildTree after removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "external", "widget")); !os.IsNotExist(err) {
		t.Errorf("external/widget survived deregistration (stat error: %v)", err)
	}
}

// TestNormalizeModesMakesSourcesReadable: the compile runs as a different
// identity than the one that recorded the module, so a private source file
// copied across verbatim is unreadable to it.
func TestNormalizeModesMakesSourcesReadable(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "internal")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(sub, "a.go")
	if err := os.WriteFile(secret, []byte("package a\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	script := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if err := normalizeModes(dir); err != nil {
		t.Fatalf("normalizeModes: %v", err)
	}

	check := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
	check(sub, 0o755)
	check(secret, 0o644)
	// An executable stays executable: a module may carry a generator script,
	// and silently stripping the bit is a change nobody asked for.
	check(script, 0o755)
}

// TestStageLocalHeadersTakesHeadersOnly: the staged directory stands in for
// the source directory on the compile's include path, so it has to carry the
// headers a relative #include could reach — and nothing else, because
// `--install` is routinely run from whatever directory the source happens to
// be sitting in.
func TestStageLocalHeadersTakesHeadersOnly(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "stage")

	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}
	write("local.h", "#define LOCAL 1\n")
	write("nested/deeper.h", "#define DEEPER 1\n")
	write("helper.hh", "// C++ header\n")
	write("mycomp.comp", "component mycomp;\n")
	write("notes.txt", "not a header\n")
	write("blob.tar.gz", "not a header either\n")
	write(".git/config", "[core]\n")

	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := stageLocalHeaders(src, dst); err != nil {
		t.Fatalf("stageLocalHeaders: %v", err)
	}

	// Headers come across, keeping their layout so "nested/deeper.h" resolves.
	for _, rel := range []string{"local.h", "nested/deeper.h", "helper.hh"} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("%s should have been staged: %v", rel, err)
		}
	}
	// Everything else stays where it is. .git especially: staging somebody's
	// repository into a shared cache directory is not a header search path.
	for _, rel := range []string{"mycomp.comp", "notes.txt", "blob.tar.gz", ".git/config", ".git"} {
		if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should not have been staged (stat error: %v)", rel, err)
		}
	}
}

// TestRegisteredModulesIsEmptyNotAnError: a system nobody has added a module
// to has no registry directory at all, and that is its normal state — not a
// condition a rebuild should refuse over.
func TestRegisteredModulesIsEmptyNotAnError(t *testing.T) {
	withStateDir(t, filepath.Join(t.TempDir(), "absent"))

	got, err := registeredModules()
	if err != nil {
		t.Fatalf("registeredModules on a system with no registry: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("registeredModules() = %v, want none", got)
	}
}
