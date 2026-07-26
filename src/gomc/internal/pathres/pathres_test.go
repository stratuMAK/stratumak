// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testEnv builds a config dir + a library dir with one file each, and returns
// a resolver over them.
func testEnv(t *testing.T) (r *Resolver, configDir, libDir string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "config")
	libDir = filepath.Join(root, "hallib")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{configDir, libDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}
	writeFile(t, filepath.Join(configDir, "machine.hal"), "# config")
	writeFile(t, filepath.Join(libDir, "core.hal"), "# lib")
	writeFile(t, filepath.Join(outside, "secret"), "s3cret")

	r, err := New(configDir, libDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, configDir, libDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func TestResolve_RelativeSearchOrder(t *testing.T) {
	r, configDir, libDir := testEnv(t)

	// The config directory is searched before the library directories.
	got, err := r.Resolve("machine.hal", Read)
	if err != nil {
		t.Fatalf("Resolve(machine.hal): %v", err)
	}
	if want := filepath.Join(configDir, "machine.hal"); got != want {
		t.Errorf("Resolve(machine.hal) = %q, want %q", got, want)
	}

	// A name only present in the library directory still resolves.
	got, err = r.Resolve("core.hal", Read)
	if err != nil {
		t.Fatalf("Resolve(core.hal): %v", err)
	}
	if want := filepath.Join(libDir, "core.hal"); got != want {
		t.Errorf("Resolve(core.hal) = %q, want %q", got, want)
	}
}

func TestResolve_ConfigDirShadowsLib(t *testing.T) {
	r, configDir, libDir := testEnv(t)
	writeFile(t, filepath.Join(configDir, "core.hal"), "# local override")

	got, err := r.Resolve("core.hal", Read)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(configDir, "core.hal"); got != want {
		t.Errorf("Resolve = %q, want the config-dir copy %q (lib copy is %q)",
			got, want, filepath.Join(libDir, "core.hal"))
	}
}

func TestResolve_LibPrefixSkipsConfigDir(t *testing.T) {
	r, configDir, libDir := testEnv(t)
	// A same-named file in the config dir must NOT satisfy a LIB: request.
	writeFile(t, filepath.Join(configDir, "core.hal"), "# local override")

	got, err := r.Resolve("LIB:core.hal", Read)
	if err != nil {
		t.Fatalf("Resolve(LIB:core.hal): %v", err)
	}
	if want := filepath.Join(libDir, "core.hal"); got != want {
		t.Errorf("Resolve(LIB:core.hal) = %q, want %q", got, want)
	}

	// LIB: for something only in the config dir must fail.
	writeFile(t, filepath.Join(configDir, "only-local.hal"), "# local")
	if _, err := r.Resolve("LIB:only-local.hal", Read); err == nil {
		t.Error("Resolve(LIB:only-local.hal): want an error, got nil")
	}
}

func TestResolve_AbsoluteContained(t *testing.T) {
	r, configDir, _ := testEnv(t)

	abs := filepath.Join(configDir, "machine.hal")
	got, err := r.Resolve(abs, Read)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", abs, err)
	}
	if got != abs {
		t.Errorf("Resolve = %q, want %q", got, abs)
	}
}

