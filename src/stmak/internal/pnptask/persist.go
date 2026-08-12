// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	// Swap carries §8's sequence constraint across a restart: material a swap
	// took out of a process station still has to be carried away by a job from
	// that station, and a restart that forgot which record was a swap would
	// let the next job leave it in the picker indefinitely.
	Swap bool `json:"swap,omitempty"`
}

// persistRetryDelay is how long the writer backs off after a failed SetEntry
// before retrying the key. Failures are expected to be transient (a locked
// sqlite file); the latest value is kept and re-tried until it lands.
var persistRetryDelay = time.Second

// persistStore is the optional persist_sqlite backing of the world model (D6).
// Every method tolerates a nil receiver, which is what "no persist_instance="
// looks like from the caller's side: the model then simply keeps its state in
// memory.
//
// A storage failure never fails a job. The machine's own state is the tracked
// model, not the database; a write that never lands means the state does not
// survive a restart, which is strictly better than aborting a cycle with a
// part in the picker over a locked sqlite file.
//
// Writes go through a dedicated goroutine, never the caller: SetEntry can
// block for seconds on a contended database (persist_sqlite's busy_timeout is
// 5 s under a module-wide mutex), and the caller is the control loop — whose
// 10 ms cycle is what bounds the reaction time to an estop. store() only
// updates a map and pokes the writer. The writer keeps the latest value per
// key and retries failed writes until they land, so a transient failure heals
// itself instead of silently costing the write (the old synchronous path
// dropped it after one attempt).
type persistStore struct {
	db     *persist.PersistClient
	handle int32
	logger *slog.Logger

	mu      sync.Mutex
	pending map[string]string // latest not-yet-stored value per key
	kick    chan struct{}     // buffered(1): wakes the writer
	stop    chan struct{}
	done    chan struct{}
	ns      string // the claimed namespace, released on close
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
	if err := claimNamespace(namespace, owner); err != nil {
		return nil, err
	}
	res, err := db.Open(namespace)
	if err != nil {
		releaseNamespace(namespace)
		return nil, fmt.Errorf("persist open namespace %q: %w", namespace, err)
	}
	logger.Info("pnptask persistence open", "instance", instance, "namespace", namespace)
	p := &persistStore{
		db: db, handle: res.Handle, logger: logger,
		pending: make(map[string]string),
		kick:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		ns:      namespace,
	}
	go p.writer()
	return p, nil
}

// The process-wide namespace claims. The sanitization below is lossy ("pnp.0"
// and "pnp_0" both become "pnp_0"), and persist_sqlite hands the SAME handle to
// every opener of a namespace — two instances silently sharing one would
// interleave their tray records. Claiming the namespace per process turns that
// into a load-time error naming both instances.
var (
	persistNSMu sync.Mutex
	persistNS   = map[string]string{} // namespace -> owning instance
)

func claimNamespace(ns, owner string) error {
	persistNSMu.Lock()
	defer persistNSMu.Unlock()
	if prev, taken := persistNS[ns]; taken {
		return fmt.Errorf("persist namespace %q derived from instance %q collides with instance %q — rename one of them",
			ns, owner, prev)
	}
	persistNS[ns] = owner
	return nil
}

func releaseNamespace(ns string) {
	persistNSMu.Lock()
	defer persistNSMu.Unlock()
	delete(persistNS, ns)
}

// persistNamespace is the instance name reduced to what the persist API
// accepts (§7.2): [A-Za-z0-9_]+, at most persistNameLen characters — the same
// rules persist_sqlite's validName enforces, because the namespace becomes a
// database filename there. The dots of an instance name like "pnp.task" become
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
	s := b.String()
	if len(s) > persistNameLen {
		s = s[:persistNameLen]
	}
	return s
}

// persistNameLen mirrors persist_sqlite's maxNameLen: sanitizing a longer name
// here and then failing Open with a generic "invalid namespace" would point
// the operator at the wrong module.
const persistNameLen = 64

// loadResult is what a load attempt learned — and, crucially, whether the
// stored record (if any) may be overwritten. "Never written" may; "could not
// be read" must NOT: the record behind a transiently locked file or a format
// this binary cannot parse may be perfectly intact, and the one thing worse
// than not restoring it would be flushing empty state over it.
type loadResult int

const (
	loadOK      loadResult = iota // v is filled from the stored record
	loadMissing                   // the key was never written
	loadFailed                    // read or parse failed; the record may be intact
)

// load reads a JSON record into v.
func (p *persistStore) load(key string, v any) loadResult {
	if p == nil {
		return loadMissing
	}
	e, err := p.db.GetEntry(p.handle, key)
	if err != nil {
		p.logger.Warn("pnptask: reading persisted state failed", "key", key, "error", err)
		return loadFailed
	}
	if e.Value == "" {
		// A missing key reads back as the zero entry with no error (that is the
		// persist API's contract), so an empty value is simply "never stored".
		return loadMissing
	}
	if err := json.Unmarshal([]byte(e.Value), v); err != nil {
		p.logger.Warn("pnptask: persisted state is not readable, keeping it untouched",
			"key", key, "error", err)
		return loadFailed
	}
	return loadOK
}

