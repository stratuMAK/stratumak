// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// Defaults for the optional [PNPTASK] keys. The two timeouts get the values the
// design document uses in its example: a timeout that defaults to "wait
// forever" turns a stuck fixture into a hung job with nothing on the error pin,
// which is the one failure mode a timeout exists to prevent. AUTOHOME defaults
// off so a machine only ever homes itself when the config says so.
const (
	defaultHomeTimeout     = 30.0
	defaultReleaseTimeout  = 5.0
	defaultMaxUnpopulated  = 1
	defaultTrayRowsAndCols = 1
)

// Config is the parsed and validated [PNPTASK*] configuration of one instance.
//
// Every length in here is millimetres and every velocity mm/s, converted from
// the machine units the INI is written in ([TRAJ]LINEAR_UNITS) at load time —
// the motion controller works in mm, so a config value that reaches it
// unconverted is 25.4x wrong on an inch machine. Times are seconds and angles
// radians; neither has a machine-unit dimension.
type Config struct {
	// LinearUnits is machine units per mm (1.0 for a metric machine, 1/25.4
	// for an inch one). Kept because the dead-zone DXF files are drawn in
	// machine units too and have to be scaled the same way when the planners
	// are built.
	LinearUnits float64

	// Axes are the [TRAJ]COORDINATES axes, in canonical XYZABCUVW order. They
	// determine which jog pins exist.
	Axes []Axis

	// NumJoints is [KINS]JOINTS: how many [JOINT_n] sections are pushed to
	// motmod and how many have to report homed before the machine may move.
	NumJoints int

	AutoHome       bool
	MoveHeight     float64
	Clearance      float64
	BlendTolerance float64

	// PosTolerance is how far a *computed* position may sit from the taught
	// one it has to reproduce before the config is refused ([PNPTASK]
	// POS_TOLERANCE, required). A tray grid is built from its step widths and
	// tilted onto its taught LAST (D24); this is how much of the leftover is
	// teaching noise rather than a wrong config.
	PosTolerance float64

	// MoveVel/MoveAcc are the XY travel limits, ZVel/ZAcc the ones for the
	// approach/retract strokes. All four are PER-AXIS ceilings, not path feeds:
	// the coordinated path value is what the blend makes of them, so a diagonal
	// travel runs faster than MOVE_VEL rather than slowing its axes down to it
	// (motion.go, moveLimits). Zero means "no ceiling beyond the axis limits".
	MoveVel float64
	MoveAcc float64
	ZVel    float64
	ZAcc    float64

	// Initial values of the RW HAL params (D2): the INI seeds them, halcmd
	// setp adjusts them at runtime.
	PosSettleTime  float64
	PickSettleTime float64
	ReleaseTime    float64

	// ReleaseTimeout is how long a proc station's released feedback may take;
	// 0 means wait forever. HomeTimeout bounds autohoming.
	ReleaseTimeout float64
	HomeTimeout    float64

	// DeadzoneFiles are the resolved absolute paths of the DXF files, in INI
	// line order — that order is what the deadzone-select pin indexes.
	DeadzoneFiles []string

	// Home is the homed XY position ([JOINT_0]HOME / [JOINT_1]HOME, mm; the
	// X/Y joints map 1:1 to the X/Y axes with trivkins, which is the
	// kinematics this module's stack uses). The first job after homing plans
	// its route from here, so a home position the drawings cannot start a
	// route from is warned about at load (see plannerSet.homeWarnings) —
	// otherwise every deployment rediscovers it at commissioning as a
	// PLANNING_FAILED that names coordinates instead of the cause.
	Home pnproute.Point

	TrayDefs []TrayDef
	Trays    []TrayStation
	Procs    []ProcStation
	Routes   []RouteOverride
}

// TrayDef is one [PNPTASK_TRAYDEF_x] section: the *geometry* of a tray, picked
// at runtime by a tray station's tray-id pin (D17). Tray geometry is expressed
// in absolute machine coordinates, so a tray station carries no X/Y of its own.
type TrayDef struct {
	Section string // section name as written, for diagnostics
	ID      uint32 // value a tray-id pin must carry to select this definition

	// Rows/Cols is the grid size. Both zero means an endless tray: a single
	// position at First whose fill state is only ever known by probing.
	Rows int
	Cols int

	// First is slot (0,0) and Last the *taught* position of slot
	// (Cols-1, Rows-1), both absolute machine coordinates. HasLast is false
	// for a single-position tray (a reject bin or a transfer place), where
	// every pick and place happens at First.
	First   pnproute.Point
	Last    pnproute.Point
	HasLast bool

	// ColStep/RowStep are the slot pitches along the tray's own column and row
	// axis (mm, from COL_STEP/ROW_STEP). The grid is built from these — Last
	// is not interpolated between, it is the measurement the tilt is fitted to.
	ColStep float64
	RowStep float64

	// Angle tilts the grid axes (radians, CCW). It is not configured but
	// derived at load (D24): the rotation about First that turns the corner
	// the step widths compute into the taught Last. A tray without Last has
	// nothing to bear on and stays at 0, the plain axis-aligned grid.
	Angle float64

	// MaxUnpopulated is how many successive empty picks declare the tray
	// empty.
	MaxUnpopulated int

	// Dir is the slot iteration order.
	Dir DirMode
}

