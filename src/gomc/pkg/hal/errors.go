// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

import "fmt"

// Error represents a HAL error with additional context.
type Error struct {
	// Code is the error code (negative values indicate errors).
	Code int

	// Message is the human-readable error message.
	Message string

	// Op is the operation that failed (e.g., "hal_init", "hal_pin_new").
	Op string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %s (code %d)", e.Op, e.Message, e.Code)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

// Is reports whether e matches target for errors.Is. Two HAL errors match when
// they carry the same code and message, so a wrapped error created with
// newError(op, sentinel.Message, sentinel.Code) still satisfies
// errors.Is(err, sentinel) even though it is a distinct value with its own Op.
// The message is part of the identity because several sentinels share a code
// (e.g. -EINVAL is used by both ErrInvalidName and ErrComponentNotFound).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code && e.Message == t.Message
}

// Common HAL errors.
// These error codes are based on typical RTAPI/HAL error codes.
var (
	// ErrInvalidName indicates an invalid component or pin name.
	ErrInvalidName = &Error{
		Code:    -22, // -EINVAL
		Message: "invalid component or pin name",
	}

	// ErrNotReady indicates the component is not ready for operation.
	ErrNotReady = &Error{
		Code:    -1,
		Message: "component not ready",
	}

	// ErrAlreadyReady indicates the component is already marked as ready.
	ErrAlreadyReady = &Error{
		Code:    -16, // -EBUSY
		Message: "component already marked ready",
	}

	// ErrInitFailed indicates HAL initialization failed.
	ErrInitFailed = &Error{
		Code:    -1,
		Message: "HAL initialization failed",
	}

	// ErrPinCreateFailed indicates pin creation failed.
	ErrPinCreateFailed = &Error{
		Code:    -1,
		Message: "failed to create pin",
	}

	// ErrComponentNotFound indicates the component ID is invalid.
	ErrComponentNotFound = &Error{
		Code:    -22, // -EINVAL
		Message: "component not found",
	}

	// ErrNoMemory indicates insufficient memory in HAL shared memory.
	ErrNoMemory = &Error{
		Code:    -12, // -ENOMEM
		Message: "insufficient HAL shared memory",
	}

	// ErrPortWriteFailed indicates a write to a HAL_PORT (string) pin was
	// dropped. The usual cause is that the pin has no backing buffer: a
	// HAL_PORT pin is unbacked until it is linked (net) to a port signal that
	// was allocated with a size.
	ErrPortWriteFailed = &Error{
		Code:    -28, // -ENOSPC
		Message: "HAL_PORT write failed (pin not linked to a sized port signal?)",
	}

	// ErrComponentExited indicates a pin was accessed after its owning
	// component was released with Exit(). The pin's HAL shared memory has been
	// freed, so the access is refused rather than dereferencing freed memory
	// (see the component-liveness barrier in Component/Pin).
	ErrComponentExited = &Error{
		Code:    -3, // -ESRCH
		Message: "component has exited; pin memory has been released",
	}
)

// newError creates a new Error with the specified operation, message, and code.
func newError(op string, message string, code int) *Error {
	return &Error{
		Op:      op,
		Message: message,
		Code:    code,
	}
}
