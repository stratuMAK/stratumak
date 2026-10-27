// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halstream

import (
	"encoding/binary"
	"testing"
)

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

func segment(first, count uint32, sampleSize int) []byte {
	b := make([]byte, SegmentHeaderSize+int(count)*sampleSize)
	binary.LittleEndian.PutUint32(b, first)
	binary.LittleEndian.PutUint32(b[4:], count)
	for i := SegmentHeaderSize; i < len(b); i++ {
		b[i] = byte(i)
	}
	return b
}

func TestSplitSegments(t *testing.T) {
	const size = 16
	msg := append(segment(7, 2, size), segment(9, 1, size)...)
	segs, err := SplitSegments(msg, size)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].First != 7 || segs[0].Count != 2 || len(segs[0].Data) != 2*size ||
		segs[1].First != 9 || segs[1].Count != 1 || len(segs[1].Data) != size {
		t.Fatalf("segments = %+v", segs)
	}

	for name, bad := range map[string][]byte{
		"stray bytes":   append(segment(1, 1, size), 0, 0, 0),
		"truncated":     segment(1, 2, size)[:SegmentHeaderSize+size],
		"empty segment": segment(1, 0, size),
	} {
		if _, err := SplitSegments(bad, size); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSequence(t *testing.T) {
	var q Sequence
	steps := []struct {
		first     uint32
		count     int
		lost      uint32
		tag       uint64
		wantError bool
	}{
		{first: 1000, count: 5, lost: 0, tag: 0},    // starts wherever the sampler is
		{first: 1005, count: 3, lost: 0, tag: 5},    // contiguous
		{first: 1100, count: 2, lost: 92, tag: 100}, // a gap: the tag jumps by the loss
		{first: 1102, count: 1, lost: 0, tag: 102},  //
		{first: 1050, count: 1, wantError: true},    // backwards
	}
	for i, s := range steps {
		lost, tag, err := q.Advance(Segment{First: s.first, Count: s.count})
		if s.wantError {
			if err == nil {
				t.Errorf("step %d: no error for a segment going backwards", i)
			}
			continue
		}
		if err != nil || lost != s.lost || tag != s.tag {
			t.Errorf("step %d: lost=%d tag=%d err=%v, want lost=%d tag=%d", i, lost, tag, err, s.lost, s.tag)
		}
	}
}

// The RT index is a uint32 and wraps after 2^32 samples (~50 days at 1 kHz);
// that is not a loss, and tags keep counting up.
func TestSequenceWraps(t *testing.T) {
	var q Sequence
	if _, _, err := q.Advance(Segment{First: 0xfffffffe, Count: 2}); err != nil {
		t.Fatal(err)
	}
	lost, tag, err := q.Advance(Segment{First: 0, Count: 1})
	if err != nil || lost != 0 || tag != 2 {
		t.Errorf("across the wrap: lost=%d tag=%d err=%v, want 0, 2, nil", lost, tag, err)
	}
}
