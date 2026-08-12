// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
)

// SlotCount returns how many slots the tray has. An endless tray and a
// single-position tray both have exactly one.
func (d TrayDef) SlotCount() int {
	if d.Endless() {
		return 1
	}
	return d.Rows * d.Cols
}

// SlotPos returns the absolute machine coordinates of slot (col, row).
//
// The grid axes are tilted by Angle and the two pitches come from LAST−FIRST
// expressed in that rotated frame (D24), so slot (Cols−1, Rows−1) lands exactly
// on LAST at any angle. A tray without LAST — endless or single-position — has
// one position, at FIRST.
//
// Out-of-range indices are not rejected here: the callers (config validation
// and the slot search) iterate the tray's own extent, and clamping or erroring
// on an index would only hide a bug in them.
func (d TrayDef) SlotPos(col, row int) pnproute.Point {
	if !d.HasLast {
		return d.First
	}
	dx, dy := gridSpan(d)
	local := pnproute.Point{
		X: dx * gridFraction(col, d.Cols),
		Y: dy * gridFraction(row, d.Rows),
	}
	s, c := math.Sincos(d.Angle)
	return pnproute.Point{
		X: d.First.X + local.X*c - local.Y*s,
		Y: d.First.Y + local.X*s + local.Y*c,
	}
}

// gridFraction is how far along an axis index i sits, 0 at the first slot and 1
// at the last. A one-slot axis has no span to divide, so it stays at 0.
func gridFraction(i, n int) float64 {
	if n < 2 {
		return 0
	}
	return float64(i) / float64(n-1)
}

// gridSpan returns Last−First expressed in the tray's own (Angle-rotated)
// frame: the total travel along the column and the row axis of the grid.
func gridSpan(d TrayDef) (col, row float64) {
	dx := d.Last.X - d.First.X
	dy := d.Last.Y - d.First.Y
	s, c := math.Sincos(-d.Angle)
	return dx*c - dy*s, dx*s + dy*c
}

// SlotAxis names one of the two indices of a tray grid.
type SlotAxis int

const (
	// AxisCol is the column index, running FIRST -> LAST_X.
	AxisCol SlotAxis = iota
	// AxisRow is the row index, running FIRST -> LAST_Y.
	AxisRow
)

func (a SlotAxis) String() string {
	if a == AxisRow {
		return "R"
	}
	return "C"
}

// other returns the axis that is not a.
func (a SlotAxis) other() SlotAxis {
	if a == AxisRow {
		return AxisCol
	}
	return AxisRow
}

// DirMode is a parsed [PNPTASK_TRAYDEF_n]DIR_MODE: the order slots are visited
// in when a pick looks for material or a place looks for a free slot.
//
// The syntax is two axis tokens, each an axis letter (C or R) followed by a
// direction (+ or -), plus an optional trailing "~" for meander:
//
//	C+R+     columns left to right, then the next row upwards
//	R-C+     rows top to bottom within a column, then the next column
//	C+R+~    same as C+R+, but every second row runs backwards
//
// The first token is the fast-varying (inner) axis, the second the slow one.
// Both axes must appear exactly once.
type DirMode struct {
	// Primary is the inner axis — the one that advances from slot to slot.
	Primary SlotAxis
	// PrimaryUp and SecondaryUp are the directions the two axes run in.
	PrimaryUp   bool
	SecondaryUp bool
	// Meander reverses the primary direction on every second pass, so the
	// head does not travel back across the whole tray between passes.
	Meander bool
}

// Secondary is the outer axis, implied by Primary.
func (m DirMode) Secondary() SlotAxis { return m.Primary.other() }

// String renders the mode back in DIR_MODE syntax.
func (m DirMode) String() string {
	s := m.Primary.String() + sign(m.PrimaryUp) + m.Secondary().String() + sign(m.SecondaryUp)
	if m.Meander {
		s += "~"
	}
	return s
}

