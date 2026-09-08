// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestParamGetSetRoundTrip verifies that Set() followed by Get() round-trips
// the value for every supported parameter type, and that the owner-side Set is
// accepted on an RO parameter too (the direction constrains halcmd, not the
// owning component — see TestParamHalcmdRoundTrip for the outside view).
func TestParamGetSetRoundTrip(t *testing.T) {
	comp, err := hal.NewComponent("test-param-roundtrip")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	bp, err := hal.NewParam[bool](comp, "b", hal.RW)
	if err != nil {
		t.Fatalf("NewParam[bool]: %v", err)
	}
	fp, err := hal.NewParam[float64](comp, "f", hal.RW)
	if err != nil {
		t.Fatalf("NewParam[float64]: %v", err)
	}
	sp, err := hal.NewParam[int32](comp, "s", hal.RW)
	if err != nil {
		t.Fatalf("NewParam[int32]: %v", err)
	}
	up, err := hal.NewParam[uint32](comp, "u", hal.RW)
	if err != nil {
		t.Fatalf("NewParam[uint32]: %v", err)
	}
	rop, err := hal.NewParam[float64](comp, "ro", hal.RO)
	if err != nil {
		t.Fatalf("NewParam[float64] (RO): %v", err)
	}

	// A fresh parameter starts at the zero value: hal_param_*_new does not
	// initialise the cell, halParamNew does.
	for _, tc := range []struct {
		name string
		got  any
	}{
		{"b", bp.Get()}, {"f", fp.Get()}, {"s", sp.Get()}, {"u", up.Get()},
	} {
		switch v := tc.got.(type) {
		case bool:
			if v {
				t.Errorf("%s: fresh param is %v, want false", tc.name, v)
			}
		case float64:
			if v != 0 {
				t.Errorf("%s: fresh param is %v, want 0", tc.name, v)
			}
		case int32:
			if v != 0 {
				t.Errorf("%s: fresh param is %v, want 0", tc.name, v)
			}
		case uint32:
			if v != 0 {
				t.Errorf("%s: fresh param is %v, want 0", tc.name, v)
			}
		}
	}

	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	bp.Set(true)
	if got := bp.Get(); got != true {
		t.Errorf("bit param: got %v, want true", got)
	}
	fp.Set(3.14)
	if got := fp.Get(); got != 3.14 {
		t.Errorf("float param: got %v, want 3.14", got)
	}
	sp.Set(-42)
	if got := sp.Get(); got != -42 {
		t.Errorf("s32 param: got %v, want -42", got)
	}
	up.Set(0xDEADBEEF)
	if got := up.Get(); got != 0xDEADBEEF {
		t.Errorf("u32 param: got %v, want 0xDEADBEEF", got)
	}
	// RO restricts writes from outside, not from the owner.
	rop.Set(1.5)
	if got := rop.Get(); got != 1.5 {
		t.Errorf("RO param owner write: got %v, want 1.5", got)
	}

	// Accessors report what was requested.
	if got := fp.Name(); got != "test-param-roundtrip.f" {
		t.Errorf("Name: got %q, want %q", got, "test-param-roundtrip.f")
	}
	if got := fp.Direction(); got != hal.RW {
		t.Errorf("Direction: got %v, want RW", got)
	}
	if got := rop.Direction(); got != hal.RO {
		t.Errorf("Direction (RO): got %v, want RO", got)
	}
	for name, tc := range map[string]struct {
		got  hal.PinType
		want hal.PinType
	}{
		"b": {bp.Type(), hal.TypeBit},
		"f": {fp.Type(), hal.TypeFloat},
		"s": {sp.Type(), hal.TypeS32},
		"u": {up.Type(), hal.TypeU32},
	} {
		if tc.got != tc.want {
			t.Errorf("Type(%s): got %v, want %v", name, tc.got, tc.want)
		}
	}
	if got := fp.String(); got == "" {
		t.Error("String: got empty, want a non-empty representation")
	}
}

