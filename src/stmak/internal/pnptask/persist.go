// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/persist"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

// persistVersion is the persist GMI version this module is built against — the
// same version task, tooltable, halscope and ngcpreview require.
const persistVersion = 2

// Keys inside the instance's namespace. The tray and proc keys carry the station
// id, so a config that gains a station does not disturb the others.
const (
	persistTrayPrefix = "tray."
	persistProcPrefix = "proc."
	// persistHeldKey holds the per-picker held-material records. The design
	// document calls this "alt_picker" from the days of a single altHeld record;
	// D20 replaced that with one record per picker, and the key is named after
	// what it now stores.
	persistHeldKey = "held_material"
)

// trayRecord is the persisted state of one tray station (§7.2). The tray-id is
// stored with the slots because the slots only mean anything for the geometry
// they were filled under.
//
// The probing state (the successive-miss counter and the "declared empty by
// probing" flag) is deliberately not persisted: it is a conclusion drawn from
// physical feedback, and re-deriving it after a restart costs at most one probing
// pass, while restoring it could leave a refilled tray declared empty with
// nothing in the record to justify it.
type trayRecord struct {
	TrayID uint32  `json:"tray_id"`
	Slots  []int64 `json:"slots"`
}

// procRecord is the persisted state of one process station.
type procRecord struct {
	HasMaterial bool `json:"has_material"`
}

// heldRecords is the persisted per-picker held-material state (D20). Only the
// pickers that actually hold something are listed, and each entry names its
// picker explicitly, so a config switched between pickers=1 and pickers=2 cannot
// silently shift a record onto the wrong picker.
type heldRecords struct {
	Pickers []heldRecord `json:"pickers"`
}

type heldRecord struct {
	Picker  int    `json:"picker"`
	Station uint32 `json:"station"`
}

// persistStore is the optional persist_sqlite backing of the world model (D6).
// Every method tolerates a nil receiver, which is what "no persist_instance="
// looks like from the caller's side: the model then simply keeps its state in
// memory.
//
// A storage failure never fails a job. The machine's own state is the tracked
// model, not the database; losing a write means the state does not survive a
// restart, which is strictly better than aborting a cycle with a part in the
// picker over a locked sqlite file.
type persistStore struct {
	db     *persist.PersistClient
	handle int32
	logger *slog.Logger
}

// openPersist resolves the persist API and opens this instance's namespace.
// Unlike the model itself this can fail the module's Start: the operator asked
// for persistence on the load line, and quietly running without it would lose
// tray state at every restart with nothing to show why.
func openPersist(owner, instance, namespace string, logger *slog.Logger) (*persistStore, error) {
	reg := apiserver.DefaultRegistry()
	if reg == nil {
		return nil, fmt.Errorf("no API registry available")
	}
	cbs, err := reg.GetAPIFor(owner, "persist", instance, persistVersion)
	if err != nil {
		return nil, fmt.Errorf("persist API lookup (%s): %w", instance, err)
	}
	db := persist.NewPersistClient(unsafe.Pointer(cbs))
	res, err := db.Open(namespace)
	if err != nil {
		return nil, fmt.Errorf("persist open namespace %q: %w", namespace, err)
	}
	logger.Info("pnptask persistence open", "instance", instance, "namespace", namespace)
	return &persistStore{db: db, handle: res.Handle, logger: logger}, nil
}

// persistNamespace is the instance name reduced to the [A-Za-z0-9_]+ the persist
// API accepts (§7.2): the dots of an instance name like "pnp.task" become
// underscores, giving "pnp_task".
func persistNamespace(instance string) string {
	var b strings.Builder
	for _, r := range instance {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "pnptask"
	}
	return b.String()
}

// load reads a JSON record into v. It reports false when the key was never
// written, when storage could not be read, or when the stored value does not
// parse — all three mean "no usable state", and the caller starts from the
// empty world rather than from a guess.
func (p *persistStore) load(key string, v any) bool {
	if p == nil {
		return false
	}
	e, err := p.db.GetEntry(p.handle, key)
	if err != nil {
		p.logger.Warn("pnptask: reading persisted state failed", "key", key, "error", err)
		return false
	}
	if e.Value == "" {
		// A missing key reads back as the zero entry with no error (that is the
		// persist API's contract), so an empty value is simply "never stored".
		return false
	}
	if err := json.Unmarshal([]byte(e.Value), v); err != nil {
		p.logger.Warn("pnptask: persisted state is not readable, ignoring",
			"key", key, "error", err)
		return false
	}
	return true
}

// store writes a JSON record. Failures are logged and swallowed — see the type
// comment for why they must not reach the job engine.
func (p *persistStore) store(key string, v any) {
	if p == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		p.logger.Error("pnptask: encoding state for persistence failed", "key", key, "error", err)
		return
	}
	if _, err := p.db.SetEntry(p.handle, key, string(b)); err != nil {
		p.logger.Warn("pnptask: persisting state failed", "key", key, "error", err)
	}
}

// close releases the namespace handle.
func (p *persistStore) close() {
	if p == nil {
		return
	}
	if err := p.db.Close(p.handle); err != nil {
		p.logger.Debug("pnptask: closing persistence failed", "error", err)
	}
}

