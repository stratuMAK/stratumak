// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import "github.com/sittner/linuxcnc/src/gomc/pkg/hal"

// Canon getter callbacks — called by the interpreter to query current state.
// These read from the Task's MotionStatus interface or from canon state.

func (c *Canon) GetExternalFeedRate() (float64, error) {
	if c.state.feedMode != 0 {
		// G95 units-per-rev: the F word is a per-revolution rate — hand back
		// the stored prog-units value unchanged (2.9 GET_EXTERNAL_FEED_RATE's
		// feed_mode branch), NOT the per-minute conversion below.
		return c.state.feedPerRev, nil
	}
	return c.state.toProg(c.state.linearFeedRate * 60.0), nil // mm/sec → units/min
}

func (c *Canon) GetExternalTraverseRate() (float64, error) {
	// Return in program units per minute (matching C emccanon behavior).
	// Read from task.maxVelocity (set by loadConfig) like C reads from STAT.
	return c.state.toProg(c.task.maxVelocity) * 60.0, nil
}

func (c *Canon) GetExternalLengthUnitType() (int32, error) {
	return c.state.lengthUnits, nil
}

func (c *Canon) GetExternalLengthUnits() (float64, error) {
	// Return the machine's native linear units (from [TRAJ]LINEAR_UNITS).
	// This must NOT vary with the active G20/G21 setting — it tells the
	// interpreter what units parameters and external data are stored in.
	return c.task.linearUnits, nil
}

func (c *Canon) GetExternalAngleUnits() (float64, error) {
	return 1.0, nil // always degrees
}

func (c *Canon) GetExternalMotionControlMode() (int32, error) {
	return c.state.motionMode, nil
}

func (c *Canon) GetExternalMotionControlTolerance() (float64, error) {
	return c.state.toProg(c.state.motionTolerance), nil
}

func (c *Canon) GetExternalMotionControlNaivecamTolerance() (float64, error) {
	return c.state.toProg(c.state.naivecamTol), nil
}

func (c *Canon) GetExternalFlood() (int32, error) {
	if c.state.floodOn {
		return 1, nil
	}
	return 0, nil
}

func (c *Canon) GetExternalMist() (int32, error) {
	if c.state.mistOn {
		return 1, nil
	}
	return 0, nil
}

// Position getters — return current position in program units.
// Like the C canon (GET_EXTERNAL_POSITION), these read from endPoint
// (absolute machine coordinates, synced from CartePosFb before each synch)
// and return the position with offsets removed, in program units.
// This matches the C canon's unoffset_and_unrotate_pos + to_prog.

func (c *Canon) syncEndPointFromMachine() {
	// Resyncing to machine position invalidates buffered readahead motion:
	// drop, don't flush (2.9 GET_EXTERNAL_POSITION calls drop_segments) —
	// this runs on abort/synch paths where the chain is stale.
	c.dropSegments()
	if c.task == nil || c.task.status == nil {
		return
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return
	}
	p := ms.CartePosFb
	c.state.endPoint = Pose{
		X: p.X, Y: p.Y, Z: p.Z,
		A: p.A, B: p.B, C: p.C,
		U: p.U, V: p.V, W: p.W,
	}
}

// getExternalPosition returns the current position with offsets removed,
// matching the C canon's unoffset_and_unrotate_pos + to_prog.
func (c *Canon) getExternalPosition() Pose {
	return c.state.fromAbsolute(c.state.endPoint)
}

func (c *Canon) GetExternalPositionX() (float64, error) {
	return c.getExternalPosition().X, nil
}

func (c *Canon) GetExternalPositionY() (float64, error) {
	return c.getExternalPosition().Y, nil
}

func (c *Canon) GetExternalPositionZ() (float64, error) {
	return c.getExternalPosition().Z, nil
}

func (c *Canon) GetExternalPositionA() (float64, error) {
	return c.getExternalPosition().A, nil
}

func (c *Canon) GetExternalPositionB() (float64, error) {
	return c.getExternalPosition().B, nil
}

func (c *Canon) GetExternalPositionC() (float64, error) {
	return c.getExternalPosition().C, nil
}

