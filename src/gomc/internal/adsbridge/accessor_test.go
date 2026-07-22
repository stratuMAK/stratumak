// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package adsbridge

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/ads"
	"github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// These tests cover the byte<->HAL-pin conversion in the accessors — the
// risk-bearing part of the bridge (endianness, sign extension, IEEE-754 float
// encoding, ADS wire-size truncation, string layout). Each accessor wraps a
// real typed HAL pin; a value written as ADS bytes must land in the pin, and a
// value set on the pin must serialise back to the same bytes.

func newTestComp(t *testing.T) *hal.Component {
	t.Helper()
	// A unique component name per test — HAL keys components by name and reusing
	// one across tests in the same binary conflicts (see pkg/hal barrier_test).
	comp, err := hal.NewComponent("adsbridge-acc-" + t.Name())
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	t.Cleanup(func() { _ = comp.Exit() })
	return comp
}

func TestBitAccessorRoundTrip(t *testing.T) {
	comp := newTestComp(t)
	pin, err := hal.NewPin[bool](comp, "bit", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	acc := newBitAccessor(pin, typeInfo{adsTypeName: "BOOL", byteSize: 1})

	// ADS byte -> pin.
	if err := acc.WriteBytes([]byte{1}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if !pin.Get() {
		t.Error("WriteBytes([1]) did not set the pin true")
	}
	// pin -> ADS bytes (both directions).
	pin.Set(true)
	if b, _ := acc.ReadBytes(); len(b) != 1 || b[0] != 1 {
		t.Errorf("ReadBytes(true) = %v, want [1]", b)
	}
	pin.Set(false)
	if b, _ := acc.ReadBytes(); len(b) != 1 || b[0] != 0 {
		t.Errorf("ReadBytes(false) = %v, want [0]", b)
	}
	// A short write is rejected, not silently accepted.
	if err := acc.WriteBytes(nil); err == nil {
		t.Error("WriteBytes(nil) should error on a bool accessor")
	}
	if acc.Size() != 1 {
		t.Errorf("Size() = %d, want 1", acc.Size())
	}
}

func TestU32AccessorWireSizes(t *testing.T) {
	comp := newTestComp(t)
	pin, err := hal.NewPin[uint32](comp, "u", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// byteSize 2: little-endian, both directions.
	acc2 := newU32Accessor(pin, typeInfo{byteSize: 2})
	if err := acc2.WriteBytes([]byte{0x34, 0x12}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if got := pin.Get(); got != 0x1234 {
		t.Errorf("u16 write: pin = 0x%x, want 0x1234", got)
	}
	pin.Set(0xABCD)
	if b, _ := acc2.ReadBytes(); !bytes.Equal(b, []byte{0xCD, 0xAB}) {
		t.Errorf("u16 read: %v, want [0xCD 0xAB]", b)
	}

	// byteSize 1: only the low byte is on the wire.
	acc1 := newU32Accessor(pin, typeInfo{byteSize: 1})
	pin.Set(0x1FF)
	if b, _ := acc1.ReadBytes(); !bytes.Equal(b, []byte{0xFF}) {
		t.Errorf("u8 read of 0x1FF: %v, want [0xFF]", b)
	}
	if err := acc1.WriteBytes([]byte{0x2A}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if got := pin.Get(); got != 0x2A {
		t.Errorf("u8 write: pin = 0x%x, want 0x2A", got)
	}

	// byteSize 4: full 32-bit round-trip.
	acc4 := newU32Accessor(pin, typeInfo{byteSize: 4})
	pin.Set(0xDEADBEEF)
	want := make([]byte, 4)
	binary.LittleEndian.PutUint32(want, 0xDEADBEEF)
	if b, _ := acc4.ReadBytes(); !bytes.Equal(b, want) {
		t.Errorf("u32 read: %v, want %v", b, want)
	}
	// A write shorter than the wire size is rejected.
	if err := acc4.WriteBytes([]byte{0x01, 0x02}); err == nil {
		t.Error("u32 WriteBytes with 2 bytes should error")
	}
}

func TestS32AccessorSignExtension(t *testing.T) {
	comp := newTestComp(t)
	pin, err := hal.NewPin[int32](comp, "s", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// byteSize 1: 0xFF must sign-extend to -1.
	acc1 := newS32Accessor(pin, typeInfo{byteSize: 1})
	if err := acc1.WriteBytes([]byte{0xFF}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if got := pin.Get(); got != -1 {
		t.Errorf("s8 write 0xFF: pin = %d, want -1", got)
	}
	pin.Set(-1)
	if b, _ := acc1.ReadBytes(); !bytes.Equal(b, []byte{0xFF}) {
		t.Errorf("s8 read(-1): %v, want [0xFF]", b)
	}

	// byteSize 2: 0x8000 must sign-extend to -32768.
	acc2 := newS32Accessor(pin, typeInfo{byteSize: 2})
	if err := acc2.WriteBytes([]byte{0x00, 0x80}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if got := pin.Get(); got != -32768 {
		t.Errorf("s16 write 0x8000: pin = %d, want -32768", got)
	}

	// byteSize 4: full negative round-trip.
	acc4 := newS32Accessor(pin, typeInfo{byteSize: 4})
	var neg int32 = -123456
	pin.Set(neg)
	want := make([]byte, 4)
	binary.LittleEndian.PutUint32(want, uint32(neg))
	if b, _ := acc4.ReadBytes(); !bytes.Equal(b, want) {
		t.Errorf("s32 read(-123456): %v, want %v", b, want)
	}
}

func TestFloatAccessorIEEE754(t *testing.T) {
	comp := newTestComp(t)
	pin, err := hal.NewPin[float64](comp, "f", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// REAL: 4-byte float32.
	acc4 := newFloatAccessor(pin, typeInfo{byteSize: 4})
	f32 := make([]byte, 4)
	binary.LittleEndian.PutUint32(f32, math.Float32bits(1.5))
	if err := acc4.WriteBytes(f32); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if got := pin.Get(); got != 1.5 {
		t.Errorf("REAL write 1.5: pin = %v, want 1.5", got)
	}
	pin.Set(-2.25)
	want4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(want4, math.Float32bits(-2.25))
	if b, _ := acc4.ReadBytes(); !bytes.Equal(b, want4) {
		t.Errorf("REAL read(-2.25): %v, want %v", b, want4)
	}

	// LREAL: 8-byte float64, exact.
	acc8 := newFloatAccessor(pin, typeInfo{byteSize: 8})
	pin.Set(3.141592653589793)
	want8 := make([]byte, 8)
	binary.LittleEndian.PutUint64(want8, math.Float64bits(3.141592653589793))
	if b, _ := acc8.ReadBytes(); !bytes.Equal(b, want8) {
		t.Errorf("LREAL read(pi): %v, want %v", b, want8)
	}
}

func TestStringAccessorReadLayout(t *testing.T) {
	comp := newTestComp(t)
	pin, err := hal.NewPin[string](comp, "str", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	// STRING(4) is 5 wire bytes (n+1, null-terminated).
	acc := newStringAccessor(pin, typeInfo{adsTypeName: "STRING(4)", adstID: ads.ADSTString, byteSize: 5, strLen: 4})
	if acc.Size() != 5 {
		t.Errorf("STRING(4) Size() = %d, want 5", acc.Size())
	}
	// An unset (empty) string reads back as byteSize zero bytes (null-padded).
	b, err := acc.ReadBytes()
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	if len(b) != 5 {
		t.Fatalf("ReadBytes len = %d, want 5", len(b))
	}
	for i, c := range b {
		if c != 0 {
			t.Errorf("empty string byte %d = 0x%02x, want 0x00", i, c)
		}
	}
	// Writing to an unlinked HAL_PORT string pin is a dropped write and must
	// surface as an error rather than a silent success (the H-3 contract).
	if err := acc.WriteBytes([]byte("hi")); err == nil {
		t.Error("WriteBytes to an unlinked string pin should error (dropped port write)")
	}
}
