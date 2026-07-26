// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pkgreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "packages.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeConf: %v", err)
	}
	return path
}

// A valid packages.conf parses, with @GOMOD:TAG@ markers honoured against the
// enabled-flag set and stripped from the emitted import path.
func TestReadConfIn_ValidAndMarkers(t *testing.T) {
	path := writeConf(t, `
# comment
gmi generated/gmi/kins
gomod internal/task
gomod external/opt @GOMOD:OPTFEATURE@
gomod external/off @GOMOD:DISABLED@
`)
	reg, err := ReadConfIn(path, map[string]bool{"OPTFEATURE": true})
	if err != nil {
		t.Fatalf("ReadConfIn: %v", err)
	}
	got := make(map[string]EntryType)
	for _, e := range reg.Entries {
		got[e.ImportPath] = e.Type
	}
	if got["generated/gmi/kins"] != TypeGMI {
		t.Errorf("kins type = %q, want gmi", got["generated/gmi/kins"])
	}
	if got["internal/task"] != TypeGomod {
		t.Errorf("task type = %q, want gomod", got["internal/task"])
	}
	if _, ok := got["external/opt"]; !ok {
		t.Error("enabled @GOMOD:OPTFEATURE@ entry was dropped")
	}
	if _, ok := got["external/off"]; ok {
		t.Error("disabled @GOMOD:DISABLED@ entry should have been skipped")
	}
}

// Regression (F1): a typo'd TYPE must fail loudly, not be silently dropped from
// the generated imports (which would make the module vanish at runtime).
func TestReadConfIn_UnknownTypeIsError(t *testing.T) {
	path := writeConf(t, "gomd internal/task\n")
	_, err := ReadConfIn(path, nil)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("error = %v, want it to mention 'unknown type'", err)
	}
}

// A non-empty entry line missing the import path must fail loudly.
func TestReadConfIn_MalformedLineIsError(t *testing.T) {
	path := writeConf(t, "gomod\n")
	_, err := ReadConfIn(path, nil)
	if err == nil {
		t.Fatal("expected error for malformed entry, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error = %v, want it to mention 'malformed'", err)
	}
}
