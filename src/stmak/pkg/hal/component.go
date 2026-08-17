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

	// realtime records that this component was created with NewRTComponent,
	// i.e. registered with HAL as COMPONENT_TYPE_REALTIME. Only such a
	// component may export a cyclic function: hal_export_funct refuses any
	// component whose comp->pid is non-zero ("component is not realtime"), and
	// a user component always has one. ExportFunct checks this here so the
	// refusal names the constructor to change rather than arriving as a bare
	// EINVAL out of hal_lib.
	realtime bool

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

	// functs records the fully-qualified name of every realtime function
	// exported on this component. It is separate from names because a function
	// and a pin live in different HAL namespaces — hal_lib keeps funct_list_ptr
	// apart from pin_list_ptr, and "mycomp.update" as both a pin and a function
	// is legal, if unkind. Guarded by mu.
	functs map[string]struct{}

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

// NewComponent creates and initializes a new HAL userspace component.
//
// The name must be unique across all HAL components in the system and
// must not exceed HAL_NAME_LEN (NameLen = 127) characters.
//
// This calls hal_init() via CGO to register the component with HAL.
//
// A userspace component cannot export a realtime function — use
// NewRTComponent for a module that has one.
//
// Returns the component on success, or an error if initialization fails.
func NewComponent(name string) (*Component, error) {
	return newComponent("NewComponent", name, false)
}

// NewRTComponent creates and initializes a new HAL REALTIME component.
//
// It differs from NewComponent in exactly one way: HAL records the component as
// COMPONENT_TYPE_REALTIME (comp->pid == 0), which is the precondition
// hal_export_funct checks before it will accept a cyclic function. Everything
// else — pins, parameters, Ready, Exit — behaves identically.
//
// Reach for it when the module contains a C cyclic function and calls
// ExportFunct; a module that only reads and writes pins from Go should stay
// with NewComponent, because a realtime component makes a claim about this
// process that a pure-Go module does not honour.
//
// The component is registered with a nil dl_handle. That is deliberate: a
// compiled-in Go module's C code lives in the stmakd binary itself, whose pages
// are already locked by the mlockall(MCL_CURRENT) that rtapi_initialize_app
// performs before any component exists — there is no separate .so to lock, and
// a dlopen(NULL) handle would only be a leak.
//
// Note that HAL only marks a component realtime when hal_lib's rtapi_pid is
// set, i.e. after RtapiAppInit. In stmakd the launcher has always done that
// long before a module loads; a test binary must do it itself.
func NewRTComponent(name string) (*Component, error) {
	return newComponent("NewRTComponent", name, true)
}

// newComponent is the shared body of the two constructors.
func newComponent(op, name string, realtime bool) (*Component, error) {
	if name == "" || len(name) > NameLen {
		return nil, newError(op, ErrInvalidName.Message, ErrInvalidName.Code)
	}

	// Call hal_init_ex() to register the component, as user or realtime.
	id, err := halInit(name, realtime)
	if err != nil {
		return nil, err
	}

	comp := &Component{
		id:       id,
		name:     name,
		ready:    false,
		realtime: realtime,
		names:    make(map[string]struct{}),
		functs:   make(map[string]struct{}),
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

// createFunct is create()'s counterpart for realtime functions: it runs fn —
// the HAL-side hal_export_funct of fullName — under the component write lock,
// after the preconditions hal_export_funct would otherwise only reject after
// allocating the funct struct in HAL shared memory (a bump allocator with no
// free). It adds the one check hal_lib cannot phrase usefully: a component
// created with NewComponent is a userspace component, and the EINVAL
// hal_export_funct answers with says nothing about which constructor to change.
//
// The name set is functs, not names: HAL keeps functions in their own list, so
// a function may share a name with a pin on the same component.
func (c *Component) createFunct(op, fullName string, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.exited {
		return newError(op, ErrComponentExited.Message, ErrComponentExited.Code)
	}
	if !c.realtime {
		return newError(op, ErrNotRealtime.Message, ErrNotRealtime.Code)
	}
	if c.ready {
		return newError(op, ErrAlreadyReady.Message, ErrAlreadyReady.Code)
	}
	if _, dup := c.functs[fullName]; dup {
		return newError(op, ErrNameExists.Message, ErrNameExists.Code)
	}

	if err := fn(); err != nil {
		return err
	}
	c.functs[fullName] = struct{}{}
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

	// Same guard as create(): after Exit() the component id is freed and may
	// have been recycled, so hal_ready(c.id) could mark an unrelated,
	// still-initializing component ready.
	if c.exited {
		return newError("Ready", ErrComponentExited.Message, ErrComponentExited.Code)
	}
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

// IsRealtime reports whether the component was created with NewRTComponent,
// i.e. whether it may export a cyclic function.
func (c *Component) IsRealtime() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.realtime
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