// TestParamHalcmdRoundTrip is the point of parameters: a value the owner
// publishes and an operator adjusts at runtime. It drives the outside view
// through the same shim halcmd's setp/getp use — an RW parameter must take an
// external write and hand it back to the owner's Get(), an owner Set() must be
// visible to getp, and an RO parameter must refuse setp.
func TestParamHalcmdRoundTrip(t *testing.T) {
	comp, err := hal.NewComponent("test-param-halcmd")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	rw, err := hal.NewParam[float64](comp, "settle-time", hal.RW)
	if err != nil {
		t.Fatalf("NewParam (RW): %v", err)
	}
	rwBit, err := hal.NewParam[bool](comp, "invert", hal.RW)
	if err != nil {
		t.Fatalf("NewParam (RW bit): %v", err)
	}
	rwU32, err := hal.NewParam[uint32](comp, "count", hal.RW)
	if err != nil {
		t.Fatalf("NewParam (RW u32): %v", err)
	}
	ro, err := hal.NewParam[int32](comp, "state", hal.RO)
	if err != nil {
		t.Fatalf("NewParam (RO): %v", err)
	}
	// Initial values, as a module would load them from INI before Ready().
	rw.Set(0.1)
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// getp sees the owner's value (and resolves the parameter by name at all —
	// the cell address handed to hal_param_new must be the one Set writes).
	if got, err := halcmd.GetP("test-param-halcmd.settle-time"); err != nil || got != "0.1" {
		t.Errorf("GetP after owner Set: got %q, %v; want %q", got, err, "0.1")
	}

	// setp adjusts the RW parameter and the owner reads the new value.
	if err := halcmd.SetP("test-param-halcmd.settle-time", "0.25"); err != nil {
		t.Fatalf("SetP on an RW param: %v", err)
	}
	if got := rw.Get(); got != 0.25 {
		t.Errorf("Get after SetP: got %v, want 0.25", got)
	}
	if err := halcmd.SetP("test-param-halcmd.invert", "TRUE"); err != nil {
		t.Fatalf("SetP on an RW bit param: %v", err)
	}
	if got := rwBit.Get(); got != true {
		t.Errorf("bit Get after SetP: got %v, want true", got)
	}
	if err := halcmd.SetP("test-param-halcmd.count", "4294967295"); err != nil {
		t.Fatalf("SetP on an RW u32 param: %v", err)
	}
	if got := rwU32.Get(); got != 4294967295 {
		t.Errorf("u32 Get after SetP: got %v, want 4294967295", got)
	}

	// An RO parameter is a view into the component: readable from outside,
	// not writable.
	ro.Set(-7)
	if got, err := halcmd.GetP("test-param-halcmd.state"); err != nil || got != "-7" {
		t.Errorf("GetP on an RO param: got %q, %v; want %q", got, err, "-7")
	}
	if err := halcmd.SetP("test-param-halcmd.state", "3"); err == nil {
		t.Error("SetP on an RO param: got nil, want a refusal")
	}
	if got := ro.Get(); got != -7 {
		t.Errorf("RO param after a refused SetP: got %v, want -7 (unchanged)", got)
	}

	// PType/LookupValue resolve parameters as well as pins.
	if got, err := halcmd.PType("test-param-halcmd.settle-time"); err != nil || got != hal.TypeFloat {
		t.Errorf("PType: got %v, %v; want %v", got, err, hal.TypeFloat)
	}
	if v, ok := hal.LookupValue("test-param-halcmd.settle-time"); !ok || v != 0.25 {
		t.Errorf("LookupValue(param): got (%v, %v), want (0.25, true)", v, ok)
	}
}

