// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

/*
#include "hal.h"
*/
import "C"
import (
	"encoding/binary"
	"fmt"
	"sync"
	"unsafe"
)

// portFrameHeaderSize is the number of bytes used for the length prefix in the
// HAL_PORT framing protocol for string pins (4-byte big-endian uint32).
const portFrameHeaderSize = 4

// Pin represents a HAL pin with type-safe access.
//
// Pins are the connection points between HAL components. They can be
// linked to signals, which allow components to exchange data. The generic
// type parameter T ensures type safety at compile time.
//
// Type T must be one of: bool, float64, int32, uint32, string (matching HAL types
// HAL_BIT, HAL_FLOAT, HAL_S32, HAL_U32, HAL_PORT).
type Pin[T PinValue] struct {
	// name is the fully-qualified pin name (e.g., "component.pinname").
	name string

	// direction is the pin direction (In, Out, or IO).
	direction Direction

	// ptr is a pointer to the HAL shared memory for this pin.
	// This is set by hal_pin_*_new() and used for Get/Set operations.
	ptr unsafe.Pointer

	// comp is the component that owns this pin. Get/Set take comp's liveness
	// read barrier (enter/leave) around every shared-memory dereference so a pin
	// access can never race Component.Exit() into freed HAL memory.
	comp *Component

	// mu serializes concurrent Go-side access to this pin's value. It does NOT
	// protect against the RT thread (which never takes it) nor against Exit()
	// freeing the pin — the latter is the job of comp's liveness barrier.
	mu sync.RWMutex
}

// NewPin creates a new pin with the specified type and direction.
//
// The name should be just the pin name (e.g., "input"), not the full name.
// The component name will be prepended automatically (e.g., "mycomp.input").
//
// Valid directions are In (component reads), Out (component writes), or
// IO (bidirectional).
//
// This calls the appropriate hal_pin_*_new() function via CGO based on the
// type parameter T:
//   - bool -> hal_pin_bit_new()
//   - float64 -> hal_pin_float_new()
//   - int32 -> hal_pin_s32_new()
//   - uint32 -> hal_pin_u32_new()
//   - string -> hal_pin_port_new()
//
// String pins (HAL_PORT) differ from scalar pins: hal_pin_port_new creates the
// pin with an empty (unbacked) port. The pin gains a buffer only when it is
// linked (via net) to a port signal that was allocated with a size. Until then
// Get() returns "" and Set() drops the value (TrySet() reports the failure).
//
// Type inference example:
//
//	pin, err := NewPin[float64](comp, "speed", hal.In)
//	pin, err := NewPin[bool](comp, "enable", hal.In)
//	pin, err := NewPin[int32](comp, "count", hal.Out)
func NewPin[T PinValue](c *Component, name string, dir Direction) (*Pin[T], error) {
	fullName, err := qualifyName(c, "NewPin", name)
	if err != nil {
		return nil, err
	}

	if dir != In && dir != Out && dir != IO {
		return nil, newError("NewPin", "invalid direction", -22)
	}

	// Map the generic type parameter T to its HAL type, then create the pin via
	// the single halPinNew wrapper (dispatched C-side by hal_type_t).
	typ, ok := pinTypeOf[T]()
	if !ok {
		return nil, newError("NewPin", "unsupported pin type", -22)
	}

	// The creation guard rejects duplicates and ready/exited components before
	// the pointer slot is allocated (HAL shm has no free), and holds the
	// component write lock across halPinNew so the C call can never race
	// Component.Exit() into a freed component id.
	var ptr unsafe.Pointer
	if err := c.create("NewPin", fullName, func() error {
		p, err := halPinNew(fullName, dir, c.id, typ)
		if err != nil {
			return err
		}
		ptr = p
		return nil
	}); err != nil {
		return nil, err
	}

	pin := &Pin[T]{
		name:      fullName,
		direction: dir,
		ptr:       ptr,
		comp:      c,
	}

	return pin, nil
}

