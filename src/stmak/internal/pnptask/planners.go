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
// and outside every offset dead zone of *at least one* configured drawing.
//
// At least one, not all: a station may deliberately sit inside a dead zone of
// one scene and be reachable only in another. A fixture inside a sphere that
// has to open before the picker may enter it is exactly that, and
// deadzone-select is how the PLC says which scene the machine is in — each leg
// is planned against the scene that applies when the leg starts
// (control.travel), so a station blocked in the scene of the moment is a job
// that waits, not a machine that is mis-configured. A position that is inside
// no scene at all is the one that can never be driven to, and that is a
// configuration error.
//
// Checking taught positions is not the same as checking routes: a reachable
// position can still turn out to have no collision-free route to some other
// station, in some scenes or in all of them. Those are only knowable per pair
// and stay a job-time PLANNING_FAILED; what this rules out is the position
// itself being unreachable everywhere.
func (s *plannerSet) checkPositions(cfg *Config) error {
	// check reports whether pt is usable in any scene and, if it is not, why
	// the first drawing rejected it. One representative reason reads better
	// than the same complaint repeated per file — a position outside the
	// machine is rejected by every drawing for the same reason anyway.
	check := func(what string, pt pnproute.Point) error {
		var first error
		for _, pl := range s.planners {
			err := pl.CheckPoint(pt)
			if err == nil {
				return nil
			}
			if first == nil {
				first = err
			}
		}
		if first == nil {
			return nil // no drawings at all; loadDeadzoneFiles already refuses that
		}
		return fmt.Errorf("%s: (%.3f, %.3f) is usable in none of the %d dead-zone drawing(s); in %s: %w",
			what, pt.X, pt.Y, len(s.planners), s.files[0], first)
	}

	for i := range cfg.Procs {
		p := &cfg.Procs[i]
		if err := check(fmt.Sprintf("[%s]X/Y", p.Section), p.Pos); err != nil {
			return err
		}
		if p.HasWait {
			if err := check(fmt.Sprintf("[%s]WAIT_X/WAIT_Y", p.Section), p.Wait); err != nil {
				return err
			}
		}
		if p.HasWaitZone {
			if err := s.checkWaitZone(p); err != nil {
				return err
			}
		}
	}

	for _, d := range cfg.TrayDefs {
		if !d.HasLast {
			if err := check(fmt.Sprintf("[%s]FIRST_X/FIRST_Y", d.Section), d.First); err != nil {
				return err
			}
			continue
		}
		// Every slot, not just the four corners: a dead zone can sit in the
		// middle of a tray's footprint without touching its corners.
		for row := 0; row < d.Rows; row++ {
			for col := 0; col < d.Cols; col++ {
				what := fmt.Sprintf("[%s] slot (col %d, row %d)", d.Section, col, row)
				if err := check(what, d.SlotPos(col, row)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkWaitZone validates a station's dead-zone-derived wait position (D29) and
// resolves which zone it names.
//
// The wait point is not taught, so there is nothing here to check the way
// WAIT_X/WAIT_Y is checked: what is validated instead is that the geometry the
// wait point will be *derived* from exists and means what the configuration
// says it means.
//
// The containment test is the load-time form of "the planned route must cross
// the nominated zone". A station inside the zone cannot be approached from
// outside without crossing its boundary, so containment makes the crossing an
// invariant rather than something a job discovers by not stopping — which is
// the worst possible way to find out. Exactly one zone must contain it: zero
// leaves nothing to derive a wait point from, and more than one leaves it
// ambiguous which boundary the job should stop at.
//
// Like the taught positions above, the station point is read in the taught
// frame; the run-time crossing is computed in machine coordinates, where the
// route and the zones both live (D28).
func (s *plannerSet) checkWaitZone(p *ProcStation) error {
	n := uint32(len(s.planners))
	if p.WaitDeadzone >= n {
		return fmt.Errorf("[%s]WAIT_DEADZONE = %d: only %d dead-zone file(s) are configured",
			p.Section, p.WaitDeadzone, n)
	}
	if p.WaitClearDeadzone >= n {
		return fmt.Errorf("[%s]WAIT_CLEAR_DEADZONE = %d: only %d dead-zone file(s) are configured",
			p.Section, p.WaitClearDeadzone, n)
	}

	blocked := s.planners[p.WaitDeadzone]
	found := -1
	for i, dz := range blocked.Scene().Deadzones {
		if !dz.Poly.Contains(p.Pos) {
			continue
		}
		if found >= 0 {
			return fmt.Errorf("[%s]WAIT_DEADZONE = %d (%s): the station (%.3f, %.3f) is inside two overlapping zones (%d and %d) — which boundary the job waits at would be arbitrary",
				p.Section, p.WaitDeadzone, s.files[p.WaitDeadzone], p.Pos.X, p.Pos.Y, found, i)
		}
		found = i
	}
	if found < 0 {
		return fmt.Errorf("[%s]WAIT_DEADZONE = %d (%s): the station (%.3f, %.3f) is inside none of that drawing's %d dead zone(s) — a route to it would never cross one, so there would be no point to wait at",
			p.Section, p.WaitDeadzone, s.files[p.WaitDeadzone], p.Pos.X, p.Pos.Y,
			len(blocked.Scene().Deadzones))
	}
	p.WaitZoneIdx = found

	// The reference route the wait point is derived from is planned in the
	// clear drawing, so the station has to be a legal goal there. This is
	// stricter than checkPositions' "usable in at least one drawing" and it is
	// the drawing that has to be the one: WAIT_CLEAR_DEADZONE names the machine
	// as it is once the station opens up, and a station still blocked there is
	// a station no job could ever drive into.
	if err := s.planners[p.WaitClearDeadzone].CheckPoint(p.Pos); err != nil {
		return fmt.Errorf("[%s]WAIT_CLEAR_DEADZONE = %d (%s): the station (%.3f, %.3f) is not reachable in the drawing that is supposed to clear it: %w",
			p.Section, p.WaitClearDeadzone, s.files[p.WaitClearDeadzone], p.Pos.X, p.Pos.Y, err)
	}
	return nil
}
