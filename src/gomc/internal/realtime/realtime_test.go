// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package realtime

import (
	"testing"
)

// TestNew verifies that New() returns a non-nil Manager with the expected
// defaults.
func TestNew(t *testing.T) {
	m := New(nil)
	if m == nil {
		t.Fatal("New() returned nil")
	}
	if m.logger == nil {
		t.Error("logger should not be nil")
	}
}

// TestStart verifies that Start() succeeds for the uspace environment (there is
// no precondition to validate at this layer).
func TestStart(t *testing.T) {
	m := New(nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned unexpected error: %v", err)
	}
}
