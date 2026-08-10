// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// The names a config refers its dead-zone drawings by, and the testdata files
// behind them: zones.dxf is a 600x500 limit with one dead zone clear of every
// station, zones_blocked.dxf puts that zone on top of proc station 20.
const (
	zonesA       = "zones_a.dxf"
	zonesB       = "zones_b.dxf"
	fixtureClear = "zones.dxf"
	fixtureBlock = "zones_blocked.dxf"
)

// setupPaths installs a resolver rooted at a temp directory holding the two
// dead-zone fixtures, and returns the directory.
func setupPaths(t *testing.T) string {
	t.Helper()
	return setupPathsWith(t, map[string]string{zonesA: fixtureClear, zonesB: fixtureClear})
}

// setupPathsWith is setupPaths with a chosen testdata drawing behind each
// configured file name.
func setupPathsWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		content, err := os.ReadFile(filepath.Join("testdata", src))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
	pathres.SetDefaultForTest(t, dir)
	return dir
}

const trajSection = `
[TRAJ]
COORDINATES = XYZ
LINEAR_UNITS = mm
`

const pnptaskSection = `
[PNPTASK]
AUTOHOME = 1
MOVE_HEIGHT = 30.0
CLEARANCE = 10.0
BLEND_TOLERANCE = 2.0
MOVE_VEL = 500.0
MOVE_ACC = 2000.0
Z_VEL = 50.0
Z_ACC = 500.0
POS_SETTLE_TIME = 0.1
PICK_SETTLE_TIME = 0.2
RELEASE_TIME = 0.3
RELEASE_TIMEOUT = 5.0
HOME_TIMEOUT = 30.0
DEADZONE_FILE = zones_a.dxf
DEADZONE_FILE = zones_b.dxf
`

const stationSections = `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 120.0
FIRST_Y = 400.0
LAST_X = 210.0
LAST_Y = 430.0
DIR_MODE = C+R-~
MAX_UNPOPULATED = 3

[PNPTASK_TRAYDEF_1]
ID = 2
ROWS = 0
COLS = 0
FIRST_X = 50.0
FIRST_Y = 50.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5

[PNPTASK_PROC_0]
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 5.0
WAIT_X = 250.0
WAIT_Y = 150.0

[PNPTASK_ROUTE_0]
ORIGIN = 10
DEST = 20
MOVE_HEIGHT = 15.0
`

// loadTestConfig parses an INI from text and loads the pnptask config from it,
// under the given instance namespace.
func loadTestConfig(t *testing.T, text, instance string) (*Config, error) {
	t.Helper()
	ini, err := inifile.ParseString(text)
	if err != nil {
		t.Fatalf("parsing test INI: %v", err)
	}
	if instance != "" {
		ini = ini.WithNamespace(instance)
	}
	return LoadConfig(ini)
}

