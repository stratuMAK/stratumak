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
	"strconv"
	"strings"
)

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
// type chars. ok is false if the message is not a cfg header.
func ParseHeader(msg []byte) (types string, ok bool) {
	s := string(msg)
	if !strings.HasPrefix(s, CfgPrefix) {
		return "", false
	}
	return s[len(CfgPrefix):], true
}

// ReadRaw returns the raw 8-byte little-endian value of pin i within a frame.
func ReadRaw(frame []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(frame[i*ValueSize:])
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