func sign(up bool) string {
	if up {
		return "+"
	}
	return "-"
}

// defaultDirMode is what an omitted DIR_MODE means: columns left to right, rows
// bottom to top, no meander — the reading order of a tray whose FIRST slot is
// the lower-left one.
var defaultDirMode = DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true}

// parseDirMode parses a DIR_MODE value. An empty string yields the default
// mode; anything that is not well-formed is an error rather than a fallback —
// a mis-typed mode would otherwise silently reorder a whole tray.
func parseDirMode(s string) (DirMode, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return defaultDirMode, nil
	}
	var m DirMode
	rest := s
	primary, primaryUp, rest, err := parseDirToken(rest)
	if err != nil {
		return m, err
	}
	secondary, secondaryUp, rest, err := parseDirToken(rest)
	if err != nil {
		return m, err
	}
	if primary == secondary {
		return m, fmt.Errorf("%q: axis %s given twice, expected one C and one R token", s, primary)
	}
	if rest == "~" {
		m.Meander = true
		rest = ""
	}
	if rest != "" {
		return m, fmt.Errorf("%q: trailing %q, expected two axis tokens and an optional %q", s, rest, "~")
	}
	m.Primary, m.PrimaryUp, m.SecondaryUp = primary, primaryUp, secondaryUp
	return m, nil
}

// parseDirToken consumes one "<axis><sign>" token from the front of s.
func parseDirToken(s string) (axis SlotAxis, up bool, rest string, err error) {
	if len(s) < 2 {
		return 0, false, s, fmt.Errorf("%q: expected an axis token (C+, C-, R+ or R-)", s)
	}
	switch s[0] {
	case 'C':
		axis = AxisCol
	case 'R':
		axis = AxisRow
	default:
		return 0, false, s, fmt.Errorf("%q: unknown axis %q, expected C or R", s, string(s[0]))
	}
	switch s[1] {
	case '+':
		up = true
	case '-':
		up = false
	default:
		return 0, false, s, fmt.Errorf("%q: unknown direction %q, expected + or -", s, string(s[1]))
	}
	return axis, up, s[2:], nil
}

// ---------------------------------------------------------------------------
// Slot iteration order
// ---------------------------------------------------------------------------

// slotOrder is the sequence of linear slot indices a tray is visited in, derived
// once from its DIR_MODE. A linear index is row*Cols + col, the layout the slot
// state slice uses.
//
// The order is precomputed rather than iterated live because it is the same for
// every pick and every place on that geometry, and because having it as a plain
// slice makes "continue the search after the slot that turned out to be empty"
// (§7.1's probing) a position in a list instead of a nested-loop resumption.
//
// A tray with a single position — endless or without LAST — has exactly one
// slot, whatever its DIR_MODE says.
func slotOrder(d TrayDef) []int {
	if d.SlotCount() <= 1 {
		return []int{0}
	}
	m := d.Dir
	// The primary axis is the inner loop, so its extent is the inner count.
	inner, outer := d.Cols, d.Rows
	if m.Primary == AxisRow {
		inner, outer = d.Rows, d.Cols
	}
	order := make([]int, 0, inner*outer)
	for o := 0; o < outer; o++ {
		oi := o
		if !m.SecondaryUp {
			oi = outer - 1 - o
		}
		// Meander flips the primary direction on every second *pass*, counted by
		// the pass number and not by the geometric index: the point is that a
		// pass ends where the next one starts, whichever end of the tray the
		// secondary direction started from.
		up := m.PrimaryUp
		if m.Meander && o%2 == 1 {
			up = !up
		}
		for i := 0; i < inner; i++ {
			ii := i
			if !up {
				ii = inner - 1 - i
			}
			col, row := ii, oi
			if m.Primary == AxisRow {
				col, row = oi, ii
			}
			order = append(order, row*d.Cols+col)
		}
	}
	return order
}

