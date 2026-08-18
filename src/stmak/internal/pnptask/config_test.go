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
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
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
[KINS]
JOINTS = 3
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
POS_TOLERANCE = 0.1
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
COL_STEP = 10.0
ROW_STEP = 10.0
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
	if cfg.PosTolerance != 0.1 {
		t.Errorf("POS_TOLERANCE = %g, want 0.1", cfg.PosTolerance)
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
	if d.ColStep != 10 || d.RowStep != 10 {
		t.Errorf("traydef 0 steps = %g/%g, want 10/10", d.ColStep, d.RowStep)
	}
	// 10 columns of 10 mm and 4 rows of 10 mm reach exactly the taught LAST
	// with no tilt at all, so the derived angle has to come out at zero.
	if math.Abs(d.Angle) > 1e-12 {
		t.Errorf("derived ANGLE = %g rad, want 0 for a grid that fits LAST head-on", d.Angle)
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

// TestLoadConfigNamedSections checks that a station section may be named
// instead of numbered. The name is only ever echoed in diagnostics — every
// reference between sections, the HAL pins and the persisted state all key off
// the ID — so this pins that the loader carries the name as written and that
// numeric names still work as plain names: no ordering, no gap-free run.
func TestLoadConfigNamedSections(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+`
[PNPTASK_TRAYDEF_MATERIAL]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 120.0
FIRST_Y = 400.0
LAST_X = 210.0
LAST_Y = 430.0
COL_STEP = 10.0
ROW_STEP = 10.0

[PNPTASK_TRAY_MATERIAL_IN]
ID = 10
Z_PICK = 2.5

[PNPTASK_TRAY_7]
ID = 11
Z_PICK = 2.5

[PNPTASK_PROC_COATER]
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 5.0

[PNPTASK_ROUTE_IN_TO_COATER]
ORIGIN = 10
DEST = 20
MOVE_HEIGHT = 15.0
`)
	if len(cfg.TrayDefs) != 1 || cfg.TrayDefs[0].Section != "PNPTASK_TRAYDEF_MATERIAL" {
		t.Errorf("traydefs = %+v, want one named PNPTASK_TRAYDEF_MATERIAL", cfg.TrayDefs)
	}
	// A numeric name alongside a word one, neither of them index 0.
	if len(cfg.Trays) != 2 ||
		cfg.Trays[0].Section != "PNPTASK_TRAY_MATERIAL_IN" || cfg.Trays[0].ID != 10 ||
		cfg.Trays[1].Section != "PNPTASK_TRAY_7" || cfg.Trays[1].ID != 11 {
		t.Errorf("trays = %+v", cfg.Trays)
	}
	if len(cfg.Procs) != 1 || cfg.Procs[0].Section != "PNPTASK_PROC_COATER" || cfg.Procs[0].ID != 20 {
		t.Errorf("procs = %+v", cfg.Procs)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Section != "PNPTASK_ROUTE_IN_TO_COATER" {
		t.Errorf("routes = %+v", cfg.Routes)
	}
}

// TestLoadConfigDefaultTrayDef checks the optional tray-id seed: present it is
// the TRAYDEF id it names, absent it is 0, which is what tells the pin builder
// to leave the selector alone.
func TestLoadConfigDefaultTrayDef(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+`
[PNPTASK_TRAYDEF_MATERIAL]
ID = 3
FIRST_X = 50.0
FIRST_Y = 50.0

[PNPTASK_TRAY_FIXED]
ID = 10
Z_PICK = 2.5
DEFAULT_TRAYDEF = 3

[PNPTASK_TRAY_SELECTED]
ID = 11
Z_PICK = 2.5
`)
	if len(cfg.Trays) != 2 {
		t.Fatalf("trays = %+v, want 2", cfg.Trays)
	}
	if cfg.Trays[0].DefaultTrayDef != 3 {
		t.Errorf("DEFAULT_TRAYDEF = %d, want 3", cfg.Trays[0].DefaultTrayDef)
	}
	if cfg.Trays[1].DefaultTrayDef != 0 {
		t.Errorf("omitted DEFAULT_TRAYDEF = %d, want 0 (no seed)", cfg.Trays[1].DefaultTrayDef)
	}
}

// TestLoadConfigNamedSectionOverlay checks that a namespaced section overlaying
// a named global one stays *one* station: the two views are the same section,
// and counting it twice would export a second set of HAL pins for it.
func TestLoadConfigNamedSectionOverlay(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+pnptaskSection+`
[PNPTASK_TRAY_MATERIAL]
ID = 10
Z_PICK = 2.5

[pnp.task:PNPTASK_TRAY_MATERIAL]
Z_PICK = 4.0
`)
	if len(cfg.Trays) != 1 {
		t.Fatalf("trays = %+v, want the overlay to stay one station", cfg.Trays)
	}
	if cfg.Trays[0].ZPick != 4.0 {
		t.Errorf("Z_PICK = %g, want the namespaced 4.0 to win", cfg.Trays[0].ZPick)
	}
}

