// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package adsmodule

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestParseArgs covers the load-command argument parsing: config= is required,
// debug= is a "1"/other boolean, unknown keys and non key=value tokens are
// ignored, and a value may itself contain '='.
func TestParseArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantConfig string
		wantDebug  bool
	}{
		{"config-only", []string{"config=hmi.conf"}, "hmi.conf", false},
		{"config-and-debug", []string{"config=hmi.conf", "debug=1"}, "hmi.conf", true},
		{"debug-zero", []string{"config=hmi.conf", "debug=0"}, "hmi.conf", false},
		{"debug-other-is-false", []string{"config=hmi.conf", "debug=yes"}, "hmi.conf", false},
		{"empty", nil, "", false},
		{"debug-without-config", []string{"debug=1"}, "", true},
		{"unknown-key-ignored", []string{"unknown=x", "config=z.conf"}, "z.conf", false},
		{"bare-token-ignored", []string{"noequals", "config=z.conf"}, "z.conf", false},
		{"value-contains-equals", []string{"config=/a/b=c.conf"}, "/a/b=c.conf", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotConfig, gotDebug := parseArgs(tc.args)
			if gotConfig != tc.wantConfig {
				t.Errorf("config = %q, want %q", gotConfig, tc.wantConfig)
			}
			if gotDebug != tc.wantDebug {
				t.Errorf("debug = %v, want %v", gotDebug, tc.wantDebug)
			}
		})
	}
}

// TestNewADSModuleMissingConfig: a load without config= is rejected before any
// HAL component is created.
func TestNewADSModuleMissingConfig(t *testing.T) {
	m, err := newADSModule(nil, discardLogger(), "ads-inst", nil)
	if err == nil {
		t.Fatal("expected an error when config= is missing, got nil")
	}
	if m != nil {
		t.Error("expected a nil module on error")
	}
	if !strings.Contains(err.Error(), "config=") {
		t.Errorf("error should mention the missing config= parameter, got: %v", err)
	}
}

// TestNewADSModuleBadConfigPath: a non-existent (absolute, so the INI dir is not
// consulted) config path fails at parse time, before the HAL component is made.
func TestNewADSModuleBadConfigPath(t *testing.T) {
	m, err := newADSModule(nil, discardLogger(), "ads-inst", []string{"config=/nonexistent/does-not-exist.conf"})
	if err == nil {
		t.Fatal("expected an error for a non-existent config file, got nil")
	}
	if m != nil {
		t.Error("expected a nil module on error")
	}
}