// ---------------------------------------------------------------------------
// Runtime state: trays, process stations, held material (§7.1)
// ---------------------------------------------------------------------------

// slotEmpty is the state of a slot that holds nothing. Every other value is a
// process step: 0 is unprocessed material, a positive value material processed
// at that step (§7.1).
//
// The states are int64 rather than the design's int32 so that every value the
// u32 process-step pin can carry fits alongside the -1 marker. With int32 a
// process step above 2^31 would wrap into a negative number and a step of
// exactly 2^32-1 would collide with "empty" — a PLC word being read as "this
// slot is free" is not a failure mode worth the four bytes it saves.
const slotEmpty int64 = -1

// trayGeometry is one TrayDef plus its precomputed visit order. Immutable after
// construction and shared by every tray station whose tray-id selects it.
type trayGeometry struct {
	def   TrayDef
	order []int
}

func newTrayGeometry(d TrayDef) *trayGeometry {
	return &trayGeometry{def: d, order: slotOrder(d)}
}

// trayState is the live state of one tray *station*: which geometry its tray-id
// pin currently selects, what its slots hold, and how many successive picks have
// come up empty.
//
// Slot state is authoritative for which slots to try (D9); the picker's
// material-present feedback only validates and corrects it, which is what
// misses/probedEmpty record.
type trayState struct {
	cfg  TrayStation
	pins *trayPins

	// defs is the shared tray-id -> geometry lookup, so a tray-id change can
	// re-select without going back to the config.
	defs map[uint32]*trayGeometry

	// geom is the selected geometry, nil while tray-id names none (which
	// includes the unwired 0 every u32 pin starts at).
	geom   *trayGeometry
	trayID uint32

	// slots is indexed row*Cols + col; empty while no geometry is selected.
	slots []int64

	// misses counts successive empty picks; MAX_UNPOPULATED of them declare the
	// tray empty by probing. probedEmpty is that declaration, and only a
	// set-full/set-empty edge or a tray change clears it — the tracked state
	// cannot tell us the tray was refilled.
	misses      int
	probedEmpty bool

	// pending is a restored record whose tray-id the pin has not confirmed yet;
	// see world.restore.
	pending *trayRecord

	// dirty marks state the persistence has not seen yet (see world.flush).
	dirty bool
}

// selectDef points the station at the geometry tray-id names and starts it from
// the empty state (D17: a tray-id change resets all slots to -1, which is also
// the startup state). It reports whether the id named a known TRAYDEF.
//
// The geometry-less branch deliberately does NOT mark the state dirty: there is
// no state worth a record, and flush skips geometry-less stations anyway — a
// {TrayID: garbage} write would only ever destroy a record that may still be
// the valid one (see applyTrayID for the id-0 case, which never even reaches
// this function).
func (t *trayState) selectDef(id uint32) bool {
	t.trayID = id
	t.geom = t.defs[id]
	if t.geom == nil {
		t.slots = nil
		t.misses, t.probedEmpty = 0, false
		return false
	}
	t.slots = make([]int64, t.geom.def.SlotCount())
	t.resetSlots()
	return true
}