func mustLoad(t *testing.T, text string) *Config {
	t.Helper()
	cfg, err := loadTestConfig(t, text, "pnp.task")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

func TestLoadConfig(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+stationSections)

	if !cfg.AutoHome {
		t.Error("AUTOHOME = 1 did not enable autohoming")
	}
	if cfg.MoveHeight != 30.0 || cfg.Clearance != 10.0 || cfg.BlendTolerance != 2.0 {
		t.Errorf("heights/clearance = %g/%g/%g, want 30/10/2", cfg.MoveHeight, cfg.Clearance, cfg.BlendTolerance)
	}
	if cfg.MoveVel != 500 || cfg.MoveAcc != 2000 || cfg.ZVel != 50 || cfg.ZAcc != 500 {
		t.Errorf("vel/acc = %g/%g/%g/%g", cfg.MoveVel, cfg.MoveAcc, cfg.ZVel, cfg.ZAcc)
	}
	if cfg.PosSettleTime != 0.1 || cfg.PickSettleTime != 0.2 || cfg.ReleaseTime != 0.3 {
		t.Errorf("settle times = %g/%g/%g", cfg.PosSettleTime, cfg.PickSettleTime, cfg.ReleaseTime)
	}
	if cfg.ReleaseTimeout != 5 || cfg.HomeTimeout != 30 {
		t.Errorf("timeouts = %g/%g", cfg.ReleaseTimeout, cfg.HomeTimeout)
	}

	if len(cfg.DeadzoneFiles) != 2 {
		t.Fatalf("deadzone files = %v, want 2 entries", cfg.DeadzoneFiles)
	}
	// Selector order is line order: deadzone-select = 0 must be the first key.
	if filepath.Base(cfg.DeadzoneFiles[0]) != zonesA || filepath.Base(cfg.DeadzoneFiles[1]) != zonesB {
		t.Errorf("deadzone files = %v, want [%s %s] in that order", cfg.DeadzoneFiles, zonesA, zonesB)
	}
	for _, f := range cfg.DeadzoneFiles {
		if !filepath.IsAbs(f) {
			t.Errorf("deadzone file %q is not resolved to an absolute path", f)
		}
	}

	if len(cfg.Axes) != 3 || cfg.Axes[0].Letter != 'X' || cfg.Axes[2].Index != 2 {
		t.Errorf("axes = %+v, want X/Y/Z with indices 0..2", cfg.Axes)
	}

	if len(cfg.TrayDefs) != 2 {
		t.Fatalf("traydefs = %d, want 2", len(cfg.TrayDefs))
	}
	d := cfg.TrayDefs[0]
	if d.ID != 1 || d.Rows != 4 || d.Cols != 10 {
		t.Errorf("traydef 0 = %+v", d)
	}
	if !d.HasLast || d.Last.X != 210 || d.Last.Y != 430 {
		t.Errorf("traydef 0 LAST = %+v (has=%v)", d.Last, d.HasLast)
	}
	if d.MaxUnpopulated != 3 {
		t.Errorf("MAX_UNPOPULATED = %d, want 3", d.MaxUnpopulated)
	}
	want := DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: false, Meander: true}
	if d.Dir != want {
		t.Errorf("DIR_MODE = %v, want %v", d.Dir, want)
	}
	if d.Endless() {
		t.Error("a 10x4 tray reports itself endless")
	}
	if e := cfg.TrayDefs[1]; !e.Endless() || e.HasLast {
		t.Errorf("traydef 1 = %+v, want an endless tray without LAST", e)
	}
	// An omitted MAX_UNPOPULATED still has to leave probing usable.
	if cfg.TrayDefs[1].MaxUnpopulated != defaultMaxUnpopulated {
		t.Errorf("default MAX_UNPOPULATED = %d, want %d", cfg.TrayDefs[1].MaxUnpopulated, defaultMaxUnpopulated)
	}
	if cfg.TrayDefs[1].Dir != defaultDirMode {
		t.Errorf("default DIR_MODE = %v, want %v", cfg.TrayDefs[1].Dir, defaultDirMode)
	}

	if len(cfg.Trays) != 1 || cfg.Trays[0].ID != 10 || cfg.Trays[0].ZPick != 2.5 {
		t.Errorf("trays = %+v", cfg.Trays)
	}
	if len(cfg.Procs) != 1 {
		t.Fatalf("procs = %d, want 1", len(cfg.Procs))
	}
	p := cfg.Procs[0]
	if p.ID != 20 || p.Pos.X != 300 || p.Pos.Y != 200 || p.ZPick != 5 {
		t.Errorf("proc = %+v", p)
	}
	if !p.HasWait || p.Wait.X != 250 || p.Wait.Y != 150 {
		t.Errorf("proc wait = %+v (has=%v)", p.Wait, p.HasWait)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Origin != 10 || cfg.Routes[0].Dest != 20 || cfg.Routes[0].MoveHeight != 15 {
		t.Errorf("routes = %+v", cfg.Routes)
	}
}

// TestLoadConfigDefaults pins the values an INI that omits every optional key
// gets — a timeout that silently defaults to "forever" would turn a stuck
// fixture into a hung job.
func TestLoadConfigDefaults(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+`
[PNPTASK]
MOVE_HEIGHT = 30.0
CLEARANCE = 10.0
DEADZONE_FILE = zones_a.dxf

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`)
	if cfg.AutoHome {
		t.Error("AUTOHOME defaults on; it must be opt-in")
	}
	if cfg.BlendTolerance != 0 || cfg.MoveVel != 0 || cfg.MoveAcc != 0 || cfg.ZVel != 0 || cfg.ZAcc != 0 {
		t.Errorf("optional limits default non-zero: %+v", cfg)
	}
	if cfg.PosSettleTime != 0 || cfg.PickSettleTime != 0 || cfg.ReleaseTime != 0 {
		t.Errorf("settle times default non-zero: %+v", cfg)
	}
	if cfg.ReleaseTimeout != defaultReleaseTimeout || cfg.HomeTimeout != defaultHomeTimeout {
		t.Errorf("timeouts = %g/%g, want %g/%g",
			cfg.ReleaseTimeout, cfg.HomeTimeout, defaultReleaseTimeout, defaultHomeTimeout)
	}
}