// Endless reports whether this is an endless tray (ROWS = COLS = 0): one
// position, no slot bookkeeping, emptiness only ever established by probing.
func (d TrayDef) Endless() bool { return d.Rows == 0 && d.Cols == 0 }

// TrayStation is one [PNPTASK_TRAY_x] section: a station that holds a tray.
// Its geometry comes from whichever TrayDef its tray-id pin selects, so all it
// contributes itself is the pick height and its HAL pins.
type TrayStation struct {
	Section string
	ID      uint32
	ZPick   float64

	// DefaultTrayDef is the TrayDef id the station's tray-id pin is seeded
	// with, or 0 for none. A station that only ever holds one tray geometry
	// has nothing to select at runtime, and without a seed its selector would
	// sit at the unwired 0 — no geometry, every job refused — until someone
	// wires a constant to it. Seeding the *pin* rather than defaulting the
	// value keeps id 0 meaning "not told yet" (D17) for the stations that do
	// select at runtime: a PLC dropping its selector must still park the
	// station, not silently fall back to a default tray.
	DefaultTrayDef uint32
}

// ProcStation is one [PNPTASK_PROC_x] section: a fixed process station.
type ProcStation struct {
	Section string
	ID      uint32
	Pos     pnproute.Point
	ZPick   float64

	// Wait is where a job waits out a busy station (D15). Without one the job
	// waits where it stands, at movement height.
	Wait    pnproute.Point
	HasWait bool
}

// RouteOverride is one [PNPTASK_ROUTE_x] section: a movement height that
// applies to travel between one specific station pair instead of the global
// MOVE_HEIGHT.
type RouteOverride struct {
	Section    string
	Origin     uint32
	Dest       uint32
	MoveHeight float64
}

// AxisMask is the [TRAJ]COORDINATES axis bitmask motmod is configured with
// (X=1, Y=2, Z=4, …), derived from the already-validated axis list rather than
// re-parsed from the INI so the pins and the pushed configuration can never
// disagree about which axes exist.
func (c *Config) AxisMask() int32 {
	var mask int32
	for _, ax := range c.Axes {
		mask |= 1 << ax.Index
	}
	return mask
}

// Axis is one axis of [TRAJ]COORDINATES.
type Axis struct {
	Letter rune  // 'X', 'Y', …; lower-cased for the HAL pin name
	Index  int32 // motctl axis index (X=0 … W=8)
}

// axisIndex maps an axis letter to its motctl index, mirroring the canonical
// XYZABCUVW order the rest of the stack uses.
var axisIndex = map[rune]int32{
	'X': 0, 'Y': 1, 'Z': 2,
	'A': 3, 'B': 4, 'C': 5,
	'U': 6, 'V': 7, 'W': 8,
}

// axisOrder is the order jog pins are exported in, so a config listing
// "ZXY" still yields x, y, z pins in the familiar order.
var axisOrder = []rune{'X', 'Y', 'Z', 'A', 'B', 'C', 'U', 'V', 'W'}

