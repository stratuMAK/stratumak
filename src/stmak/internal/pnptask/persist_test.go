// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"

	// Registers the persist_sqlite gomod so these cases exercise the real
	// persistence backend. The interesting behaviour — what survives a restart
	// and what is deliberately discarded — lives in the interaction between the
	// two, not in either alone.
	_ "github.com/stratuMAK/stratumak/src/stmak/internal/persist_sqlite"
)

func TestPersistNamespace(t *testing.T) {
	cases := map[string]string{
		"pnp.task":   "pnp_task",
		"pnptask":    "pnptask",
		"pnp.task.0": "pnp_task_0",
		"a-b c":      "a_b_c",
		"":           "pnptask",
	}
	for in, want := range cases {
		if got := persistNamespace(in); got != want {
			t.Errorf("persistNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

// persistCounter keeps the persist_sqlite instance names unique; the API
// registry is process-global.
var persistCounter int

// newTestPersist brings up a real persist_sqlite instance over a database in dir
// and returns its instance name.
func newTestPersist(t *testing.T, dir string) string {
	t.Helper()
	if apiserver.DefaultRegistry() == nil {
		prev := apiserver.DefaultRegistry()
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
		t.Cleanup(func() { apiserver.SetDefaultRegistry(prev) })
	}
	persistCounter++
	name := fmt.Sprintf("pnpt_persist_%d", persistCounter)
	// persist_sqlite resolves its dbpath through the shared resolver, which the
	// module fixtures only install later (with their own directory, for the
	// dead-zone drawings). The path here is absolute, so all it needs is a
	// resolver that exists.
	pathres.SetDefaultForTest(t, dir)

	newPersist := stmak.GetFactory("persist_sqlite")
	if newPersist == nil {
		t.Fatal("persist_sqlite is not registered")
	}
	p, err := newPersist(nil, testLogger(), name, []string{"dbpath=" + filepath.Join(dir, "db")})
	if err != nil {
		t.Fatalf("persist_sqlite: %v", err)
	}
	t.Cleanup(p.Destroy)
	if err := p.Start(); err != nil {
		t.Fatalf("persist_sqlite Start: %v", err)
	}
	return name
}

// withPersist returns a fixture prep hook that attaches a persistence store over
// the given instance and namespace, and optionally seeds the pins first.
//
// The namespace is passed explicitly rather than derived from the module name
// (as the real Start does): each fixture needs its own HAL component name, and
// two "runs" of the same machine have to meet in the same namespace for the
// restore to have anything to read.
func withPersist(persistName, namespace string, seed func(*pnptaskModule)) func(*testing.T, *pnptaskModule) {
	return func(t *testing.T, m *pnptaskModule) {
		t.Helper()
		if seed != nil {
			seed(m)
		}
		store, err := openPersist(m.name, persistName, namespace, testLogger())
		if err != nil {
			t.Fatalf("openPersist: %v", err)
		}
		// The restore itself is startControl's job (through world.start), the
		// same way the real Start does it.
		m.world.persist = store
	}
}

// TestPersistenceRoundTrip runs the machine twice over one database: the first
// run fills a tray, occupies a process station and loads a picker, the second
// finds all three where it left them.
func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_roundtrip"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
			// has-material and the held record have no pin to set them from;
			// the job engine is what moves them in real life.
			m.world.procs[0].setHasMaterial(true)
			m.world.setHeld(0, 20, false, 0, true)
		}),
	})
	// Fill the tray at step 5 through the pins, which is how an operator does it.
	first.eventually("tray empty before filling", func() bool { return first.bit("tray.10.empty") })
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })
	// Stop joins the control goroutine and flushes what its last cycles changed.
	first.m.Stop()

	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	second.eventually("restored tray state", func() bool {
		return second.bit("tray.10.full") && !second.bit("tray.10.empty")
	})
	second.eventually("restored has-material", func() bool { return second.bit("proc.20.has-material") })

	w := second.stopped()
	if !w.held[0].present || w.held[0].station != 20 {
		t.Errorf("restored held record = %+v, want material from station 20", w.held[0])
	}
	if _, ok := w.freePicker(); ok {
		t.Error("a picker reported free although the restored record holds material")
	}
}

