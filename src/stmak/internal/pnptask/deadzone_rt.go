// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

/*
#cgo CFLAGS: -I${SRCDIR}/../../pkg/cmodule -I${SRCDIR}/../../generated/gmi/mot -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include

#include "pnp_deadzone_rt.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// deadzoneFunct is the HAL function name suffix the cyclic dead-zone check is
// exported under, so a HAL file writes
//
//	addf <instance>.deadzone-check servo-thread
//
// It has to come after motion-controller in that thread — see the header.
const deadzoneFunct = "deadzone-check"

// deadzoneRT owns the flat structure the cyclic function walks.
//
// The split of labour is the whole point of the design: the expensive,
// allocating, Go-shaped work — parsing the drawings, offsetting the zones —
// already happened in newPlanners, and what is left here is a copy into one
// RT-hardened block. The cyclic function then walks that block and nothing
// else, needing no lock, no atomics (bar the one mot pointer) and no
// double-buffer, because nothing writes it once the threads are running.
type deadzoneRT struct {
	blk unsafe.Pointer
}

// newDeadzoneRT copies each planner's offset zones, in the order
// deadzone-select indexes them by, into a single RTCalloc block and points each
// scene at the pin that publishes its answer.
//
// The pins must already exist — their data-pointer slots are what the block
// stores — so this runs after newPins and before Ready.
//
// Every copy out of a Go slice happens while this call is on the stack, which
// is what keeps it inside the cgo pointer rules: no Go pointer is ever stored
// in the block. That is not a formality — a Go pointer in C memory is one the
// collector is free to move under the servo thread.
func newDeadzoneRT(planners *plannerSet, pins []*hal.Pin[bool]) (*deadzoneRT, error) {
	if len(planners.planners) != len(pins) {
		return nil, fmt.Errorf("dead-zone RT: %d planner(s) but %d pin(s)",
			len(planners.planners), len(pins))
	}

	// One pass to size the block, one to fill it. Reading OffsetZones twice
	// would clone every polygon twice, so hold the zones across both.
	zones := make([][]pnproute.Polygon, len(planners.planners))
	npolys, npoints := 0, 0
	for i, pl := range planners.planners {
		zones[i] = pl.OffsetZones()
		npolys += len(zones[i])
		for _, poly := range zones[i] {
			npoints += len(poly)
		}
	}

	size := uintptr(C.pnp_dz_size(C.int(len(zones)), C.int(npolys), C.int(npoints)))
	blk := hal.RTCalloc(size)
	if blk == nil {
		return nil, fmt.Errorf("dead-zone RT: rtapi_calloc(%d) failed", size)
	}
	C.pnp_dz_layout(blk, C.int(len(zones)), C.int(npolys), C.int(npoints))

	d := (*C.pnp_dz_t)(blk)
	polyIdx, pointIdx := 0, 0
	for scene, polys := range zones {
		firstPoly := polyIdx
		for _, poly := range polys {
			first := pointIdx
			for _, pt := range poly {
				C.pnp_dz_set_point(d, C.int(pointIdx), C.double(pt.X), C.double(pt.Y))
				pointIdx++
			}
			// A zero-vertex polygon cannot happen (the DXF loader rejects one),
			// but bounding-box derivation reads pts[0], so refuse rather than
			// read off the end.
			if len(poly) == 0 {
				hal.RTFree(blk)
				return nil, fmt.Errorf("dead-zone RT: drawing %d has an empty zone", scene)
			}
			C.pnp_dz_set_poly(d, C.int(polyIdx), C.int(first), C.int(len(poly)))
			polyIdx++
		}
		C.pnp_dz_set_scene(d, C.int(scene), C.int(firstPoly), C.int(len(polys)),
			pins[scene].RTDataPtr())
	}

	return &deadzoneRT{blk: blk}, nil
}

// export registers the cyclic function on comp. Called from the factory: the
// addf lines of a HAL file run after every load and before any Start, so a
// function exported any later would not exist when its addf runs.
func (d *deadzoneRT) export(comp *hal.Component) error {
	// usesFP: the whole predicate is double arithmetic, so HAL must save and
	// restore the FPU state around it. reentrant: false — one thread only.
	return comp.ExportFunct(deadzoneFunct,
		hal.CFunct(unsafe.Pointer(C.pnp_dz_check_fp)), d.blk, true, false)
}

// setMot publishes (or with nil retracts) the motmod callback table the cyclic
// function reads the Cartesian position from. Resolved in Start, because the
// provider may be loaded on a later HAL line — by which time the servo thread
// is already calling the function, which is why this one field is atomic.
//
// Until it is published every pin reads "not clear", which is the conservative
// answer and the same one an invalid feedback produces.
func (d *deadzoneRT) setMot(cbs unsafe.Pointer) {
	C.pnp_dz_set_mot((*C.pnp_dz_t)(d.blk), cbs)
}

// free releases the block. Legal only once nothing can still be executing the
// cyclic function: the launcher removes the component's functions and waits for
// a full RT cycle before Destroy, so Destroy — and not Stop — is where this
// belongs. See hal.ExportFunct.
func (d *deadzoneRT) free() {
	hal.RTFree(d.blk)
	d.blk = nil
}
