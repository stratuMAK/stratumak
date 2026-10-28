// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// Park (D34): one move to a configured machine position, for a PLC that wants
// the head out of the way -- before a maintenance stop, while it waits for the
// next tray. It is not a job: there is no material and no station, only a
// route planned around the dead zones like any leg, at MOVE_HEIGHT, and a Z
// stroke down to PARK_Z.

// runParkRequest runs one park the way runJobHandshake runs one job: busy
// for its duration, the outcome on the error pins. parked rises at the end.
func (c *control) runParkRequest() {
	if c.m.pins.errorFlag.Get() {
		// As a job: an error is latched first-wins, so a park started under
		// it could fail with nothing to show for it.
		c.m.logger.Warn("pnptask: park refused, an error is still latched",
			"error_id", c.m.pins.errorID.Get())
		return
	}
	c.m.pins.busy.Set(true)
	c.parked = false
	err := c.runPark()
	switch {
	case err == nil:
		c.parked = true
		c.m.logger.Info("pnptask: parked")
	case errors.Is(err, errStopping):
		c.m.logger.Warn("pnptask: park abandoned, module stopping")
	default:
		c.raise(err)
	}
	c.m.pins.busy.Set(false)
}

func (c *control) runPark() error {
	cfg := c.m.cfg
	if !cfg.HasPark {
		return faultf(errParkNotConfigured, "park requested, but [PNPTASK]PARK_X/PARK_Y/PARK_Z are not set")
	}
	if c.estopped {
		return faultf(errEstop, "cannot park: estop is active")
	}
	if !c.enabled {
		return faultf(errMachineOff, "cannot park: machine is off")
	}
	if err := c.ensureHomed(); err != nil {
		return err
	}
	if err := c.seedCmdPos(); err != nil {
		return err
	}
	// The legs are planned for picker 0 with its offset added back, so the
	// machine itself -- not a pick point -- ends up on PARK_X/PARK_Y.
	j := &job{height: cfg.MoveHeight, travel: cfg.MoveHeight, pickedSlot: -1}
	off := c.pickerOffset(0)
	if err := c.retract(j.height); err != nil {
		return err
	}
	if err := c.travel(j, 0, pnproute.Point{X: cfg.Park.X + off.X, Y: cfg.Park.Y + off.Y}); err != nil {
		return err
	}
	return c.zStroke(cfg.ParkZ)
}
