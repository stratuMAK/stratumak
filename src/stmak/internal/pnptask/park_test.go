// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"strings"
	"testing"
)

// Park (D34): one move to [PNPTASK]PARK_X/Y/Z on a rising park edge, in auto
// mode, with parked up until the head is moved again.

// parkTweak configures a park position inside the fixture's travel envelope.
// The load-time check does not see it (tweak runs after the load); the cases
// for that check load an INI of their own.
func parkTweak(c *Config) {
	c.HasPark = true
	c.Park.X, c.Park.Y = 150, 160
	c.ParkZ = 20
}

func newParkFixture(t *testing.T, tweak func(*Config)) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{tweak: tweak})
	f.homed()
	f.mot.setPos(100, 100, 60)
	return f
}

// TestParkMovesToTheParkPosition: up to the movement height, across, down to
// PARK_Z -- and parked once it has arrived.
func TestParkMovesToTheParkPosition(t *testing.T) {
	f := newParkFixture(t, parkTweak)
	if f.bit("parked") {
		t.Fatal("parked is up before any park")
	}
	f.mot.resetCalls()
	f.press(f.m.pins.park)
	f.eventually("parked", func() bool { return f.bit("parked") })
	f.eventually("busy to clear", func() bool { return !f.bit("busy") })
	f.requireOK("park")

	moves := f.mot.moveList()
	zs := zAtXY(moves, 150, 160)
	if len(zs) < 2 || zs[0] != 30 || zs[len(zs)-1] != 20 {
		t.Errorf("Z sequence at the park position = %v, want the move height 30 first and PARK_Z 20 last", zs)
	}
	last := moves[len(moves)-1].pos
	if last.X != 150 || last.Y != 160 || last.Z != 20 {
		t.Errorf("last move = %+v, want (150, 160, 20)", last)
	}
}

// TestParkedClearsOnTheNextJob: parked describes where the head is, so the
// next motion takes it down.
func TestParkedClearsOnTheNextJob(t *testing.T) {
	f := newParkFixture(t, parkTweak)
	f.selectTray(1)
	f.fillTray(0)
	f.press(f.m.pins.park)
	f.eventually("parked", func() bool { return f.bit("parked") })
	f.eventually("busy to clear", func() bool { return !f.bit("busy") })

	f.runJob(10, 20, 0)
	f.requireOK("a job after the park")
	if f.bit("parked") {
		t.Error("parked is still up after a job moved the head")
	}
}

// TestParkedClearsOnMachineOff: a machine that is off is not standing
// anywhere it can vouch for.
func TestParkedClearsOnMachineOff(t *testing.T) {
	f := newParkFixture(t, parkTweak)
	f.press(f.m.pins.park)
	f.eventually("parked", func() bool { return f.bit("parked") })
	f.setBit(f.m.pins.machineOn, false)
	f.eventually("parked to drop with the machine", func() bool { return !f.bit("parked") })
}

// TestParkNotConfigured: a park request without the keys is a fault that
// says so, not a request that silently does nothing.
func TestParkNotConfigured(t *testing.T) {
	f := newParkFixture(t, nil)
	f.press(f.m.pins.park)
	f.eventually("the refusal", func() bool { return f.bit("error") })
	f.requireError("park without PARK_X/Y/Z", errParkNotConfigured)
	if n := len(f.mot.moveList()); n != 0 {
		t.Errorf("a refused park dispatched %d moves", n)
	}
}

// TestParkRefusedInManualMode: like a job, a park is the PLC's, and the PLC
// owns the machine only in auto mode.
func TestParkRefusedInManualMode(t *testing.T) {
	f := newParkFixture(t, parkTweak)
	f.setBit(f.m.pins.autoEnable, false)
	f.press(f.m.pins.park)
	f.eventually("the refusal", func() bool { return f.bit("error") })
	f.requireError("park in manual mode", errAutoDisabled)
	if f.bit("parked") {
		t.Error("parked rose for a refused park")
	}
}

// TestLoadConfigPark: the three keys come together, in machine units.
func TestLoadConfigPark(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)
	if cfg.HasPark {
		t.Error("a park position without the keys")
	}
	with := func(keys string) string {
		return trajSection + strings.Replace(pnptaskSection, "HOME_TIMEOUT = 30.0\n", "HOME_TIMEOUT = 30.0\n"+keys, 1) + stationSections
	}
	cfg = mustLoad(t, with("PARK_X = 150\nPARK_Y = 160\nPARK_Z = 20\n"))
	if !cfg.HasPark || cfg.Park.X != 150 || cfg.Park.Y != 160 || cfg.ParkZ != 20 {
		t.Errorf("park = %v (%+v, Z %g), want (150, 160, 20)", cfg.HasPark, cfg.Park, cfg.ParkZ)
	}
	if _, err := loadTestConfig(t, with("PARK_X = 150\nPARK_Y = 160\n"), "pnp.task"); err == nil ||
		!strings.Contains(err.Error(), "must be given together") {
		t.Errorf("PARK_X/Y without PARK_Z: err = %v, want a must-be-given-together refusal", err)
	}
}

// TestParkOutsideTheMachineRefusedAtLoad: the park point is checked against
// every drawing at load, like a station.
func TestParkOutsideTheMachineRefusedAtLoad(t *testing.T) {
	setupPaths(t)
	ini := trajSection + strings.Replace(pnptaskSection, "HOME_TIMEOUT = 30.0\n",
		"HOME_TIMEOUT = 30.0\nPARK_X = 700\nPARK_Y = 200\nPARK_Z = 20\n", 1) + stationSections
	_, err := loadModule(t, ini, testInstanceName(t))
	if err == nil || !strings.Contains(err.Error(), "PARK_X/PARK_Y") {
		t.Fatalf("factory error = %v, want the park position refused", err)
	}
}