// LoadConfig parses and validates the [PNPTASK*] sections of one instance's
// (namespaced) INI view.
//
// Everything it can check without leaving the INI is checked here and fails the
// load: the module has no way to report a bad station coordinate once jobs are
// running, and a pick-and-place head that limps on half a config is a head that
// drives into a fixture. The geometric half of the validation — every station
// and slot position inside the eroded boundary and outside every offset dead
// zone — needs the planners built from the DEADZONE_FILE drawings and lands
// with them (design §5.1); the files are resolved here so a typo in a path
// still fails at load rather than at the first job.
func LoadConfig(ini *inifile.IniFile) (*Config, error) {
	units, err := parseLinearUnits(ini.Get("TRAJ", "LINEAR_UNITS"))
	if err != nil {
		return nil, err
	}
	r := &iniReader{ini: ini, units: units}
	cfg := &Config{LinearUnits: r.units}

	axes, err := parseCoordinates(ini.Get("TRAJ", "COORDINATES"))
	if err != nil {
		return nil, err
	}
	cfg.Axes = axes

	// [KINS]JOINTS is required rather than defaulted: it decides which joints
	// are activated and which have to be homed, and a default that happens to
	// be too small would leave a joint unconfigured and never homed — a machine
	// that moves one axis into a hard stop rather than one that fails to load.
	cfg.NumJoints = r.integer("KINS", "JOINTS", 0)
	if err := r.err; err != nil {
		return nil, err
	}
	if cfg.NumJoints < 1 || cfg.NumJoints > motsetup.MaxJoints {
		return nil, fmt.Errorf("[KINS]JOINTS = %d: must be between 1 and %d",
			cfg.NumJoints, motsetup.MaxJoints)
	}

	const sec = "PNPTASK"
	cfg.AutoHome = r.boolean(sec, "AUTOHOME", false)
	cfg.MoveHeight = r.lengthReq(sec, "MOVE_HEIGHT")
	cfg.Clearance = r.lengthReq(sec, "CLEARANCE")
	cfg.BlendTolerance = r.lengthNonNeg(sec, "BLEND_TOLERANCE", 0)
	// Required rather than defaulted: it decides which taught geometry the
	// load accepts, and a default would be this module's guess at how well the
	// machine it is running on can be taught.
	cfg.PosTolerance = r.lengthReq(sec, "POS_TOLERANCE")
	cfg.MoveVel = r.lengthNonNeg(sec, "MOVE_VEL", 0)
	cfg.MoveAcc = r.lengthNonNeg(sec, "MOVE_ACC", 0)
	cfg.ZVel = r.lengthNonNeg(sec, "Z_VEL", 0)
	cfg.ZAcc = r.lengthNonNeg(sec, "Z_ACC", 0)
	cfg.PosSettleTime = r.duration(sec, "POS_SETTLE_TIME", 0)
	cfg.PickSettleTime = r.duration(sec, "PICK_SETTLE_TIME", 0)
	cfg.ReleaseTime = r.duration(sec, "RELEASE_TIME", 0)
	cfg.ReleaseTimeout = r.duration(sec, "RELEASE_TIMEOUT", defaultReleaseTimeout)
	cfg.HomeTimeout = r.duration(sec, "HOME_TIMEOUT", defaultHomeTimeout)
	// [JOINT_n]HOME is otherwise motsetup's business; the X/Y values are read
	// here too because the homed position is where the first job's route
	// starts (see Config.Home). Absent keys default to 0, exactly as motmod
	// homes them.
	cfg.Home.X = r.length("JOINT_0", "HOME", 0)
	cfg.Home.Y = r.length("JOINT_1", "HOME", 0)
	if err := r.err; err != nil {
		return nil, err
	}

	// CLEARANCE has to cover the blend tolerance on top of the true safety
	// margin: the trajectory planner blends corners *inward*, toward the dead
	// zone, so a route planned with clearance <= tolerance can be blended into
	// the zone it was planned around.
	if cfg.Clearance <= cfg.BlendTolerance {
		return nil, fmt.Errorf("[%s]CLEARANCE (%g) must be greater than BLEND_TOLERANCE (%g)",
			sec, cfg.Clearance, cfg.BlendTolerance)
	}
	// Zero would demand that a taught corner reproduce to the last bit of a
	// float, which no teaching does — every grid would be a config error.
	if cfg.PosTolerance <= 0 {
		return nil, fmt.Errorf("[%s]POS_TOLERANCE = %g: must be positive", sec, cfg.PosTolerance)
	}

	if err := loadDeadzoneFiles(ini, cfg); err != nil {
		return nil, err
	}
	if err := loadTrayDefs(r, cfg); err != nil {
		return nil, err
	}
	if err := loadStations(r, cfg); err != nil {
		return nil, err
	}
	if err := loadRoutes(r, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDeadzoneFiles resolves the repeated DEADZONE_FILE key. Line order is the
// selector value the deadzone-select pin carries, so the list is kept in file
// order and never sorted or deduplicated.
func loadDeadzoneFiles(ini *inifile.IniFile, cfg *Config) error {
	files := deadzoneFileValues(ini)
	if len(files) == 0 {
		// The outer limit lives in the DXF as well, so "no dead-zone file" is
		// not "no dead zones" — it is a machine with no known boundary, which
		// is not a machine this module can plan travel for.
		return fmt.Errorf("[PNPTASK]DEADZONE_FILE: at least one dead-zone drawing is required")
	}
	for i, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			return fmt.Errorf("[PNPTASK]DEADZONE_FILE #%d: empty path", i)
		}
		// Dead-zone drawings are configuration, resolved by the shared rule
		// (config dir, then HALLIB_PATH) like any other server-side config
		// file — see internal/pathres.
		path, err := pathres.Resolve(f, pathres.Read)
		if err != nil {
			return fmt.Errorf("[PNPTASK]DEADZONE_FILE #%d (%q): %w", i, f, err)
		}
		cfg.DeadzoneFiles = append(cfg.DeadzoneFiles, path)
	}
	return nil
}

