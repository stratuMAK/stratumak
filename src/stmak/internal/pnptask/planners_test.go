// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// boundsOfPolygon returns the lower-left and upper-right corner of a polygon.
func boundsOfPolygon(p pnproute.Polygon) (min, max pnproute.Point) {
	min = pnproute.Point{X: math.Inf(1), Y: math.Inf(1)}
	max = pnproute.Point{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, v := range p {
		min.X, min.Y = math.Min(min.X, v.X), math.Min(min.Y, v.Y)
		max.X, max.Y = math.Max(max.X, v.X), math.Max(max.Y, v.Y)
	}
	return min, max
}

func TestNewPlanners(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)

	set, err := newPlanners(cfg)
	if err != nil {
		t.Fatalf("newPlanners: %v", err)
	}
	// One planner per DEADZONE_FILE, in the order the deadzone-select pin
	// indexes them.
	if len(set.planners) != 2 || len(set.files) != 2 {
		t.Fatalf("planners/files = %d/%d, want 2/2", len(set.planners), len(set.files))
	}

	// The fixture limit is 600x500 and CLEARANCE is 10, so the usable region
	// is the limit eroded by 10 on every side.
	min, max := boundsOfPolygon(set.planners[0].Boundary())
	if math.Abs(min.X-10) > 1e-6 || math.Abs(min.Y-10) > 1e-6 ||
		math.Abs(max.X-590) > 1e-6 || math.Abs(max.Y-490) > 1e-6 {
		t.Errorf("eroded boundary = (%g,%g)..(%g,%g), want (10,10)..(590,490)", min.X, min.Y, max.X, max.Y)
	}

	// A planner that came out of this usable is one that can route between two
	// configured stations.
	route, err := set.planners[0].Plan(cfg.TrayDefs[0].SlotPos(0, 0), cfg.Procs[0].Pos)
	if err != nil {
		t.Fatalf("Plan between two configured stations: %v", err)
	}
	if len(route.Waypoints) < 2 {
		t.Errorf("route has %d points, want at least a start and a goal", len(route.Waypoints))
	}
}

// TestHomeWarnings: a homed position the drawings cannot start a route from is
// named at load — a warning, not an error (a machine jogged clear before its
// first job is legitimate), so commissioning learns the cause instead of
// meeting a PLANNING_FAILED that only names coordinates.
func TestHomeWarnings(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)
	set, err := newPlanners(cfg)
	if err != nil {
		t.Fatalf("newPlanners: %v", err)
	}

	// The default home (0,0) sits at the fixture drawing's corner, inside the
	// eroded band: both drawings warn.
	if warns := set.homeWarnings(cfg); len(warns) != 2 {
		t.Errorf("home (0,0): %d warnings, want 2: %q", len(warns), warns)
	}

	// A home inside the usable region is silent.
	cfg.Home = pnproute.Point{X: 100, Y: 100}
	if warns := set.homeWarnings(cfg); len(warns) != 0 {
		t.Errorf("home (100,100): unexpected warnings %q", warns)
	}
}

// TestConfigReadsHomePosition: [JOINT_0]HOME/[JOINT_1]HOME feed Config.Home in
// machine units.
func TestConfigReadsHomePosition(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+"[JOINT_0]\nHOME = 25\n[JOINT_1]\nHOME = 35\n"+pnptaskSection+stationSections)
	if cfg.Home.X != 25 || cfg.Home.Y != 35 {
		t.Errorf("Home = %+v, want (25, 35)", cfg.Home)
	}
}

// TestPlannersScaleToMM checks D23: the drawings are in machine units, so on an
// inch machine both the scene and the clearance have to be scaled to the
// internal mm — otherwise every station would sit far outside a 600 mm limit.
func TestPlannersScaleToMM(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, `
[KINS]
JOINTS = 3
[TRAJ]
COORDINATES = XYZ
LINEAR_UNITS = inch

[PNPTASK]
MOVE_HEIGHT = 1.0
CLEARANCE = 10.0
DEADZONE_FILE = zones_a.dxf

[PNPTASK_PROC_0]
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 0.25
`)
	set, err := newPlanners(cfg)
	if err != nil {
		t.Fatalf("newPlanners: %v", err)
	}
	// 600 x 500 inch, eroded by a 10 inch clearance, all in mm.
	min, max := boundsOfPolygon(set.planners[0].Boundary())
	wantMin, wantMax := 10*25.4, 590*25.4
	if math.Abs(min.X-wantMin) > 1e-6 || math.Abs(max.X-wantMax) > 1e-6 {
		t.Errorf("eroded boundary X = %g..%g, want %g..%g", min.X, max.X, wantMin, wantMax)
	}
	if math.Abs(set.planners[0].Clearance()-254) > 1e-9 {
		t.Errorf("clearance = %g mm, want 254", set.planners[0].Clearance())
	}
	// The station is at 300 inch = 7620 mm, which is only inside the limit if
	// the scene was scaled with it.
	if err := set.planners[0].CheckPoint(cfg.Procs[0].Pos); err != nil {
		t.Errorf("configured station rejected after scaling: %v", err)
	}
}

