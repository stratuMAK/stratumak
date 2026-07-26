// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// The three iniX helpers back the //export'ed gomc_ini_* callbacks, where a
// panic would unwind into a C caller and kill the process.  l.ini is nil
// whenever the launcher runs without an INI file (halrun mode never sets it)
// and pkg/inifile's methods dereference the receiver immediately, so no-INI
// must read as "key not found" rather than crash.

func TestIniHelpers_NoIni(t *testing.T) {
	l := &Launcher{}
	if l.ini != nil {
		t.Fatal("precondition: expected a nil ini")
	}

	if val, ok := l.iniGet("TRAJ", "MAX_VELOCITY"); ok || val != "" {
		t.Errorf("iniGet with no INI = (%q, %v), want (\"\", false)", val, ok)
	}
	if vals := l.iniGetAll("HAL", "HALFILE"); vals != nil {
		t.Errorf("iniGetAll with no INI = %q, want nil", vals)
	}
	// gomc_ini_source_file keeps its "always a valid string" contract (C
	// callers may strlen/strcpy it), so no-INI is "" — never a nil string.
	if src := l.iniSourceFile(); src != "" {
		t.Errorf("iniSourceFile with no INI = %q, want \"\"", src)
	}
}

func TestIniHelpers_WithIni(t *testing.T) {
	dir := t.TempDir()
	path := writeIni(t, dir, "test.ini", `[HAL]
HALFILE = core.hal
HALFILE = io.hal

[TRAJ]
MAX_VELOCITY = 12.5
`)
	l := newLauncherWithIniPath(t, path)

	if val, ok := l.iniGet("TRAJ", "MAX_VELOCITY"); !ok || val != "12.5" {
		t.Errorf("iniGet = (%q, %v), want (\"12.5\", true)", val, ok)
	}
	// A missing key reads the same way as a missing INI — that equivalence is
	// what lets the C side treat both without a special case.
	if val, ok := l.iniGet("TRAJ", "NO_SUCH_KEY"); ok || val != "" {
		t.Errorf("iniGet(missing key) = (%q, %v), want (\"\", false)", val, ok)
	}
	if vals := l.iniGetAll("HAL", "HALFILE"); len(vals) != 2 ||
		vals[0] != "core.hal" || vals[1] != "io.hal" {
		t.Errorf("iniGetAll = %q, want [core.hal io.hal]", vals)
	}
	if src := l.iniSourceFile(); src != path {
		t.Errorf("iniSourceFile = %q, want %q", src, path)
	}
}

// resolveConfigPath backs the //export'ed gomc_path_resolve callback, which C
// modules use for every configuration-supplied path (ethercat config= and its
// nested initCmds files, filestream in/out, hm2_modbus mbccbs=, mb2hal config=,
// xhc-hb04 I=).  The cgo callback itself cannot be unit tested, so the mode
// mapping and the failure contract are pinned here.
func TestResolveConfigPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	pathres.SetDefaultForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "lcec.xml"), []byte("<masters/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// An existing file just outside the config directory — the interesting
	// case, since a non-existent one is refused as "not found" regardless.
	if err := os.WriteFile(filepath.Join(root, "escape.xml"), []byte("<nope/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	l := &Launcher{}

	// GOMC_PATH_READ
	got, err := l.resolveConfigPath("lcec.xml", 0)
	if err != nil {
		t.Fatalf("resolveConfigPath(read): %v", err)
	}
	if want := filepath.Join(dir, "lcec.xml"); got != want {
		t.Errorf("resolveConfigPath = %q, want %q", got, want)
	}

	// GOMC_PATH_WRITE — the target need not exist yet.
	if _, err := l.resolveConfigPath("capture.log", 1); err != nil {
		t.Errorf("resolveConfigPath(write): %v", err)
	}
	// GOMC_PATH_DIR
	if _, err := l.resolveConfigPath("state", 2); err != nil {
		t.Errorf("resolveConfigPath(dir): %v", err)
	}

	// Escaping the allowed roots fails, and the reason is reported — the C
	// caller prints it, so "not found" must stay distinguishable from
	// "outside the allowed directories".
	_, err = l.resolveConfigPath("../escape.xml", 0)
	if err == nil {
		t.Fatal("resolveConfigPath of an escaping path: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("error %q does not explain the containment failure", err)
	}

	// An unknown mode is rejected rather than silently treated as a read.
	if _, err := l.resolveConfigPath("lcec.xml", 99); err == nil {
		t.Error("resolveConfigPath with an unknown mode: want an error, got nil")
	}
}