func (c *Canon) GetExternalPositionU() (float64, error) {
	return c.getExternalPosition().U, nil
}

func (c *Canon) GetExternalPositionV() (float64, error) {
	return c.getExternalPosition().V, nil
}

func (c *Canon) GetExternalPositionW() (float64, error) {
	return c.getExternalPosition().W, nil
}

// Probe position getters — return probe trip position in program units.

func (c *Canon) getProbePos() Pose {
	c.flushSegments() // 2.9 GET_EXTERNAL_PROBE_POSITION flushes
	if c.task.status == nil {
		return Pose{}
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return Pose{}
	}
	// Convert motstat Pose to task Pose
	machinePos := Pose{
		X: ms.Probe.Pos.X, Y: ms.Probe.Pos.Y, Z: ms.Probe.Pos.Z,
		A: ms.Probe.Pos.A, B: ms.Probe.Pos.B, C: ms.Probe.Pos.C,
		U: ms.Probe.Pos.U, V: ms.Probe.Pos.V, W: ms.Probe.Pos.W,
	}
	return c.state.fromAbsolute(machinePos)
}

func (c *Canon) GetExternalProbePositionX() (float64, error) { return c.getProbePos().X, nil }
func (c *Canon) GetExternalProbePositionY() (float64, error) { return c.getProbePos().Y, nil }
func (c *Canon) GetExternalProbePositionZ() (float64, error) { return c.getProbePos().Z, nil }
func (c *Canon) GetExternalProbePositionA() (float64, error) { return c.getProbePos().A, nil }
func (c *Canon) GetExternalProbePositionB() (float64, error) { return c.getProbePos().B, nil }
func (c *Canon) GetExternalProbePositionC() (float64, error) { return c.getProbePos().C, nil }
func (c *Canon) GetExternalProbePositionU() (float64, error) { return c.getProbePos().U, nil }
func (c *Canon) GetExternalProbePositionV() (float64, error) { return c.getProbePos().V, nil }
func (c *Canon) GetExternalProbePositionW() (float64, error) { return c.getProbePos().W, nil }

func (c *Canon) GetExternalProbeValue() (float64, error) {
	if c.task.status == nil {
		return 0, nil
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return 0, nil
	}
	return float64(ms.Probe.Val), nil
}

func (c *Canon) GetExternalProbeTrippedValue() (int32, error) {
	if c.task.status == nil {
		return 0, nil
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return 0, nil
	}
	return ms.Probe.Tripped, nil
}

// Spindle getters.

func (c *Canon) GetExternalSpeed(spindle int32) (float64, error) {
	if spindle >= 0 && spindle < 8 {
		return c.state.spindleSpeed[spindle], nil
	}
	return 0, nil
}

func (c *Canon) GetExternalSpindle(spindle int32) (int32, error) {
	// CANON_STOPPED=1, CANON_CLOCKWISE=2, CANON_COUNTERCLOCKWISE=3
	if int(spindle) < len(c.state.spindleSpeed) {
		speed := c.state.spindleSpeed[spindle]
		if speed > 0 {
			return 2, nil // CANON_CLOCKWISE
		} else if speed < 0 {
			return 3, nil // CANON_COUNTERCLOCKWISE
		}
	}
	return 1, nil // CANON_STOPPED
}

// Tool getters. toolOffset is stored in internal mm (UseToolLengthOffset
// converts on receipt); the interpreter expects these back in program units,
// like the C canon's GET_EXTERNAL_TOOL_LENGTH_*OFFSET (TO_PROG_LEN). Angular
// components are degrees in both domains.

func (c *Canon) GetExternalToolLengthXoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.X), nil
}
func (c *Canon) GetExternalToolLengthYoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.Y), nil
}
func (c *Canon) GetExternalToolLengthZoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.Z), nil
}
func (c *Canon) GetExternalToolLengthAoffset() (float64, error) {
	return c.state.toolOffset.A, nil
}
func (c *Canon) GetExternalToolLengthBoffset() (float64, error) {
	return c.state.toolOffset.B, nil
}
func (c *Canon) GetExternalToolLengthCoffset() (float64, error) {
	return c.state.toolOffset.C, nil
}
func (c *Canon) GetExternalToolLengthUoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.U), nil
}
func (c *Canon) GetExternalToolLengthVoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.V), nil
}
func (c *Canon) GetExternalToolLengthWoffset() (float64, error) {
	return c.state.toProg(c.state.toolOffset.W), nil
}