// deadzoneFileValues collects the DEADZONE_FILE lines with override semantics:
// if the namespaced [<ns>:PNPTASK] section defines any, they *replace* the
// global [PNPTASK] ones. GetAll would concatenate the two views — but this
// list is what the deadzone-select pin indexes, so an instance overriding the
// drawings must not have a foreign instance's files appended under selector
// values it never asked for (module.go's namespace doc promises override
// semantics for the whole [PNPTASK*] configuration).
func deadzoneFileValues(ini *inifile.IniFile) []string {
	names := []string{"PNPTASK"}
	if ns := ini.Namespace(); ns != "" {
		names = []string{ns + ":PNPTASK", "PNPTASK"}
	}
	for _, name := range names {
		var vals []string
		for i := range ini.Sections {
			if ini.Sections[i].Name != name {
				continue
			}
			for _, e := range ini.Sections[i].Entries {
				if e.Key == "DEADZONE_FILE" {
					vals = append(vals, e.Value)
				}
			}
		}
		if len(vals) > 0 {
			return vals
		}
	}
	return nil
}

// loadTrayDefs parses every [PNPTASK_TRAYDEF_x] section.
func loadTrayDefs(r *iniReader, cfg *Config) error {
	secs, err := sectionNames(r.ini, "PNPTASK_TRAYDEF")
	if err != nil {
		return err
	}
	seen := make(map[uint32]string, len(secs))
	for _, sec := range secs {
		d := TrayDef{Section: sec}
		d.ID = r.stationID(sec, "ID")
		d.Rows = r.integer(sec, "ROWS", defaultTrayRowsAndCols)
		d.Cols = r.integer(sec, "COLS", defaultTrayRowsAndCols)
		d.First.X = r.lengthReq(sec, "FIRST_X")
		d.First.Y = r.lengthReq(sec, "FIRST_Y")
		hasLastX, hasLastY := r.has(sec, "LAST_X"), r.has(sec, "LAST_Y")
		if hasLastX != hasLastY {
			return fmt.Errorf("[%s]: LAST_X and LAST_Y must be given together", sec)
		}
		if hasLastX {
			d.HasLast = true
			d.Last.X = r.lengthReq(sec, "LAST_X")
			d.Last.Y = r.lengthReq(sec, "LAST_Y")
		}
		hasColStep, hasRowStep := r.has(sec, "COL_STEP"), r.has(sec, "ROW_STEP")
		d.ColStep = r.length(sec, "COL_STEP", 0)
		d.RowStep = r.length(sec, "ROW_STEP", 0)
		d.MaxUnpopulated = r.integer(sec, "MAX_UNPOPULATED", defaultMaxUnpopulated)
		d.Dir, err = parseDirMode(r.str(sec, "DIR_MODE"))
		if err != nil {
			return fmt.Errorf("[%s]DIR_MODE: %w", sec, err)
		}
		if err := r.err; err != nil {
			return err
		}

		if prev, dup := seen[d.ID]; dup {
			return fmt.Errorf("[%s]ID = %d: already used by [%s]", sec, d.ID, prev)
		}
		seen[d.ID] = sec

		if d.Rows < 0 || d.Cols < 0 {
			return fmt.Errorf("[%s]: ROWS (%d) and COLS (%d) must not be negative", sec, d.Rows, d.Cols)
		}
		// ROWS and COLS are a pair: 0/0 is the endless tray, anything else is
		// a grid. One of them alone at 0 would be a grid with no slots, which
		// is far more likely a typo than an intent.
		if (d.Rows == 0) != (d.Cols == 0) {
			return fmt.Errorf("[%s]: ROWS (%d) and COLS (%d) must both be 0 (endless tray) or both be positive",
				sec, d.Rows, d.Cols)
		}
		if (d.Rows > 1 || d.Cols > 1) && !d.HasLast {
			return fmt.Errorf("[%s]: a %dx%d grid needs LAST_X/LAST_Y (only a single-position tray may omit them)",
				sec, d.Cols, d.Rows)
		}
		// An endless tray is one position at FIRST. A LAST alongside it means
		// whoever wrote the section expected a grid, and reading it as "one
		// slot, extent ignored" would hide that.
		if d.Endless() && d.HasLast {
			return fmt.Errorf("[%s]: an endless tray (ROWS = COLS = 0) has a single position at FIRST and must not define LAST_X/LAST_Y", sec)
		}
		if d.Endless() && (hasColStep || hasRowStep) {
			return fmt.Errorf("[%s]: an endless tray (ROWS = COLS = 0) has a single position at FIRST and must not define COL_STEP/ROW_STEP", sec)
		}
		if d.MaxUnpopulated < 1 {
			return fmt.Errorf("[%s]MAX_UNPOPULATED = %d: must be at least 1", sec, d.MaxUnpopulated)
		}
		if err := checkStep(sec, "COL_STEP", d.ColStep, d.Cols, hasColStep); err != nil {
			return err
		}
		if err := checkStep(sec, "ROW_STEP", d.RowStep, d.Rows, hasRowStep); err != nil {
			return err
		}
		if err := fitGridAngle(sec, &d, cfg.PosTolerance); err != nil {
			return err
		}
		cfg.TrayDefs = append(cfg.TrayDefs, d)
	}
	return nil
}

