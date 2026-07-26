// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// interp_inspect.go — read-only views of interpreter state that the canon
// stream does not surface (numbered parameters, length units, current
// position and the two offset families).  Backed by the interp_inspection.hh
// seam in librs274; see the matching block in interp_shim.h.
//
// The #cgo build flags come from interp.go — cgo applies them package-wide.

// #include "interp_shim.h"
import "C"

// Axis selectors for the CInterp position/offset accessors.
const (
	AxisX = int(C.INTERP_AXIS_X)
	AxisY = int(C.INTERP_AXIS_Y)
	AxisZ = int(C.INTERP_AXIS_Z)
	AxisA = int(C.INTERP_AXIS_A)
	AxisB = int(C.INTERP_AXIS_B)
	AxisC = int(C.INTERP_AXIS_C)
)

// Length units, mirroring the CANON_UNITS enum in emc/nml_intf/canon.hh.
const (
	LengthUnitsInches = 1
	LengthUnitsMM     = 2
	LengthUnitsCM     = 3
)

// Numbered parameters of interest. The full map lives in
// emc/rs274ngc/interp_parameter_def.hh.
const (
	ParamG92X = 5211 // G92/G52 axis offsets, 5211..5219 (X Y Z A B C U V W)
	ParamG92Y = 5212
	ParamG92Z = 5213
)

// Parameter returns numbered parameter #index (0 if out of range).
func (i *CInterp) Parameter(index int) float64 {
	return float64(C.interp_get_parameter(i.handle, C.int(index)))
}

// LengthUnits returns the interpreter's current length units as one of the
// LengthUnits* constants.
func (i *CInterp) LengthUnits() int {
	return int(C.interp_length_units(i.handle))
}

// CurrentPosition returns the current position on one axis, in the
// interpreter's internal units.
func (i *CInterp) CurrentPosition(axis int) float64 {
	return float64(C.interp_current_position(i.handle, C.int(axis)))
}

// CurrentWorkOffset returns the active G5x work offset on one axis.
func (i *CInterp) CurrentWorkOffset(axis int) float64 {
	return float64(C.interp_current_work_offset(i.handle, C.int(axis)))
}

// CurrentAxisOffset returns the active G92/G52 axis offset on one axis.
func (i *CInterp) CurrentAxisOffset(axis int) float64 {
	return float64(C.interp_current_axis_offset(i.handle, C.int(axis)))
}