// GetExternalToolSlot mirrors 2.9's GET_EXTERNAL_TOOL_SLOT: the table SLOT of
// the tool in the spindle (feeds _setup.current_pocket / #<_current_pocket>).
// Classic resolves it with tooldata_find_index_for_tool, which yields 0 for
// the empty non-random spindle (the toolno==0 special case, tooldata_mmap.cc)
// and 0 for the random spindle slot; so does the store's find_index_for_tool.
func (c *Canon) GetExternalToolSlot() (int32, error) {
	if c.task.io == nil {
		return -1, nil
	}
	tis, err := c.task.io.GetToolInSpindle()
	if err != nil {
		return -1, nil
	}
	return toolIdxFor(tis), nil
}

// GetExternalSelectedToolSlot mirrors 2.9's GET_EXTERNAL_SELECTED_TOOL_SLOT
// (feeds _setup.selected_pocket / #<_selected_pocket>): the prepped tool's
// slot, -1 when nothing is prepped. io already tracks the prepped SLOT (it is
// what the tool-prep-index HAL pin carries), so this is a straight read.
func (c *Canon) GetExternalSelectedToolSlot() (int32, error) {
	if c.task.io == nil {
		return -1, nil
	}
	pp, err := c.task.io.GetPocketPrepped()
	if err != nil {
		return -1, nil
	}
	return pp, nil // -1 = idle, 0 = the spindle slot (non-random T0 unload)
}

// GetExternalToolTable serves the interp's tool_table[] straight out of the
// store, slot for slot — including slot 0, the spindle, which is a real row
// that iocontrol maintains on every tool change. That directness is the point
// of keying the store by slot: the previous toolno-keyed store could not
// represent slot 0 at all, so this getter had to reconstruct the spindle from
// io's tool-in-spindle plus a last-known-good snapshot in the Canon.
func (c *Canon) GetExternalToolTable(idx int32) (int32, int32, int32, [9]float64, float64, float64, float64, int32, error) {
	// A write this run has queued but not yet executed wins over the store —
	// the interpreter caches whatever it reads here and would otherwise
	// clobber its own uncommitted edit (canon_tooltable_pending.go).
	if e, ok := c.pendingTool(idx); ok {
		return 0, e.Toolno, e.Pocketno, toolOffsets(&e), e.Diameter, e.Frontangle, e.Backangle, e.Orientation, nil
	}
	retval, toolno, pocketno, offset, diameter, frontangle, backangle, orientation := getToolSlot(idx)
	return retval, toolno, pocketno, offset, diameter, frontangle, backangle, orientation, nil
}

// GetToolByNumber answers with the tool's SLOT (what find_tool_index reports)
// and its carousel POCKET (what find_tool_pocket reports) — two different
// numbers on a non-random toolchanger.
func (c *Canon) GetToolByNumber(toolno int32) (int32, int32, int32, [9]float64, float64, float64, float64, int32, error) {
	idx := toolIdxFor(toolno)
	// Same pending-write precedence as GetExternalToolTable. The slot lookup
	// still goes to the store: a pending write never changes which slot holds
	// a tool number, only that slot's contents.
	if idx >= 0 {
		if e, ok := c.pendingTool(idx); ok && e.Toolno == toolno {
			return 0, idx, e.Pocketno, toolOffsets(&e), e.Diameter, e.Frontangle, e.Backangle, e.Orientation, nil
		}
	}
	if idx < 0 || pkgTTClient == nil {
		// Missing or unresolvable tools report "not found" so the interp
		// raises its tool-not-in-table error (classic G43 Hn on an unknown
		// tool errors; it must not silently apply a zero offset).
		return -1, 0, 0, [9]float64{}, 0, 0, 0, 0, nil
	}
	entry, err := pkgTTClient.GetTool(idx)
	if err != nil || entry.Toolno != toolno {
		return -1, 0, 0, [9]float64{}, 0, 0, 0, 0, nil
	}
	return 0, idx, entry.Pocketno, toolOffsets(&entry), entry.Diameter, entry.Frontangle, entry.Backangle, entry.Orientation, nil
}