// checkStep validates one axis' step width against the number of slots on it.
// An axis with more than one slot has a pitch and must state it; an axis with
// one slot has nothing to step and may leave it out.
func checkStep(sec, key string, step float64, count int, given bool) error {
	if count > 1 && !given {
		return fmt.Errorf("[%s]: %s is required for a %d-slot axis (the slot-to-slot distance along it)",
			sec, key, count)
	}
	// Positive only: a step is a distance, and which way the tray runs on the
	// table is what the derived tilt says (D24). A negative one would mirror
	// the axis behind the fit's back, and anything below the minimum pitch is
	// a typo rather than a tray.
	if given && step < gridMinPitch {
		return fmt.Errorf("[%s]%s = %g mm: a step width must be at least %g mm",
			sec, key, step, gridMinPitch)
	}
	return nil
}

// fitGridAngle derives how the tray sits on the table and checks that the grid
// the section describes is the tray that was taught (D24).
//
// The step widths build the grid in the tray's own frame, so its far corner —
// slot (COLS−1, ROWS−1) — sits at FIRST + (COL_STEP·(COLS−1), ROW_STEP·(ROWS−1))
// before any rotation. LAST is where that same corner was *measured*, so the
// angle between the two vectors, bearing on FIRST, is the tray's tilt, and it
// is the only thing LAST contributes.
//
// A rotation cannot change a vector's length, so whatever separation is left
// after it is the two descriptions disagreeing: a mistyped step width, a
// mis-taught LAST, or the wrong ROWS/COLS. That is a config error rather than
// something to discover by driving to the far end of the tray, and
// POS_TOLERANCE is how much of it is teaching noise.
func fitGridAngle(sec string, d *TrayDef, tol float64) error {
	if !d.HasLast || d.Endless() {
		return nil
	}
	nomX := d.ColStep * float64(d.Cols-1)
	nomY := d.RowStep * float64(d.Rows-1)
	measX, measY := d.Last.X-d.First.X, d.Last.Y-d.First.Y
	if nomX == 0 && nomY == 0 {
		// A 1x1 tray has no diagonal to bear a tilt on: FIRST and LAST are the
		// same slot, so LAST may only repeat it.
		if off := math.Hypot(measX, measY); off > tol {
			return fmt.Errorf("[%s]: a %dx%d tray has a single slot, but LAST sits %g mm from FIRST — more than POS_TOLERANCE (%g mm)",
				sec, d.Cols, d.Rows, off, tol)
		}
		return nil
	}
	d.Angle = normalizeAngle(math.Atan2(measY, measX) - math.Atan2(nomY, nomX))
	corner := d.SlotPos(d.Cols-1, d.Rows-1)
	if off := math.Hypot(corner.X-d.Last.X, corner.Y-d.Last.Y); off > tol {
		return fmt.Errorf("[%s]: COLS/ROWS and COL_STEP/ROW_STEP put slot (%d, %d) at (%.4f, %.4f) under the %.4f° tilt they imply, %g mm off the taught LAST (%.4f, %.4f) — more than POS_TOLERANCE (%g mm)",
			sec, d.Cols-1, d.Rows-1, corner.X, corner.Y, d.Angle*180/math.Pi,
			off, d.Last.X, d.Last.Y, tol)
	}
	return nil
}

// normalizeAngle folds an angle into (-pi, pi], so a tray tilted by a hair
// reads as -0.06° in a diagnostic rather than as 359.94°.
func normalizeAngle(a float64) float64 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a <= -math.Pi {
		a += 2 * math.Pi
	}
	return a
}

