// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package emccalib

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/calibreg"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestEmccalib builds a module from a set of calibreg mappings, bypassing
// the API registration (which needs a unique instance name per call).
func newTestEmccalib(t *testing.T, mappings ...calibreg.IniPinMapping) *emccalib {
	t.Helper()
	calibreg.Reset()
	t.Cleanup(calibreg.Reset)
	for _, m := range mappings {
		calibreg.Record(m)
	}
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	mod, err := newEmccalib(nil, testLogger(), t.Name(), nil)
	if err != nil {
		t.Fatalf("newEmccalib: %v", err)
	}
	return mod.(*emccalib)
}

// TestIndexTracksTunableUpdates pins the aliasing bug that the index used to
// carry: it stored &e.tunables[len-1] taken *during* the append loop, so every
// pointer captured before a reallocation aliased an orphaned backing array.
// The visible symptom was Revert: SaveIni writes the freshly-saved value into
// e.tunables[i].iniValue, but Revert read iniValue through the index and so
// reverted to the startup value forever. Four mappings is enough to force the
// 2→4 growth that strands the first two.
func TestIndexTracksTunableUpdates(t *testing.T) {
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: "pid.0.Pgain", IniValue: 1},
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "I", Pin: "pid.0.Igain", IniValue: 2},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "P", Pin: "pid.1.Pgain", IniValue: 3},
		calibreg.IniPinMapping{Section: "JOINT_1", Key: "I", Pin: "pid.1.Igain", IniValue: 4},
	)

	// Stand in for SaveIni's write-back over the slice.
	for i := range e.tunables {
		e.tunables[i].iniValue = 99
	}

	for _, tc := range []struct{ section, key string }{
		{"JOINT_0", "P"}, {"JOINT_0", "I"}, {"JOINT_1", "P"}, {"JOINT_1", "I"},
	} {
		got := e.lookup(tc.section, tc.key)
		if got == nil {
			t.Fatalf("%s/%s not in the index", tc.section, tc.key)
		}
		if got.iniValue != 99 {
			t.Errorf("%s/%s: lookup sees iniValue %v, slice holds 99 — index is stale",
				tc.section, tc.key, got.iniValue)
		}
	}
}

func TestLookupUnknownKey(t *testing.T) {
	e := newTestEmccalib(t,
		calibreg.IniPinMapping{Section: "JOINT_0", Key: "P", Pin: "pid.0.Pgain", IniValue: 1},
	)
	if got := e.lookup("JOINT_0", "NOPE"); got != nil {
		t.Errorf("lookup of an unrecorded key returned %+v, want nil", got)
	}
	if got := e.lookup("NOSUCH", "P"); got != nil {
		t.Errorf("lookup in an unrecorded section returned %+v, want nil", got)
	}
}

func TestReplaceINIValue(t *testing.T) {
	for _, tc := range []struct {
		name, line, newVal, want string
	}{
		{"spaced", "P = 100", "150.5", "P = 150.5"},
		{"unspaced", "P=100", "150.5", "P=150.5"},
		{"indented", "\tP = 100", "150.5", "\tP = 150.5"},
		// The inline comment is an operator annotation; a save used to drop it.
		{"inline comment", "P = 100 # tuned 2024", "150.5", "P = 150.5 # tuned 2024"},
		{"tab comment", "P = 100\t# tuned", "150.5", "P = 150.5\t# tuned"},
		// ';' is data, not a comment — same rule the INI parser reads by.
		{"semicolon is data", "CMD = G0 Z25;X0", "1", "CMD = 1"},
		// '#' not preceded by whitespace is data too.
		{"hash as data", "COLOR = #ff0000", "1", "COLOR = 1"},
		{"no assignment", "[JOINT_0]", "1", "[JOINT_0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replaceINIValue(tc.line, tc.newVal); got != tc.want {
				t.Errorf("replaceINIValue(%q, %q) = %q, want %q", tc.line, tc.newVal, got, tc.want)
			}
		})
	}
}

func TestIniLineKey(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"P = 100", "P"},
		{"  MAX_VELOCITY   =  5", "MAX_VELOCITY"},
		{"# P = 100", ""},
		{"; P = 100", ""},
		{"[JOINT_0]", ""},
		{"", ""},
		{"no equals here", ""},
	} {
		if got := iniLineKey(tc.line); got != tc.want {
			t.Errorf("iniLineKey(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestUpdateINIFileVerifiesKey pins the guard against writing to a stale line
// number. Source lines are captured when the INI is parsed at startup; if the
// file is edited on disk before the operator hits save, the recorded line can
// now hold a different key — which used to be overwritten silently.
func TestUpdateINIFileVerifiesKey(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	path := filepath.Join(dir, "test.ini")
	const original = "[JOINT_0]\nMAX_VELOCITY = 5\nMAX_ACCELERATION = 50\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &emccalib{logger: testLogger()}

	// Line 3 really is MAX_ACCELERATION — this one must land.
	if err := e.updateINIFile(path, []tunable{
		{key: "MAX_ACCELERATION", sourceLine: 3, iniValue: 75},
	}); err != nil {
		t.Fatalf("updateINIFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const wantApplied = "[JOINT_0]\nMAX_VELOCITY = 5\nMAX_ACCELERATION = 75\n"
	if string(got) != wantApplied {
		t.Fatalf("after a matching save:\n got %q\nwant %q", got, wantApplied)
	}

	// Now claim MAX_VELOCITY lives on line 3 (it does not, any more). The
	// mismatch must be refused, leaving MAX_ACCELERATION alone.
	if err := e.updateINIFile(path, []tunable{
		{key: "MAX_VELOCITY", sourceLine: 3, iniValue: 999},
	}); err != nil {
		t.Fatalf("updateINIFile with a stale line: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != wantApplied {
		t.Errorf("a stale line number overwrote an unrelated key:\n got %q\nwant %q", got, wantApplied)
	}
}

// TestUpdateINIFileBackup pins that a save leaves the pre-save contents in .bak.
func TestUpdateINIFileBackup(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	path := filepath.Join(dir, "test.ini")
	const original = "[JOINT_0]\nP = 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &emccalib{logger: testLogger()}
	if err := e.updateINIFile(path, []tunable{{key: "P", sourceLine: 2, iniValue: 2}}); err != nil {
		t.Fatalf("updateINIFile: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(bak) != original {
		t.Errorf("backup holds %q, want the pre-save %q", bak, original)
	}
}
