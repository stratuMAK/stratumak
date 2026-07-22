// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"io"
	"log/slog"
	"testing"
)

// TestTryImportLegacy_NilIni pins the nil-INI guard.  The launcher passes
// ini == nil when it runs without an INI file (halrun mode), and pkg/inifile's
// methods dereference the receiver immediately, so m.ini.Get("EMCIO",
// "TOOL_TABLE") killed the process on the first-run legacy import.  With no
// INI there is simply no TOOL_TABLE to import from.
func TestTryImportLegacy_NilIni(t *testing.T) {
	m := &module{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		name:   "tooltable",
	}
	if m.ini != nil {
		t.Fatal("precondition: expected a nil ini")
	}
	m.tryImportLegacy() // must return quietly, not panic
}
