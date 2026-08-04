// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package mqttbridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
)

func TestParseConfigArg(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"config=mqtt.xml"}, "mqtt.xml"},
		{[]string{"dryrun", "config=/etc/mqtt.xml"}, "/etc/mqtt.xml"},
		{[]string{"config="}, ""},
		{[]string{"configx=a.xml"}, ""},
		{[]string{"config"}, ""},
		// First wins — a repeated key is a config mistake, not an override.
		{[]string{"config=a.xml", "config=b.xml"}, "a.xml"},
		// A value containing '=' survives intact (e.g. a query-ish suffix).
		{[]string{"config=a=b.xml"}, "a=b.xml"},
	}
	for _, tt := range tests {
		if got := parseConfigArg(tt.args); got != tt.want {
			t.Errorf("parseConfigArg(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestParseDryrunArg(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"dryrun"}, true},
		{[]string{"config=a.xml", "dryrun"}, true},
		{[]string{"dryrun=1"}, false}, // only the bare flag counts
		{[]string{"DRYRUN"}, false},
	}
	for _, tt := range tests {
		if got := parseDryrunArg(tt.args); got != tt.want {
			t.Errorf("parseDryrunArg(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestNewMQTTBridgeRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)

	if _, err := newMQTTBridge(nil, testLogger(), uniq("mqttmod"), nil); err == nil {
		t.Error("a load line without config= must be rejected")
	}
	if _, err := newMQTTBridge(nil, testLogger(), uniq("mqttmod"),
		[]string{"config=missing.xml"}); err == nil {
		t.Error("an unresolvable config path must be rejected")
	}
	// Containment: a path escaping the config root is refused by pathres, so a
	// load line cannot make the bridge read arbitrary files.
	if _, err := newMQTTBridge(nil, testLogger(), uniq("mqttmod"),
		[]string{"config=../../etc/passwd"}); err == nil {
		t.Error("a path outside the config root must be rejected")
	}

	// A resolvable but malformed config fails at parse time, before any HAL
	// component is created.
	bad := filepath.Join(dir, "bad.xml")
	if err := os.WriteFile(bad, []byte(`<mqttBridge/>`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := newMQTTBridge(nil, testLogger(), uniq("mqttmod"),
		[]string{"config=bad.xml"}); err == nil {
		t.Error("a config without a broker must be rejected")
	}
}

func TestNewMQTTBridgeDryrunLifecycle(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)

	cfgPath := filepath.Join(dir, "mqtt.xml")
	body := `<mqttBridge broker="tcp://unused:1883">
  <topic path="cnc/x" dir="out" type="pin" halType="float" rate="5" publish="always"/>
  <topic path="cnc/y" dir="in" type="pin" halType="bit"/>
</mqttBridge>`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mod, err := newMQTTBridge(nil, testLogger(), uniq("mqttmod"),
		[]string{"config=mqtt.xml", "dryrun"})
	if err != nil {
		t.Fatalf("newMQTTBridge: %v", err)
	}

	// The full module lifecycle must run without a broker: Start spins up the
	// publish loop, Stop joins it, Destroy releases the HAL component.
	if err := mod.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mod.Stop()
	mod.Destroy()
}