func (c *Canon) GetExternalTcFault() (int32, error)  { return 0, nil }
func (c *Canon) GetExternalTcReason() (int32, error) { return 0, nil }

// Queue/status getters.

func (c *Canon) GetExternalQueueEmpty() (int32, error) {
	c.flushSegments() // 2.9 GET_EXTERNAL_QUEUE_EMPTY flushes
	if c.task.status != nil {
		v, err := c.task.status.GetInpos()
		if err == nil && v != 0 {
			return 1, nil
		}
	}
	return 0, nil
}

func (c *Canon) GetExternalAxisMask() (int32, error) {
	return c.task.axisMask, nil
}

func (c *Canon) GetExternalDigitalInput(index, def int32) (int32, error) {
	// An M66 wait that timed out returns -1 so the interpreter stores -1 into
	// #5399 (C++ GET_EXTERNAL_DIGITAL_INPUT returns -1 when input_timeout==1).
	if c.task.inputTimedOut() {
		return -1, nil
	}
	if c.task.status == nil {
		return def, nil
	}
	ms, err := c.task.status.GetStatus()
	if err != nil || index < 0 || index >= 64 {
		return def, nil
	}
	return ms.SynchDi[index], nil
}

func (c *Canon) GetExternalAnalogInput(index int32, def float64) (float64, error) {
	// See GetExternalDigitalInput: a timed-out M66 wait returns -1 (#5399).
	if c.task.inputTimedOut() {
		return -1, nil
	}
	if c.task.status == nil {
		return def, nil
	}
	ms, err := c.task.status.GetStatus()
	if err != nil || index < 0 || index >= 64 {
		return def, nil
	}
	return ms.AnalogInput[index], nil
}

func (c *Canon) GetExternalFeedOverrideEnable() (int32, error) {
	if c.state.feedOverrideEnabled {
		return 1, nil
	}
	return 0, nil
}

func (c *Canon) GetExternalSpindleOverrideEnable(spindle int32) (int32, error) {
	if spindle >= 0 && spindle < 8 && c.state.speedOverrideEnabled[spindle] {
		return 1, nil
	}
	return 0, nil
}

func (c *Canon) GetExternalAdaptiveFeedEnable() (int32, error) {
	if c.state.adaptiveFeedEnabled {
		return 1, nil
	}
	return 0, nil
}

func (c *Canon) GetExternalFeedHoldEnable() (int32, error) {
	if c.state.feedHoldEnabled {
		return 1, nil
	}
	return 0, nil
}

func (c *Canon) GetExternalPlane() (int32, error) {
	return c.state.activePlane, nil
}

func (c *Canon) GetExternalParameterFileName() string {
	return c.parameterFileName
}

func (c *Canon) GetExternalOffsetApplied() (int32, error) {
	if c.task.status == nil {
		return 0, nil
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return 0, nil
	}
	return ms.ExternalOffsetsApplied, nil
}

func (c *Canon) GetExternalOffsets() [9]float64 {
	if c.task.status == nil {
		return [9]float64{}
	}
	ms, err := c.task.status.GetStatus()
	if err != nil {
		return [9]float64{}
	}
	return [9]float64{
		ms.EoffsetPose.X, ms.EoffsetPose.Y, ms.EoffsetPose.Z,
		ms.EoffsetPose.A, ms.EoffsetPose.B, ms.EoffsetPose.C,
		ms.EoffsetPose.U, ms.EoffsetPose.V, ms.EoffsetPose.W,
	}
}

func (c *Canon) GetUserDefinedResult() (float64, error) {
	return 0, nil
}
func (c *Canon) GetExternalHalValue(name string) (float64, int32, error) {
	val, found := hal.LookupValue(name)
	if found {
		return val, 1, nil
	}
	return 0, 0, nil
}
