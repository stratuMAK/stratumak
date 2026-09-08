// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
/*
Package hal provides Go bindings for LinuxCNC's Hardware Abstraction Layer (HAL).

HAL is the Hardware Abstraction Layer used by LinuxCNC to transfer realtime data
to and from I/O devices and other low-level modules. This package allows Go
programs to create userspace HAL components that can interact with other HAL
components (both realtime and userspace).

# Overview

A HAL component is a program that exports pins and/or parameters. Pins can be
connected to signals, which allow components to exchange data. The HAL maintains
all data in shared memory, enabling efficient communication between components.

# Basic Usage

The typical flow for a HAL component is:

 1. Create a component with NewComponent()
 2. Create pins with NewPin[T]() and parameters with NewParam[T]()
 3. Mark the component ready with Ready()
 4. Read and write pins as data flows through HAL
 5. Release the component with Exit() when it is torn down

In stratuMAK a component is normally owned by a compiled-in module (see package
stmak): the launcher calls the module's Start/Stop/Destroy, and pin data flows
as HAL threads drive the connected components. The module does its per-cycle
work (or runs its own worker goroutines) and releases the component from
Destroy(). Components do not run their own main loop or install signal handlers.

Example (as used from a module):

	import (
		"log"

		"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	)

	// Create component and pins (typically in the module factory).
	comp, err := hal.NewComponent("go-example")
	if err != nil {
		log.Fatal(err)
	}

	input, err := hal.NewPin[float64](comp, "input", hal.In)
	if err != nil {
		log.Fatal(err)
	}

	output, err := hal.NewPin[float64](comp, "output", hal.Out)
	if err != nil {
		log.Fatal(err)
	}

	enable, err := hal.NewPin[bool](comp, "enable", hal.In)
	if err != nil {
		log.Fatal(err)
	}

	if err := comp.Ready(); err != nil {
		log.Fatal(err)
	}

	// Per-cycle work (called from the module, or a worker goroutine it owns):
	if enable.Get() {
		output.Set(input.Get() * 2.0)
	}

	// On teardown (typically the module's Destroy()):
	// _ = comp.Exit()

# Pin Types

HAL supports several data types for pins:

  - bool (HAL_BIT): Boolean values
  - float64 (HAL_FLOAT): 64-bit floating point
  - int32 (HAL_S32): Signed 32-bit integer
  - uint32 (HAL_U32): Unsigned 32-bit integer
  - string (HAL_PORT): Variable-length UTF-8 strings via port buffering

Pins are created using the generic NewPin[T]() function, which provides
compile-time type safety:

	boolPin, _ := hal.NewPin[bool](comp, "enable", hal.In)
	floatPin, _ := hal.NewPin[float64](comp, "velocity", hal.Out)
	int32Pin, _ := hal.NewPin[int32](comp, "count", hal.IO)
	uint32Pin, _ := hal.NewPin[uint32](comp, "status", hal.Out)
	strPin, _ := hal.NewPin[string](comp, "message", hal.Out)

# Pin Directions

Pins have a direction that specifies how data flows:

  - In: Component reads the pin value (input)
  - Out: Component writes the pin value (output)
  - IO: Component can both read and write (bidirectional)

# Parameters

A parameter is a value that lives inside a component but may need to be
inspected or adjusted from outside it. Unlike a pin it is never linked to a
signal and never carries data between components — reach for a parameter when a
value is a tuning knob or a diagnostic view, and for a pin when it has to be
wired.

Parameters are created with the generic NewParam[T]() and support the four
scalar HAL types (bool, float64, int32, uint32); there is no string parameter,
because HAL has no HAL_PORT parameter type. Their direction describes access
from outside only — the owning component always writes its own parameters with
Set():

  - RO: read-only from outside; "halcmd setp" is refused
  - RW: writable from outside with "halcmd setp"

Like pins, parameters must be created before Ready(). A new parameter starts at
the zero value of T, so the component loads its configured default (typically
from INI) before marking itself ready:

	settle, _ := hal.NewParam[float64](comp, "settle-time", hal.RW)
	settle.Set(iniSettleTime)            // initial value
	// ... later, per cycle: an operator may have retuned it with
	//     halcmd setp mycomp.settle-time 0.25
	dwell := settle.Get()

# Realtime Functions

A Go module that needs one cyclic function no longer has to be a C module. It
contains a C function and registers it — the function itself is C and calls no
Go, which is what keeps the invariant recorded in
docs/dev/RT_HARDENING_CHECKLIST.md §0 ("the RT cycle dispatches only C function
pointers — no Go in the cycle by construction") true by construction rather than
by convention. Three pieces make that up:

  - NewRTComponent, because HAL only accepts a cyclic function from a
    COMPONENT_TYPE_REALTIME component.
  - Component.ExportFunct, which takes a CFunct — the address of a C function —
    and deliberately offers no overload taking a Go func.
  - RTCalloc / RTFree, the RT-hardened allocator, for the structure the cyclic
    function walks. Pin.RTDataPtr hands that structure a pin to publish on.

The division of labour is the one the EtherCAT driver already uses: the
high-level work happens once at init, in whatever language suits it, and
assembles a flat structure; the cyclic function then walks that structure and
nothing else. Sketch:

	// The factory. addf lines run after every load and before any Start, so
	// the export belongs here — as it does in a cmod's New().
	comp, err := hal.NewRTComponent(name)
	free, err := hal.NewPin[bool](comp, "clear", hal.Out)

	scene := hal.RTCalloc(sizeOfScene)      // never Go memory
	fillFromGo(scene, zones)                // copies; stores no Go pointer
	setOutputPin(scene, free.RTDataPtr())

	fn := hal.CFunct(unsafe.Pointer(C.my_funct_fp))
	usesFP, reentrant := true, false
	err = comp.ExportFunct("check", fn, scene, usesFP, reentrant)
	err = comp.Ready()

	// Destroy — and only Destroy, which runs after the launcher's RT barrier.
	_ = comp.Exit()
	hal.RTFree(scene)

See CFunct for where the C must live (a real .c file, added to
"make rt-effects-check" in the same change) and ExportFunct for the teardown
contract. The full design, including why a cgo call into Go from the servo
thread is not an option, is docs/dev/GOMOD_RT_DESIGN.md.

# Build Requirements

This package uses CGO to interface with the LinuxCNC HAL library. To build
programs using this package, you need:

  - Go 1.22 or later
  - LinuxCNC development headers
  - CGO_ENABLED=1

# Integration with LinuxCNC

HAL components written in Go integrate seamlessly with the rest of LinuxCNC:

  - Use 'halcmd show comp' to see loaded components
  - Use 'halcmd show pin' to see exported pins
  - Use 'halcmd net' to connect pins to signals
  - Use 'halcmd show param' / 'halcmd setp' to inspect and tune parameters

# Lifecycle and Shutdown

pkg/hal itself runs no goroutines and installs no signal handlers. In stratuMAK a
component is owned by a compiled-in module (package stmak); the launcher drives
each module's Start/Stop/Destroy. A module stops its own worker goroutines in
Stop() and releases its HAL component (Exit()) from Destroy(). Process-level
shutdown (SIGTERM/SIGINT) is handled by the launcher, not by individual
components.

# References

For more information about LinuxCNC HAL:

  - HAL Manual: https://linuxcnc.org/docs/html/hal/intro.html
  - HAL C API: src/hal/hal.h
  - Python HAL bindings: lib/python/hal.py
*/
package hal
