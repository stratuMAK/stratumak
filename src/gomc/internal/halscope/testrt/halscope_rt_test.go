// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package testrt

import "testing"

// TestHalscopeRT runs the C halscope RT suite (state transitions, trigger
// detection, capture and ring-buffer linearization — 26 tests over three
// suites). greatest prints the per-test breakdown to stdout, which `go test`
// shows when this fails.
func TestHalscopeRT(t *testing.T) {
	if failed := RunAll(); failed != 0 {
		t.Errorf("halscope RT suite: %d assertion(s) failed — see the greatest report above", failed)
	}
}
