// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"regexp"
	"testing"
)

// milltask forwards C-module ERROR messages to the operator message list via a
// global hook that fires for every instance. forwardsErrorFrom is the per-
// instance routing decision fed by the optional error_filter=<regexp> module
// param. This guards both the default (nil filter => forward all) and the
// multi-instance isolation case (e.g. error_filter=^pnp\. on pnp.task).
func TestForwardsErrorFrom(t *testing.T) {
	tests := []struct {
		name      string
		filter    *regexp.Regexp
		component string
		want      bool
	}{
		{"nil filter forwards motmod", nil, "pnp.mot", true},
		{"nil filter forwards anything", nil, "whatever", true},
		{"pnp filter matches own motmod", regexp.MustCompile(`^pnp\.`), "pnp.mot", true},
		{"pnp filter matches own homemod", regexp.MustCompile(`^pnp\.`), "pnp.home.0", true},
		{"pnp filter matches own io", regexp.MustCompile(`^pnp\.`), "pnp.io", true},
		{"pnp filter rejects coat motmod", regexp.MustCompile(`^pnp\.`), "coat.mot", false},
		{"coat filter rejects pnp following error", regexp.MustCompile(`^coat\.`), "pnp.mot", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &milltaskModule{errorFilter: tt.filter}
			if got := m.forwardsErrorFrom(tt.component); got != tt.want {
				t.Errorf("forwardsErrorFrom(%q) = %v, want %v", tt.component, got, tt.want)
			}
		})
	}
}
