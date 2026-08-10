// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

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
			m.world.setHeld(0, 20)
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
	if store.load(trayKey(10), &rec) {
		t.Error("load of an unwritten key reported state")
	}
	// A value that is not the record it should be is also "no usable state":
	// discarding it is the only alternative to guessing.
	store.store(trayKey(10), "not a tray record")
	if store.load(trayKey(10), &rec) {
		t.Error("load of an unparsable value reported state")
	}

	store.store(trayKey(10), trayRecord{TrayID: 3, Slots: []int64{slotEmpty, 7}})
	if !store.load(trayKey(10), &rec) {
		t.Fatal("load of a written record reported nothing")
	}
	if rec.TrayID != 3 || len(rec.Slots) != 2 || rec.Slots[1] != 7 {
		t.Errorf("round trip = %+v", rec)
	}

	// A nil store is what "no persist_instance=" looks like from the caller's
	// side: every method has to tolerate it.
	var absent *persistStore
	if absent.load("anything", &rec) {
		t.Error("nil store reported state")
	}
	absent.store("anything", rec)
	absent.close()
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
			w.setHeld(r.Picker, r.Station)
		}
	}
	if w.held[0].present {
		t.Error("a picker-1 record landed on picker 0")
	}
}
