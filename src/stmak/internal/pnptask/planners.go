// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"strings"

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
	// movable is the region a scene change can move something through (D45):
	// every drawn zone that is not drawn identically in every drawing.
	movable []pnproute.Polygon
	// warnings are load-time findings about the drawings (D54), logged once
	// by the module next to homeWarnings.
	warnings []string
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
		if w := clipOuterToAxes(scene, cfg); w != "" {
			s.warnings = append(s.warnings, fmt.Sprintf("dead-zone file %d (%s): %s", i, path, w))
		}
		pl, err := pnproute.NewPlanner(scene, cfg.Clearance)
		if err != nil {
			return nil, fmt.Errorf("dead-zone file %d (%s): %w", i, path, err)
		}
		s.planners = append(s.planners, pl)
	}
	if err := s.checkPositions(cfg); err != nil {
		return nil, err
	}
	s.movable = movableRegion(s.planners)
	return s, nil
}

// clipOuterToAxes lays the X/Y axis position limits over the drawing's outer
// limit (D54) and says, if it cut anything, where the drawing reached past
// them.
//
// The planner keeps CLEARANCE from the outer limit, so what it may use is the
// outer limit eroded by CLEARANCE. The drawing is clipped to the axis limits
// grown by CLEARANCE, which makes that usable region end exactly at the axis
// limits wherever the drawing reached further: the route, the smooth curve
// and the Z arcs (all checked against the eroded limit) can no longer
// command a position the axis refuses. Before this the coating machine's
// drawing let a spline leg bow to Y = -1 against a limit of -0.2, and the
// per-segment limit check faulted the leg with the lift already running.
//
// A warning, not an error: the drawing is not wrong to be generous -- the
// outer limit is the physical envelope, the axis limits are where the head
// may be sent, and the planner now uses the tighter of the two. It is worth
// knowing, though, because a station that only the drawing's surplus made
// reachable is now refused at load.
//
// The box is wider than the axis limits by half the dispatch check's
// tolerance (moveFuzz): the containment test does not decide a point exactly
// on the boundary, and a head standing on a limit -- homed at the corner of
// its range -- has to start a route. Half, so everything the planner accepts
// still passes checkMoveInLimits.
func clipOuterToAxes(scene *pnproute.Scene, cfg *Config) string {
	c := cfg.Clearance + moveFuzz/2
	lo := pnproute.Point{X: cfg.AxisMin.X - c, Y: cfg.AxisMin.Y - c}
	hi := pnproute.Point{X: cfg.AxisMax.X + c, Y: cfg.AxisMax.Y + c}
	before, after := scene.Bounds()
	clipped, cut := scene.Outer.ClipBox(lo, hi)
	if !cut {
		return ""
	}
	if len(clipped) < 3 {
		// Nothing of the drawing inside the axis range: leave it to
		// NewPlanner and checkPositions to refuse with their own words.
		return fmt.Sprintf("the outer limit lies entirely outside the axis limits X [%.3f, %.3f], Y [%.3f, %.3f]",
			cfg.AxisMin.X, cfg.AxisMax.X, cfg.AxisMin.Y, cfg.AxisMax.Y)
	}
	scene.Outer = clipped
	var past []string
	side := func(axis, which string, usable, limit float64) {
		past = append(past, fmt.Sprintf("%s %s %.3f (after CLEARANCE) against [AXIS_%s]%s_LIMIT %.3f",
			axis, which, usable, axis, strings.ToUpper(which), limit))
	}
	if before.X < lo.X {
		side("X", "min", before.X+cfg.Clearance, cfg.AxisMin.X)
	}
	if after.X > hi.X {
		side("X", "max", after.X-cfg.Clearance, cfg.AxisMax.X)
	}
	if before.Y < lo.Y {
		side("Y", "min", before.Y+cfg.Clearance, cfg.AxisMin.Y)
	}
	if after.Y > hi.Y {
		side("Y", "max", after.Y-cfg.Clearance, cfg.AxisMax.Y)
	}
	return "the outer limit reaches past the axis limits (" + strings.Join(past, "; ") +
		"); routes are planned within the axis limits there"
}

// zoneSameEps is how close two drawn vertices have to be to be the same
// vertex, in mm: the drawings are typed coordinates, so one outline drawn in
// two files matches exactly; the tolerance only absorbs the unit scaling.
const zoneSameEps = 1e-6

// movableRegion is D45's movable region: the drawn zones that are not drawn
// identically in every drawing. A zone present in all of them is something
// that never moves; one that is not is where a fixture stands in some scene
// and not in another, so it is where a scene change sweeps. The drawn zones,
// not the offset ones -- the question is physical, as for deadzone.N.free
// (D28) -- and every drawing's own copy of a moving zone is kept: no union is
// needed to ask whether a path touches any of them. One drawing: empty.
func movableRegion(planners []*pnproute.Planner) []pnproute.Polygon {
	if len(planners) < 2 {
		return nil
	}
	var region []pnproute.Polygon
	for i, pl := range planners {
		for _, z := range pl.Zones() {
			static := true
			for j, other := range planners {
				if j == i {
					continue
				}
				found := false
				for _, w := range other.Zones() {
					if z.Same(w, zoneSameEps) {
						found = true
						break
					}
				}
				if !found {
					static = false
					break
				}
			}
			if !static {
				region = append(region, z)
			}
		}
	}
	return region
}

// touchesMovable reports whether a path -- a leg's route from where the head
// stands, both ends included -- touches the movable region (D45).
func (s *plannerSet) touchesMovable(path []pnproute.Point) bool {
	for _, z := range s.movable {
		if pnproute.PathTouches(path, z) {
			return true
		}
	}
	return false
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

	// The park position (D34) is where the head goes to be out of the way,
	// whatever the machine is doing meanwhile, so it has to be clear in EVERY
	// drawing, not in one: a park inside a zone that only the other scene
	// draws is a head standing where something is about to move.
	if cfg.HasPark {
		for n, pl := range s.planners {
			if err := pl.CheckPoint(cfg.Park); err != nil {
				return fmt.Errorf("[PNPTASK]PARK_X/PARK_Y: (%.3f, %.3f) is not usable in %s: %w",
					cfg.Park.X, cfg.Park.Y, s.files[n], err)
			}
		}
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