// gridMinPitch is the smallest plausible slot-to-slot distance, in mm. A step
// width below it is a typo, not a tray — even micro-component waffle trays
// pitch well above it. Raise it here if that assumption ever breaks.
const gridMinPitch = 0.1

// loadStations parses [PNPTASK_TRAY_x] and [PNPTASK_PROC_x]. Station ids share
// one namespace across both kinds: origin-id and dest-id name a station without
// saying which kind it is, so a duplicate would make the action selection of
// §7.4 ambiguous.
func loadStations(r *iniReader, cfg *Config) error {
	seen := make(map[uint32]string)

	// loadTrayDefs has already run, so a DEFAULT_TRAYDEF can be checked
	// against the definitions it names here rather than surfacing as an
	// INVALID_TRAY_ID at the first job that touches the station.
	knownDefs := make(map[uint32]bool, len(cfg.TrayDefs))
	for _, d := range cfg.TrayDefs {
		knownDefs[d.ID] = true
	}

	traySecs, err := sectionNames(r.ini, "PNPTASK_TRAY")
	if err != nil {
		return err
	}
	for _, sec := range traySecs {
		s := TrayStation{Section: sec}
		s.ID = r.stationID(sec, "ID")
		s.ZPick = r.lengthReq(sec, "Z_PICK")
		if r.has(sec, "DEFAULT_TRAYDEF") {
			// stationID's checks are the right ones: the key carries a
			// TRAYDEF id, and 0 is as reserved here as it is there.
			s.DefaultTrayDef = r.stationID(sec, "DEFAULT_TRAYDEF")
		}
		if err := r.err; err != nil {
			return err
		}
		if s.DefaultTrayDef != 0 && !knownDefs[s.DefaultTrayDef] {
			return fmt.Errorf("[%s]DEFAULT_TRAYDEF = %d: no TRAYDEF has that ID", sec, s.DefaultTrayDef)
		}
		if prev, dup := seen[s.ID]; dup {
			return fmt.Errorf("[%s]ID = %d: already used by [%s]", sec, s.ID, prev)
		}
		seen[s.ID] = sec
		cfg.Trays = append(cfg.Trays, s)
	}

	procSecs, err := sectionNames(r.ini, "PNPTASK_PROC")
	if err != nil {
		return err
	}
	for _, sec := range procSecs {
		s := ProcStation{Section: sec}
		s.ID = r.stationID(sec, "ID")
		s.Pos.X = r.lengthReq(sec, "X")
		s.Pos.Y = r.lengthReq(sec, "Y")
		s.ZPick = r.lengthReq(sec, "Z_PICK")
		hasWaitX, hasWaitY := r.has(sec, "WAIT_X"), r.has(sec, "WAIT_Y")
		if hasWaitX != hasWaitY {
			return fmt.Errorf("[%s]: WAIT_X and WAIT_Y must be given together", sec)
		}
		if hasWaitX {
			s.HasWait = true
			s.Wait.X = r.lengthReq(sec, "WAIT_X")
			s.Wait.Y = r.lengthReq(sec, "WAIT_Y")
		}
		if err := r.err; err != nil {
			return err
		}
		if prev, dup := seen[s.ID]; dup {
			return fmt.Errorf("[%s]ID = %d: already used by [%s]", sec, s.ID, prev)
		}
		seen[s.ID] = sec
		cfg.Procs = append(cfg.Procs, s)
	}

	if len(cfg.Trays) == 0 && len(cfg.Procs) == 0 {
		return fmt.Errorf("no stations configured: at least one [PNPTASK_TRAY_x] or [PNPTASK_PROC_x] section is required")
	}
	return nil
}

// loadRoutes parses [PNPTASK_ROUTE_x], the per-pair movement-height overrides.
func loadRoutes(r *iniReader, cfg *Config) error {
	secs, err := sectionNames(r.ini, "PNPTASK_ROUTE")
	if err != nil {
		return err
	}
	known := make(map[uint32]bool, len(cfg.Trays)+len(cfg.Procs))
	for _, s := range cfg.Trays {
		known[s.ID] = true
	}
	for _, s := range cfg.Procs {
		known[s.ID] = true
	}
	type pair struct{ origin, dest uint32 }
	seen := make(map[pair]string, len(secs))
	for _, sec := range secs {
		o := RouteOverride{Section: sec}
		o.Origin = r.stationID(sec, "ORIGIN")
		o.Dest = r.stationID(sec, "DEST")
		o.MoveHeight = r.lengthReq(sec, "MOVE_HEIGHT")
		if err := r.err; err != nil {
			return err
		}
		if !known[o.Origin] {
			return fmt.Errorf("[%s]ORIGIN = %d: no such station", sec, o.Origin)
		}
		if !known[o.Dest] {
			return fmt.Errorf("[%s]DEST = %d: no such station", sec, o.Dest)
		}
		p := pair{o.Origin, o.Dest}
		if prev, dup := seen[p]; dup {
			return fmt.Errorf("[%s]: route %d -> %d already overridden by [%s]",
				sec, o.Origin, o.Dest, prev)
		}
		seen[p] = sec
		cfg.Routes = append(cfg.Routes, o)
	}
	return nil
}

