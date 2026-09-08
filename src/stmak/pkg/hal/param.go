// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

/*
#include "hal.h"
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// Param represents a HAL parameter with type-safe access.
//
// A parameter is a value that lives inside a component but may need to be
// inspected or adjusted from outside it — a tuning knob (RW, settable with
// "halcmd setp") or a diagnostic view into the component (RO). Unlike a Pin, a
// parameter is never linked to a signal and never carries data between
// components; use a pin when a value has to be wired.
//
// The generic type parameter T ensures type safety at compile time. It must be
// one of: bool, float64, int32, uint32 (matching HAL types HAL_BIT, HAL_FLOAT,
// HAL_S32, HAL_U32). There is no string parameter — HAL has no HAL_PORT
// parameter type.
type Param[T ParamValue] struct {
	// name is the fully-qualified parameter name (e.g., "component.paramname").
	name string

	// direction is the parameter direction (RO or RW).
	direction ParamDirection

	// ptr points at the parameter's value cell in HAL shared memory.
	//
	// This is a single pointer, not the double-pointer a Pin holds: HAL fills
	// in a pin's pointer slot again whenever the pin is linked to a signal,
	// while a parameter is never linked, so its cell address is fixed for the
	// lifetime of the component.
	ptr unsafe.Pointer

	// comp is the component that owns this parameter. Get/Set take comp's
	// liveness read barrier (enter/leave) around every shared-memory
	// dereference so a parameter access can never race Component.Exit() into
	// freed HAL memory.
	comp *Component

	// mu serializes concurrent Go-side access to this parameter's value. It
	// does NOT protect against the RT thread nor against halcmd writing an RW
	// parameter (neither takes it), and not against Exit() freeing the cell —
	// the latter is the job of comp's liveness barrier.
	mu sync.RWMutex
}

// NewParam creates a new parameter with the specified type and direction.
//
// The name should be just the parameter name (e.g., "settle-time"), not the
// full name. The component name will be prepended automatically (e.g.,
// "mycomp.settle-time").
//
// Valid directions are RO (read-only from outside) or RW (writable from
// outside with "halcmd setp"). The owning component writes the value with
// Set() in both cases — the direction constrains halcmd, not the owner.
//
// Like pins, parameters must be created before the component is marked ready:
// hal_param_new fails once hal_ready() has run. A newly created parameter
// starts at the zero value of T; the component should Set() its configured
// default before calling Ready().
//
// This calls the appropriate hal_param_*_new() function via CGO based on the
// type parameter T:
//   - bool -> hal_param_bit_new()
//   - float64 -> hal_param_float_new()
//   - int32 -> hal_param_s32_new()
//   - uint32 -> hal_param_u32_new()
//
// Type inference example:
//
//	p, err := NewParam[float64](comp, "settle-time", hal.RW)
//	p.Set(0.1) // initial value, e.g. from INI
func NewParam[T ParamValue](c *Component, name string, dir ParamDirection) (*Param[T], error) {
	fullName, err := qualifyName(c, "NewParam", name)
	if err != nil {
		return nil, err
	}

	if dir != RO && dir != RW {
		return nil, newError("NewParam", "invalid direction", -22)
	}

	// Map the generic type parameter T to its HAL type, then create the
	// parameter via the single halParamNew wrapper (dispatched C-side by
	// hal_type_t).
	typ, ok := pinTypeOf[T]()
	if !ok {
		return nil, newError("NewParam", "unsupported parameter type", -22)
	}

	// The creation guard rejects duplicates and ready/exited components before
	// the value cell is allocated (HAL shm has no free), and holds the
	// component write lock across halParamNew so the C call can never race
	// Component.Exit() into a freed component id.
	var ptr unsafe.Pointer
	if err := c.create("NewParam", fullName, func() error {
		p, err := halParamNew(fullName, dir, c.id, typ)
		if err != nil {
			return err
		}
		ptr = p
		return nil
	}); err != nil {
		return nil, err
	}

	param := &Param[T]{
		name:      fullName,
		direction: dir,
		ptr:       ptr,
		comp:      c,
	}

	return param, nil
}

// Get reads the current parameter value.
//
// For an RW parameter this is the value last written by either the owning
// component (Set) or an outside writer ("halcmd setp"), whichever came last.
//
// This dereferences the parameter's cell in HAL shared memory. If the owning
// component has already been released with Exit(), that memory is gone; Get
// then takes no dereference and returns the zero value of T.
//
// Concurrency note: the cell is read with a plain (non-atomic) load, mirroring
// C HAL's volatile-only access model — p.mu is process-local and cannot order
// this load against an out-of-process writer ("halcmd setp"). On 32-bit
// targets a concurrent external write to an 8-byte (float) parameter can
// therefore in principle be observed torn for one read. This is inherited from
// the HAL ecosystem (pins share it): treat externally-written parameters as
// tuning knobs, not as synchronization.
func (p *Param[T]) Get() T {
	// Liveness barrier: refuse to dereference freed HAL memory if the owning
	// component has exited (see Component.enter). Held across the whole read.
	if !p.comp.enter() {
		var zeroValue T
		return zeroValue
	}
	defer p.comp.leave()

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Read from the value cell based on the type. Unlike a pin this is a single
	// dereference — a parameter's cell is never repointed (see Param.ptr).
	var zeroValue T
	switch any(zeroValue).(type) {
	case bool:
		val := bool(*(*C.hal_bit_t)(p.ptr))
		return any(val).(T)
	case float64:
		val := float64(*(*C.hal_float_t)(p.ptr))
		return any(val).(T)
	case int32:
		val := int32(*(*C.hal_s32_t)(p.ptr))
		return any(val).(T)
	case uint32:
		val := uint32(*(*C.hal_u32_t)(p.ptr))
		return any(val).(T)
	default:
		// Should never happen due to the ParamValue constraint
		return *new(T)
	}
}

// Set writes a value to the parameter.
//
// This is the owner-side write and is valid for both RO and RW parameters —
// the direction restricts writes from outside the component ("halcmd setp"),
// not the owner. A scalar write into HAL shared memory cannot fail, so Set
// reports nothing.
//
// If the owning component has already been released with Exit(), the
// parameter's HAL memory is gone; the write is dropped rather than
// dereferencing freed memory.
func (p *Param[T]) Set(value T) {
	// Liveness barrier: refuse to dereference freed HAL memory if the owning
	// component has exited (see Component.enter). Held across the whole write.
	if !p.comp.enter() {
		return
	}
	defer p.comp.leave()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Write to the value cell based on the type.
	var zeroValue T
	switch any(zeroValue).(type) {
	case bool:
		*(*C.hal_bit_t)(p.ptr) = C.hal_bit_t(any(value).(bool))
	case float64:
		*(*C.hal_float_t)(p.ptr) = C.hal_float_t(any(value).(float64))
	case int32:
		*(*C.hal_s32_t)(p.ptr) = C.hal_s32_t(any(value).(int32))
	case uint32:
		*(*C.hal_u32_t)(p.ptr) = C.hal_u32_t(any(value).(uint32))
	}
}

// Name returns the fully-qualified parameter name.
func (p *Param[T]) Name() string {
	return p.name
}

// Direction returns the parameter direction.
func (p *Param[T]) Direction() ParamDirection {
	return p.direction
}

// Type returns the HAL type of the parameter.
func (p *Param[T]) Type() PinType {
	if typ, ok := pinTypeOf[T](); ok {
		return typ
	}
	return -1 // Should never happen due to the ParamValue constraint
}

// String returns a string representation of the parameter.
//
// Like Pin.String it must not take p.mu: name/direction are immutable after
// construction and Type()/Get() do their own locking. Taking an RLock here and
// then calling Get() (which RLocks again) is a recursive read-lock — Go's
// RWMutex forbids it and deadlocks if a Set() writer contends between the two
// RLocks.
func (p *Param[T]) String() string {
	return fmt.Sprintf("Param{name=%s, type=%s, dir=%s, value=%v}",
		p.name, p.Type(), p.direction, p.Get())
}
