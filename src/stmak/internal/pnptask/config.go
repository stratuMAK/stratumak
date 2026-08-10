// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"math"
	"strconv"
	"strings"

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

	AutoHome       bool
	MoveHeight     float64
	Clearance      float64
	BlendTolerance float64

	// MoveVel/MoveAcc are the XY travel limits, ZVel/ZAcc the ones for the
	// approach/retract strokes. Zero means "no override" — the [TRAJ] defaults
	// apply, exactly as the design document specifies for MOVE_VEL/MOVE_ACC.
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

	TrayDefs []TrayDef
	Trays    []TrayStation
	Procs    []ProcStation
	Routes   []RouteOverride
}

// TrayDef is one [PNPTASK_TRAYDEF_n] section: the *geometry* of a tray, picked
// at runtime by a tray station's tray-id pin (D17). Tray geometry is expressed
// in absolute machine coordinates, so a tray station carries no X/Y of its own.
type TrayDef struct {
	Index int    // n in [PNPTASK_TRAYDEF_n], for diagnostics
	ID    uint32 // value a tray-id pin must carry to select this definition

	// Rows/Cols is the grid size. Both zero means an endless tray: a single
	// position at First whose fill state is only ever known by probing.
	Rows int
	Cols int

	// First is slot (0,0) and Last slot (Cols-1, Rows-1), both absolute
	// machine coordinates. HasLast is false for a single-position tray (a
	// reject bin or a transfer place), where every pick and place happens at
	// First.
	First   pnproute.Point
	Last    pnproute.Point
	HasLast bool

	// Angle tilts the grid axes (radians, CCW, from INI degrees). The column
	// and row directions are rotated by it and the two pitches are derived by
	// expressing Last-First in that rotated frame, so slot (Cols-1, Rows-1)
	// lands exactly on Last whatever the angle is — both taught corners stay
	// honest and Angle only says how the tray sits on the table. At Angle = 0
	// this degenerates to the plain axis-aligned grid.
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

// TrayStation is one [PNPTASK_TRAY_n] section: a station that holds a tray.
// Its geometry comes from whichever TrayDef its tray-id pin selects, so all it
// contributes itself is the pick height and its HAL pins.
type TrayStation struct {
	Index int
	ID    uint32
	ZPick float64
}

// ProcStation is one [PNPTASK_PROC_n] section: a fixed process station.
type ProcStation struct {
	Index int
	ID    uint32
	Pos   pnproute.Point
	ZPick float64

	// Wait is where a job waits out a busy station (D15). Without one the job
	// waits where it stands, at movement height.
	Wait    pnproute.Point
	HasWait bool
}

// RouteOverride is one [PNPTASK_ROUTE_n] section: a movement height that
// applies to travel between one specific station pair instead of the global
// MOVE_HEIGHT.
type RouteOverride struct {
	Index      int
	Origin     uint32
	Dest       uint32
	MoveHeight float64
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
	r := &iniReader{ini: ini, units: parseLinearUnits(ini.Get("TRAJ", "LINEAR_UNITS"))}
	cfg := &Config{LinearUnits: r.units}

	axes, err := parseCoordinates(ini.Get("TRAJ", "COORDINATES"))
	if err != nil {
		return nil, err
	}
	cfg.Axes = axes

	const sec = "PNPTASK"
	cfg.AutoHome = r.boolean(sec, "AUTOHOME", false)
	cfg.MoveHeight = r.lengthReq(sec, "MOVE_HEIGHT")
	cfg.Clearance = r.lengthReq(sec, "CLEARANCE")
	cfg.BlendTolerance = r.length(sec, "BLEND_TOLERANCE", 0)
	cfg.MoveVel = r.length(sec, "MOVE_VEL", 0)
	cfg.MoveAcc = r.length(sec, "MOVE_ACC", 0)
	cfg.ZVel = r.length(sec, "Z_VEL", 0)
	cfg.ZAcc = r.length(sec, "Z_ACC", 0)
	cfg.PosSettleTime = r.duration(sec, "POS_SETTLE_TIME", 0)
	cfg.PickSettleTime = r.duration(sec, "PICK_SETTLE_TIME", 0)
	cfg.ReleaseTime = r.duration(sec, "RELEASE_TIME", 0)
	cfg.ReleaseTimeout = r.duration(sec, "RELEASE_TIMEOUT", defaultReleaseTimeout)
	cfg.HomeTimeout = r.duration(sec, "HOME_TIMEOUT", defaultHomeTimeout)
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
	files := ini.GetAll("PNPTASK", "DEADZONE_FILE")
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

// loadTrayDefs parses every [PNPTASK_TRAYDEF_n] section.
func loadTrayDefs(r *iniReader, cfg *Config) error {
	idxs, err := sectionIndices(r.ini, "PNPTASK_TRAYDEF")
	if err != nil {
		return err
	}
	seen := make(map[uint32]int, len(idxs))
	for _, n := range idxs {
		sec := fmt.Sprintf("PNPTASK_TRAYDEF_%d", n)
		d := TrayDef{Index: n}
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
		d.Angle = r.angle(sec, "ANGLE", 0)
		d.MaxUnpopulated = r.integer(sec, "MAX_UNPOPULATED", defaultMaxUnpopulated)
		d.Dir, err = parseDirMode(r.str(sec, "DIR_MODE"))
		if err != nil {
			return fmt.Errorf("[%s]DIR_MODE: %w", sec, err)
		}
		if err := r.err; err != nil {
			return err
		}

		if prev, dup := seen[d.ID]; dup {
			return fmt.Errorf("[%s]ID = %d: already used by [PNPTASK_TRAYDEF_%d]", sec, d.ID, prev)
		}
		seen[d.ID] = n

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
		if d.MaxUnpopulated < 1 {
			return fmt.Errorf("[%s]MAX_UNPOPULATED = %d: must be at least 1", sec, d.MaxUnpopulated)
		}
		if err := checkGridPitch(sec, d); err != nil {
			return err
		}
		cfg.TrayDefs = append(cfg.TrayDefs, d)
	}
	return nil
}

// checkGridPitch rejects a grid whose slots would collapse onto each other.
// Last-First is resolved in the frame Angle rotates into, so an angle that puts
// the whole span onto one of the two grid axes leaves the other with zero pitch
// — every slot of that index would sit on top of its neighbour, which is a
// mis-taught tray and not something to discover by driving to it.
func checkGridPitch(sec string, d TrayDef) error {
	if !d.HasLast || d.Endless() {
		return nil
	}
	dx, dy := gridSpan(d)
	if d.Cols > 1 && math.Abs(dx) < gridPitchEpsilon {
		return fmt.Errorf("[%s]: COLS = %d but the column pitch is zero (FIRST/LAST and ANGLE = %g° describe a grid with no column spacing)",
			sec, d.Cols, d.Angle*180/math.Pi)
	}
	if d.Rows > 1 && math.Abs(dy) < gridPitchEpsilon {
		return fmt.Errorf("[%s]: ROWS = %d but the row pitch is zero (FIRST/LAST and ANGLE = %g° describe a grid with no row spacing)",
			sec, d.Rows, d.Angle*180/math.Pi)
	}
	return nil
}

// gridPitchEpsilon is the span below which a grid axis counts as collapsed, in
// mm. A tray whose corner slots are a micron apart is a typo either way.
const gridPitchEpsilon = 1e-6

// loadStations parses [PNPTASK_TRAY_n] and [PNPTASK_PROC_n]. Station ids share
// one namespace across both kinds: origin-id and dest-id name a station without
// saying which kind it is, so a duplicate would make the action selection of
// §7.4 ambiguous.
func loadStations(r *iniReader, cfg *Config) error {
	seen := make(map[uint32]string)

	trayIdxs, err := sectionIndices(r.ini, "PNPTASK_TRAY")
	if err != nil {
		return err
	}
	for _, n := range trayIdxs {
		sec := fmt.Sprintf("PNPTASK_TRAY_%d", n)
		s := TrayStation{Index: n}
		s.ID = r.stationID(sec, "ID")
		s.ZPick = r.lengthReq(sec, "Z_PICK")
		if err := r.err; err != nil {
			return err
		}
		if prev, dup := seen[s.ID]; dup {
			return fmt.Errorf("[%s]ID = %d: already used by [%s]", sec, s.ID, prev)
		}
		seen[s.ID] = sec
		cfg.Trays = append(cfg.Trays, s)
	}

	procIdxs, err := sectionIndices(r.ini, "PNPTASK_PROC")
	if err != nil {
		return err
	}
	for _, n := range procIdxs {
		sec := fmt.Sprintf("PNPTASK_PROC_%d", n)
		s := ProcStation{Index: n}
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
		return fmt.Errorf("no stations configured: at least one [PNPTASK_TRAY_n] or [PNPTASK_PROC_n] section is required")
	}
	return nil
}

// loadRoutes parses [PNPTASK_ROUTE_n], the per-pair movement-height overrides.
func loadRoutes(r *iniReader, cfg *Config) error {
	idxs, err := sectionIndices(r.ini, "PNPTASK_ROUTE")
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
	seen := make(map[pair]int, len(idxs))
	for _, n := range idxs {
		sec := fmt.Sprintf("PNPTASK_ROUTE_%d", n)
		o := RouteOverride{Index: n}
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
			return fmt.Errorf("[%s]: route %d -> %d already overridden by [PNPTASK_ROUTE_%d]",
				sec, o.Origin, o.Dest, prev)
		}
		seen[p] = n
		cfg.Routes = append(cfg.Routes, o)
	}
	return nil
}

// sectionIndices returns the n of every [<prefix>_n] section present, in
// ascending order. The indices must form a gap-free run from 0: a config that
// numbers its stations 0, 1, 3 has almost certainly lost section 2 to a typo,
// and silently running with one station fewer than the file describes is the
// kind of quiet loss this loader exists to prevent.
//
// A prefix is matched against the *base* section name, so a namespaced
// [pnp.task:PNPTASK_TRAY_0] counts as tray 0 exactly like the global section
// it overlays.
func sectionIndices(ini *inifile.IniFile, prefix string) ([]int, error) {
	nsPrefix := ""
	if ns := ini.Namespace(); ns != "" {
		nsPrefix = ns + ":"
	}
	found := make(map[int]bool)
	for _, s := range ini.Sections {
		name := s.Name
		if nsPrefix != "" {
			name = strings.TrimPrefix(name, nsPrefix)
		}
		rest, ok := strings.CutPrefix(name, prefix+"_")
		if !ok || rest == "" {
			continue
		}
		// The prefixes are distinct enough that nothing else lands here —
		// "PNPTASK_TRAYDEF_0" does not start with "PNPTASK_TRAY_" — so a
		// suffix that is not a number is a typo, and skipping it quietly would
		// drop the whole section.
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("[%s]: section index %q is not a number", s.Name, rest)
		}
		found[n] = true
	}
	idxs := make([]int, 0, len(found))
	for n := 0; n < len(found); n++ {
		if !found[n] {
			return nil, fmt.Errorf("[%s_*]: sections must be numbered from 0 without gaps ([%s_%d] is missing)",
				prefix, prefix, n)
		}
		idxs = append(idxs, n)
	}
	return idxs, nil
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
	if err != nil {
		r.fail("[%s]%s = %q: not a number", section, key, s)
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

// angle reads an angle in degrees and returns radians.
func (r *iniReader) angle(section, key string, def float64) float64 {
	return r.float(section, key, def) * math.Pi / 180
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

// parseLinearUnits mirrors milltask's reading of [TRAJ]LINEAR_UNITS: the result
// is machine units per mm.
func parseLinearUnits(s string) float64 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 1.0 // mm default
	case "mm", "metric":
		return 1.0
	case "in", "inch", "imperial":
		return 1.0 / 25.4
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && v > 0 {
		return v
	}
	return 1.0
}
