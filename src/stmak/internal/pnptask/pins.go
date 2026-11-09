// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// pinSet is the complete HAL interface of one pnptask instance: the module is
// commanded entirely over these pins, with no UI or REST path into a job (the
// point of the design — it has to integrate with a PLC world).
//
// Names follow HAL convention, not the design document's underscores:
// "pnp.task.start-job", "pnp.task.picker.0.close", "pnp.task.tray.10.set-full".
type pinSet struct {
	// Machine state and mode.
	estopOn     *hal.Pin[bool] // in:  external estop chain, high aborts
	machineOn   *hal.Pin[bool] // in:  request machine enable (level)
	machineIsOn *outBit        // out: motion enabled and not estopped
	autoEnable  *hal.Pin[bool] // in:  high = auto mode, low = manual mode
	waitAbort   *hal.Pin[bool] // in:  level, ends a busy-station wait (D33)
	park        *hal.Pin[bool] // in:  rising edge, auto mode: go to PARK_X/Y/Z (D34)
	parked      *outBit        // out: standing at the park position (D34)
	homed       *outBit        // out: all joints homed
	// home is the operator's homing request (D25). Without it a machine with
	// AUTOHOME = 0 could never be homed at all: jobs would refuse with
	// NOT_HOMED and the jog pins are ignored while unhomed (D18).
	home *hal.Pin[bool] // in: rising edge homes all joints

	// Manual jog, one pair per [TRAJ]COORDINATES axis.
	jog      []jogPins
	jogSpeed *hal.Pin[float64] // in: jog velocity, latched at jog start

	// Job handshake. The three id pins are latched on the start-job edge.
	processStep    *hal.Pin[uint32]
	originID       *hal.Pin[uint32]
	destID         *hal.Pin[uint32]
	deadzoneSelect *hal.Pin[uint32]
	// startJob (in): a rising edge with busy low and no error latched is a
	// job, accepted by the cycle function in that servo cycle -- busy rises,
	// and the parameters above and stay-near are latched from that same
	// sample (PNPTASK_RT_BOUNDARY.md §4.4). An edge while busy or latched is
	// ignored. The requester drops it when it likes; the module never writes
	// it.
	startJob *hal.Pin[bool]
	stayNear *hal.Pin[bool] // in:  the next job starts where this one ends: end at NEAR_HEIGHT (D37)
	// busy (out, the cycle function's): a job or a park accepted and not
	// over. It rises in the servo cycle of the request and falls in the cycle
	// that also carries the outcome -- the error pins and every model output.
	busy *rtBit

	// planTime is the slowest route plan of the current (or last) job, in
	// seconds — the D13 budget made observable from outside. A job plans one
	// route per leg, so a pin carrying only the *latest* one could not be
	// sampled reliably from a PLC or a test: the interesting number is the
	// worst case, and it is reset when a job latches so it always describes
	// one job.
	planTime *outF64

	// The scene gate (D45). sceneHold (in): whatever changes the dead-zone
	// scene is about to, or is doing it — start no leg that touches the
	// movable region. sceneBusy (out): such a leg is in flight, or taking the
	// lock. Unwired, sceneHold reads low and no leg ever waits.
	sceneHold *hal.Pin[bool]
	sceneBusy *outBit

	// cycle counts the cycle function's runs, and sync is the cycle of the
	// input sample the control loop's latest published frame was computed
	// from. A producer that changed an input in cycle k knows the loop has
	// seen it once sync - k >= 0 (wrap-safe, PNPTASK_RT_BOUNDARY.md §5.2).
	cycle *rtU32
	sync  *outU32

	// Error latch, the cycle function's (§4.4): the control loop raises, the
	// function latches the first fault and clears it on error-reset, flag, id,
	// station and the pickers' missing flags each in one servo cycle.
	errorFlag  *rtBit         // out
	errorID    *rtU32         // out, 0 = none
	errorReset *hal.Pin[bool] // in, rising edge clears (D11); one servo cycle is enough

	// errorStation is the station the latched fault is about (a TRAYDEF or
	// PROCDEF id), 0 when it is about none: error-id says what went wrong,
	// this says where -- RELEASE_TIMEOUT alone cannot tell the PLC whose
	// fixture stuck when a job touches two.
	errorStation *rtU32 // out, 0 = none

	// Tuning knobs. Params rather than pins because they are adjusted with
	// halcmd setp and never wired (D2); the INI only seeds them.
	posSettleTime *hal.Param[float64]

	pickSettleTime *hal.Param[float64]
	releaseTime    *hal.Param[float64]

	pickers []pickerPins
	trays   []trayPins
	procs   []procPins

	// deadzoneFree reports, per configured DEADZONE_FILE and in the same order
	// the deadzone-select pin indexes them by, whether the machine point is
	// currently clear of that drawing's zones. It answers a question the
	// planner cannot: planning keeps the head OUT of a zone, but nothing
	// retracts a head that is already inside one, and a fixture that closes
	// around the machine — a sphere, a door, a press — has to know the portal
	// has left before it moves.
	//
	// These are the only pins this module does not write from Go. They are
	// published by the cyclic function in the servo thread (deadzone_rt.go),
	// which is why they are created here but never Set anywhere: their names,
	// direction and meaning are unchanged, only their freshness — every servo
	// cycle rather than every control cycle. A configuration that does not addf
	// that function leaves them at "not clear"; Start warns when it finds none
	// scheduled.
	deadzoneFree []*hal.Pin[bool]
}