// applyTrayID is the single owner of the tray-id policy: what a new selector
// value means for the station's live state, its pending record and the
// persistence. The control loop routes every pin change through here, and the
// restore path shares its adoption half (adoptTray), so the rule cannot drift
// between the two.
func (w *world) applyTrayID(t *trayState, id uint32) {
	if id == t.trayID {
		return
	}
	if id == 0 {
		// "Not told yet" — an unwired selector, or a PLC rebooting while
		// stmakd keeps running. That is a dropout, not a tray change: the live
		// state is parked as pending exactly like a restart parks the restored
		// record, so the id coming back adopts it unchanged — and nothing is
		// marked dirty, because the persisted record has to survive the blip
		// too (it may be the only copy if stmakd dies mid-blip).
		if t.geom != nil {
			t.pending = &trayRecord{TrayID: t.trayID, Slots: append([]int64(nil), t.slots...)}
		}
		t.trayID, t.geom, t.slots = 0, nil, nil
		t.misses, t.probedEmpty = 0, false
		w.logger.Info("pnptask: tray-id cleared, station has no geometry", "station", t.cfg.ID)
		return
	}
	// A record waiting for its tray-id — restored at boot, or parked by the
	// dropout branch above — is adopted the moment the pin names the geometry
	// it was recorded under.
	if t.pending != nil && t.pending.TrayID == id {
		rec := *t.pending
		t.pending = nil
		if w.adoptTray(t, rec) {
			w.logger.Info("pnptask: tray state restored",
				"station", t.cfg.ID, "tray_id", id, "slots", len(t.slots))
			return
		}
	}
	t.pending = nil
	if !t.selectDef(id) {
		// Not fatal — the id is a PLC value, not configuration. The station
		// simply has no geometry, and a job against it raises INVALID_TRAY_ID.
		w.logger.Warn("pnptask: tray-id names no TRAYDEF", "station", t.cfg.ID, "tray_id", id)
		return
	}
	w.logger.Info("pnptask: tray changed, slot state reset",
		"station", t.cfg.ID, "tray_id", id, "slots", len(t.slots))
}

// resetSlots empties every slot and forgets the probing history.
func (t *trayState) resetSlots() { t.setAll(slotEmpty) }

// setAll writes one state into every slot — the set-full/set-empty edges (D8).
// It also clears the probing history: whoever pressed the button knows better
// than the counters do what is in the tray.
func (t *trayState) setAll(v int64) {
	for i := range t.slots {
		t.slots[i] = v
	}
	t.misses, t.probedEmpty = 0, false
	t.dirty = true
}

// endless reports whether the selected geometry is an endless tray, whose fill
// state is only ever known by probing.
func (t *trayState) endless() bool { return t.geom != nil && t.geom.def.Endless() }

// slotPos is the absolute machine position of a linear slot index.
func (t *trayState) slotPos(slot int) pnproute.Point {
	cols := t.geom.def.Cols
	if cols < 1 {
		return t.geom.def.First
	}
	return t.geom.def.SlotPos(slot%cols, slot/cols)
}

// nextPick returns the next slot to try a pick at, searching the DIR_MODE order
// from position from onwards, plus the position to resume at after a miss.
//
// The step matching is what makes the tray a work-in-progress store rather than
// a bin: a job asking for step 0 picks unprocessed material, a job asking for
// step 1 picks what a previous job put back after its first process station.
func (t *trayState) nextPick(step int64, from int) (slot, next int, ok bool) {
	if t.geom == nil || t.probedEmpty {
		return 0, 0, false
	}
	if t.endless() {
		// One position, no bookkeeping: it is worth exactly one attempt per
		// search, and only probing can say it is empty.
		if from > 0 {
			return 0, 0, false
		}
		return 0, 1, true
	}
	for p := from; p < len(t.geom.order); p++ {
		if s := t.geom.order[p]; t.slots[s] == step {
			return s, p + 1, true
		}
	}
	return 0, 0, false
}

// freeSlot returns the first empty slot in DIR_MODE order. Places follow the
// same order as picks: it is the order the tray is meant to be worked through,
// and a place that ignored it would scatter material across a tray a later pick
// then travels back and forth over.
func (t *trayState) freeSlot() (slot int, ok bool) {
	if t.geom == nil {
		return 0, false
	}
	if t.endless() {
		return 0, true
	}
	for _, s := range t.geom.order {
		if t.slots[s] == slotEmpty {
			return s, true
		}
	}
	return 0, false
}

// markEmpty records that a slot turned out to hold nothing (D9: the physical
// feedback corrects the tracked state) and counts the miss.
func (t *trayState) markEmpty(slot int) {
	if t.geom != nil && !t.endless() {
		t.slots[slot] = slotEmpty
	}
	t.misses++
	t.dirty = true
	if t.geom != nil && t.misses >= t.geom.def.MaxUnpopulated {
		t.probedEmpty = true
	}
}

