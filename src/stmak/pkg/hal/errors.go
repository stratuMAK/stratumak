// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

import (
	"fmt"
	"syscall"
)

// Error represents a HAL error with additional context.
type Error struct {
	// Code is the error code (negative values indicate errors).
	Code int

	// Message is the human-readable error message.
	Message string

	// Op is the operation that failed (e.g., "hal_init", "hal_pin_new").
	Op string

	// Detail is the diagnostic hal_lib itself reported for this failure, e.g.
	// "duplicate thread name loop1" for a -EINVAL out of hal_create_thread_cpu.
	// It is empty when nothing was captured, which is why Message keeps the
	// generic per-code text rather than being replaced by it: Detail is the
	// reason, Message is the fallback.
	//
	// Detail is deliberately NOT part of Is() identity — a sentinel comparison
	// must not depend on which machine-specific name appeared in a hal_lib
	// message. See Is below.
	Detail string
}

// Error implements the error interface. When hal_lib reported a reason it
// leads, because that is what the operator needs; the generic per-code text is
// demoted to a parenthetical so the mapping from code to category is still
// visible.
func (e *Error) Error() string {
	msg := e.Message
	if e.Detail != "" {
		msg = fmt.Sprintf("%s [%s]", e.Detail, e.Message)
	}
	if e.Op != "" {
		return fmt.Sprintf("%s: %s (code %d)", e.Op, msg, e.Code)
	}
	return fmt.Sprintf("%s (code %d)", msg, e.Code)
}

// Is reports whether e matches target for errors.Is. Two HAL errors match when
// they carry the same code and message, so a wrapped error created with
// newError(op, sentinel.Message, sentinel.Code) still satisfies
// errors.Is(err, sentinel) even though it is a distinct value with its own Op.
// The message is part of the identity because several sentinels share a code
// (e.g. -EINVAL is used by both ErrInvalidName and ErrComponentNotFound).
// Detail is excluded: it carries hal_lib's reason text, which embeds pin and
// thread names, so including it would make every sentinel comparison fail as
// soon as a diagnostic is captured.
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

	// ErrNameExists indicates a pin or parameter with this fully-qualified
	// name was already created on the component. The duplicate is rejected on
	// the Go side before any HAL shared memory is allocated: HAL shm is a
	// bump allocator with no free, so a duplicate that only failed inside
	// hal_pin_new/hal_param_new would permanently leak the already-allocated
	// value cell (see Component.create).
	ErrNameExists = &Error{
		Code:    -17, // -EEXIST
		Message: "pin or parameter name already exists on this component",
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

// CodeMessage returns the generic human-readable text for a HAL/RTAPI return
// code (a negative errno). It is the single copy of this table: both the
// component-facing wrappers in this package and internal/halcmd's ~40 shim
// wrappers route through it, so the two cannot drift apart.
func CodeMessage(code int) string {
	switch code {
	case -int(syscall.EPERM):
		return "operation not permitted (HAL may be locked)"
	case -int(syscall.ENOENT):
		return "not found (no such pin, signal, parameter, function, thread, or component)"
	case -int(syscall.ESRCH):
		return "no such process or component not running"
	case -int(syscall.ECHILD):
		return "child process error"
	case -int(syscall.EAGAIN):
		return "resource temporarily unavailable"
	case -int(syscall.ENOMEM):
		return "insufficient HAL shared memory"
	case -int(syscall.EACCES):
		return "permission denied"
	case -int(syscall.EFAULT):
		return "bad address or invalid pointer"
	case -int(syscall.EBUSY):
		return "resource busy (pin already linked to a signal, or signal has writers)"
	case -int(syscall.EEXIST):
		return "already exists (signal, pin, parameter, function, or component with this name exists)"
	case -int(syscall.ENODEV):
		return "no such device or component"
	case -int(syscall.EINVAL):
		return "invalid argument (bad value, wrong type, or malformed name)"
	case -int(syscall.ENFILE):
		return "too many open files or components"
	case -int(syscall.ENOSPC):
		return "no space left in HAL shared memory"
	case -int(syscall.ENAMETOOLONG):
		return "name too long (exceeds HAL_NAME_LEN)"
	case -int(syscall.ETIMEDOUT):
		return "operation timed out (component did not become ready)"
	}
	// For unmapped codes, include both the code and the errno name if possible.
	if code < 0 && code > -256 {
		return fmt.Sprintf("system error %d (%s)", -code, syscall.Errno(-code).Error())
	}
	return fmt.Sprintf("HAL error code %d", code)
}

// CodeError builds the Error for a HAL/RTAPI return code, returning nil for
// success. detail is hal_lib's own diagnostic for this failure, or "" when
// none was captured.
func CodeError(op string, code int, detail string) error {
	if code == 0 {
		return nil
	}
	return &Error{
		Op:      op,
		Message: CodeMessage(code),
		Code:    code,
		Detail:  detail,
	}
}
