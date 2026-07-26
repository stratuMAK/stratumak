// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ngcpreview

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// TestNewNgcPreview_NilIni pins the nil-INI guard.  The launcher passes
// ini == nil when it runs without an INI file (halrun mode), and pkg/inifile's
// methods dereference the receiver immediately, so ini.WithNamespace(ns) and
// ini.SourceFile() killed the process.  Loading must succeed, and the get_file
// allow-list must fall back to the system share dir only — narrower than with
// an INI, never wider.
func TestNewNgcPreview_NilIni(t *testing.T) {
	// The factory registers its API; in the launcher the default registry is
	// set up long before any module loads.
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mod, err := newNgcPreview(nil, logger, "ngcpreview-nilini-test", nil)
	if err != nil {
		t.Fatalf("newNgcPreview with a nil INI: %v", err)
	}
	m, ok := mod.(*ngcPreview)
	if !ok {
		t.Fatalf("newNgcPreview returned %T, want *ngcPreview", mod)
	}
	// Whatever ends up allowed must not come from an INI that does not exist.
	for _, d := range m.allowedDirs {
		if d == "" {
			t.Error("allowedDirs contains an empty entry")
		}
	}
}

// TestProgramDirs_NilIni covers the allow-list builder directly: with no INI
// there are no PROGRAM_PREFIX / SUBROUTINE_PATH entries to add.
func TestProgramDirs_NilIni(t *testing.T) {
	for _, d := range pathres.ProgramDirs(nil, t.TempDir()) {
		if d == "" {
			t.Error("ProgramDirs returned an empty entry")
		}
	}
}
