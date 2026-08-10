// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

import (
	"fmt"
	"sync"
)

// Component represents a HAL component.
// A component is the basic unit of HAL functionality. It can export pins
// and parameters that other components can connect to via signals.
//
// In stratuMAK a component is owned by a compiled-in module (package stmak); the
// launcher drives the module's Start/Stop/Destroy lifecycle. pkg/hal itself
// runs no goroutines and installs no signal handlers — a component has no
// "running" state of its own and no self-driven main loop.
type Component struct {
	// id is the HAL component ID assigned by hal_init().
	id int

	// name is the unique name of the component.
	name string

	// ready indicates whether the component has been marked ready.
	ready bool

	// exited indicates whether Exit() has released the component. Once set,
	// hal_exit() has freed this component's pins from HAL shared memory, so any
	// further pin access must be refused (see enter/leave).
	exited bool

	// names records the fully-qualified name of every pin and parameter
	// created on this component. It backs the early duplicate rejection in
	// create(): HAL shared memory is a bump allocator with no free, so a
	// duplicate that only failed inside hal_pin_new/hal_param_new would do so
	// after the value cell was already — permanently — allocated. Pins and
	// parameters share the one set; that is stricter than hal_lib's separate
	// per-kind lists, but a pin and a parameter with the same full name would
	// be hopelessly confusing in halcmd anyway. Guarded by mu.
	names map[string]struct{}

	// mu protects the component state and doubles as the component-liveness
	// barrier: Exit() takes the write lock across hal_exit(), and every pin
	// Get/Set takes the read lock (via enter/leave) around its shared-memory
	// dereference. The write lock cannot be granted while any pin access holds
	// the read lock, and any access that starts after Exit() sees exited==true
	// and bails — so no goroutine can ever dereference freed pin memory.
	mu sync.RWMutex
}

// enter acquires the component-liveness read barrier for a single pin access.
//
// It returns true with the read lock HELD when the component is still live —
// the caller MUST pair it with a deferred leave(). It returns false with the
// lock already released when the component has exited; the caller must then not
// touch the pin's HAL shared memory (it has been freed by hal_exit).
//
// This is the read side of the barrier that serializes pin Get/Set against
// Component.Exit(); see the Pin methods and the mu doc above.
func (c *Component) enter() bool {
	c.mu.RLock()
	if c.exited {
		c.mu.RUnlock()
		return false
	}
	return true
}

// leave releases the read barrier taken by a successful enter().
func (c *Component) leave() { c.mu.RUnlock() }

// NewComponent creates and initializes a new HAL component.
//
// The name must be unique across all HAL components in the system and
// must not exceed HAL_NAME_LEN (NameLen = 127) characters.
//
// This calls hal_init() via CGO to register the component with HAL.
//
// Returns the component on success, or an error if initialization fails.
func NewComponent(name string) (*Component, error) {
	if name == "" || len(name) > NameLen {
		return nil, newError("NewComponent", ErrInvalidName.Message, ErrInvalidName.Code)
	}

	// Call hal_init() to register the component
	id, err := halInit(name)
	if err != nil {
		return nil, err
	}

	comp := &Component{
		id:    id,
		name:  name,
		ready: false,
		names: make(map[string]struct{}),
	}

	return comp, nil
}

// qualifyName validates a new pin/parameter name and builds the
// fully-qualified "component.name" form. It is the single copy of the
// name-qualification contract, shared by NewPin and NewParam in both build
// modes, so the constructors cannot drift in what names they accept.
func qualifyName(c *Component, op, name string) (string, error) {
	if c == nil {
		return "", newError(op, "component is nil", -22)
	}
	if name == "" {
		return "", newError(op, ErrInvalidName.Message, ErrInvalidName.Code)
	}
	fullName := c.Name() + "." + name
	if len(fullName) > NameLen {
		return "", newError(op, ErrInvalidName.Message, ErrInvalidName.Code)
	}
	return fullName, nil
}

// create runs fn — the HAL-side creation of the pin or parameter fullName —
// under the component write lock, after checking the component-side
// preconditions that hal_pin_new/hal_param_new would otherwise only reject
// *after* the value cell was allocated in HAL shared memory (a bump allocator
// with no free, so such a rejection permanently leaks the cell): the component
// must not have exited, must not be marked ready yet, and fullName must be
// unused on this component. Holding the write lock across fn also serializes
// creation against Exit(), so fn can never call into HAL with a freed
// component id. On success the name is recorded for future duplicate checks.
//
// fn reads c.id directly rather than through ID() — the lock is already held
// and Go mutexes are not reentrant.
func (c *Component) create(op, fullName string, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.exited {
		return newError(op, ErrComponentExited.Message, ErrComponentExited.Code)
	}
	if c.ready {
		return newError(op, ErrAlreadyReady.Message, ErrAlreadyReady.Code)
	}
	if _, dup := c.names[fullName]; dup {
		return newError(op, ErrNameExists.Message, ErrNameExists.Code)
	}

	if err := fn(); err != nil {
		return err
	}
	c.names[fullName] = struct{}{}
	return nil
}

// Ready marks the component as ready for operation.
//
// This must be called after all pins and parameters have been created,
// but before the component enters its main loop.
//
// This calls hal_ready() via CGO.
func (c *Component) Ready() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ready {
		return newError("Ready", ErrAlreadyReady.Message, ErrAlreadyReady.Code)
	}

	// Call hal_ready()
	if err := halReady(c.id); err != nil {
		return err
	}

	c.ready = true
	return nil
}

// Exit cleans up the component and releases all HAL resources.
//
// This should be called (typically via defer, or from a module's Destroy())
// when the component is shutting down. It unregisters the component and
// removes all pins and parameters.
//
// Exit is idempotent: a second call is a no-op that returns nil, so the common
// "defer comp.Exit()" + explicit teardown-path Exit() pattern does not call
// hal_exit() twice on the same id (which would error, or — if HAL recycled the
// id — tear down a different component).
//
// Exit holds the component write lock across hal_exit(): this is the write side
// of the liveness barrier (see the mu doc). It blocks until every in-flight pin
// Get/Set has released the read barrier, and marks the component exited before
// releasing the lock, so no pin access can dereference the freed HAL memory.
//
// This calls hal_exit() via CGO.
func (c *Component) Exit() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.exited {
		return nil
	}

	// Call hal_exit() while holding the write lock so pin access is fully
	// serialized against the freeing of this component's HAL shared memory.
	if err := halExit(c.id); err != nil {
		return err
	}

	c.exited = true
	return nil
}

// Name returns the component name.
func (c *Component) Name() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.name
}

// ID returns the HAL component ID.
//
// This ID is used internally for HAL API calls and is assigned by
// hal_init().
func (c *Component) ID() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.id
}

// IsReady returns true if the component has been marked ready.
func (c *Component) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready
}

// String returns a string representation of the component.
func (c *Component) String() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("Component{name=%s, id=%d, ready=%t}",
		c.name, c.id, c.ready)
}
