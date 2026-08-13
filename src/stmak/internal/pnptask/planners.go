// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// plannerSet holds one route planner per configured DEADZONE_FILE, in INI line
// order — the order the deadzone-select pin indexes them by.
//
// Building them is the expensive half of route planning (offsetting the zones,
// eroding the limit and building the static visibility graph, ~20 ms per file),
// which is exactly why it happens once here at load time: a per-job Plan then
// only inserts its two query points, keeping job-time planning far inside the
// 100 ms budget of D13.
type plannerSet struct {
	files    []string // absolute paths, parallel to planners
	planners []*pnproute.Planner
}

// at returns the planner the deadzone-select pin names. The selector is a PLC
// value, so an index past the configured list is a job error (§7.5), not a
// configuration failure — and never a silent fallback to drawing 0, which would
// plan travel around obstacles the operator did not choose.
func (s *plannerSet) at(index uint32) (*pnproute.Planner, error) {
	if uint64(index) >= uint64(len(s.planners)) {
		return nil, faultf(errInvalidDeadzoneSelect,
			"deadzone-select %d: only %d dead-zone file(s) are configured", index, len(s.planners))
	}
	return s.planners[index], nil
}

// newPlanners loads every dead-zone drawing, builds its planner and validates
// every taught position against all of them.
//
// The drawings describe the same coordinates as the INI (D23), so they are
// read in machine units and scaled to the internal mm here. cfg.Clearance is
// NOT scaled here: like every INI length it was already converted to mm by
// the config loader — scaling it again would square the factor on a non-mm
// machine.
func newPlanners(cfg *Config) (*plannerSet, error) {
	s := &plannerSet{files: cfg.DeadzoneFiles}
	for i, path := range cfg.DeadzoneFiles {
		scene, err := pnproute.LoadDXFFile(path)
		if err != nil {
			return nil, fmt.Errorf("dead-zone file %d (%s): %w", i, path, err)
		}
		scaleScene(scene, machineToMMFactor(cfg.LinearUnits))
		pl, err := pnproute.NewPlanner(scene, cfg.Clearance)
		if err != nil {
			return nil, fmt.Errorf("dead-zone file %d (%s): %w", i, path, err)
		}
		s.planners = append(s.planners, pl)
	}
	if err := s.checkPositions(cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// machineToMMFactor is what a machine-unit length is multiplied by to become
// millimetres. linearUnits is machine units per mm, so the factor is its
// reciprocal — 1 on a metric machine, 25.4 on an inch one.
func machineToMMFactor(linearUnits float64) float64 {
	if linearUnits <= 0 {
		return 1
	}
	return 1 / linearUnits
}

// scaleScene scales a loaded scene in place. Scaling the parsed geometry rather
// than the query points keeps every later comparison — CheckPoint, Plan, the
// route it returns — in one unit system, the internal mm.
func scaleScene(s *pnproute.Scene, factor float64) {
	if factor == 1 {
		return
	}
	scalePoly(s.Outer, factor)
	for i := range s.Deadzones {
		dz := &s.Deadzones[i]
		scalePoly(dz.Poly, factor)
		// Kind, Center and Radius are not decoration: NewPlanner offsets a
		// circle analytically from them, so a scaled polygon with an unscaled
		// centre would silently plan around a circle somewhere else entirely.
		dz.Center.X *= factor
		dz.Center.Y *= factor
		dz.Radius *= factor
	}
}

func scalePoly(p pnproute.Polygon, factor float64) {
	for i := range p {
		p[i].X *= factor
		p[i].Y *= factor
	}
}

// homeWarnings names every drawing the homed position cannot start a route in.
// A warning, not an error: a machine that is jogged off the home corner before
// its first job is legitimate — but the operator gets told at load, with the
// cause, instead of at commissioning by a PLANNING_FAILED that only names
// coordinates.
func (s *plannerSet) homeWarnings(cfg *Config) []string {
	var out []string
	for i, pl := range s.planners {
		if err := pl.CheckPoint(cfg.Home); err != nil {
			out = append(out, fmt.Sprintf(
				"the homed position (%.3f, %.3f) cannot start a route in dead-zone file %d (%s): %v — the first job after homing will fail with PLANNING_FAILED unless the machine is jogged clear first; widen the outer limit (it must cover the home switch positions with CLEARANCE to spare)",
				cfg.Home.X, cfg.Home.Y, i, s.files[i], err))
		}
	}
	return out
}

// checkPositions is the geometric half of the startup validation (§5.1): every
// position the module can ever drive to must be inside the eroded outer limit
// and outside every offset dead zone, in *every* configured drawing — the
// deadzone-select pin picks the drawing at job start, so a position that is
// only valid in some of them is a job that fails on the machine.
//
// Checking taught positions is not the same as checking routes: a reachable
// position can still turn out to have no collision-free route to some other
// station. Those are only knowable per pair and stay a job-time
// PLANNING_FAILED; what this rules out is the position itself being
// unreachable, which is a configuration error and belongs here.
func (s *plannerSet) checkPositions(cfg *Config) error {
	for i, pl := range s.planners {
		where := fmt.Sprintf("dead-zone file %d (%s)", i, s.files[i])

		for _, p := range cfg.Procs {
			sec := p.Section
			if err := pl.CheckPoint(p.Pos); err != nil {
				return fmt.Errorf("[%s]X/Y: %s: %w", sec, where, err)
			}
			if p.HasWait {
				if err := pl.CheckPoint(p.Wait); err != nil {
					return fmt.Errorf("[%s]WAIT_X/WAIT_Y: %s: %w", sec, where, err)
				}
			}
		}

		for _, d := range cfg.TrayDefs {
			sec := d.Section
			if !d.HasLast {
				if err := pl.CheckPoint(d.First); err != nil {
					return fmt.Errorf("[%s]FIRST_X/FIRST_Y: %s: %w", sec, where, err)
				}
				continue
			}
			// Every slot, not just the four corners: a dead zone can sit in
			// the middle of a tray's footprint without touching its corners.
			for row := 0; row < d.Rows; row++ {
				for col := 0; col < d.Cols; col++ {
					if err := pl.CheckPoint(d.SlotPos(col, row)); err != nil {
						return fmt.Errorf("[%s] slot (col %d, row %d): %s: %w", sec, col, row, where, err)
					}
				}
			}
		}
	}
	return nil
}