// Get reads the current pin value.
//
// For input pins, this reads the value written by the connected signal.
// For output pins, this reads the value last written by Set().
//
// This dereferences the pointer to HAL shared memory. If the owning component
// has already been released with Exit(), the pin's HAL memory is gone; Get then
// takes no dereference and returns the zero value of T.
func (p *Pin[T]) Get() T {
	// Liveness barrier: refuse to dereference freed HAL memory if the owning
	// component has exited (see Component.enter). Held across the whole read.
	if !p.comp.enter() {
		var zeroValue T
		return zeroValue
	}
	defer p.comp.leave()

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Read from HAL shared memory based on the type
	var zeroValue T
	switch any(zeroValue).(type) {
	case bool:
		// p.ptr is **hal_bit_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_bit_t)(p.ptr)
		val := bool(**ptrPtr)
		return any(val).(T)
	case float64:
		// p.ptr is **hal_float_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_float_t)(p.ptr)
		val := float64(**ptrPtr)
		return any(val).(T)
	case int32:
		// p.ptr is **hal_s32_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_s32_t)(p.ptr)
		val := int32(**ptrPtr)
		return any(val).(T)
	case uint32:
		// p.ptr is **hal_u32_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_u32_t)(p.ptr)
		val := uint32(**ptrPtr)
		return any(val).(T)
	case string:
		// String pins use HAL_PORT with 4-byte big-endian length-prefix framing.
		// Use peek (non-consuming) so repeated Get() calls return the same value.
		// An unlinked HAL_PORT pin has no backing buffer, so readable == 0 and
		// this returns "" (see NewPin/TrySet on the sized-port-signal contract).
		// p.ptr is **hal_port_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_port_t)(p.ptr)
		portPtr := *ptrPtr
		readable := halPortReadable(portPtr)
		if readable < portFrameHeaderSize {
			return any("").(T)
		}
		header := halPortPeek(portPtr, portFrameHeaderSize)
		if header == nil {
			return any("").(T)
		}
		length := binary.BigEndian.Uint32(header)
		// Guard against overflow: length near MaxUint32 would wrap when adding the header size.
		if length > ^uint32(0)-portFrameHeaderSize {
			return any("").(T)
		}
		total := uint(portFrameHeaderSize) + uint(length)
		// Bounds check: total must not exceed available data to prevent excessive allocation.
		if readable < total {
			return any("").(T)
		}
		frame := halPortPeek(portPtr, total)
		if frame == nil {
			return any("").(T)
		}
		return any(string(frame[portFrameHeaderSize:])).(T)
	default:
		// Should never happen due to PinValue constraint
		return *new(T)
	}
}

// Set writes a value to the pin (fire-and-forget).
//
// For output pins, this writes the value that will be read by connected
// components. For input pins, calling Set() has no effect (the value is
// overwritten by the connected signal).
//
// Set ignores any write failure. For scalar pins a write always succeeds, but
// for string (HAL_PORT) pins the write can be dropped when the pin has no
// backing buffer — use TrySet if you need to confirm the value was delivered.
//
// This writes to HAL shared memory.
func (p *Pin[T]) Set(value T) {
	_ = p.TrySet(value)
}