// store hands a JSON record to the writer goroutine — see the type comment for
// why it must not block or fail the caller.
func (p *persistStore) store(key string, v any) {
	if p == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		p.logger.Error("pnptask: encoding state for persistence failed", "key", key, "error", err)
		return
	}
	p.mu.Lock()
	p.pending[key] = string(b)
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// writer is the goroutine every write goes through. It drains the pending map
// one key at a time; a failed SetEntry puts the value back (unless a newer one
// arrived meanwhile) and backs off, so a transiently locked database delays
// the write instead of losing it.
func (p *persistStore) writer() {
	defer close(p.done)
	for {
		for {
			p.mu.Lock()
			var key, val string
			ok := false
			for k, v := range p.pending {
				key, val, ok = k, v, true
				break
			}
			if ok {
				delete(p.pending, key)
			}
			p.mu.Unlock()
			if !ok {
				break
			}
			if _, err := p.db.SetEntry(p.handle, key, val); err != nil {
				p.logger.Warn("pnptask: persisting state failed, will retry",
					"key", key, "error", err)
				p.mu.Lock()
				if _, newer := p.pending[key]; !newer {
					p.pending[key] = val
				}
				p.mu.Unlock()
				select {
				case <-p.stop:
					return
				case <-time.After(persistRetryDelay):
				}
			}
		}
		select {
		case <-p.stop:
			return
		case <-p.kick:
		}
	}
}

// close stops the writer, makes one last synchronous attempt at whatever it
// had not stored yet (blocking is fine here — the control loop is already
// gone), and releases the namespace handle.
func (p *persistStore) close() {
	if p == nil {
		return
	}
	close(p.stop)
	<-p.done
	p.mu.Lock()
	left := p.pending
	p.pending = map[string]string{}
	p.mu.Unlock()
	for key, val := range left {
		if _, err := p.db.SetEntry(p.handle, key, val); err != nil {
			p.logger.Warn("pnptask: final persistence write failed, state lost",
				"key", key, "error", err)
		}
	}
	if err := p.db.Close(p.handle); err != nil {
		p.logger.Debug("pnptask: closing persistence failed", "error", err)
	}
	releaseNamespace(p.ns)
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
		switch w.persist.load(trayKey(t.cfg.ID), &rec) {
		case loadFailed:
			// The record may be intact behind a transient read failure, and
			// world.start's fresh-empty selection has armed a flush that would
			// overwrite it. Disarm it: a read hiccup must cost at most this
			// run's restore, never the record (the stated failure model is
			// that only writes are lossy). A real state change later re-dirties
			// and overwrites legitimately.
			t.dirty = false
			continue
		case loadMissing:
			continue
		case loadOK:
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
		switch w.persist.load(procKey(p.cfg.ID), &rec) {
		case loadOK:
			p.hasMaterial = rec.HasMaterial
		case loadFailed:
			p.dirty = false // same rule as the trays: never overwrite what could not be read
		case loadMissing:
		}
	}

	var held heldRecords
	if w.persist.load(persistHeldKey, &held) == loadOK {
		for _, r := range held.Pickers {
			if r.Picker < 0 || r.Picker >= len(w.held) {
				// A record for a picker this instance does not have: the load
				// line was changed from pickers=2 to pickers=1 while something
				// was held. Dropping it is the only honest option — there is no
				// picker to hold it — and it is logged because the operator has
				// a part to find. The drop is written back (heldDirty), or a
				// later switch to pickers=2 would resurrect the record as a
				// phantom held part.
				w.logger.Warn("pnptask: persisted held material dropped, no such picker",
					"picker", r.Picker, "station", r.Station)
				w.heldDirty = true
				continue
			}
			w.held[r.Picker] = heldMaterial{present: true, station: r.Station, swap: r.Swap}
			// D14's intent for a short restart: the part is meant to stay
			// held, but a process restart re-exports the close output at its
			// zero value. Re-drive it from the record — and let the control
			// loop's first cycles validate the grip (verifyRestoredHeld): if
			// the part was lost in the downtime, the record is cleared with a
			// warning instead of refusing every job over a phantom part.
			w.pickerClose[r.Picker].Set(true)
			w.restoredHeld[r.Picker] = true
			w.logger.Info("pnptask: held material restored, picker re-closed",
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

// flush hands whatever changed since the last call to the store's writer. It
// runs once per control cycle rather than at every assignment, so the several
// state changes of one pick (slot emptied, held record set) cost one write
// instead of three and nothing is ever more than a cycle behind. Clearing the
// dirty flags here is safe because store() never fails and never blocks: the
// writer keeps the latest value per key and retries until it lands.
func (w *world) flush() {
	if w.persist == nil {
		return
	}
	for _, t := range w.trays {
		if !t.dirty {
			continue
		}
		if t.geom == nil {
			// A geometry-less station has no state worth a record, and a
			// {TrayID: 0} write would destroy the one that may still be valid
			// on disk (a selector dropout parks the state, see applyTrayID).
			t.dirty = false
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
				rec.Pickers = append(rec.Pickers, heldRecord{
					Picker: n, Station: w.held[n].station, Swap: w.held[n].swap,
				})
			}
		}
		w.persist.store(persistHeldKey, rec)
	}
}