// markPicked records a successful pick: the slot is now empty and the tray is
// demonstrably not exhausted.
func (t *trayState) markPicked(slot int) {
	if t.geom != nil && !t.endless() {
		t.slots[slot] = slotEmpty
	}
	t.misses, t.probedEmpty = 0, false
	t.dirty = true
}

// markPlaced records material placed into a slot at the job's process step.
func (t *trayState) markPlaced(slot int, step int64) {
	if t.geom != nil && !t.endless() {
		t.slots[slot] = step
	}
	t.dirty = true
}

// full reports whether the tray has no empty slot left. An endless tray is never
// full and a station with no geometry selected reports neither full nor empty —
// it has no slot state at all, and the operator's cue for that is the
// INVALID_TRAY_ID a job raises, not a pin that guesses.
func (t *trayState) full() bool {
	if t.geom == nil || t.endless() {
		return false
	}
	for _, v := range t.slots {
		if v == slotEmpty {
			return false
		}
	}
	return true
}

// emptyFor reports whether the tray has nothing to offer a pick at that step,
// either because no slot matches or because probing declared it empty.
func (t *trayState) emptyFor(step int64) bool {
	if t.geom == nil {
		return false
	}
	if t.probedEmpty {
		return true
	}
	if t.endless() {
		return false
	}
	_, _, ok := t.nextPick(step, 0)
	return !ok
}

// procState is the live state of one process station. has-material is an output
// pin because pnptask owns that knowledge: the station itself has no way to tell
// whether the fixture holds a part.
type procState struct {
	cfg  ProcStation
	pins *procPins

	// idx is this station's position in the config (and so in the control loop's
	// per-station input slices), kept so a sequence holding a *procState can
	// still read the busy/released feedback of this cycle's snapshot.
	idx int

	hasMaterial bool
	dirty       bool
}

func (p *procState) setHasMaterial(v bool) {
	if p.hasMaterial != v {
		p.hasMaterial = v
		p.dirty = true
	}
}

// station is one addressable station: exactly one of tray/proc is set. The two
// kinds share one id space (config.go enforces that), because origin-id and
// dest-id name a station without saying which kind it is.
type station struct {
	id   uint32
	tray *trayState
	proc *procState
}

func (s *station) isTray() bool { return s.tray != nil }

// heldMaterial is one picker's held-material record (D20). Roles are not fixed:
// the free picker performs the next pick, the picker holding the job's material
// performs the place, so "which picker" is a question the engine has to ask
// rather than assume. With pickers=1 the slice has one entry and the answer is
// always the same — but it is still asked, which is what keeps phase 6 additive.
type heldMaterial struct {
	// present is whether the picker holds material at all; station is where that
	// material came from, which is what the §8 sequence constraint is about.
	present bool
	station uint32
}

// world is the module's model of everything it tracks: tray contents, process
// station occupancy and which picker holds what. It belongs to the control
// goroutine like the rest of the module's state, so nothing in it is locked.
type world struct {
	logger *slog.Logger

	trays []*trayState
	procs []*procState
	byID  map[uint32]*station

	// held is indexed by picker number (D20).
	held      []heldMaterial
	heldDirty bool

	// persist is nil unless persist_instance= was given (D6: no default lookup,
	// absent means in-memory only).
	persist *persistStore
}