// TestPersistenceTrayIDMismatchDiscards covers §7.2's condition: slot states only
// mean anything for the geometry they were recorded under, so a tray-id that has
// changed since the last run makes them fiction.
func TestPersistenceTrayIDMismatchDiscards(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_mismatch"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
		}),
	})
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })
	first.m.Stop()

	// Second run with the other tray in the station: TRAYDEF 2 is the endless
	// one, which is never full and never empty by bookkeeping.
	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(2)
		}),
	})
	second.consistently("no state carried over to a different tray", func() bool {
		return !second.bit("tray.10.full")
	})
	w := second.stopped()
	if w.trays[0].trayID != 2 {
		t.Errorf("tray-id = %d after the change, want 2", w.trays[0].trayID)
	}
}

// TestPersistenceSurvivesTrayIDBlip: a selector dropout while the machine runs
// must not cost the persisted record — the X->0->X blip parks the live state,
// and the flush never writes a geometry-less station.
func TestPersistenceSurvivesTrayIDBlip(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_blip"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })

	// The PLC reboots: the selector reads 0 for a while, then comes back.
	first.m.pins.trays[0].trayID.Set(0)
	first.eventually("geometry parked", func() bool { return !first.bit("tray.10.full") })
	first.m.pins.trays[0].trayID.Set(1)
	first.eventually("state back after the blip", func() bool { return first.bit("tray.10.full") })
	first.m.Stop()

	// A restart still finds the full tray: neither the blip nor its flushes
	// overwrote the record.
	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	second.eventually("restored full tray", func() bool { return second.bit("tray.10.full") })
}

// TestPersistenceSeededTrayRestores: a station whose tray-id comes from
// DEFAULT_TRAYDEF restores its slots at boot instead of parking them pending.
// The seed is on the pin before the loop starts, so restore compares the
// record against a real tray-id rather than the "not told yet" 0 — which is
// what makes the state of a station nobody wires survive a restart.
func TestPersistenceSeededTrayRestores(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_seeded"
	const seeded = `
[PNPTASK_TRAY_FIXED]
ID = 11
Z_PICK = 2.5
DEFAULT_TRAYDEF = 1
`

	first := newMachineFixtureOpts(t, fixtureOpts{
		ini: seeded,
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.processStep.Set(5)
		}),
	})
	first.eventually("seeded tray has geometry", func() bool { return first.bit("tray.11.empty") })
	first.pulse(first.m.pins.trays[1].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.11.full") })
	first.m.Stop()

	// Nothing touches the selector on the way back up.
	second := newMachineFixtureOpts(t, fixtureOpts{
		ini: seeded,
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.processStep.Set(5)
		}),
	})
	second.eventually("restored full tray", func() bool { return second.bit("tray.11.full") })
}

// TestGeometrylessResetKeepsRecord: a PLC init sequence that pulses set-empty
// before writing tray-id (a real boot ordering) must not destroy the record a
// restart just parked as pending.
func TestGeometrylessResetKeepsRecord(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_geomless"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })
	first.m.Stop()

	// Restart with the selector still unwritten; the PLC pulses set-empty
	// before it writes tray-id.
	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.processStep.Set(5)
		}),
	})
	second.pulse(second.m.pins.trays[0].setEmpty)
	time.Sleep(20 * pollInterval)
	// tray-id arrives late: the pending record is adopted, not the reset.
	second.m.pins.trays[0].trayID.Set(1)
	second.eventually("record adopted despite the early reset", func() bool {
		return second.bit("tray.10.full")
	})
	second.m.Stop()

	// And it survived on disk, too.
	third := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	third.eventually("record still on disk", func() bool { return third.bit("tray.10.full") })
}

// TestPersistencePendingTrayID covers the refinement over the design document: a
// tray-id pin still reading 0 at startup is "not told yet", not a mismatch. The
// PLC that drives it has typically not written the pin when stmakd starts, and
// discarding on that would make persistence useless in exactly the configuration
// it exists for.
func TestPersistencePendingTrayID(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_pending"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
		}),
	})
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })
	first.m.Stop()

	// Second run: the selector starts at 0, so the station has no geometry and
	// the record waits.
	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, nil),
	})
	second.consistently("no geometry without a tray-id", func() bool {
		return !second.bit("tray.10.full") && !second.bit("tray.10.empty")
	})
	// The PLC comes up and names the tray it was recorded under.
	second.m.pins.trays[0].trayID.Set(1)
	second.eventually("pending state adopted", func() bool { return second.bit("tray.10.full") })

	// A pending record is for its own geometry only: a different tray-id
	// discards it, and going back to the recorded one starts from empty.
	second.m.pins.trays[0].trayID.Set(2)
	second.eventually("tray changed", func() bool { return !second.bit("tray.10.full") })
	second.m.pins.trays[0].trayID.Set(1)
	second.eventually("tray back, state not resurrected", func() bool {
		return second.bit("tray.10.empty") && !second.bit("tray.10.full")
	})
}

