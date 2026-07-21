// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halstream

import "testing"

func TestParseHeader(t *testing.T) {
	types, ok := ParseHeader([]byte("cfg:uffb"))
	if !ok || types != "uffb" {
		t.Errorf("ParseHeader(cfg:uffb) = %q,%v; want uffb,true", types, ok)
	}
	if _, ok := ParseHeader([]byte("nope")); ok {
		t.Errorf("ParseHeader(nope) ok=true; want false")
	}
}

// TestEncodeDecodeRoundTrip exercises the encode→wire→decode path for each pin
// type (the contract halstreamer produces and halsampler consumes), through the
// 8-byte frame codec.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		typeChar byte
		token    string
		want     any
	}{
		{TypeFloat, "3.5", 3.5},
		{TypeBit, "1", true},
		{TypeBit, "0", false},
		{TypeU32, "4000000000", uint32(4000000000)},
		{TypeS32, "-2000000000", int32(-2000000000)},
	}
	for _, c := range cases {
		raw, err := Encode(c.typeChar, c.token)
		if err != nil {
			t.Fatalf("Encode(%q,%q): %v", string(c.typeChar), c.token, err)
		}
		frame := make([]byte, ValueSize)
		WriteRaw(frame, 0, raw)
		got, err := Decode(c.typeChar, ReadRaw(frame, 0))
		if err != nil {
			t.Fatalf("Decode(%q): %v", string(c.typeChar), err)
		}
		if got != c.want {
			t.Errorf("round-trip %q %q = %v (%T); want %v (%T)", string(c.typeChar), c.token, got, got, c.want, c.want)
		}
	}
}

func TestUnknownType(t *testing.T) {
	if _, err := Decode('x', 0); err == nil {
		t.Errorf("Decode(x) err=nil; want error")
	}
	if _, err := Encode('x', "1"); err == nil {
		t.Errorf("Encode(x) err=nil; want error")
	}
}
