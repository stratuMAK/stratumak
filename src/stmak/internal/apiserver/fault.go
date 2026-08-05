// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import "fmt"

// FaultKind classifies a provider failure so that each transport can render it
// faithfully. The provider says what *kind* of failure it was; the transport
// decides how to say it.
//
// Without this every provider error that carries no errno becomes an HTTP 500,
// which tells a client the controller broke. Usually it did not: it declined a
// request its current state does not allow ("must be in MDI mode", "can't issue
// MDI command when not homed"). That is a 409, and the difference matters —
// a 500 invites a retry against a controller presumed sick, and it is what
// monitoring escalates on.
type FaultKind int

const (
	// FaultInternal — the controller itself failed. HTTP 500. The zero value,
	// so an unclassified error keeps the old conservative behaviour.
	FaultInternal FaultKind = iota

	// FaultState — the machine's current state forbids this request. Nothing
	// happened; re-sending it unchanged will fail the same way until the state
	// changes. HTTP 409 Conflict.
	FaultState

	// FaultNotReady — the module is not started yet, or has been stopped. The
	// same request may well succeed shortly. HTTP 503 Service Unavailable.
	FaultNotReady

	// FaultNotFound — the named thing does not exist. HTTP 404.
	FaultNotFound

	// FaultCapacity — a resource limit is reached. HTTP 503. Distinct from
	// FaultNotReady, which is about lifecycle: the module here is running and
	// healthy, it is simply full. Both render as 503 because both mean "not
	// now"; keeping them apart stops the name lying in logs and lets them
	// diverge later if one wants Retry-After.
	FaultCapacity
)

// Fault wraps an error with its kind. Use errors.As to recover it; the wrapped
// error keeps its own message, so nothing is lost by classifying.
type Fault struct {
	Kind FaultKind
	Err  error
}

func (f *Fault) Error() string { return f.Err.Error() }
func (f *Fault) Unwrap() error { return f.Err }

// NewFault classifies err. A nil err stays nil so call sites can wrap
// unconditionally.
func NewFault(kind FaultKind, err error) error {
	if err == nil {
		return nil
	}
	return &Fault{Kind: kind, Err: err}
}

// Faultf builds a classified error from a format string.
func Faultf(kind FaultKind, format string, a ...interface{}) error {
	return &Fault{Kind: kind, Err: fmt.Errorf(format, a...)}
}