func TestResolve_AbsoluteOutsideRejected(t *testing.T) {
	r, configDir, _ := testEnv(t)
	outside := filepath.Join(filepath.Dir(configDir), "outside", "secret")

	_, err := r.Resolve(outside, Read)
	if err == nil {
		t.Fatal("Resolve of an uncontained absolute path: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("error %q does not explain the containment failure", err)
	}
}

func TestResolve_TraversalRejected(t *testing.T) {
	r, configDir, _ := testEnv(t)

	// ../outside/secret escapes the config dir.
	_, err := r.Resolve(filepath.Join("..", "outside", "secret"), Read)
	if err == nil {
		t.Fatal("Resolve of a traversing relative path: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("error %q does not explain the containment failure", err)
	}
	_ = configDir
}

func TestResolve_SymlinkEscapeRejected(t *testing.T) {
	r, configDir, _ := testEnv(t)
	outside := filepath.Join(filepath.Dir(configDir), "outside", "secret")

	link := filepath.Join(configDir, "innocent.hal")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The name is inside the config dir; where it leads is not.
	_, err := r.Resolve("innocent.hal", Read)
	if err == nil {
		t.Fatal("Resolve through an escaping symlink: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("error %q does not explain the containment failure", err)
	}
}

func TestResolve_DirectoryRejectedForRead(t *testing.T) {
	r, configDir, _ := testEnv(t)
	if err := os.Mkdir(filepath.Join(configDir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// A directory that name-matches must read as "not found", not be returned
	// and then fail confusingly in the parser (2.9 linuxcnc.in does the same).
	if _, err := r.Resolve("subdir", Read); err == nil {
		t.Error("Resolve of a directory in Read mode: want an error, got nil")
	}
}

func TestResolve_WriteAllowsMissingFile(t *testing.T) {
	r, configDir, _ := testEnv(t)

	got, err := r.Resolve("new-output.log", Write)
	if err != nil {
		t.Fatalf("Resolve(new-output.log, Write): %v", err)
	}
	if want := filepath.Join(configDir, "new-output.log"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}

	// ... but not outside the roots.
	outside := filepath.Join(filepath.Dir(configDir), "outside", "new.log")
	if _, err := r.Resolve(outside, Write); err == nil {
		t.Error("Resolve of an uncontained write target: want an error, got nil")
	}
}

func TestResolve_WriteRejectsNonRegular(t *testing.T) {
	r, configDir, _ := testEnv(t)

	fifo := filepath.Join(configDir, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	// Opening a FIFO blocks forever, and the launcher holds modMu across a
	// whole module load — that would wedge every load, unload and shutdown.
	if _, err := r.Resolve("pipe", Write); err == nil {
		t.Error("Resolve of a FIFO in Write mode: want an error, got nil")
	}
	if _, err := r.Resolve("pipe", Read); err == nil {
		t.Error("Resolve of a FIFO in Read mode: want an error, got nil")
	}
}

func TestResolve_DirMode(t *testing.T) {
	r, configDir, _ := testEnv(t)
	if err := os.Mkdir(filepath.Join(configDir, "db"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	got, err := r.Resolve("db", Dir)
	if err != nil {
		t.Fatalf("Resolve(db, Dir): %v", err)
	}
	if want := filepath.Join(configDir, "db"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}

	// A not-yet-created directory with a contained parent is allowed.
	if _, err := r.Resolve("db-new", Dir); err != nil {
		t.Errorf("Resolve(db-new, Dir): %v", err)
	}
}

func TestResolve_Rejects(t *testing.T) {
	r, _, _ := testEnv(t)

	for _, tc := range []struct{ name, path string }{
		{"empty", ""},
		{"blank", "   "},
		{"nul-byte", "conf\x00ig.hal"},
		{"empty-lib", "LIB:"},
		{"missing", "no-such-file.hal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Resolve(tc.path, Read); err == nil {
				t.Errorf("Resolve(%q): want an error, got nil", tc.path)
			}
		})
	}
}

func TestNew_DropsDotFromHalibPath(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	libDir := filepath.Join(root, "hallib")
	for _, d := range []string{configDir, libDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// "." is the base by definition; as a search root it would silently widen
	// containment to wherever the process happens to be (HF-1).
	r, err := New(configDir, ".:"+libDir+"::  ")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	roots := r.Roots()
	if len(roots) != 2 {
		t.Fatalf("roots = %q, want the base and one library dir", roots)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for _, got := range roots {
		if got == wd {
			t.Errorf("roots contain the working directory %q", wd)
		}
	}
}

func TestNew_NoIniUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "standalone.hal"), "# halrun mode")

	// halrun mode has no INI, so the working directory is the base.
	r, err := New("", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := r.Resolve("standalone.hal", Read)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, "standalone.hal"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if gotReal, err := filepath.EvalSymlinks(got); err != nil || gotReal != real {
		t.Errorf("Resolve = %q, want %q", got, real)
	}
}

func TestDefault_NotInitialised(t *testing.T) {
	SetDefault(nil)
	t.Cleanup(func() { SetDefault(nil) })

	// A module must fail loudly rather than silently fall back to an
	// unchecked path when the launcher has not installed a resolver.
	if _, err := Resolve("anything.hal", Read); err == nil {
		t.Fatal("Resolve with no default resolver: want an error, got nil")
	}
}
