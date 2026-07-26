// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Config-parsing tests. The XML config is operator-authored and is the only
// untrusted-ish input the bridge takes at load time, so every malformed shape
// must be rejected with a message that names the offending topic — never
// silently defaulted into a half-configured bridge.
package mqttbridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig writes body to a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mqtt.xml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestParseConfigFullyPopulated(t *testing.T) {
	path := writeConfig(t, `
<mqttBridge broker="tcp://broker:1883" clientId="cnc1" username="u" password="p" tls="true">
  <topic path="cnc/spindle/speed" dir="out" type="pin" halType="float" rate="250" qos="1" retain="true"/>
  <topic path="/cnc/status/" dir="in" type="json" publish="delta">
    <pin name="running" type="bit" dir="in"/>
    <pin name="line" type="s32"/>
  </topic>
</mqttBridge>`)

	cfg, err := parseConfig(path)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Broker != "tcp://broker:1883" || cfg.ClientID != "cnc1" ||
		cfg.Username != "u" || cfg.Password != "p" || !cfg.TLS {
		t.Fatalf("connection attributes not parsed: %+v", cfg)
	}
	if len(cfg.Topics) != 2 {
		t.Fatalf("got %d topics, want 2", len(cfg.Topics))
	}

	pinTopic := cfg.Topics[0]
	if pinTopic.Mode != ModePin || pinTopic.Dir != DirOut || pinTopic.HalType != PinTypeFloat {
		t.Errorf("pin topic mis-parsed: %+v", pinTopic)
	}
	if pinTopic.Rate != 250*time.Millisecond || pinTopic.QoS != 1 || !pinTopic.Retain {
		t.Errorf("pin topic transport attrs mis-parsed: %+v", pinTopic)
	}

	jsonTopic := cfg.Topics[1]
	if jsonTopic.Mode != ModeJSON || jsonTopic.Dir != DirIn || jsonTopic.PublishMode != PublishDelta {
		t.Errorf("json topic mis-parsed: %+v", jsonTopic)
	}
	if len(jsonTopic.Pins) != 2 {
		t.Fatalf("got %d pins, want 2", len(jsonTopic.Pins))
	}
	if jsonTopic.Pins[0].Name != "running" || jsonTopic.Pins[0].Type != PinTypeBit || jsonTopic.Pins[0].Dir != DirIn {
		t.Errorf("pin[0] mis-parsed: %+v", jsonTopic.Pins[0])
	}
	// An omitted pin dir defaults to out, not to the topic's direction.
	if jsonTopic.Pins[1].Dir != DirOut || jsonTopic.Pins[1].Type != PinTypeS32 {
		t.Errorf("pin[1] mis-parsed: %+v", jsonTopic.Pins[1])
	}
}

func TestParseConfigDefaults(t *testing.T) {
	// Everything optional omitted: clientId, dir, type, rate, publish.
	path := writeConfig(t, `
<mqttBridge broker="tcp://b:1883">
  <topic path="a/b">
    <pin name="x" type="u32"/>
  </topic>
</mqttBridge>`)

	cfg, err := parseConfig(path)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ClientID != "linuxcnc-mqtt" {
		t.Errorf("ClientID = %q, want the default", cfg.ClientID)
	}
	tc := cfg.Topics[0]
	if tc.Dir != DirOut {
		t.Errorf("default dir = %v, want DirOut", tc.Dir)
	}
	if tc.Mode != ModeJSON {
		t.Errorf("default type = %v, want ModeJSON", tc.Mode)
	}
	if tc.Rate != 100*time.Millisecond {
		t.Errorf("default rate = %v, want 100ms", tc.Rate)
	}
	if tc.PublishMode != PublishFull {
		t.Errorf("default publish = %v, want PublishFull", tc.PublishMode)
	}
}

func TestParseConfigAliasesAndRateFloor(t *testing.T) {
	path := writeConfig(t, `
<mqttBridge broker="tcp://b:1883">
  <topic path="p" dir="PUBLISH" type="pin" halType="BIT" rate="0" publish="ALWAYS"/>
  <topic path="s" dir="Subscribe" type="pin" halType="s32" rate="-5"/>
</mqttBridge>`)

	cfg, err := parseConfig(path)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Topics[0].Dir != DirOut || cfg.Topics[0].HalType != PinTypeBit {
		t.Errorf("uppercase attributes not accepted: %+v", cfg.Topics[0])
	}
	if cfg.Topics[0].PublishMode != PublishAlways {
		t.Errorf("publish=ALWAYS not accepted: %+v", cfg.Topics[0])
	}
	// A zero/negative rate must fall back to the default, never produce a
	// zero-period ticker (time.NewTicker panics on <= 0).
	for i, tc := range cfg.Topics {
		if tc.Rate != 100*time.Millisecond {
			t.Errorf("topic[%d] rate = %v, want the 100ms fallback", i, tc.Rate)
		}
	}
}

func TestParseConfigRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing broker", `<mqttBridge><topic path="a" type="pin" halType="bit"/></mqttBridge>`},
		{"malformed XML", `<mqttBridge broker="b"><topic></mqttBridge>`},
		{"topic without path", `<mqttBridge broker="b"><topic type="pin" halType="bit"/></mqttBridge>`},
		{"invalid dir", `<mqttBridge broker="b"><topic path="a" dir="sideways" type="pin" halType="bit"/></mqttBridge>`},
		{"invalid type", `<mqttBridge broker="b"><topic path="a" type="blob"/></mqttBridge>`},
		{"pin mode without halType", `<mqttBridge broker="b"><topic path="a" type="pin"/></mqttBridge>`},
		{"pin mode bad halType", `<mqttBridge broker="b"><topic path="a" type="pin" halType="double"/></mqttBridge>`},
		{"json mode without pins", `<mqttBridge broker="b"><topic path="a" type="json"/></mqttBridge>`},
		{"json pin without name", `<mqttBridge broker="b"><topic path="a" type="json"><pin type="bit"/></topic></mqttBridge>`},
		{"json pin bad type", `<mqttBridge broker="b"><topic path="a" type="json"><pin name="x" type="blob"/></topic></mqttBridge>`},
		{"json pin bad dir", `<mqttBridge broker="b"><topic path="a" type="json"><pin name="x" type="bit" dir="sideways"/></topic></mqttBridge>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseConfig(writeConfig(t, tt.body)); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestParseConfigMissingFile(t *testing.T) {
	if _, err := parseConfig(filepath.Join(t.TempDir(), "nope.xml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestParsePinType(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want PinType
	}{
		{"bit", PinTypeBit},
		{"float", PinTypeFloat},
		{"s32", PinTypeS32},
		{"u32", PinTypeU32},
		{"U32", PinTypeU32},
	} {
		got, err := parsePinType(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("parsePinType(%q) = %v, %v; want %v, nil", tt.in, got, err, tt.want)
		}
	}
	if _, err := parsePinType(""); err == nil {
		t.Error("parsePinType(\"\") should fail — an omitted halType is a config error")
	}
}

func TestTopicToHalName(t *testing.T) {
	for in, want := range map[string]string{
		"cnc/spindle/speed": "cnc-spindle-speed",
		"/cnc/status/":      "cnc-status",
		"flat":              "flat",
		"":                  "",
	} {
		if got := topicToHalName(in); got != want {
			t.Errorf("topicToHalName(%q) = %q, want %q", in, got, want)
		}
	}
}