// TestLoadConfigInchMachine checks the machine-units -> mm conversion: on an
// inch machine every length in the INI is 25.4x the internal value.
func TestLoadConfigInchMachine(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, `
[TRAJ]
COORDINATES = XYZ
LINEAR_UNITS = inch

[PNPTASK]
MOVE_HEIGHT = 1.0
CLEARANCE = 0.5
MOVE_VEL = 2.0
POS_SETTLE_TIME = 0.1
DEADZONE_FILE = zones_a.dxf

[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 2
COLS = 2
FIRST_X = 1.0
FIRST_Y = 2.0
LAST_X = 3.0
LAST_Y = 4.0

[PNPTASK_PROC_0]
ID = 20
X = 10.0
Y = 20.0
Z_PICK = 0.25
`)
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %g, want %g", name, got, want)
		}
	}
	near("MOVE_HEIGHT", cfg.MoveHeight, 25.4)
	near("CLEARANCE", cfg.Clearance, 12.7)
	near("MOVE_VEL", cfg.MoveVel, 50.8)
	near("FIRST_X", cfg.TrayDefs[0].First.X, 25.4)
	near("LAST_Y", cfg.TrayDefs[0].Last.Y, 101.6)
	near("PROC X", cfg.Procs[0].Pos.X, 254)
	near("Z_PICK", cfg.Procs[0].ZPick, 6.35)
	// A dwell is a time, not a length: it must survive the conversion intact.
	near("POS_SETTLE_TIME", cfg.PosSettleTime, 0.1)
}

