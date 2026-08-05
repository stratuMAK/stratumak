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

// TestParseHeaderRejectsZeroPins pins the guard that stops a consumer spinning.
// A "cfg:" with no type chars makes every sample zero bytes wide, and
// halsampler's frame walk (`for off+sampleSize <= len(data); off += sampleSize`)
// then never advances — it printed empty lines forever. The header is the only
// place that can tell, so it refuses the config outright.
func TestParseHeaderRejectsZeroPins(t *testing.T) {
	if types, ok := ParseHeader([]byte("cfg:")); ok {
		t.Errorf("ParseHeader(cfg:) = %q,true; want a rejection", types)
	}
}

// TestReadRawShortFrame pins that a truncated frame yields zero rather than
// panicking. Frames come off the wire, so their length is not ours to trust.
func TestReadRawShortFrame(t *testing.T) {
	frame := make([]byte, ValueSize) // room for exactly one pin
	WriteRaw(frame, 0, 0x1122334455667788)

	if got := ReadRaw(frame, 0); got != 0x1122334455667788 {
		t.Errorf("ReadRaw(frame,0) = %#x, want the written value", got)
	}
	for _, i := range []int{1, 2, -1} {
		if got := ReadRaw(frame, i); got != 0 {
			t.Errorf("ReadRaw past the end (i=%d) = %#x, want 0", i, got)
		}
	}
	if got := ReadRaw(frame[:3], 0); got != 0 {
		t.Errorf("ReadRaw of a partial value = %#x, want 0", got)
	}
	if got := ReadRaw(nil, 0); got != 0 {
		t.Errorf("ReadRaw(nil) = %#x, want 0", got)
	}
}

func TestEncodeRejectsOutOfRange(t *testing.T) {
	for _, c := range []struct {
		typeChar byte
		token    string
	}{
		{TypeBit, "2"}, // ParseUint(...,1) — a bit is 0 or 1
		{TypeBit, "-1"},
		{TypeU32, "4294967296"}, // one past uint32
		{TypeU32, "-1"},
		{TypeS32, "2147483648"}, // one past int32
		{TypeS32, "-2147483649"},
		{TypeFloat, "not a number"},
	} {
		if _, err := Encode(c.typeChar, c.token); err == nil {
			t.Errorf("Encode(%q, %q) succeeded, want a range/parse error", string(c.typeChar), c.token)
		}
	}
}

func TestHTTPToWS(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:5080", "ws://127.0.0.1:5080"},
		{"https://host:5080", "wss://host:5080"},
		{"http://host/base", "ws://host/base"},
		// A schemeless STMAK_REST_URL passes through unchanged; the WebSocket
		// dial then rejects it. Documented rather than papered over — the
		// variable is specified as a base URL and its default carries a
		// scheme, and a dial error names the bad value plainly.
		{"127.0.0.1:5080", "127.0.0.1:5080"},
	} {
		if got := HTTPToWS(tc.in); got != tc.want {
			t.Errorf("HTTPToWS(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