// TestLoadConfigDefaults pins the values an INI that omits every optional key
// gets — a timeout that silently defaults to "forever" would turn a stuck
// fixture into a hung job.
// TestLoadConfigKinematicsWarning: pnptask assumes trivkins' identity
// joint/axis mapping; a declared non-trivkins kinematics cannot be refused
// (the HAL file decides what actually loads) but must earn a warning, and a
// declared trivkins — or an INI that stays silent — must not.
func TestLoadConfigKinematicsWarning(t *testing.T) {
	setupPaths(t)
	base := trajSection + pnptaskSection + stationSections

	for _, tc := range []struct {
		kins string
		warn bool
	}{
		{"", false},
		{"KINEMATICS = trivkins coordinates=XYZ\n", false},
		{"KINEMATICS = genserkins\n", true},
	} {
		cfg := mustLoad(t, base+"[KINS]\n"+tc.kins)
		got := false
		for _, w := range cfg.Warnings {
			if strings.Contains(w, "KINEMATICS") {
				got = true
			}
		}
		if got != tc.warn {
			t.Errorf("[KINS]%q: kinematics warning = %v, want %v (warnings: %q)",
				tc.kins, got, tc.warn, cfg.Warnings)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setupPaths(t)
	cfg := mustLoad(t, trajSection+`
[PNPTASK]
MOVE_HEIGHT = 30.0
CLEARANCE = 10.0
POS_TOLERANCE = 0.1
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
[KINS]
JOINTS = 3
[TRAJ]
COORDINATES = XYZ
LINEAR_UNITS = inch

[PNPTASK]
MOVE_HEIGHT = 1.0
CLEARANCE = 0.5
POS_TOLERANCE = 0.01
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
COL_STEP = 2.0
ROW_STEP = 2.0

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
	near("POS_TOLERANCE", cfg.PosTolerance, 0.254)
	near("FIRST_X", cfg.TrayDefs[0].First.X, 25.4)
	near("LAST_Y", cfg.TrayDefs[0].Last.Y, 101.6)
	// A step width is a length like any other, and the grid it builds has to
	// land on the converted LAST — an unconverted one would miss it by 25.4x
	// and the POS_TOLERANCE fit would already have refused the load.
	near("COL_STEP", cfg.TrayDefs[0].ColStep, 50.8)
	near("slot(1,1)", cfg.TrayDefs[0].SlotPos(1, 1).Y, 101.6)
	near("PROC X", cfg.Procs[0].Pos.X, 254)
	near("Z_PICK", cfg.Procs[0].ZPick, 6.35)
	// A dwell is a time, not a length: it must survive the conversion intact.
	near("POS_SETTLE_TIME", cfg.PosSettleTime, 0.1)
}

// TestLoadConfigTiltedGrid covers the D24 fit: COL_STEP/ROW_STEP build the
// grid, and LAST only says how the tray sits on the table. The angle is
// derived from the two corners — never configured — and it bears on FIRST.
func TestLoadConfigTiltedGrid(t *testing.T) {
	setupPaths(t)
	// 5 x 3 slots of 10 mm, so the far corner sits 40 mm along the column axis
	// and 20 mm along the row axis. Taught at 30 degrees that is
	// (40cos30 - 20sin30, 40sin30 + 20cos30) = (24.6410, 37.3205) off FIRST.
	cfg := mustLoad(t, trajSection+pnptaskSection+`
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 3
COLS = 5
FIRST_X = 100.0
FIRST_Y = 200.0
LAST_X = 124.6410161514
LAST_Y = 237.3205080757
COL_STEP = 10.0
ROW_STEP = 10.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`)
	d := cfg.TrayDefs[0]
	if got := d.Angle * 180 / math.Pi; math.Abs(got-30) > 1e-6 {
		t.Errorf("derived angle = %g deg, want 30", got)
	}
	nearPoint(t, "slot(0,0)", d.SlotPos(0, 0), d.First)
	nearPoint(t, "slot(4,2)", d.SlotPos(4, 2), d.Last)
	// One column step along the tilted column axis, not along X.
	nearPoint(t, "slot(1,0)", d.SlotPos(1, 0), pnproute.Point{X: 108.660254038, Y: 205})
}

// TestLoadConfigTeachingResidue pins what POS_TOLERANCE buys: a LAST taught a
// little short of where the step widths put the corner is accepted, and the
// grid still comes out of the step widths — the corner slot lands where the
// pitch says, not on the taught point. Interpolating FIRST->LAST instead would
// smear that teaching error across every slot of the tray.
func TestLoadConfigTeachingResidue(t *testing.T) {
	setupPaths(t)
	// 40 mm of column axis taught 0.05 mm short, well inside the 0.1 mm
	// POS_TOLERANCE of pnptaskSection.
	cfg := mustLoad(t, trajSection+pnptaskSection+`
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 1
COLS = 5
FIRST_X = 100.0
FIRST_Y = 200.0
LAST_X = 139.95
LAST_Y = 200.0
COL_STEP = 10.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`)
	d := cfg.TrayDefs[0]
	if math.Abs(d.Angle) > 1e-12 {
		t.Errorf("derived angle = %g rad, want 0 for a tray taught straight along X", d.Angle)
	}
	nearPoint(t, "slot(4,0)", d.SlotPos(4, 0), pnproute.Point{X: 140, Y: 200})
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
POS_TOLERANCE = 0.1
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
	// Override, not concatenation: the namespaced DEADZONE_FILE lines replace
	// the global ones. GetAll would append both, and this list is indexed by
	// the deadzone-select pin — a concatenated foreign entry would be
	// selectable under a value the instance never configured.
	if len(cfg.DeadzoneFiles) != 1 || filepath.Base(cfg.DeadzoneFiles[0]) != zonesB {
		t.Errorf("deadzone files = %v, want exactly the namespaced [%s]", cfg.DeadzoneFiles, zonesB)
	}

	// The same INI read without the namespace is the other instance's view.
	other, err := loadTestConfig(t, text, "other.task")
	if err != nil {
		t.Fatalf("LoadConfig (other namespace): %v", err)
	}
	if other.MoveHeight != 30 || other.Procs[0].ID != 20 {
		t.Errorf("unnamespaced view = %g / %+v, want the global 30 / station 20", other.MoveHeight, other.Procs)
	}
	if len(other.DeadzoneFiles) != 1 || filepath.Base(other.DeadzoneFiles[0]) != zonesA {
		t.Errorf("other view deadzone files = %v, want exactly the global [%s]", other.DeadzoneFiles, zonesA)
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
		// A defaulted joint count that happened to be too small would leave a
		// joint unconfigured and never homed.
		name: "missing [KINS]JOINTS",
		ini:  "[TRAJ]\nCOORDINATES = XYZ\n" + pnptaskSection + stationSections,
		want: "[KINS]JOINTS = 0",
	}, {
		name: "more joints than motion has",
		ini:  "[KINS]\nJOINTS = 99\n[TRAJ]\nCOORDINATES = XYZ\n" + pnptaskSection + stationSections,
		want: "[KINS]JOINTS = 99",
	}, {
		name: "clearance not above blend tolerance",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 2.0\nBLEND_TOLERANCE = 2.0\nPOS_TOLERANCE = 0.1\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "must be greater than BLEND_TOLERANCE",
	}, {
		// The tolerance every taught geometry is judged against is not this
		// module's to guess at.
		name: "missing POS_TOLERANCE",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nDEADZONE_FILE = zones_a.dxf\n" +
			stationSections,
		want: "POS_TOLERANCE is required",
	}, {
		// Zero would demand a taught corner reproduce bit-exactly, which makes
		// every real grid a config error.
		name: "zero POS_TOLERANCE",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nPOS_TOLERANCE = 0\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "POS_TOLERANCE = 0: must be positive",
	}, {
		name: "no dead-zone file",
		ini:  trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nPOS_TOLERANCE = 0.1\n" + stationSections,
		want: "at least one dead-zone drawing",
	}, {
		name: "dead-zone file not found",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nPOS_TOLERANCE = 0.1\nDEADZONE_FILE = nope.dxf\n" +
			stationSections,
		want: "nope.dxf",
	}, {
		name: "malformed number",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = high\nCLEARANCE = 10.0\nDEADZONE_FILE = zones_a.dxf\n" +
			stationSections,
		want: "not a finite number",
	}, {
		name: "non-finite number",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = NaN\nCLEARANCE = 10.0\nDEADZONE_FILE = zones_a.dxf\n" +
			stationSections,
		want: "not a finite number",
	}, {
		name: "unknown LINEAR_UNITS",
		ini:  "[TRAJ]\nCOORDINATES = XYZ\nLINEAR_UNITS = inches\n" + pnptaskSection + stationSections,
		want: "LINEAR_UNITS",
	}, {
		// NaN passes both "v <= 0" and the Inf check; as the unit scale it
		// would poison every converted length and defeat every guard.
		name: "NaN LINEAR_UNITS",
		ini:  "[TRAJ]\nCOORDINATES = XYZ\nLINEAR_UNITS = nan\n" + pnptaskSection + stationSections,
		want: "LINEAR_UNITS",
	}, {
		name: "negative blend tolerance",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 2.0\nBLEND_TOLERANCE = -8\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "BLEND_TOLERANCE = -8: must not be negative",
	}, {
		name: "negative velocity",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nPOS_TOLERANCE = 0.1\nMOVE_VEL = -500\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "MOVE_VEL = -500: must not be negative",
	}, {
		name: "negative dwell",
		ini: trajSection + "[PNPTASK]\nMOVE_HEIGHT = 30.0\nCLEARANCE = 10.0\nPOS_TOLERANCE = 0.1\nRELEASE_TIME = -1\n" +
			"DEADZONE_FILE = zones_a.dxf\n" + stationSections,
		want: "must not be negative",
	}, {
		name: "no stations",
		ini:  trajSection + pnptaskSection,
		want: "no stations configured",
	}, {
		// A seed that names nothing would sit on the pin as an id no geometry
		// matches, and only the first job against the station would say so.
		name: "unknown DEFAULT_TRAYDEF",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
DEFAULT_TRAYDEF = 7
`,
		want: "DEFAULT_TRAYDEF = 7: no TRAYDEF has that ID",
	}, {
		name: "DEFAULT_TRAYDEF zero",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
DEFAULT_TRAYDEF = 0
`,
		want: "id 0 is reserved",
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
		name: "empty section name",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_]
ID = 10
Z_PICK = 2.5
`,
		want: "section name is empty",
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
		name: "capacity on a grid",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 2
COLS = 2
CAPACITY = 8
FIRST_X = 1.0
FIRST_Y = 2.0
LAST_X = 3.0
LAST_Y = 4.0
COL_STEP = 2.0
ROW_STEP = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "CAPACITY applies to an endless tray",
	}, {
		name: "capacity of zero",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 0
COLS = 0
CAPACITY = 0
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "must be at least 1",
	}, {
		name: "negative default step",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 0
COLS = 0
FIRST_X = 1.0
FIRST_Y = 2.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
DEFAULT_STEP = -1
`,
		want: "DEFAULT_STEP",
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
		// A section name is never parsed, but a header that came out wrong is
		// still a header the config did not mean to write.
		name: "malformed section name",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAY_MATERIAL IN]
ID = 10
Z_PICK = 2.5
`,
		want: `section name "MATERIAL IN" may only contain`,
	}, {
		// An axis with slots to step along has a pitch, and it is the pitch the
		// grid is built from — there is nothing to fall back on.
		name: "grid without ROW_STEP",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 280.0
LAST_Y = 430.0
COL_STEP = 20.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "ROW_STEP is required for a 4-slot axis",
	}, {
		name: "step width below the minimum pitch",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 280.0
LAST_Y = 430.0
COL_STEP = 0.05
ROW_STEP = 10.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "COL_STEP = 0.05 mm: a step width must be at least 0.1 mm",
	}, {
		// Which way the tray runs on the table is the derived tilt's business;
		// a negative step would mirror an axis behind the fit's back.
		name: "negative step width",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 280.0
LAST_Y = 430.0
COL_STEP = -20.0
ROW_STEP = 10.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "a step width must be at least",
	}, {
		// The whole point of the fit: no rotation about FIRST turns a
		// 180 x 30 mm grid into a corner taught 100 x 30 mm away, so the two
		// descriptions of this tray disagree and the load says so (D24).
		name: "grid that cannot reach the taught LAST",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 4
COLS = 10
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 200.0
LAST_Y = 430.0
COL_STEP = 20.0
ROW_STEP = 10.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "more than POS_TOLERANCE",
	}, {
		// One slot, so FIRST and LAST are the same position; a LAST anywhere
		// else means the section was meant to describe a grid.
		name: "single-slot tray with a LAST elsewhere",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 1
COLS = 1
FIRST_X = 100.0
FIRST_Y = 400.0
LAST_X = 200.0
LAST_Y = 400.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "single slot, but LAST sits 100 mm from FIRST",
	}, {
		name: "endless tray with step widths",
		ini: trajSection + pnptaskSection + `
[PNPTASK_TRAYDEF_0]
ID = 1
ROWS = 0
COLS = 0
FIRST_X = 100.0
FIRST_Y = 400.0
COL_STEP = 20.0

[PNPTASK_TRAY_0]
ID = 10
Z_PICK = 2.5
`,
		want: "must not define COL_STEP/ROW_STEP",
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
