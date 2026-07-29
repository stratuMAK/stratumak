// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"os"
	"path/filepath"
	"testing"
)

// The filtered-output path under os.TempDir() is predictable, and the server
// can hold cap_dac_override — so creation must refuse to adopt anything this
// process did not make itself. Each refusal below checks the WHY: a test that
// only sees "some error" would pass for the wrong reason.

func TestEnsureFilteredDirCreatesPrivateDirs(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "gomc-filtered-test")
	dir := filepath.Join(parent, "mill1")
	if err := EnsureFilteredDir(dir); err != nil {
		t.Fatalf("EnsureFilteredDir: %v", err)
	}
	for _, d := range []string{parent, dir} {
		fi, err := os.Lstat(d)
		if err != nil {
			t.Fatalf("%s not created: %v", d, err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %o, want 0700 (nothing else may read or plant files)", d, fi.Mode().Perm())
		}
	}
	// Idempotent on its own previous run.
	if err := EnsureFilteredDir(dir); err != nil {
		t.Fatalf("EnsureFilteredDir (second run): %v", err)
	}
}

func TestEnsureFilteredDirRefusesSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "gomc-filtered-test")
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}
	err := EnsureFilteredDir(filepath.Join(parent, "mill1"))
	if err == nil {
		t.Fatal("a symlink planted at the filtered dir was adopted; writes would land in the attacker's target")
	}
	if !contains(err.Error(), "not a directory") {
		t.Errorf("refusal reason = %q, want it to say the path is not a (real) directory", err)
	}
	// Nothing may have been created inside the target through the link.
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("creation followed the symlink into %s: %v", target, entries)
	}
}

func TestEnsureFilteredDirRefusesPlainFile(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "gomc-filtered-test")
	if err := os.WriteFile(parent, []byte("squat"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EnsureFilteredDir(filepath.Join(parent, "mill1"))
	if err == nil {
		t.Fatal("a plain file squatting on the filtered dir was accepted")
	}
	if !contains(err.Error(), "not a directory") {
		t.Errorf("refusal reason = %q, want 'not a directory'", err)
	}
}

func TestEnsureFilteredDirTightensLoosePermissions(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "gomc-filtered-test")
	// Our own dir, but world-writable — a foreign process could swap files
	// inside. It must come out 0700, not be trusted as-is.
	if err := os.Mkdir(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "mill1")
	if err := EnsureFilteredDir(dir); err != nil {
		t.Fatalf("EnsureFilteredDir on own loose dir: %v", err)
	}
	fi, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("parent left at %o, want tightened to 0700", fi.Mode().Perm())
	}
}

func TestInFilteredDir(t *testing.T) {
	inside := filepath.Join(FilteredDir(), "mill1", "shape.ngc")
	if !InFilteredDir(inside) {
		t.Errorf("InFilteredDir(%q) = false, want true", inside)
	}
	// A sibling whose name merely shares the prefix is outside.
	sibling := FilteredDir() + "-evil/shape.ngc"
	if InFilteredDir(sibling) {
		t.Errorf("InFilteredDir(%q) = true; prefix-string matching would let a sibling dir pose as ours", sibling)
	}
	if InFilteredDir("/somewhere/else.ngc") {
		t.Error("unrelated path reported as filtered output")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
