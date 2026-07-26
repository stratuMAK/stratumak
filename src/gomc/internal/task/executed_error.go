// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// executedError marks a failure that happened *after* the command was accepted
// and run, as opposed to one that stopped it from being accepted at all.
//
// The two are different events and clients need to tell them apart:
//
//   - **Refused** — wrong mode, not homed, the task not ready, the MDI queue
//     full. The request was invalid in the machine's current state, nothing
//     happened, and re-sending it unchanged will fail the same way. This is a
//     transport error: HTTP 4xx/5xx with the reason.
//
//   - **Executed and faulted** — the interpreter took the command and reported
//     an operator error (`G10 L1 P0` → "P value out of range"). The call did
//     its job: the command reached the machine. The outcome is a machine event,
//     already published on the error channel by faultMDI, and the task is the
//     authority on the resulting state. This reports RCS_ERROR in a normal
//     response, which is what the `-> i32` return on every emccmd function is
//     for and what classic linuxcnc.command() semantics do.
//
// Collapsing them either way loses something real. Before REST reported
// provider errors at all, both were silent and a refused command looked like a
// successful one. Making both transport errors is the opposite mistake: a test
// or GUI that deliberately issues an erroring command — which the tool-table
// tests do, and which is ordinary operator behaviour — cannot then read the
// state it was checking, and the error is reported twice for one event.
type executedError struct{ err error }

func (e *executedError) Error() string { return e.err.Error() }
func (e *executedError) Unwrap() error { return e.err }

// executed wraps err as an executed-and-faulted failure. Call it only where the
// command has already reached the machine and the fault has been published to
// the operator (i.e. alongside faultMDI/faultProgram).
func executed(err error) error {
	if err == nil {
		return nil
	}
	return &executedError{err: err}
}