// TestParamValidation covers the argument checks in NewParam plus the two
// failures HAL itself reports: a duplicate name and a parameter created after
// hal_ready().
func TestParamValidation(t *testing.T) {
	comp, err := hal.NewComponent("test-param-validate")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	if _, err := hal.NewParam[float64](nil, "p", hal.RW); err == nil {
		t.Error("NewParam(nil component): got nil, want an error")
	}
	if _, err := hal.NewParam[float64](comp, "", hal.RW); !errors.Is(err, hal.ErrInvalidName) {
		t.Errorf("NewParam(empty name): got %v, want ErrInvalidName", err)
	}
	// Pin directions are not parameter directions: HAL_IN/HAL_OUT/HAL_IO must
	// be rejected rather than reaching hal_param_new as a bogus dir.
	for _, dir := range []hal.ParamDirection{0, 16, 32, 48, 128} {
		if _, err := hal.NewParam[float64](comp, "bad-dir", dir); err == nil {
			t.Errorf("NewParam(dir=%d): got nil, want an error", int(dir))
		}
	}
	longName := make([]byte, hal.NameLen)
	for i := range longName {
		longName[i] = 'x'
	}
	if _, err := hal.NewParam[float64](comp, string(longName), hal.RW); !errors.Is(err, hal.ErrInvalidName) {
		t.Errorf("NewParam(over-long name): got %v, want ErrInvalidName", err)
	}

	// Duplicates are refused Go-side (before any HAL shm is allocated — shm is
	// a bump allocator, a C-side rejection would leak the cell), with the
	// ErrNameExists sentinel. Pins and parameters share the name set.
	if _, err := hal.NewParam[float64](comp, "dup", hal.RW); err != nil {
		t.Fatalf("NewParam(dup): %v", err)
	}
	if _, err := hal.NewParam[float64](comp, "dup", hal.RW); !errors.Is(err, hal.ErrNameExists) {
		t.Errorf("NewParam with a duplicate name: got %v, want ErrNameExists", err)
	}
	if _, err := hal.NewPin[float64](comp, "dup", hal.Out); !errors.Is(err, hal.ErrNameExists) {
		t.Errorf("NewPin reusing a param name: got %v, want ErrNameExists", err)
	}

	// hal_param_new is refused once the component is ready — parameters, like
	// pins, are declared during construction. The guard rejects this Go-side
	// with the ErrAlreadyReady sentinel.
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := hal.NewParam[float64](comp, "late", hal.RW); !errors.Is(err, hal.ErrAlreadyReady) {
		t.Errorf("NewParam after Ready(): got %v, want ErrAlreadyReady", err)
	}
}

// TestParamAccessAfterExit verifies parameters observe the same
// component-liveness barrier as pins: once Exit() has released the component,
// the parameter's cell in HAL shared memory is freed, so Get() must return the
// zero value and Set() must drop the write instead of dereferencing it.
func TestParamAccessAfterExit(t *testing.T) {
	comp, err := hal.NewComponent("test-param-after-exit")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	param, err := hal.NewParam[float64](comp, "v", hal.RW)
	if err != nil {
		t.Fatalf("NewParam: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	param.Set(3.5)
	if got := param.Get(); got != 3.5 {
		t.Fatalf("Get before Exit: got %v, want 3.5", got)
	}

	if err := comp.Exit(); err != nil {
		t.Fatalf("Exit: %v", err)
	}

	if got := param.Get(); got != 0 {
		t.Errorf("Get after Exit: got %v, want 0 (zero value, no dereference)", got)
	}
	// Set must not panic on a freed parameter, and must stay a no-op.
	param.Set(9.9)
	if got := param.Get(); got != 0 {
		t.Errorf("Get after Set-on-exited: got %v, want 0", got)
	}
	// String() reads through Get(), so it must survive the exited component too.
	if got := param.String(); got == "" {
		t.Error("String after Exit: got empty, want a representation")
	}
}

// TestParamStringConcurrentWithSet mirrors the Pin.String() regression guard:
// String() must not take p.mu before calling Get(), which RLocks again — Go's
// RWMutex forbids recursive read-locking and deadlocks when a Set() writer
// contends between the two RLocks. Run under -race to also confirm the value
// accesses are properly synchronized.
func TestParamStringConcurrentWithSet(t *testing.T) {
	comp, err := hal.NewComponent("test-param-race")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	p, err := hal.NewParam[float64](comp, "v", hal.RW)
	if err != nil {
		t.Fatalf("NewParam: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	const iters = 2000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.Set(float64(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = p.String()
		}
	}()
	wg.Wait()
}