// jogPins is the manual jog pair of one axis.
type jogPins struct {
	axis Axis
	pos  *hal.Pin[bool]
	neg  *hal.Pin[bool]
}

// pickerPins is one picker's interface. Picker 1 exists only with pickers=2
// (D5); the two are symmetrical (D3) and differ only in their offset params.
type pickerPins struct {
	n int

	close       *outBit        // out: close command
	opened      *hal.Pin[bool] // in:  opened feedback
	closed      *hal.Pin[bool] // in:  fully closed — after a pick this means nothing was gripped
	missing     *rtBit         // out: the latched fault is this picker finding no material
	manualOpen  *hal.Pin[bool] // in:  rising edge opens, manual mode only
	manualClose *hal.Pin[bool] // in:  rising edge closes, manual mode only

	// holds/originID are the picker's contents: whether it is carrying
	// material and which station that material came from. The model has always
	// known this and never published it, which left a PLC unable to see the §8
	// swap obligation — the record that decides which job it is allowed to
	// command next, and which survives a restart in persistence, so a
	// sequencer cannot reconstruct it from its own history either. 0 on
	// originID means "holding nothing" while holds is low; station ids start
	// at 1. holds high with originID 0 is material of unknown origin — a
	// manual close that gripped something the model had no record of
	// (§8.1) — which no job can place and only a manual open lets go of.
	//
	// A retained record (§8.1 — material a manual open let go of, still in the
	// operator's hands) reads as holding nothing, because the picker is not
	// holding it. Jobs are refused for the whole of that window anyway.
	holds    *outBit
	originID *outU32

	// posX/posY report where this picker actually is (feedback + offset), for
	// UI display and manual position teaching (D21). Like every float pin they
	// carry the internal millimetres (D23).
	posX *outF64
	posY *outF64

	// posXMu/posYMu are the same positions in *machine units* (D26, "-mu"
	// suffix): the teach workflow pastes these into the INI, which is written
	// in machine units — pasting the mm pins would be 25.4x off on an inch
	// machine. On a metric machine both pairs carry the same value.
	posXMu *outF64
	posYMu *outF64

	xOffset *hal.Param[float64] // this picker's XY offset from the machine position
	yOffset *hal.Param[float64]
}

// trayPins is one tray station's interface.
//
// step, empty and avail are three answers to what used to be one pin. set-full
// wrote the *job's* process-step into every slot and empty asked "is there
// anything at the job's process-step", which made both change meaning whenever
// a sequencer varied the job step across the legs of one cascade. The tray's
// own step is now its own input, and the two questions a caller actually has —
// "is this tray bare" and "has it anything left to process" — are separate
// outputs.
type trayPins struct {
	id uint32

	trayID   *hal.Pin[uint32]  // in:  selects the TRAYDEF; a change resets all slots (D17)
	step     *hal.Pin[uint32]  // in:  the tray's own process step, seeded by DEFAULT_STEP
	setFull  *hal.Pin[bool]    // in:  edge, all slots := step (D8)
	setEmpty *hal.Pin[bool]    // in:  edge, all slots := -1
	zOffset  *hal.Pin[float64] // in:  added to Z_PICK
	empty    *outBit           // out: every slot is -1 — physically bare
	avail    *outBit           // out: a slot holds `step` and probing has not given up
	full     *outBit           // out: no free slot
	count    *outU32           // out: slots holding something — a bin's fill level
	// resets counts the set-full/set-empty requests applied (they wait for
	// the end of a job): a PLC compares it with the count it asked for.
	resets *outU32
}

