// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// withStateDir points the config package at a temporary state directory for
// the duration of one test, and restores it afterwards. The variables are
// normally set once by -ldflags and never written, so a test that forgets to
// restore them leaks into every test that follows.
func withStateDir(t *testing.T, stateDir string) {
	t.Helper()
	oldState, oldStmak := config.EMC2StateDir, config.EMC2StmakDir
	t.Cleanup(func() {
		config.EMC2StateDir, config.EMC2StmakDir = oldState, oldStmak
	})
	config.EMC2StateDir = stateDir
	// $STMAK_DIR would take precedence over everything below and turn
	// DerivedBuild off; the build sets it, so a developer's shell may have it.
	t.Setenv(config.StmakDirEnv, "")
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
			t.Error("a layout with a state directory and no $STMAK_DIR must derive its build tree")
		}
		if got, want := config.BuildTreeDir(), "/var/lib/stratumak/stmak"; got != want {
			t.Errorf("BuildTreeDir() = %q, want %q", got, want)
		}
	})

	t.Run("run-in-place does not derive", func(t *testing.T) {
		withStateDir(t, "")
		config.EMC2StmakDir = "/some/rip/tree/src/stmak"
		if config.DerivedBuild() {
			t.Error("a layout with no state directory has nothing to derive from")
		}
		if got, want := config.BuildTreeDir(), "/some/rip/tree/src/stmak"; got != want {
			t.Errorf("BuildTreeDir() = %q, want the stratuMAK sources %q", got, want)
		}
	})

	t.Run("STMAK_DIR wins over the state directory", func(t *testing.T) {
		// This is how the build itself invokes modcompile: an installed
		// modcompile, run against the source tree. It must not divert into
		// /var, which on a build machine it has no business writing.
		withStateDir(t, "/var/lib/stratumak")
		t.Setenv(config.StmakDirEnv, "/build/src/stmak")
		if config.DerivedBuild() {
			t.Error("$STMAK_DIR names the tree outright; there is nothing to derive")
		}
		if got, want := config.BuildTreeDir(), "/build/src/stmak"; got != want {
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
	config.EMC2StmakDir = "/rip/src/stmak"
	if got, want := moduleStoreDir(), "/rip/src/stmak/external"; got != want {
		t.Errorf("moduleStoreDir() = %q, want the in-tree external dir %q", got, want)
	}
}

// TestCModInstallDirIsNotThePackagesOwn is the point of relocating local
// cmods: a .so built here must not land among the ones dpkg shipped, where it
// would survive upgrades and shadow a module of the same name.
func TestCModInstallDirIsNotThePackagesOwn(t *testing.T) {
	oldCmod := config.EMC2CmodDir
	t.Cleanup(func() { config.EMC2CmodDir = oldCmod })
	config.EMC2CmodDir = "/usr/lib/stratumak/cmod"

	withStateDir(t, "/var/lib/stratumak")
	if got, want := cmodInstallDir(), "/var/lib/stratumak/cmod"; got != want {
		t.Errorf("cmodInstallDir() = %q, want %q", got, want)
	}

	withStateDir(t, "")
	if got, want := cmodInstallDir(), "/usr/lib/stratumak/cmod"; got != want {
		t.Errorf("cmodInstallDir() = %q, want the only cmod dir there is, %q", got, want)
	}
}

// TestSyncBuildTreeRegeneratesFromSources covers the property the whole
// derived-tree design rests on: the tree is rebuilt from the pristine sources
// plus the registry every time, so an upgrade of the sources reaches the next
// build and a module removed from the registry disappears from it.
func TestSyncBuildTreeRegeneratesFromSources(t *testing.T) {
	base := t.TempDir()
	pristine := filepath.Join(base, "usr", "stmak")
	state := filepath.Join(base, "var")

	withStateDir(t, state)
	config.EMC2StmakDir = pristine

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

	write(filepath.Join(pristine, "go.mod.in"), "module stratuMAK\n")
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

// TestLinuxcncIncludeDirFollowsLayout guards the include root every cmod
// compile depends on. It was wrong for installed systems from the start —
// $(EMC2Home)/include is right only for a run-in-place tree, where the headers
// are collected flat under the tree root; an installed system puts them in
// $(includedir)/stratumak. So `modcompile --install` searched /usr/include,
// found no rtapi_math.h, and every component doing floating-point maths
// failed to compile against a header sitting one directory further down.
//
// Only visible on a packaged system, which is why it survived until a real
// component set was installed on one.
func TestLinuxcncIncludeDirFollowsLayout(t *testing.T) {
	oldHome, oldRIP := config.EMC2Home, config.RunInPlace
	t.Cleanup(func() { config.EMC2Home, config.RunInPlace = oldHome, oldRIP })

	config.EMC2Home, config.RunInPlace = "/usr", "no"
	if got, want := linuxcncIncludeDir(), "/usr/include/stratumak"; got != want {
		t.Errorf("installed layout: linuxcncIncludeDir() = %q, want %q", got, want)
	}

	config.EMC2Home, config.RunInPlace = "/home/dev/stratumak", "yes"
	if got, want := linuxcncIncludeDir(), "/home/dev/stratumak/include"; got != want {
		t.Errorf("run-in-place layout: linuxcncIncludeDir() = %q, want %q", got, want)
	}
}

// TestGetFileCapsResolvesSymlinks is the regression guard for a bug that
// reached a real machine: getcap lstat()s its argument and silently skips
// anything that is not a regular file, so asking it about a symlink produced
// no output and exit status zero — the same answer as "this file has no
// capabilities". $(bindir)/stmakd is a symlink onto the package's binary
// until the first local rebuild, so the rebuild read "none" for a file that
// had ten, and installed a server that could not do realtime.
//
// This cannot assert the capabilities themselves (setting any needs root), so
// it asserts the thing that was actually wrong: which path gets looked at.
func TestGetFileCapsResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()

	// A stand-in for getcap, placed first on PATH. It reproduces the one
	// behaviour that caused the bug: the real getcap lstat()s its argument and
	// silently skips anything that is not a regular file, exiting zero. A
	// stand-in that answered for symlinks too would make this test pass
	// whether or not the resolution is there — which is exactly what the first
	// version of it did.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fake := "#!/bin/sh\n" +
		"[ -L \"$1\" ] && exit 0\n" +
		"[ -f \"$1\" ] || exit 0\n" +
		"echo \"$1 cap_probe=ep\"\n"
	if err := os.WriteFile(filepath.Join(bin, "getcap"), []byte(fake), 0o755); err != nil {
		t.Fatalf("writing the getcap stand-in: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	real := filepath.Join(dir, "real-binary")
	if err := os.WriteFile(real, []byte("ELF"), 0o755); err != nil {
		t.Fatalf("writing: %v", err)
	}
	link := filepath.Join(dir, "link-to-binary")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Asked about the symlink, it must have looked at the real file.
	if got, want := getFileCaps(link), "cap_probe=ep"; got != want {
		t.Errorf("getFileCaps(symlink) = %q, want %q — the symlink was not resolved", got, want)
	}
	if got, want := getFileCaps(real), "cap_probe=ep"; got != want {
		t.Errorf("getFileCaps(regular file) = %q, want %q", got, want)
	}

	// A path that does not exist yet is the staging file every build writes
	// to, and must stay quiet rather than warn.
	if got := getFileCaps(filepath.Join(dir, "not-here")); got != "" {
		t.Errorf("getFileCaps(missing) = %q, want \"\"", got)
	}
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
