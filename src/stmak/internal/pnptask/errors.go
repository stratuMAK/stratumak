// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import "fmt"

// errorID is the value published on the error-id pin. It is the module's whole
// diagnostic channel: there is no UI and no message bus here, so a failure that
// does not map onto one of these numbers is a failure the operator cannot see.
//
// The numbers are the contract with the PLC that reads the pin — they are
// written out explicitly rather than generated with iota so that inserting a
// code can never silently renumber the ones below it. They match §7.5 of
// docs/dev/PNPTASK_DESIGN.md.
type errorID uint32

const (
	errNone errorID = 0

	errEstop      errorID = 1 // estop during a job or a machine operation
	errMachineOff errorID = 2 // machine-on dropped, or a job started while off

	errNotHomed     errorID = 3 // unhomed joints and AUTOHOME = 0
	errHomingFailed errorID = 4 // autohoming timed out or the sequence faulted

	errInvalidOrigin         errorID = 5 // unknown origin-id
	errInvalidDest           errorID = 6 // unknown dest-id
	errInvalidRoute          errorID = 7 // reserved — every tray/proc pairing is legal
	errInvalidDeadzoneSelect errorID = 8 // deadzone-select out of range
	errPlanningFailed        errorID = 9 // no route (start or goal blocked)

	errInvalidTrayID   errorID = 10 // tray-id pin matches no TRAYDEF
	errTrayEmpty       errorID = 11 // no matching slot / probing exhausted
	errTrayFull        errorID = 12 // no free slot
	errProcNoMaterial  errorID = 13 // pick-proc precondition, or material vanished
	errProcHasMaterial errorID = 14 // place-proc precondition (single picker)
	errPickerCloseFail errorID = 15 // opened still high after pick-settle
	errPlaceFailed     errorID = 16 // opened stayed low after release
	errReleaseTimeout  errorID = 17 // proc released feedback timed out
	errMotionError     errorID = 18 // motctl/motstat comm failure or fault
	errAltPickerSeq    errorID = 19 // next job must originate from the held station
	errAutoDisabled    errorID = 20 // start-job while auto-enable is low
	errWaitAborted     errorID = 21 // busy-wait aborted by auto-enable going low
	errNoFreePicker    errorID = 22 // a pick or swap needs a free picker, none is
	errPickerOpenFail  errorID = 23 // close withdrawn but opened never came back
	errUnreachable     errorID = 24 // job accept: an endpoint − a candidate picker's offset is outside the scene or the axis limits
)

// errorNames is the log-side spelling of each id. The pin carries the number;
// this is what makes a log line readable without the design document open.
var errorNames = map[errorID]string{
	errNone:                  "NONE",
	errEstop:                 "ESTOP",
	errMachineOff:            "MACHINE_OFF",
	errNotHomed:              "NOT_HOMED",
	errHomingFailed:          "HOMING_FAILED",
	errInvalidOrigin:         "INVALID_ORIGIN",
	errInvalidDest:           "INVALID_DEST",
	errInvalidRoute:          "INVALID_ROUTE",
	errInvalidDeadzoneSelect: "INVALID_DEADZONE_SELECT",
	errPlanningFailed:        "PLANNING_FAILED",
	errInvalidTrayID:         "INVALID_TRAY_ID",
	errTrayEmpty:             "TRAY_EMPTY",
	errTrayFull:              "TRAY_FULL",
	errProcNoMaterial:        "PROC_NO_MATERIAL",
	errProcHasMaterial:       "PROC_HAS_MATERIAL",
	errPickerCloseFail:       "PICKER_CLOSE_FAILED",
	errPlaceFailed:           "PLACE_FAILED",
	errReleaseTimeout:        "RELEASE_TIMEOUT",
	errMotionError:           "MOTION_ERROR",
	errAltPickerSeq:          "ALT_PICKER_SEQUENCE",
	errAutoDisabled:          "AUTO_DISABLED",
	errWaitAborted:           "WAIT_ABORTED",
	errNoFreePicker:          "NO_FREE_PICKER",
	errPickerOpenFail:        "PICKER_OPEN_FAILED",
	errUnreachable:           "TARGET_UNREACHABLE",
}

func (id errorID) String() string {
	if n, ok := errorNames[id]; ok {
		return n
	}
	return fmt.Sprintf("UNKNOWN(%d)", uint32(id))
}

// pnpFault is a failure that carries the error-id it must be published under.
// Every abort path returns one, so the number on the pin and the sentence in
// the log are produced at the same place and cannot drift apart.
type pnpFault struct {
	id  errorID
	msg string
}

func (f *pnpFault) Error() string { return fmt.Sprintf("%s: %s", f.id, f.msg) }

// faultf builds a pnpFault. The message is for the log; the id is for the pin.
func faultf(id errorID, format string, args ...any) *pnpFault {
	return &pnpFault{id: id, msg: fmt.Sprintf(format, args...)}
}

// errStopping ends a wait because the module is shutting down. It is not a
// machine fault and is never published on the error pins — the loop is simply
// gone, and latching an error on the way out would greet the next start with a
// failure that never happened.
var errStopping = fmt.Errorf("pnptask: control loop stopping")
