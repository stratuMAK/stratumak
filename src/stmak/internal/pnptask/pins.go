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
	machineIsOn *hal.Pin[bool] // out: motion enabled and not estopped
	autoEnable  *hal.Pin[bool] // in:  high = auto mode, low = manual mode
	homed       *hal.Pin[bool] // out: all joints homed
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
	// startJob is IO, not in: the module clears it itself when the job ends,
	// so the PLC sees the handshake complete. An external clear mid-job is
	// ignored (D16).
	startJob *hal.Pin[bool]
	busy     *hal.Pin[bool]

	// planTime is the slowest route plan of the current (or last) job, in
	// seconds — the D13 budget made observable from outside. A job plans one
	// route per leg, so a pin carrying only the *latest* one could not be
	// sampled reliably from a PLC or a test: the interesting number is the
	// worst case, and it is reset when a job latches so it always describes
	// one job.
	planTime *hal.Pin[float64]

	// Error latch.
	errorFlag  *hal.Pin[bool]   // out
	errorID    *hal.Pin[uint32] // out, 0 = none
	errorReset *hal.Pin[bool]   // in, rising edge clears (D11)

	// Tuning knobs. Params rather than pins because they are adjusted with
	// halcmd setp and never wired (D2); the INI only seeds them.
	posSettleTime  *hal.Param[float64]
	pickSettleTime *hal.Param[float64]
	releaseTime    *hal.Param[float64]

	pickers []pickerPins
	trays   []trayPins
	procs   []procPins
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

	close       *hal.Pin[bool] // out: close command
	opened      *hal.Pin[bool] // in:  opened feedback
	closed      *hal.Pin[bool] // in:  fully closed — after a pick this means nothing was gripped
	missing     *hal.Pin[bool] // out: last pick found no material
	manualOpen  *hal.Pin[bool] // in:  rising edge opens, manual mode only
	manualClose *hal.Pin[bool] // in:  rising edge closes, manual mode only

	// posX/posY report where this picker actually is (feedback + offset), for
	// UI display and manual position teaching (D21). Like every float pin they
	// carry the internal millimetres (D23).
	posX *hal.Pin[float64]
	posY *hal.Pin[float64]

	// posXMu/posYMu are the same positions in *machine units* (D26, "-mu"
	// suffix): the teach workflow pastes these into the INI, which is written
	// in machine units — pasting the mm pins would be 25.4x off on an inch
	// machine. On a metric machine both pairs carry the same value.
	posXMu *hal.Pin[float64]
	posYMu *hal.Pin[float64]

	xOffset *hal.Param[float64] // this picker's XY offset from the machine position
	yOffset *hal.Param[float64]
}

// trayPins is one tray station's interface.
type trayPins struct {
	id uint32

	trayID   *hal.Pin[uint32]  // in:  selects the TRAYDEF; a change resets all slots (D17)
	setFull  *hal.Pin[bool]    // in:  edge, all slots := process-step (D8)
	setEmpty *hal.Pin[bool]    // in:  edge, all slots := -1
	zOffset  *hal.Pin[float64] // in:  added to Z_PICK
	empty    *hal.Pin[bool]    // out
	full     *hal.Pin[bool]    // out
}

// procPins is one process station's interface.
type procPins struct {
	id uint32

	zOffset     *hal.Pin[float64] // in:  added to Z_PICK
	busy        *hal.Pin[bool]    // in:  gates the approach (D15)
	hasMaterial *hal.Pin[bool]    // out: owned by pnptask, restored from persistence
	release     *hal.Pin[bool]    // out: request fixture release
	released    *hal.Pin[bool]    // in:  fixture released feedback
}