// TrySet writes a value to the pin and reports whether the write reached HAL.
//
// Scalar pins (bool/float64/int32/uint32) write directly into HAL shared memory
// and always return nil.
//
// String pins (HAL_PORT) can fail: a HAL_PORT pin has no backing buffer until
// it is linked (via net) to a port signal that was allocated with a size. On an
// unlinked port — or one whose buffer is too small for the framed message — the
// write is dropped and TrySet returns ErrPortWriteFailed. This is the one pin
// type whose Set can silently no-op, so callers that must confirm delivery of a
// string value should use TrySet.
//
// If the owning component has already been released with Exit(), the pin's HAL
// memory is gone; TrySet takes no dereference and returns ErrComponentExited.
func (p *Pin[T]) TrySet(value T) error {
	// Liveness barrier: refuse to dereference freed HAL memory if the owning
	// component has exited (see Component.enter). Held across the whole write.
	if !p.comp.enter() {
		return newError("Pin.TrySet", ErrComponentExited.Message, ErrComponentExited.Code)
	}
	defer p.comp.leave()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Write to HAL shared memory based on the type
	var zeroValue T
	switch any(zeroValue).(type) {
	case bool:
		// p.ptr is **hal_bit_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_bit_t)(p.ptr)
		**ptrPtr = C.hal_bit_t(any(value).(bool))
	case float64:
		// p.ptr is **hal_float_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_float_t)(p.ptr)
		**ptrPtr = C.hal_float_t(any(value).(float64))
	case int32:
		// p.ptr is **hal_s32_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_s32_t)(p.ptr)
		**ptrPtr = C.hal_s32_t(any(value).(int32))
	case uint32:
		// p.ptr is **hal_u32_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_u32_t)(p.ptr)
		**ptrPtr = C.hal_u32_t(any(value).(uint32))
	case string:
		// String pins use HAL_PORT with 4-byte big-endian length-prefix framing.
		// Clear first for "latest value" semantics, then write the framed message.
		// p.ptr is **hal_port_t; dereference at access time so HAL's updated
		// pointer (set when pin is linked via net) is always followed.
		ptrPtr := (**C.hal_port_t)(p.ptr)
		portPtr := *ptrPtr
		halPortClear(portPtr)
		str := any(value).(string)
		strBytes := []byte(str)
		frame := make([]byte, portFrameHeaderSize+len(strBytes))
		binary.BigEndian.PutUint32(frame[:portFrameHeaderSize], uint32(len(strBytes)))
		copy(frame[portFrameHeaderSize:], strBytes)
		if !halPortWrite(portPtr, frame) {
			return newError("Pin.TrySet", ErrPortWriteFailed.Message, ErrPortWriteFailed.Code)
		}
	}
	return nil
}

// RTDataPtr returns the pin's HAL data-pointer slot, for a cyclic function
// exported with ExportFunct to read or write the pin from the servo thread.
//
// The value is a "double pointer": HAL stores the address of the data cell in
// this slot and REPOINTS it at the signal's cell when the pin is linked with
// net, so the C side must dereference twice, at access time, exactly as Get and
// Set do — a cyclic function that caches the inner pointer at init reads the
// pin's private dummy cell forever and never sees the signal.
//
//	hal_bit_t **slot;   // what this returns
//	**slot = 1;         // publish
//
// This is the one escape hatch in an otherwise fully guarded API, and it hands
// out an address into HAL shared memory with no liveness barrier attached: none
// of the checks that make Get and Set safe against Component.Exit apply to what
// C does with the result. That is sound only under the ExportFunct contract —
// the launcher removes the component's functions and waits for a full RT cycle
// before Destroy, and Destroy is where Exit belongs — and it is why this is
// spelled RT rather than offered as a general accessor. Go code has Get and Set
// and should use them.
func (p *Pin[T]) RTDataPtr() unsafe.Pointer {
	return p.ptr
}

// Name returns the fully-qualified pin name.
func (p *Pin[T]) Name() string {
	return p.name
}

// Direction returns the pin direction.
func (p *Pin[T]) Direction() Direction {
	return p.direction
}

// Type returns the HAL type of the pin.
func (p *Pin[T]) Type() PinType {
	if typ, ok := pinTypeOf[T](); ok {
		return typ
	}
	return -1 // Should never happen due to PinValue constraint
}

// String returns a string representation of the pin.
//
// It must not take p.mu: name/direction are immutable after construction and
// Type()/Get() do their own locking. Taking an RLock here and then calling
// Get() (which RLocks again) is a recursive read-lock — Go's RWMutex forbids
// it and deadlocks if a Set() writer contends between the two RLocks.
func (p *Pin[T]) String() string {
	return fmt.Sprintf("Pin{name=%s, type=%s, dir=%s, value=%v}",
		p.name, p.Type(), p.direction, p.Get())
}