// TestAdoptTrayRejectsUnusableRecords covers the guards on a config edited
// between runs: stored slots that no longer describe the configured grid are
// discarded rather than stretched, because a slot index that corresponds to
// nothing on the machine is a head driven at nothing.
func TestAdoptTrayRejectsUnusableRecords(t *testing.T) {
	w := &world{logger: testLogger()}
	ts := newTestTray(t, grid3x2()) // TRAYDEF 1, six slots

	if w.adoptTray(ts, trayRecord{TrayID: 9, Slots: make([]int64, 6)}) {
		t.Error("adopted a record naming a TRAYDEF this config does not have")
	}
	if w.adoptTray(ts, trayRecord{TrayID: 1, Slots: []int64{0, 0, 0}}) {
		t.Error("adopted a record whose slot count does not match the grid")
	}

	rec := trayRecord{TrayID: 1, Slots: []int64{0, slotEmpty, 1, slotEmpty, 2, slotEmpty}}
	if !w.adoptTray(ts, rec) {
		t.Fatal("a matching record was not adopted")
	}
	for i, want := range rec.Slots {
		if ts.slots[i] != want {
			t.Errorf("slot %d = %d after adopting, want %d", i, ts.slots[i], want)
		}
	}
	if ts.dirty {
		t.Error("a state that came from storage was marked for writing back")
	}
}

// TestPersistStoreMissingKey pins the "never written" case: the persist API
// answers a missing key with the zero entry and no error, and the model has to
// read that as "start empty" rather than as a failure.
func TestPersistStoreMissingKey(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	store, err := openPersist("pnptask-test", persistName, "pnptask_missing", testLogger())
	if err != nil {
		t.Fatalf("openPersist: %v", err)
	}
	t.Cleanup(store.close)

	var rec trayRecord
	if got := store.load(trayKey(10), &rec); got != loadMissing {
		t.Errorf("load of an unwritten key = %v, want loadMissing", got)
	}
	// A value that is not the record it should be is a FAILED read, not
	// "start empty": it may be a format a newer binary wrote, and the one
	// thing worse than not restoring it would be overwriting it.
	store.store(trayKey(10), "not a tray record")
	loadEventually(t, store, trayKey(10), &rec, loadFailed)

	store.store(trayKey(10), trayRecord{TrayID: 3, Slots: []int64{slotEmpty, 7}})
	loadEventually(t, store, trayKey(10), &rec, loadOK)
	if rec.TrayID != 3 || len(rec.Slots) != 2 || rec.Slots[1] != 7 {
		t.Errorf("round trip = %+v", rec)
	}

	// A nil store is what "no persist_instance=" looks like from the caller's
	// side: every method has to tolerate it.
	var absent *persistStore
	if got := absent.load("anything", &rec); got != loadMissing {
		t.Errorf("nil store load = %v, want loadMissing", got)
	}
	absent.store("anything", rec)
	absent.close()
}

// loadEventually polls a load until the writer goroutine has landed the value
// (store hands writes to it asynchronously, so a test cannot read back in the
// same breath).
func loadEventually(t *testing.T, p *persistStore, key string, v any, want loadResult) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := p.load(key, v)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("load(%s) never returned %v (last %v)", key, want, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRestoredHeldPartStillThere: a restored held record re-drives the picker
// close output (D14's intent for a short restart), and when the gripper
// feedback confirms the part is still gripped, the record stands.
func TestRestoredHeldPartStillThere(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_heldok"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.world.setHeld(0, 20, false, 0, true)
		}),
	})
	first.m.Stop()

	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.pickSettleTime.Set(10 * pollInterval.Seconds())
		}),
	})
	sim := newMachineSim(second) // default: a close command grips material
	defer sim.shutdown()

	second.eventually("close re-driven from the record", func() bool {
		return second.bit("picker.0.close")
	})
	second.consistently("record and grip stand", func() bool {
		return second.bit("picker.0.close")
	})
	w := second.stopped()
	if !w.held[0].present || w.held[0].station != 20 {
		t.Errorf("held record = %+v, want material from station 20", w.held[0])
	}
}

