// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"io"
	"log/slog"
	"testing"
)

// TestTryImportLegacy_NoInitTbl pins the "nothing to seed from" guard on a bare
// struct, with no persist client bound.  The store used to read
// [EMCIO]TOOL_TABLE from an INI the launcher passes as nil in halrun mode, and
// pkg/inifile's methods dereference the receiver immediately, so the first-run
// import killed the process.  The INI is gone — what to seed from is named by
// init_tbl= — and the same shape of bug now looks like this: an unset
// parameter must return quietly, before anything reaches the database.
func TestTryImportLegacy_NoInitTbl(t *testing.T) {
	m := &module{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		name:   "tooltable",
	}
	if m.initTbl != "" {
		t.Fatal("precondition: expected no init_tbl")
	}
	m.tryImportLegacy() // must return quietly, not panic
}