// TestLoadConfigNamespace checks that a namespaced section overrides the global
// one, which is how two task instances share one INI file.
func TestLoadConfigNamespace(t *testing.T) {
	setupPaths(t)
	text := trajSection + `
[pnp.task:PNPTASK]
MOVE_HEIGHT = 55.0
CLEARANCE = 10.0
DEADZONE_FILE = zones_b.dxf

[PNPTASK]
MOVE_HEIGHT = 30.0
CLEARANCE = 10.0
DEADZONE_FILE = zones_a.dxf

[pnp.task:PNPTASK_PROC_0]
ID = 77
X = 1.0
Y = 2.0
Z_PICK = 3.0

[PNPTASK_PROC_0]
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 5.0
`
	cfg := mustLoad(t, text)
	if cfg.MoveHeight != 55 {
		t.Errorf("MOVE_HEIGHT = %g, want the namespaced 55", cfg.MoveHeight)
	}
	if len(cfg.Procs) != 1 || cfg.Procs[0].ID != 77 {
		t.Errorf("procs = %+v, want the namespaced station 77", cfg.Procs)
	}

	// The same INI read without the namespace is the other instance's view.
	other, err := loadTestConfig(t, text, "other.task")
	if err != nil {
		t.Fatalf("LoadConfig (other namespace): %v", err)
	}
	if other.MoveHeight != 30 || other.Procs[0].ID != 20 {
		t.Errorf("unnamespaced view = %g / %+v, want the global 30 / station 20", other.MoveHeight, other.Procs)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	setupPaths(t)

	// Each case is a complete INI; want is a substring of the expected error.
	cases := []struct {
		name string
		ini  string
		want string
	}{{
		name: "missing MOVE_HEIGHT",
		ini:  trajSection + "[PNPTASK]\nCLEARANCE = 10.0\nDEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "MOVE_HEIGHT is required",
	}, {
		name: "clearance not above blend tolerance",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 2.0\nBLEND_TOLERANCE = 2.0\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "must be greater than BLEND_TOLERANCE",
	}, {
		name: "no dead-zone file",
		ini:  trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\n" + stationSections,
		want: "at least one dead-zone drawing",
	}, {
		name: "dead-zone file not found",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nDEADZONE_FILE = nope.dxf\n" +
			stationSections,
		want: "nope.dxf",
	}, {
		name: "malformed number",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = high\nCLEARANCE = 10.0\nDEADZONE_FILE = zones_a.dxf\n" +
			stationSections,
		want: "not a number",
	}, {
		name: "negative dwell",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nRELEASE_TIME = -1\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "must not be negative",
	}, {
		name: "no stations",
		ini:  trajSection + pnptaskSection,
		want: "no stations configured",
	}, {
		name: "duplicate id across kinds",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5

[PNPTASK_PROC_0]
ID = 10
X = 1.0
Y = 2.0
Z_PICK = 3.0
`,
		want: "already used by [PNPTASK_TRAY_0]",
	}, {
		name: "duplicate traydef id",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAYDEF_1]
ID = 1
FIRST_X = 3.0
FIRST_Y = 4.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "already used by [PNPTASK_TRAYDEF_0]",
	}, {
		name: "station id zero",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_0]
ID = 0
Z_PICK = 2.5
`,
		want: "id 0 is reserved",
	}, {
		name: "section numbering gap",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5

[PNPTASK_TRAY_2]
ID = 11
Z_PICK = 2.5
`,
		want: "[PNPTASK_TRAY_1] is missing",
	}, {
		name: "unknown DIR_MODE",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 1.0
FIRST_Y = 2.0
DIR_MODE = X+R+

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "DIR_MODE",
	}, {
		name: "grid without LAST",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "needs LAST_X/LAST_Y",
	}, {
		name: "half a LAST",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 2
COLS = 2
FIRST_X = 1.0
FIRST_Y = 2.0
LAST_X = 3.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "LAST_X and LAST_Y must be given together",
	}, {
		name: "half an endless tray",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 0
COLS = 5
FIRST_X = 1.0
FIRST_Y = 2.0
LAST_X = 3.0
LAST_Y = 4.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "must both be 0 (endless tray) or both be positive",
	}, {
		name: "endless tray with LAST",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 0
COLS = 0
FIRST_X = 1.0
FIRST_Y = 2.0
LAST_X = 3.0
LAST_Y = 4.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "must not define LAST_X/LAST_Y",
	}, {
		name: "non-numeric section index",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_O]
ID = 10
Z_PICK = 2.5
`,
		want: `section index "O" is not a number`,
	}, {
		name: "collapsed grid axis",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 200.0
LAST_Y = 400.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "row pitch is zero",
	}, {
		name: "zero MAX_UNPOPULATED",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 1.0
FIRST_Y = 2.0
MAX_UNPOPULATED = 0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "MAX_UNPOPULATED",
	}, {
		name: "half a wait position",
		ini: trajSection + pnptaskSection + `
[PNPTASK_PROC_0]
ID = 20
X = 1.0
Y = 2.0
Z_PICK = 3.0
WAIT_X = 4.0
`,
		want: "WAIT_X and WAIT_Y must be given together",
	}, {
		name: "route to unknown station",
		ini: trajSection + pnptaskSection + stationSections + `
[PNPTASK_ROUTE_1]
ORIGIN = 10
DEST = 99
MOVE_HEIGHT = 15.0
`,
		want: "DEST = 99: no such station",
	}, {
		name: "duplicate route pair",
		ini: trajSection + pnptaskSection + stationSections + `
[PNPTASK_ROUTE_1]
ORIGIN = 10
DEST = 20
MOVE_HEIGHT = 20.0
`,
		want: "already overridden by [PNPTASK_ROUTE_0]",
	}, {
		name: "missing COORDINATES",
		ini:  "[TRAJ]\nLINEAR_UNITS = mm\n" + pnptaskSection + stationSections,
		want: "[TRAJ]COORDINATES is required",
	}, {
		name: "COORDINATES without Z",
		ini:  "[TRAJ]\nCOORDINATES = XY\n" + pnptaskSection + stationSections,
		want: `needs the X, Y and Z axes ("Z" is missing)`,
	}, {
		name: "unknown axis letter",
		ini:  "[TRAJ]\nCOORDINATES = XYZQ\n" + pnptaskSection + stationSections,
		want: "unknown axis letter",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTestConfig(t, tc.ini, "pnp.task")
			if err == nil {
				t.Fatalf("LoadConfig succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