// TestRestoredHeldPartGone: the part was lost in the downtime — the gripper
// closes onto nothing. The record is cleared and the picker opened (with a
// warning naming the station), instead of every job being refused
// NO_FREE_PICKER over a phantom part.
func TestRestoredHeldPartGone(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_heldgone"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.world.setHeld(0, 20, false, 0, true)
		}),
	})
	first.m.Stop()

	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.pickSettleTime.Set(10 * pollInterval.Seconds())
		}),
	})
	// One miss pre-armed via the constructor hook, so the close command the
	// restore issued during startControl is judged as "gripped nothing".
	newMachineSimOpts(second, func(s *machineSim) { s.missesLeft = 1 })

	second.eventually("phantom record cleared and picker opened", func() bool {
		return !second.bit("picker.0.close")
	})
	w := second.stopped()
	if w.held[0].present {
		t.Error("the held record survived a grip that found nothing")
	}
}

// TestPersistNamespaceCollision: the lossy sanitization can map two instance
// names onto one namespace, and persist_sqlite would silently hand both the
// same handle — a load-time error is the honest outcome.
func TestPersistNamespaceCollision(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)

	s1, err := openPersist("pnp.0", persistName, persistNamespace("pnp.0"), testLogger())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := openPersist("pnp_0", persistName, persistNamespace("pnp_0"), testLogger()); err == nil {
		t.Fatal("a colliding namespace opened without error")
	}
	s1.close()
	// Released with its owner: the name is usable again.
	s2, err := openPersist("pnp_0", persistName, persistNamespace("pnp_0"), testLogger())
	if err != nil {
		t.Fatalf("open after release: %v", err)
	}
	s2.close()
}

// TestFailedRestoreReadDoesNotOverwrite: a record that cannot be read (here:
// unparsable, the same path a transiently locked database takes) must survive
// the run untouched — the restore disarms the flush instead of letting fresh
// empty state overwrite what may be intact.
func TestFailedRestoreReadDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	persistName := newTestPersist(t, dir)
	const ns = "pnptask_badread"

	first := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
			m.pins.processStep.Set(5)
		}),
	})
	first.pulse(first.m.pins.trays[0].setFull)
	first.eventually("tray full", func() bool { return first.bit("tray.10.full") })
	first.m.Stop()

	// Corrupt the record the way a newer binary's format would look to this one.
	raw, err := openPersist("corruptor", persistName, ns, testLogger())
	if err != nil {
		t.Fatalf("openPersist: %v", err)
	}
	if _, err := raw.db.SetEntry(raw.handle, trayKey(10), "not json"); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	raw.close()

	second := newMachineFixtureOpts(t, fixtureOpts{
		prep: withPersist(persistName, ns, func(m *pnptaskModule) {
			m.pins.trays[0].trayID.Set(1)
		}),
	})
	second.consistently("nothing restored from the unreadable record", func() bool {
		return !second.bit("tray.10.full")
	})
	second.m.Stop()

	// The unreadable record is still there, byte for byte: this run did not
	// flush its fresh-empty state over it.
	check, err := openPersist("checker", persistName, ns, testLogger())
	if err != nil {
		t.Fatalf("openPersist: %v", err)
	}
	defer check.close()
	e, err := check.db.GetEntry(check.handle, trayKey(10))
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if e.Value != "not json" {
		t.Errorf("the unreadable record was overwritten: %q", e.Value)
	}
}

// TestHeldRecordCodec pins the wire format of the held-material records: they
// name their picker explicitly, so an instance reloaded with a different
// pickers= count cannot shift a record onto the wrong picker.
func TestHeldRecordCodec(t *testing.T) {
	b, err := json.Marshal(heldRecords{Pickers: []heldRecord{{Picker: 1, Station: 20}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"pickers":[{"picker":1,"station":20}]}`
	if string(b) != want {
		t.Errorf("encoded held records = %s, want %s", b, want)
	}

	// A record for a picker the instance does not have is dropped, not shifted.
	w := &world{logger: testLogger(), held: make([]heldMaterial, 1)}
	w.persist = nil
	var rec heldRecords
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range rec.Pickers {
		if r.Picker < len(w.held) {
			w.setHeld(r.Picker, r.Station, r.Swap, r.Step, r.StepKnown)
		}
	}
	if w.held[0].present {
		t.Error("a picker-1 record landed on picker 0")
	}
}
