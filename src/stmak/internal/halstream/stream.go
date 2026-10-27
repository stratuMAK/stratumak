// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package halstream implements the shared wire framing for the HAL streaming
// WebSocket transport: halsampler consumes it (decode), halstreamer produces it
// (encode). The authoritative binary layout lives in the C cmod header
// hal/components/hal_stream_common.h — this package MUST match it. Housing the
// framing (the "cfg:" header, the 8-byte value width, and the type-char codec)
// in one place stops the two CLIs from silently drifting apart, or from the
// server, when the format changes — the failure mode that bit the hand-written
// ethercat client.
package halstream

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// HTTPToWS converts a stmakd base URL into its WebSocket form. It lives
// here for the same reason the framing does: halsampler and halstreamer each
// carried an identical copy.
func HTTPToWS(httpURL string) string {
	u, err := url.Parse(httpURL)
	if err != nil {
		return strings.Replace(strings.Replace(httpURL, "https://", "wss://", 1), "http://", "ws://", 1)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	return u.String()
}

const (
	// CfgPrefix marks the first WebSocket message from the server:
	// "cfg:<types>", where <types> is one type char per streamed pin.
	CfgPrefix = "cfg:"

	// ValueSize is the wire width of a single pin value: every value occupies 8
	// bytes (a little-endian hal_stream_val_t union), regardless of HAL type.
	ValueSize = 8
)

// Pin value type chars (one per pin, in the cfg header). These mirror the C
// cmod's hal_stream_common.h.
const (
	TypeFloat = 'f' // HAL_FLOAT -> float64
	TypeBit   = 'b' // HAL_BIT   -> bool (0/1)
	TypeU32   = 'u' // HAL_U32   -> uint32
	TypeS32   = 's' // HAL_S32   -> int32
)

// ParseHeader parses the "cfg:<types>" header message, returning the per-pin
// type chars. ok is false if the message is not a cfg header, or if it declares
// no pins — a zero-pin config makes every frame zero bytes wide, which is not a
// stream any client can make progress on. Rejecting it here is what stops a
// consumer looping forever on an empty sample size.
func ParseHeader(msg []byte) (types string, ok bool) {
	s := string(msg)
	if !strings.HasPrefix(s, CfgPrefix) {
		return "", false
	}
	types = s[len(CfgPrefix):]
	if types == "" {
		return "", false
	}
	return types, true
}

// ReadRaw returns the raw 8-byte little-endian value of pin i within a frame,
// or 0 if the frame is too short to hold it. Frames arrive off the wire, so a
// truncated one must not panic the caller.
func ReadRaw(frame []byte, i int) uint64 {
	off := i * ValueSize
	if i < 0 || off < 0 || off+ValueSize > len(frame) {
		return 0
	}
	return binary.LittleEndian.Uint64(frame[off:])
}

// WriteRaw stores the raw 8-byte little-endian value of pin i into a frame.
func WriteRaw(frame []byte, i int, raw uint64) {
	binary.LittleEndian.PutUint64(frame[i*ValueSize:], raw)
}

// Decode interprets a raw value according to its pin type char, returning a
// float64, bool, uint32, or int32.
func Decode(typeChar byte, raw uint64) (any, error) {
	switch typeChar {
	case TypeFloat:
		return math.Float64frombits(raw), nil
	case TypeBit:
		return raw != 0, nil
	case TypeU32:
		return uint32(raw), nil
	case TypeS32:
		return int32(raw), nil
	}
	return nil, fmt.Errorf("halstream: unknown pin type %q", string(typeChar))
}

// Encode parses a text token into a raw value according to its pin type char.
func Encode(typeChar byte, token string) (uint64, error) {
	switch typeChar {
	case TypeFloat:
		v, err := strconv.ParseFloat(token, 64)
		return math.Float64bits(v), err
	case TypeBit:
		v, err := strconv.ParseUint(token, 10, 1)
		return v, err
	case TypeU32:
		v, err := strconv.ParseUint(token, 10, 32)
		return v, err
	case TypeS32:
		v, err := strconv.ParseInt(token, 10, 32)
		return uint64(v), err
	}
	return 0, fmt.Errorf("halstream: unknown pin type %q", string(typeChar))
}

// SegmentHeaderSize is the width of the header leading each sampler segment:
// the little-endian uint32 index of the segment's first sample, then its
// sample count (hal_stream_common.h HAL_SAMPLER_SEGMENT_HDR).
const SegmentHeaderSize = 8

// Segment is one run of consecutive samples in a sampler message.
type Segment struct {
	First uint32 // index of the first sample, as counted by the RT sampler
	Count int
	Data  []byte // Count samples of sampleSize bytes each
}

// SplitSegments splits a sampler message into its segments. sampleSize is the
// width of one sample, len(types)*ValueSize. The sampler sends one segment per
// message, but the framing is self-delimiting and any number of whole segments
// is accepted.
func SplitSegments(msg []byte, sampleSize int) ([]Segment, error) {
	if sampleSize <= 0 {
		return nil, fmt.Errorf("halstream: sample size %d", sampleSize)
	}
	var segs []Segment
	for len(msg) > 0 {
		if len(msg) < SegmentHeaderSize {
			return nil, fmt.Errorf("halstream: %d stray bytes after the last segment", len(msg))
		}
		first := binary.LittleEndian.Uint32(msg)
		count := int(binary.LittleEndian.Uint32(msg[4:]))
		size := count * sampleSize
		if count <= 0 || size/sampleSize != count || len(msg)-SegmentHeaderSize < size {
			return nil, fmt.Errorf("halstream: segment of %d samples does not fit the %d bytes left",
				count, len(msg)-SegmentHeaderSize)
		}
		segs = append(segs, Segment{First: first, Count: count, Data: msg[SegmentHeaderSize : SegmentHeaderSize+size]})
		msg = msg[SegmentHeaderSize+size:]
	}
	return segs, nil
}

// Sequence follows the sample indices of one sampler connection and turns the
// gaps between segments into a count of lost samples.
type Sequence struct {
	started bool
	next    uint32 // index the next segment should start at
	tag     uint64 // samples since the connection's first one, losses included
}

// Advance takes the next segment's position and returns how many samples were
// lost right before it and the tag of its first sample: the number of samples
// the sampler took between the connection's first sample and this one. Tags
// do not wrap, and they jump by exactly the samples lost, so a consumer that
// prints them shows where a gap is.
//
// A segment that starts before the previous one ended is a protocol error: the
// RT sampler only ever moves forward.
func (q *Sequence) Advance(seg Segment) (lost uint32, tag uint64, err error) {
	if q.started {
		lost = seg.First - q.next
		if lost >= 1<<31 {
			return 0, 0, fmt.Errorf("halstream: segment at sample %d, before the expected %d", seg.First, q.next)
		}
		q.tag += uint64(lost)
	}
	q.started = true
	tag = q.tag
	q.tag += uint64(seg.Count)
	q.next = seg.First + uint32(seg.Count)
	return lost, tag, nil
}