// ---------------------------------------------------------------------------
// World <-> store
// ---------------------------------------------------------------------------

func trayKey(id uint32) string { return fmt.Sprintf("%s%d", persistTrayPrefix, id) }
func procKey(id uint32) string { return fmt.Sprintf("%s%d", persistProcPrefix, id) }

// restore loads the persisted state into the model. It runs once, before the
// control loop starts, so the first published cycle already carries the restored
// tray contents and has-material flags.
//
// Tray records are only adopted for the geometry they were recorded under
// (§7.2): a tray-id that has changed since the last run means the tray in the
// station is a different one, and its slot states would be fiction.
//
// The one refinement over the design document: a tray-id pin reading 0 is not
// treated as a mismatch but as "not told yet". Zero is no valid TRAYDEF id
// (config.go refuses id 0 precisely because an unconnected u32 pin reads 0), and
// at the moment Start runs the PLC that drives the pin has typically not written
// it yet — comparing against it then would discard the state of every restart.
// The record is kept pending instead and adopted the moment tray-id names the
// geometry it was recorded under; any *other* id discards it immediately.
func (w *world) restore() {
	if w.persist == nil {
		return
	}
	for _, t := range w.trays {
		var rec trayRecord
		if !w.persist.load(trayKey(t.cfg.ID), &rec) {
			continue
		}
		id := t.pins.trayID.Get()
		switch {
		case id == rec.TrayID:
			if !w.adoptTray(t, rec) {
				continue
			}
			w.logger.Info("pnptask: tray state restored",
				"station", t.cfg.ID, "tray_id", rec.TrayID, "slots", len(rec.Slots))
		case id == 0:
			t.pending = &rec
			w.logger.Debug("pnptask: tray state pending its tray-id",
				"station", t.cfg.ID, "tray_id", rec.TrayID)
		default:
			w.logger.Info("pnptask: persisted tray state discarded, tray-id changed",
				"station", t.cfg.ID, "persisted", rec.TrayID, "current", id)
		}
	}

	for _, p := range w.procs {
		var rec procRecord
		if w.persist.load(procKey(p.cfg.ID), &rec) {
			p.hasMaterial = rec.HasMaterial
		}
	}

	var held heldRecords
	if w.persist.load(persistHeldKey, &held) {
		for _, r := range held.Pickers {
			if r.Picker < 0 || r.Picker >= len(w.held) {
				// A record for a picker this instance does not have: the load
				// line was changed from pickers=2 to pickers=1 while something
				// was held. Dropping it is the only honest option — there is no
				// picker to hold it — and it is logged because the operator has
				// a part to find.
				w.logger.Warn("pnptask: persisted held material dropped, no such picker",
					"picker", r.Picker, "station", r.Station)
				continue
			}
			w.held[r.Picker] = heldMaterial{present: true, station: r.Station}
			w.logger.Info("pnptask: held material restored",
				"picker", r.Picker, "station", r.Station)
		}
	}
}

// adoptTray installs a persisted slot state on a station. It reports false if
// the record does not fit the geometry the tray-id names — a config edited
// between runs (a tray that gained a row, a TRAYDEF that was removed) makes the
// stored slots meaningless, and stretching or truncating them would hand the
// engine slot indices that do not correspond to anything on the machine.
func (w *world) adoptTray(t *trayState, rec trayRecord) bool {
	if !t.selectDef(rec.TrayID) {
		w.logger.Warn("pnptask: persisted tray state discarded, no such TRAYDEF",
			"station", t.cfg.ID, "tray_id", rec.TrayID)
		return false
	}
	if len(rec.Slots) != len(t.slots) {
		w.logger.Warn("pnptask: persisted tray state discarded, slot count changed",
			"station", t.cfg.ID, "tray_id", rec.TrayID,
			"persisted", len(rec.Slots), "configured", len(t.slots))
		return false
	}
	copy(t.slots, rec.Slots)
	// The state came *from* storage, so there is nothing to write back.
	t.dirty = false
	return true
}

// flush writes out whatever changed since the last call. It runs once per
// control cycle rather than at every assignment, so the several state changes of
// one pick (slot emptied, held record set) cost one write instead of three and
// nothing is ever more than a cycle behind.
func (w *world) flush() {
	if w.persist == nil {
		return
	}
	for _, t := range w.trays {
		if !t.dirty {
			continue
		}
		t.dirty = false
		w.persist.store(trayKey(t.cfg.ID), trayRecord{TrayID: t.trayID, Slots: t.slots})
	}
	for _, p := range w.procs {
		if !p.dirty {
			continue
		}
		p.dirty = false
		w.persist.store(procKey(p.cfg.ID), procRecord{HasMaterial: p.hasMaterial})
	}
	if w.heldDirty {
		w.heldDirty = false
		rec := heldRecords{}
		for n := range w.held {
			if w.held[n].present {
				rec.Pickers = append(rec.Pickers, heldRecord{Picker: n, Station: w.held[n].station})
			}
		}
		w.persist.store(persistHeldKey, rec)
	}
}
