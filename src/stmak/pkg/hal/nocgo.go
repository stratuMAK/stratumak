//go:build !cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package hal

import (
	"errors"
	"sync"
)

// errNoCGO is returned by all stub functions when CGO is not available.
var errNoCGO = errors.New("hal: CGO is required but not available")

// Pin stub for non-CGO builds. Provides an in-memory pin that satisfies
// the same interface as the CGO-backed Pin but does not interact with HAL.
type Pin[T PinValue] struct {
	// mu serializes Set/Get like the cgo Pin's mutex does, so concurrent use
	// behaves the same (race-free) in both build modes.
	mu    sync.RWMutex
	value T
	name  string
	dir   Direction
	typ   PinType
}

// NewPin creates a stub pin for non-CGO builds. It runs the same validation,
// name qualification and component creation guard as the cgo constructor so
// the two build modes agree at the API boundary — including the stored
// fully-qualified "component.name". Note that NewComponent always fails
// without cgo, so in practice every call errors on the component check.
func NewPin[T PinValue](c *Component, name string, dir Direction) (*Pin[T], error) {
	fullName, err := qualifyName(c, "NewPin", name)
	if err != nil {
		return nil, err
	}
	if dir != In && dir != Out && dir != IO {
		return nil, newError("NewPin", "invalid direction", -22)
	}
	typ, ok := pinTypeOf[T]()
	if !ok {
		return nil, newError("NewPin", "unsupported pin type", -22)
	}
	if err := c.create("NewPin", fullName, func() error { return nil }); err != nil {
		return nil, err
	}
	return &Pin[T]{name: fullName, dir: dir, typ: typ}, nil
}

// Set sets the pin value.
func (p *Pin[T]) Set(v T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = v
}

// Get returns the pin value.
func (p *Pin[T]) Get() T {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.value
}

// Type returns the pin type.
func (p *Pin[T]) Type() PinType { return p.typ }

// Name returns the pin name.
func (p *Pin[T]) Name() string { return p.name }

// Direction returns the pin direction.
func (p *Pin[T]) Direction() Direction { return p.dir }

// String returns a string representation of the pin.
func (p *Pin[T]) String() string { return p.name }

// Param stub for non-CGO builds. Provides an in-memory parameter that
// satisfies the same interface as the CGO-backed Param but does not interact
// with HAL.
type Param[T ParamValue] struct {
	// mu serializes Set/Get like the cgo Param's mutex does, so concurrent
	// use behaves the same (race-free) in both build modes.
	mu    sync.RWMutex
	value T
	name  string
	dir   ParamDirection
	typ   PinType
}

// NewParam creates a stub parameter for non-CGO builds. Like the NewPin stub
// it runs the same validation, name qualification and component creation guard
// as the cgo constructor so the two build modes agree at the API boundary.
func NewParam[T ParamValue](c *Component, name string, dir ParamDirection) (*Param[T], error) {
	fullName, err := qualifyName(c, "NewParam", name)
	if err != nil {
		return nil, err
	}
	if dir != RO && dir != RW {
		return nil, newError("NewParam", "invalid direction", -22)
	}
	typ, ok := pinTypeOf[T]()
	if !ok {
		return nil, newError("NewParam", "unsupported parameter type", -22)
	}
	if err := c.create("NewParam", fullName, func() error { return nil }); err != nil {
		return nil, err
	}
	return &Param[T]{name: fullName, dir: dir, typ: typ}, nil
}

// Set sets the parameter value.
func (p *Param[T]) Set(v T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = v
}

// Get returns the parameter value.
func (p *Param[T]) Get() T {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.value
}

// Type returns the parameter type.
func (p *Param[T]) Type() PinType { return p.typ }

// Name returns the parameter name.
func (p *Param[T]) Name() string { return p.name }

// Direction returns the parameter direction.
func (p *Param[T]) Direction() ParamDirection { return p.dir }

// String returns a string representation of the parameter.
func (p *Param[T]) String() string { return p.name }

// --- CGO function stubs ---
// These provide stub implementations of the core unexported hal* functions
// defined in cgo.go (which is excluded from non-CGO builds). They allow the
// package to compile with CGO_ENABLED=0 for pure-Go tests.

func halInit(_ string) (int, error) { return 0, errNoCGO }
func halReady(_ int) error          { return errNoCGO }
func halExit(_ int) error           { return errNoCGO }

// LookupValue stub for non-CGO builds (the cgo implementation is in lookup.go,
// which is excluded without cgo). Always reports "not found" for build symmetry.
func LookupValue(_ string) (float64, bool) { return 0, false }
