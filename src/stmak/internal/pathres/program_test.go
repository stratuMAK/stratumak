// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// setNCFilesDir points the build-time nc_files path at a test directory.
func setNCFilesDir(t *testing.T, dir string) {
	t.Helper()
	saved := config.EMC2NCFilesDir
	config.EMC2NCFilesDir = dir
	t.Cleanup(func() { config.EMC2NCFilesDir = saved })
}

// The install-tree nc_files directory is a program root even without an INI
// (halrun mode), so the stock demo programs stay openable everywhere.
func TestProgramDirsIncludeNCFilesDir(t *testing.T) {
	nc := t.TempDir()
	setNCFilesDir(t, nc)

	resolved, err := filepath.EvalSymlinks(nc)
	if err != nil {
		t.Fatal(err)
	}
	for _, get := range []func(string, string) string{nil, func(string, string) string { return "" }} {
		dirs := ProgramDirs(get, t.TempDir())
		found := false
		for _, d := range dirs {
			if d == resolved {
				found = true
			}
		}
		if !found {
			t.Errorf("ProgramDirs = %v; want the nc_files dir %s included", dirs, resolved)
		}
	}
}

// A program under nc_files resolves; a symlink inside nc_files pointing
// outside is refused FOR CONTAINMENT — the assertion is on the reason, not
// just the refusal, so removing the containment check fails this test rather
// than passing it on a "not found".
func TestProgramResolveNCFilesContainment(t *testing.T) {
	nc := t.TempDir()
	setNCFilesDir(t, nc)
	demo := filepath.Join(nc, "3dtest.ngc")
	if err := os.WriteFile(demo, []byte("(demo)\nM2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "secret.ngc")
	if err := os.WriteFile(outside, []byte("stolen"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(nc, "escape.ngc")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	r, err := New(base, "")
	if err != nil {
		t.Fatal(err)
	}
	pr := r.WithRoots(ProgramDirs(nil, base)...)

	if got, err := pr.Resolve(demo, Read); err != nil {
		t.Errorf("demo program under nc_files refused: %v", err)
	} else if want, _ := filepath.EvalSymlinks(demo); got != want {
		t.Errorf("resolved to %s, want %s", got, want)
	}

	_, err = pr.Resolve(escape, Read)
	if err == nil {
		t.Fatal("symlink escaping nc_files was resolved")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("escape refused for the wrong reason: %v", err)
	}
}
