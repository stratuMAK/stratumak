// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"fmt"
	"testing"
)

// TestRcForSeparatesRefusedFromExecuted pins the distinction the emccmd contract
// rests on. Both classes used to be silent (REST swallowed every provider
// error); making both transport errors is the opposite mistake, and it broke the
// tool-table tests, which deliberately issue a command the interpreter rejects
// (`G10 L1 P0` → "P value out of range") and then read the resulting state.
func TestRcForSeparatesRefusedFromExecuted(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		rc, err := rcFor(nil)
		if rc != rcsDone || err != nil {
			t.Errorf("rcFor(nil) = %d, %v; want %d, nil", rc, err, rcsDone)
		}
	})

	t.Run("refused is a transport error", func(t *testing.T) {
		// Nothing reached the machine; the request was invalid in this state.
		refusal := errors.New("Must be in MDI mode to issue MDI command")
		rc, err := rcFor(refusal)
		if rc != rcsError {
			t.Errorf("rc = %d, want %d", rc, rcsError)
		}
		if err == nil {
			t.Fatal("a refusal must surface as an error, or the caller cannot tell " +
				"the command was never accepted")
		}
		if !errors.Is(err, refusal) {
			t.Errorf("the reason was lost: %v", err)
		}
	})

	t.Run("executed and faulted is not a transport error", func(t *testing.T) {
		// The command reached the machine and the fault is already on the error
		// channel; the caller needs to read the state that resulted.
		rc, err := rcFor(executed(fmt.Errorf("MDI execute: %w",
			errors.New("P value out of range with G10 L1"))))
		if err != nil {
			t.Errorf("an executed-and-faulted command must not be a transport error, got %v", err)
		}
		if rc != rcsError {
			t.Errorf("rc = %d, want RCS_ERROR (%d) — the outcome still has to be reported",
				rc, rcsError)
		}
	})

	t.Run("wrapping survives errors.As through fmt.Errorf", func(t *testing.T) {
		wrapped := fmt.Errorf("outer: %w", executed(errors.New("inner")))
		if _, err := rcFor(wrapped); err != nil {
			t.Error("an executed error wrapped further up must still be recognised")
		}
	})

	t.Run("executed(nil) stays nil", func(t *testing.T) {
		if executed(nil) != nil {
			t.Error("executed(nil) must not manufacture an error")
		}
	})
}
