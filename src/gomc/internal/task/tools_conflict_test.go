// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tools"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// TestCheckToolConflict pins the pre-check that produces the readable 409 for
// the REST PUT path (the tooltable module's atomic backstop can only surface a
// bare rc through the in-process shim).
func TestCheckToolConflict(t *testing.T) {
	entry := func(updated int64) *tools.ToolEntry {
		return &tools.ToolEntry{Toolno: 5, Updated: updated}
	}
	wantState := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("want a conflict error, got nil")
		}
		var f *apiserver.Fault
		if !errors.As(err, &f) || f.Kind != apiserver.FaultState {
			t.Fatalf("conflict must be FaultState (HTTP 409), got %v", err)
		}
	}

	// Zero baseline = last-write-wins caller: never a conflict, whatever the
	// current state is.
	if err := checkToolConflict(entry(111), 0, 5); err != nil {
		t.Errorf("zero baseline: %v", err)
	}
	if err := checkToolConflict(nil, 0, 5); err != nil {
		t.Errorf("zero baseline against nil: %v", err)
	}

	// Matching baseline: no conflict.
	if err := checkToolConflict(entry(111), 111, 5); err != nil {
		t.Errorf("matching baseline: %v", err)
	}

	// Moved-on baseline: FaultState.
	wantState(t, checkToolConflict(entry(222), 111, 5))

	// Deleted tool (zero entry / nil): a stale dialog must not recreate it.
	wantState(t, checkToolConflict(&tools.ToolEntry{}, 111, 5))
	wantState(t, checkToolConflict(nil, 111, 5))
}