// newPins exports the whole pin and param tree on comp and seeds the params
// from the INI. It must run before the component is marked ready — and, because
// HAL "net" lines in the same file execute right after the load line, before
// the factory returns.
func newPins(comp *hal.Component, cfg *Config, pickers int) (*pinSet, error) {
	b := &pinBuilder{comp: comp}
	p := &pinSet{}

	p.estopOn = mkPin[bool](b, "estop-on", hal.In)
	p.machineOn = mkPin[bool](b, "machine-on", hal.In)
	p.machineIsOn = mkPin[bool](b, "machine-is-on", hal.Out)
	p.autoEnable = mkPin[bool](b, "auto-enable", hal.In)
	p.homed = mkPin[bool](b, "homed", hal.Out)
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
	p.startJob = mkPin[bool](b, "start-job", hal.IO)
	p.busy = mkPin[bool](b, "busy", hal.Out)
	p.planTime = mkPin[float64](b, "plan-time", hal.Out)

	p.errorFlag = mkPin[bool](b, "error", hal.Out)
	p.errorID = mkPin[uint32](b, "error-id", hal.Out)
	p.errorReset = mkPin[bool](b, "error-reset", hal.In)

	p.posSettleTime = mkParam[float64](b, "pos-settle-time", hal.RW)
	p.pickSettleTime = mkParam[float64](b, "pick-settle-time", hal.RW)
	p.releaseTime = mkParam[float64](b, "release-time", hal.RW)

	for n := 0; n < pickers; n++ {
		pre := fmt.Sprintf("picker.%d.", n)
		p.pickers = append(p.pickers, pickerPins{
			n:           n,
			close:       mkPin[bool](b, pre+"close", hal.Out),
			opened:      mkPin[bool](b, pre+"opened", hal.In),
			closed:      mkPin[bool](b, pre+"closed", hal.In),
			missing:     mkPin[bool](b, pre+"missing", hal.Out),
			manualOpen:  mkPin[bool](b, pre+"manual-open", hal.In),
			manualClose: mkPin[bool](b, pre+"manual-close", hal.In),
			posX:        mkPin[float64](b, pre+"pos-x", hal.Out),
			posY:        mkPin[float64](b, pre+"pos-y", hal.Out),
			posXMu:      mkPin[float64](b, pre+"pos-x-mu", hal.Out),
			posYMu:      mkPin[float64](b, pre+"pos-y-mu", hal.Out),
			xOffset:     mkParam[float64](b, pre+"x-offset", hal.RW),
			yOffset:     mkParam[float64](b, pre+"y-offset", hal.RW),
		})
	}

	for _, t := range cfg.Trays {
		pre := fmt.Sprintf("tray.%d.", t.ID)
		p.trays = append(p.trays, trayPins{
			id:       t.ID,
			trayID:   mkPin[uint32](b, pre+"tray-id", hal.In),
			setFull:  mkPin[bool](b, pre+"set-full", hal.In),
			setEmpty: mkPin[bool](b, pre+"set-empty", hal.In),
			zOffset:  mkPin[float64](b, pre+"z-offset", hal.In),
			empty:    mkPin[bool](b, pre+"empty", hal.Out),
			full:     mkPin[bool](b, pre+"full", hal.Out),
		})
	}

	for _, s := range cfg.Procs {
		pre := fmt.Sprintf("proc.%d.", s.ID)
		p.procs = append(p.procs, procPins{
			id:          s.ID,
			zOffset:     mkPin[float64](b, pre+"z-offset", hal.In),
			busy:        mkPin[bool](b, pre+"busy", hal.In),
			hasMaterial: mkPin[bool](b, pre+"has-material", hal.Out),
			release:     mkPin[bool](b, pre+"release", hal.Out),
			released:    mkPin[bool](b, pre+"released", hal.In),
		})
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

	// DEFAULT_TRAYDEF seeds the tray-id pin, exactly as a halcmd setp would.
	// This runs before the instance's net lines, so it only ever decides what
	// an *unwired* selector reads: linking the pin to a signal points it at
	// that signal and the seed is gone, which is what a station that does
	// select its geometry at runtime needs.
	for i, t := range cfg.Trays {
		if t.DefaultTrayDef != 0 {
			p.trays[i].trayID.Set(t.DefaultTrayDef)
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
