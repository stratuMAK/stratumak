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
 2. Create pins with NewPin[T]()
 3. Mark the component ready with Ready()
 4. Read and write pins as data flows through HAL
 5. Release the component with Exit() when it is torn down

In stmak a component is normally owned by a compiled-in module (see package
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

# Lifecycle and Shutdown

pkg/hal itself runs no goroutines and installs no signal handlers. In stmak a
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