func TestPlannersRejectBadPositions(t *testing.T) {
	// The blocked fixture puts a dead zone over proc station 20, and only in
	// the *second* configured file — a position has to be usable in every
	// drawing the deadzone-select pin can pick.
	blockedInSecondFile := func(t *testing.T) {
		t.Helper()
		setupPathsWith(t, map[string]string{zonesA: fixtureClear, zonesB: fixtureBlock})
	}
	clearZones := func(t *testing.T) {
		t.Helper()
		setupPaths(t)
	}

	cases := []struct {
		name    string
		setup   func(*testing.T)
		ini     string
		want    string
		wantErr error
	}{{
		name:    "proc station inside a dead zone",
		setup:   blockedInSecondFile,
		ini:     trajSection + pnptaskSection + stationSections,
		want:    "[PNPTASK_PROC_0]X/Y",
		wantErr: pnproute.ErrInDeadzone,
	}, {
		name:  "proc station outside the limit",
		setup: clearZones,
		ini: trajSection + pnptaskSection + `
[PNPTASK_PROC_0]
ID = 20
X = 700.0
Y = 200.0
Z_PICK = 5.0
`,
		want:    "[PNPTASK_PROC_0]X/Y",
		wantErr: pnproute.ErrOutsideLimit,
	}, {
		name:  "wait position inside a dead zone",
		setup: clearZones,
		ini: trajSection + pnptaskSection + `
[PNPTASK_PROC_0]
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 5.0
WAIT_X = 500.0
WAIT_Y = 100.0
`,
		want:    "[PNPTASK_PROC_0]WAIT_X/WAIT_Y",
		wantErr: pnproute.ErrInDeadzone,
	}, {
		// A dead zone in the middle of a tray's footprint touches no corner,
		// which is why every slot is checked and not just the extremes.
		name:  "interior tray slot inside a dead zone",
		setup: clearZones,
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 1
COLS = 5
FIRST_X = 300.0
FIRST_Y = 100.0
LAST_X = 500.0
LAST_Y = 100.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want:    "[PNPTASK_TRAYDEF_0] slot (col 3, row 0)",
		wantErr: pnproute.ErrInDeadzone,
	}, {
		name:  "single-position tray outside the limit",
		setup: clearZones,
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 5.0
FIRST_Y = 5.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want:    "[PNPTASK_TRAYDEF_0]FIRST_X/FIRST_Y",
		wantErr: pnproute.ErrOutsideLimit,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			cfg := mustLoad(t, tc.ini)
			_, err := newPlanners(cfg)
			if err == nil {
				t.Fatalf("newPlanners succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %s", err, tc.want)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
			// The message has to say which drawing rejected the position:
			// with several configured, "it is in a dead zone" alone leaves the
			// operator to guess which one.
			if !strings.Contains(err.Error(), "dead-zone file") {
				t.Errorf("error = %v, want it to name the dead-zone file", err)
			}
		})
	}
}

// TestPlannersRejectBadDrawing covers the load-time failure of the drawing
// itself: pnproute refuses geometry it cannot plan around, and that has to fail
// the module load rather than surface at the first job.
func TestPlannersRejectBadDrawing(t *testing.T) {
	dir := setupPaths(t)
	empty := "0\nSECTION\n2\nENTITIES\n0\nENDSEC\n0\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, zonesA), []byte(empty), 0o644); err != nil {
		t.Fatalf("overwriting fixture: %v", err)
	}
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)
	_, err := newPlanners(cfg)
	if err == nil {
		t.Fatal("newPlanners accepted a drawing with no outer limit")
	}
	if !strings.Contains(err.Error(), "dead-zone file 0") {
		t.Errorf("error = %v, want it to name the offending file", err)
	}
}
