// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"testing"
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
