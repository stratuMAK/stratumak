// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"os"
	"path/filepath"
	"testing"
)

// Canonical exists so that comparing two program paths for equality answers
// "the same file?". These pin the ways the same file can arrive spelled
// differently.

func TestCanonicalResolvesSymlinkedDirectories(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(real, "main.ngc")
	if err := os.WriteFile(prog, []byte("m2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	viaLink := Canonical(filepath.Join(link, "main.ngc"))
	viaReal := Canonical(prog)
	if viaLink != viaReal {
		t.Errorf("the same file spelled through a symlink canonicalises to %q, "+
			"direct to %q — an identity test between them would say 'different'",
			viaLink, viaReal)
	}
}

func TestCanonicalCleansAndAbsolutises(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nc")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(sub, "main.ngc")
	if err := os.WriteFile(prog, []byte("m2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// find_ngc_file's first branch opens a cwd-relative name as-is.
	t.Chdir(dir)
	if got, want := Canonical("nc/main.ngc"), Canonical(prog); got != want {
		t.Errorf("relative %q canonicalises to %q, want %q", "nc/main.ngc", got, want)
	}
	// …and the interpreter composes sub-file paths from SUBROUTINE_PATH, which
	// can leave traversal segments in them.
	if got, want := Canonical(filepath.Join(sub, "..", "nc", "main.ngc")), Canonical(prog); got != want {
		t.Errorf("uncleaned path canonicalises to %q, want %q", got, want)
	}
}

// A path that does not exist cannot be symlink-resolved. Canonical must still
// return the best identity it can rather than an error or an empty string:
// a motion segment's file is reported whether or not the file is still there.
func TestCanonicalFallsBackForMissingPaths(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone", "main.ngc")
	got := Canonical(missing)
	if !filepath.IsAbs(got) {
		t.Errorf("Canonical(%q) = %q, want an absolute path", missing, got)
	}
	if got != filepath.Clean(missing) {
		t.Errorf("Canonical(%q) = %q, want the cleaned absolute form", missing, got)
	}
	if Canonical("") != "" {
		t.Errorf("Canonical(\"\") = %q, want empty (nothing is executing)", Canonical(""))
	}
}