// newWorld builds the runtime model from the parsed config and the exported pin
// tree. It does not touch the pins: the tray-id sampling and the first publish
// belong to the control loop, and the persistence restore between them needs the
// model to exist first.
func newWorld(cfg *Config, pins *pinSet, pickers int, logger *slog.Logger) *world {
	w := &world{
		logger: logger,
		byID:   make(map[uint32]*station, len(cfg.Trays)+len(cfg.Procs)),
		held:   make([]heldMaterial, pickers),
	}

	defs := make(map[uint32]*trayGeometry, len(cfg.TrayDefs))
	for _, d := range cfg.TrayDefs {
		defs[d.ID] = newTrayGeometry(d)
	}

	for i := range cfg.Trays {
		t := &trayState{cfg: cfg.Trays[i], pins: &pins.trays[i], defs: defs}
		w.trays = append(w.trays, t)
		w.byID[t.cfg.ID] = &station{id: t.cfg.ID, tray: t}
	}
	for i := range cfg.Procs {
		p := &procState{cfg: cfg.Procs[i], pins: &pins.procs[i], idx: i}
		w.procs = append(w.procs, p)
		w.byID[p.cfg.ID] = &station{id: p.cfg.ID, proc: p}
	}
	return w
}

// station looks a station up by the id an origin-id/dest-id pin carries.
func (w *world) station(id uint32) *station { return w.byID[id] }

// start seeds the model from the pins and the persisted state. It runs once,
// before the control loop begins, so the loop's first publish already carries
// the restored tray contents.
//
// The tray-id pins are read here and not only watched for changes: a config
// whose PLC (or HAL file) has the pin at its final value before stmakd starts
// never produces a change, and a station left without geometry because its
// selector happened to be correct from the first cycle would refuse every job.
func (w *world) start() {
	for _, t := range w.trays {
		id := t.pins.trayID.Get()
		if id == 0 {
			// Not told yet: id 0 is no valid TRAYDEF (config.go refuses it
			// because an unconnected u32 pin reads 0).
			continue
		}
		if !t.selectDef(id) {
			w.logger.Warn("pnptask: tray-id names no TRAYDEF",
				"station", t.cfg.ID, "tray_id", id)
		}
	}
	w.restore()
}

// freePicker returns a picker that holds nothing, lowest number first (§8:
// picker 0 is preferred when both are free). Only *a* free picker is needed for
// a pick, not a specific one (D20).
func (w *world) freePicker() (int, bool) {
	for n := range w.held {
		if !w.held[n].present {
			return n, true
		}
	}
	return 0, false
}

// holderOf returns the picker holding material that came from that station, if
// any. This is the question the place phase asks (D20) — never "picker 0".
func (w *world) holderOf(station uint32) (int, bool) {
	for n := range w.held {
		if w.held[n].present && w.held[n].station == station {
			return n, true
		}
	}
	return 0, false
}

// setHeld records that picker n now holds material from that station.
func (w *world) setHeld(n int, station uint32) {
	if n < 0 || n >= len(w.held) {
		return
	}
	w.held[n] = heldMaterial{present: true, station: station}
	w.heldDirty = true
}

// clearHeld forgets picker n's held material — it was placed, or released.
func (w *world) clearHeld(n int) {
	if n < 0 || n >= len(w.held) || !w.held[n].present {
		return
	}
	w.held[n] = heldMaterial{}
	w.heldDirty = true
}

// clearAllHeld forgets every held record. Estop opens all pickers (D14), so
// whatever they held is on the table now and the records would be fiction.
func (w *world) clearAllHeld() {
	for n := range w.held {
		w.clearHeld(n)
	}
}

// publish writes the station output pins. It runs every control cycle, so the
// PLC sees a tray fill up slot by slot rather than in one jump at the end of a
// job.
//
// step is the current process-step pin value, which is what "no slot matching a
// pick" is measured against (§5.2's empty pin). Deliberately the *live* pin and
// not a job's latched copy: between jobs there is no latched step, and the PLC
// reads this pin to decide what to command next.
func (w *world) publish(step int64) {
	for _, t := range w.trays {
		t.pins.empty.Set(t.emptyFor(step))
		t.pins.full.Set(t.full())
	}
	for _, p := range w.procs {
		p.pins.hasMaterial.Set(p.hasMaterial)
	}
}