// sectionNames returns the base name of every [<prefix>_<name>] section
// present, in the order the file lists them.
//
// The <name> is free-form and carries no meaning: nothing downstream keys off
// it. A station is identified by its ID everywhere it matters — origin-id and
// dest-id name one, the route overrides reference one, the HAL pins are named
// after one and the persisted slot state is filed under one — and a tray
// definition is selected by the value on a tray-id pin, again its ID. So the
// name is purely how the section reads, and a config is free to write
// [PNPTASK_TRAY_MATERIAL] instead of [PNPTASK_TRAY_0] and describe the machine
// in the machine's own words. Numeric names still work; they are just names,
// no longer indices, so they need not start at 0 or run without gaps.
//
// A prefix is matched against the *base* section name, so a namespaced
// [pnp.task:PNPTASK_TRAY_0] is the same tray as the global section it
// overlays and is returned once, under the base name every value lookup uses.
func sectionNames(ini *inifile.IniFile, prefix string) ([]string, error) {
	nsPrefix := ""
	if ns := ini.Namespace(); ns != "" {
		nsPrefix = ns + ":"
	}
	var names []string
	seen := make(map[string]bool)
	for _, s := range ini.Sections {
		name := s.Name
		if nsPrefix != "" {
			name = strings.TrimPrefix(name, nsPrefix)
		}
		suffix, ok := strings.CutPrefix(name, prefix+"_")
		if !ok {
			continue
		}
		// The prefixes stay distinct now that names are free-form —
		// "PNPTASK_TRAYDEF_x" does not start with "PNPTASK_TRAY_" — so
		// anything landing here was meant to be a section of this kind, and
		// skipping a malformed one quietly would drop the whole section.
		if err := checkSectionName(s.Name, suffix); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// checkSectionName rejects a [<prefix>_<name>] whose name is not a plain
// identifier. The name is never parsed, only echoed in diagnostics, so the
// check is about catching a mistyped section header rather than about the
// value: an empty name is a dangling underscore, and a space or a bracket in
// there is a section header that did not come out the way it was meant to.
func checkSectionName(section, name string) error {
	if name == "" {
		return fmt.Errorf("[%s]: section name is empty", section)
	}
	for _, c := range name {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return fmt.Errorf("[%s]: section name %q may only contain letters, digits, '_' and '-'", section, name)
		}
	}
	return nil
}

// parseCoordinates turns [TRAJ]COORDINATES into the axis list the jog pins are
// built from. Duplicates (a gantry's "XYYZ") collapse to one axis: the letters
// name axes, and two pins for one axis would be two jog requests for the same
// motion.
func parseCoordinates(coord string) ([]Axis, error) {
	if strings.TrimSpace(coord) == "" {
		return nil, fmt.Errorf("[TRAJ]COORDINATES is required")
	}
	present := make(map[rune]bool)
	for _, c := range strings.ToUpper(coord) {
		if c == ' ' || c == '\t' || c == ',' {
			continue
		}
		if _, ok := axisIndex[c]; !ok {
			return nil, fmt.Errorf("[TRAJ]COORDINATES: unknown axis letter %q", string(c))
		}
		present[c] = true
	}
	// XY travel and Z strokes are what this module does; a config without one
	// of them cannot run a single action.
	for _, c := range []rune{'X', 'Y', 'Z'} {
		if !present[c] {
			return nil, fmt.Errorf("[TRAJ]COORDINATES: pnptask needs the X, Y and Z axes (%q is missing)", string(c))
		}
	}
	axes := make([]Axis, 0, len(present))
	for _, c := range axisOrder {
		if present[c] {
			axes = append(axes, Axis{Letter: c, Index: axisIndex[c]})
		}
	}
	return axes, nil
}

// iniReader reads typed values from an INI, strictly: a malformed number fails
// the load instead of falling back to a default. Errors accumulate (first one
// wins) so a block of reads can be written without an error check per line; the
// caller checks r.err before using any of the values.
type iniReader struct {
	ini   *inifile.IniFile
	units float64 // machine units per mm, for the length conversions
	err   error
}

func (r *iniReader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

// has reports whether a key is present and non-empty.
func (r *iniReader) has(section, key string) bool {
	return strings.TrimSpace(r.ini.Get(section, key)) != ""
}

func (r *iniReader) str(section, key string) string {
	return strings.TrimSpace(r.ini.Get(section, key))
}

// float reads a plain number (no unit conversion).
func (r *iniReader) float(section, key string, def float64) float64 {
	s := r.str(section, key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		// ParseFloat happily accepts "NaN" and "Inf", but no station
		// coordinate, height or limit means anything non-finite — and NaN
		// additionally slides through every comparison-based guard below.
		r.fail("[%s]%s = %q: not a finite number", section, key, s)
		return def
	}
	return v
}

func (r *iniReader) floatReq(section, key string) float64 {
	if !r.has(section, key) {
		r.fail("[%s]%s is required", section, key)
		return 0
	}
	return r.float(section, key, 0)
}

// length reads a length or a linear velocity/acceleration and converts it from
// machine units to the internal mm. def is already in mm.
func (r *iniReader) length(section, key string, def float64) float64 {
	if !r.has(section, key) {
		return def
	}
	return r.machineToMM(r.float(section, key, 0))
}

func (r *iniReader) lengthReq(section, key string) float64 {
	return r.machineToMM(r.floatReq(section, key))
}

// lengthNonNeg reads a length-typed value that has no meaningful negative — a
// tolerance, velocity or acceleration. A sign typo would otherwise slip
// through comparisons written for positive values (a negative BLEND_TOLERANCE
// vacuously passes the CLEARANCE guard) and reach the motion stack. The value
// is checked in machine units, before conversion, so the message shows what
// the INI says.
func (r *iniReader) lengthNonNeg(section, key string, def float64) float64 {
	if !r.has(section, key) {
		return def
	}
	raw := r.float(section, key, 0)
	if raw < 0 {
		r.fail("[%s]%s = %g: must not be negative", section, key, raw)
		return def
	}
	return r.machineToMM(raw)
}

// duration reads a time in seconds. Negative is rejected: every use is a dwell
// or a timeout, and a negative one silently means "none".
func (r *iniReader) duration(section, key string, def float64) float64 {
	v := r.float(section, key, def)
	if v < 0 {
		r.fail("[%s]%s = %g: a time must not be negative", section, key, v)
		return def
	}
	return v
}

func (r *iniReader) integer(section, key string, def int) int {
	s := r.str(section, key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		r.fail("[%s]%s = %q: not an integer", section, key, s)
		return def
	}
	return v
}

// stationID reads a station or tray id. Zero is refused: an unwired u32 pin
// reads 0, so a station numbered 0 would be selected by a config that wired
// nothing at all.
func (r *iniReader) stationID(section, key string) uint32 {
	if !r.has(section, key) {
		r.fail("[%s]%s is required", section, key)
		return 0
	}
	s := r.str(section, key)
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		r.fail("[%s]%s = %q: not an unsigned integer", section, key, s)
		return 0
	}
	if v == 0 {
		r.fail("[%s]%s = 0: id 0 is reserved (an unconnected id pin reads 0)", section, key)
		return 0
	}
	return uint32(v)
}

func (r *iniReader) boolean(section, key string, def bool) bool {
	s := r.str(section, key)
	if s == "" {
		return def
	}
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	r.fail("[%s]%s = %q: not a boolean", section, key, s)
	return def
}

// machineToMM converts a length from the machine's configured linear units to
// the internal millimetres. units is machine-units-per-mm, so mm = v / units —
// the same conversion milltask applies to its INI lengths.
func (r *iniReader) machineToMM(v float64) float64 {
	if r.units <= 0 {
		return v
	}
	return v / r.units
}

// parseLinearUnits reads [TRAJ]LINEAR_UNITS; the result is machine units per
// mm. It accepts what milltask accepts, but an unrecognized value is an error
// rather than milltask's silent mm fallback: a typo like "inches" quietly
// meaning mm scales *everything* — stations, heights, velocities and the DXF
// scenes — self-consistently 25.4x wrong, which no later validation can see.
func parseLinearUnits(s string) (float64, error) {
	trimmed := strings.TrimSpace(s)
	switch strings.ToLower(trimmed) {
	case "":
		return 1.0, nil // mm default
	case "mm", "metric":
		return 1.0, nil
	case "in", "inch", "imperial":
		return 1.0 / 25.4, nil
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	// NaN needs its own check: it fails BOTH comparisons below ("v <= 0" is
	// false for NaN), and as the unit scale it would poison every converted
	// length while sliding through every comparison-based guard downstream.
	if err != nil || v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("[TRAJ]LINEAR_UNITS = %q: expected mm, in or a positive units-per-mm number", trimmed)
	}
	return v, nil
}