// procPins is one process station's interface.
type procPins struct {
	id uint32

	zOffset *hal.Pin[float64] // in:  added to Z_PICK
	busy    *hal.Pin[bool]    // in:  gates the approach (D15)

	// releaseSettle is RELEASE_SETTLE: the dwell between this fixture
	// confirming it released and the lift out of it. A param and not a pin
	// because it is tuning, like the other times, and per station because it
	// describes the fixture -- see ProcStation.ReleaseSettle.
	releaseSettle *hal.Param[float64]

	hasMaterial *outBit        // out: owned by pnptask, restored from persistence
	release     *outBit        // out: request fixture release
	released    *hal.Pin[bool] // in:  fixture released feedback

	// setHasMaterial/setEmpty are the operator resync the tray resets have
	// always had (§6.4). "Model occupied, fixture empty" self-corrects — a pick
	// that grips nothing clears the flag — but the inverse does not: a fixture
	// hand-loaded while the model thought it free sends the next place-to-proc
	// down onto the occupant. set-has-material is also what makes a station
	// *probe* possible at all: a job may only originate at a station the model
	// believes occupied, so "assume something is there and go and check" needs
	// a way to say the first half. Edges, honored in both modes.
	setHasMaterial *hal.Pin[bool] // in: edge, has-material := true
	setEmpty       *hal.Pin[bool] // in: edge, has-material := false
	resets         *outU32        // out: those requests applied, as on the trays
}

