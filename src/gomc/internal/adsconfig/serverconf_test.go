// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package adsconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

// TestServerConfDefaults pins the security-relevant defaults: the listener binds
// loopback (ADS_REVIEW_FINDINGS.md A9) and the resource caps default to 8/256
// (A7). A minimal config with no $ directives must produce these.
func TestServerConfDefaults(t *testing.T) {
	conf, _, _, err := ParseConfFile(writeConf(t, "aVar : BOOL\n"))
	if err != nil {
		t.Fatalf("ParseConfFile: %v", err)
	}
	if conf.Bind != "127.0.0.1" {
		t.Errorf("default Bind = %q, want 127.0.0.1 (loopback; exposure must be opt-in)", conf.Bind)
	}
	if conf.Port != 48898 {
		t.Errorf("default Port = %d, want 48898", conf.Port)
	}
	if conf.MaxConnections != 8 {
		t.Errorf("default MaxConnections = %d, want 8", conf.MaxConnections)
	}
	if conf.MaxSubscriptions != 256 {
		t.Errorf("default MaxSubscriptions = %d, want 256", conf.MaxSubscriptions)
	}
}

// TestServerConfOverrides verifies every $ directive overrides its default.
func TestServerConfOverrides(t *testing.T) {
	body := "$bind 0.0.0.0\n$port 12345\n$max-connections 32\n$max-subscriptions 1024\naVar : BOOL\n"
	conf, _, _, err := ParseConfFile(writeConf(t, body))
	if err != nil {
		t.Fatalf("ParseConfFile: %v", err)
	}
	if conf.Bind != "0.0.0.0" {
		t.Errorf("Bind = %q, want 0.0.0.0", conf.Bind)
	}
	if conf.Port != 12345 {
		t.Errorf("Port = %d, want 12345", conf.Port)
	}
	if conf.MaxConnections != 32 {
		t.Errorf("MaxConnections = %d, want 32", conf.MaxConnections)
	}
	if conf.MaxSubscriptions != 1024 {
		t.Errorf("MaxSubscriptions = %d, want 1024", conf.MaxSubscriptions)
	}
}

// TestServerConfCapValidation rejects non-positive / non-numeric cap values.
func TestServerConfCapValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"zero-conns", "$max-connections 0\naVar : BOOL\n"},
		{"negative-conns", "$max-connections -1\naVar : BOOL\n"},
		{"nan-conns", "$max-connections lots\naVar : BOOL\n"},
		{"zero-subs", "$max-subscriptions 0\naVar : BOOL\n"},
		{"negative-subs", "$max-subscriptions -5\naVar : BOOL\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := ParseConfFile(writeConf(t, tc.body)); err == nil {
				t.Fatalf("expected error for %q, got nil", strings.TrimSpace(tc.body))
			}
		})
	}
}