// newPins exports the whole pin and param tree on comp and seeds the params
// from the INI. It must run before the component is marked ready — and, because
// HAL "net" lines in the same file execute right after the load line, before
// the factory returns.
func newPins(comp *hal.Component, cfg *Config, pickers int, out *outFrame) (*pinSet, error) {
	b := &pinBuilder{comp: comp}
	p := &pinSet{}

	p.estopOn = mkPin[bool](b, "estop-on", hal.In)
	p.machineOn = mkPin[bool](b, "machine-on", hal.In)
	p.machineIsOn = mkOutBit(b, "machine-is-on", &out.machine_is_on)
	p.autoEnable = mkPin[bool](b, "auto-enable", hal.In)
	p.waitAbort = mkPin[bool](b, "wait-abort", hal.In)
	p.park = mkPin[bool](b, "park", hal.In)
	p.parked = mkOutBit(b, "parked", &out.parked)
	p.homed = mkOutBit(b, "homed", &out.homed)
	p.home = mkPin[bool](b, "home", hal.In)

	for _, ax := range cfg.Axes {
		letter := strings.ToLower(string(ax.Letter))
		p.jog = append(p.jog, jogPins{
			axis: ax,
			pos:  mkPin[bool](b, "jog-"+letter+"-pos", hal.In),
			neg:  mkPin[bool](b, "jog-"+letter+"-neg", hal.In),
		})
	}
	p.jogSpeed = mkPin[float64](b, "jog-speed", hal.In)

	p.processStep = mkPin[uint32](b, "process-step", hal.In)
	p.originID = mkPin[uint32](b, "origin-id", hal.In)
	p.destID = mkPin[uint32](b, "dest-id", hal.In)
	p.deadzoneSelect = mkPin[uint32](b, "deadzone-select", hal.In)
	p.startJob = mkPin[bool](b, "start-job", hal.In)
	p.stayNear = mkPin[bool](b, "stay-near", hal.In)
	p.busy = mkRTBit(b, "busy", &out.busy)
	p.planTime = mkOutF64(b, "plan-time", &out.plan_time)
	p.cycle = mkRTU32(b, "cycle", &out.cycle)
	p.sync = mkOutU32(b, "sync", &out.sync)
	p.sceneHold = mkPin[bool](b, "scene-hold", hal.In)
	p.sceneBusy = mkOutBit(b, "scene-busy", &out.scene_busy)

	p.errorFlag = mkRTBit(b, "error", &out.error)
	p.errorID = mkRTU32(b, "error-id", &out.error_id)
	p.errorStation = mkRTU32(b, "error-station", &out.error_station)
	p.errorReset = mkPin[bool](b, "error-reset", hal.In)

	p.posSettleTime = mkParam[float64](b, "pos-settle-time", hal.RW)
	p.pickSettleTime = mkParam[float64](b, "pick-settle-time", hal.RW)
	p.releaseTime = mkParam[float64](b, "release-time", hal.RW)

	for n := 0; n < pickers; n++ {
		pre := fmt.Sprintf("picker.%d.", n)
		op := &out.picker[n]
		p.pickers = append(p.pickers, pickerPins{
			n:           n,
			close:       mkOutBit(b, pre+"close", &op.close),
			opened:      mkPin[bool](b, pre+"opened", hal.In),
			closed:      mkPin[bool](b, pre+"closed", hal.In),
			missing:     mkRTBit(b, pre+"missing", &op.missing),
			manualOpen:  mkPin[bool](b, pre+"manual-open", hal.In),
			manualClose: mkPin[bool](b, pre+"manual-close", hal.In),
			holds:       mkOutBit(b, pre+"holds", &op.holds),
			originID:    mkOutU32(b, pre+"origin-id", &op.origin_id),
			posX:        mkOutF64(b, pre+"pos-x", &op.pos_x),
			posY:        mkOutF64(b, pre+"pos-y", &op.pos_y),
			posXMu:      mkOutF64(b, pre+"pos-x-mu", &op.pos_x_mu),
			posYMu:      mkOutF64(b, pre+"pos-y-mu", &op.pos_y_mu),
			xOffset:     mkParam[float64](b, pre+"x-offset", hal.RW),
			yOffset:     mkParam[float64](b, pre+"y-offset", hal.RW),
		})
	}

	for i, t := range cfg.Trays {
		pre := fmt.Sprintf("tray.%d.", t.ID)
		ot := &out.tray[i]
		p.trays = append(p.trays, trayPins{
			id:       t.ID,
			trayID:   mkPin[uint32](b, pre+"tray-id", hal.In),
			step:     mkPin[uint32](b, pre+"step", hal.In),
			setFull:  mkPin[bool](b, pre+"set-full", hal.In),
			setEmpty: mkPin[bool](b, pre+"set-empty", hal.In),
			zOffset:  mkPin[float64](b, pre+"z-offset", hal.In),
			empty:    mkOutBit(b, pre+"empty", &ot.empty),
			avail:    mkOutBit(b, pre+"avail", &ot.avail),
			full:     mkOutBit(b, pre+"full", &ot.full),
			count:    mkOutU32(b, pre+"count", &ot.count),
			resets:   mkOutU32(b, pre+"resets", &ot.resets),
		})
	}

	for i, s := range cfg.Procs {
		pre := fmt.Sprintf("proc.%d.", s.ID)
		op := &out.proc[i]
		p.procs = append(p.procs, procPins{
			id:             s.ID,
			zOffset:        mkPin[float64](b, pre+"z-offset", hal.In),
			releaseSettle:  mkParam[float64](b, pre+"release-settle", hal.RW),
			busy:           mkPin[bool](b, pre+"busy", hal.In),
			hasMaterial:    mkOutBit(b, pre+"has-material", &op.has_material),
			release:        mkOutBit(b, pre+"release", &op.release),
			released:       mkPin[bool](b, pre+"released", hal.In),
			setHasMaterial: mkPin[bool](b, pre+"set-has-material", hal.In),
			setEmpty:       mkPin[bool](b, pre+"set-empty", hal.In),
			resets:         mkOutU32(b, pre+"resets", &op.resets),
		})
	}

	// One per drawing, indexed exactly as deadzone-select indexes them.
	for i := range cfg.DeadzoneFiles {
		p.deadzoneFree = append(p.deadzoneFree,
			mkPin[bool](b, fmt.Sprintf("deadzone.%d.free", i), hal.Out))
	}

	if b.err != nil {
		return nil, b.err
	}

	// Params start at the zero value of their type, so the INI seeding has to
	// happen explicitly (see hal.NewParam). The picker offsets have no INI
	// keys by design (D3): they are taught with halcmd setp, and picker 0
	// defaults to 0/0.
	p.posSettleTime.Set(cfg.PosSettleTime)
	p.pickSettleTime.Set(cfg.PickSettleTime)
	p.releaseTime.Set(cfg.ReleaseTime)
	for i, s := range cfg.Procs {
		p.procs[i].releaseSettle.Set(s.ReleaseSettle)
	}

	// DEFAULT_TRAYDEF and DEFAULT_STEP seed their pins, exactly as a halcmd
	// setp would. This runs before the instance's net lines, so it only ever
	// decides what an *unwired* pin reads: linking one points it at the signal
	// and the seed is gone, which is what a station that does select its
	// geometry — or its step — at runtime needs.
	for i, t := range cfg.Trays {
		if t.DefaultTrayDef != 0 {
			p.trays[i].trayID.Set(t.DefaultTrayDef)
		}
		if t.DefaultStep != 0 {
			p.trays[i].step.Set(t.DefaultStep)
		}
	}

	return p, nil
}

// pinBuilder collects the first creation error so a whole pin tree can be built
// as one expression per pin instead of five lines of error handling each. A
// failed pin leaves a nil in the set, which is safe only because the caller
// checks b.err before the set is used for anything.
type pinBuilder struct {
	comp *hal.Component
	err  error
}

func mkPin[T hal.PinValue](b *pinBuilder, name string, dir hal.Direction) *hal.Pin[T] {
	if b.err != nil {
		return nil
	}
	p, err := hal.NewPin[T](b.comp, name, dir)
	if err != nil {
		b.err = fmt.Errorf("creating pin %q: %w", name, err)
		return nil
	}
	return p
}

func mkParam[T hal.ParamValue](b *pinBuilder, name string, dir hal.ParamDirection) *hal.Param[T] {
	if b.err != nil {
		return nil
	}
	p, err := hal.NewParam[T](b.comp, name, dir)
	if err != nil {
		b.err = fmt.Errorf("creating param %q: %w", name, err)
		return nil
	}
	return p
}
