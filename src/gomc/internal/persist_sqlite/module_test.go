// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package persist_sqlite

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// newTestModule builds a module rooted in a temp directory.
func newTestModule(t *testing.T) *module {
	t.Helper()
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	root := t.TempDir()
	pathres.SetDefaultForTest(t, root)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mod, err := newPersistSQLite(nil, logger, t.Name(), []string{"dbpath=" + filepath.Join(root, "db")})
	if err != nil {
		t.Fatalf("newPersistSQLite: %v", err)
	}
	t.Cleanup(mod.Destroy)
	return mod.(*module)
}

func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"tooltable", true},
		{"mill_1", true},
		{"ABC123", true},
		{"", false},
		{"has space", false},
		{"has/slash", false},   // would escape dbDir via filepath.Join
		{"..", false},          // ditto
		{"dot.name", false},    // would collide with the .db suffix
		{"nul\x00byte", false}, // would truncate the filename in the syscall
		{strings.Repeat("a", maxNameLen), true},
		{strings.Repeat("a", maxNameLen+1), false},
	} {
		if got := validName(tc.name); got != tc.want {
			t.Errorf("validName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestOpenRejectsInvalidNamespace pins that a bad name never reaches the
// filesystem. open is REST-reachable (POST /{namespace}), and the namespace is
// filepath.Join'd into the db directory, so this is the containment boundary.
func TestOpenRejectsInvalidNamespace(t *testing.T) {
	m := newTestModule(t)
	for _, ns := range []string{"", "..", "../escape", "a/b", strings.Repeat("x", maxNameLen+1)} {
		if _, err := m.Open(ns); err == nil {
			t.Errorf("Open(%q) succeeded, want an error", ns)
		}
	}
	entries, err := os.ReadDir(m.dbDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected namespaces still created %d file(s) in the db dir", len(entries))
	}
}

func TestOpenIsIdempotentPerNamespace(t *testing.T) {
	m := newTestModule(t)
	first, err := m.Open("tooltable")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	second, err := m.Open("tooltable")
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if first.Handle != second.Handle {
		t.Errorf("re-Open of the same namespace gave handle %d, want the shared %d",
			second.Handle, first.Handle)
	}
	if got := len(m.handles); got != 1 {
		t.Errorf("re-Open allocated a second slot (%d handles)", got)
	}
}

// TestOpenNamespaceLimit pins the ceiling on live namespaces. Without it, a
// caller on the REST surface can walk the name space and exhaust file
// descriptors and disk: every distinct name creates a .db plus -wal/-shm, an
// *sql.DB with its own pool, and a permanent handle slot.
func TestOpenNamespaceLimit(t *testing.T) {
	m := newTestModule(t)
	for i := 0; i < maxNamespaces; i++ {
		if _, err := m.Open(fmt.Sprintf("ns_%d", i)); err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
	}
	if _, err := m.Open("one_too_many"); err == nil {
		t.Fatalf("Open past the %d-namespace limit succeeded", maxNamespaces)
	}
	// An already-open namespace must still resolve — the cap bounds growth, it
	// does not lock out existing consumers.
	if _, err := m.Open("ns_0"); err != nil {
		t.Errorf("re-Open of a live namespace at the limit: %v", err)
	}
}

// TestDeleteAllFreesTheSlot pins the slot reuse. delete_all and open are both
// REST-reachable, and DeleteAll used to nil the db but leave the slot behind,
// so cycling the pair grew m.handles without bound while the number of live
// namespaces stayed at one.
func TestDeleteAllFreesTheSlot(t *testing.T) {
	m := newTestModule(t)
	for i := 0; i < 5; i++ {
		res, err := m.Open("cycle")
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if _, err := m.DeleteAll(res.Handle); err != nil {
			t.Fatalf("DeleteAll #%d: %v", i, err)
		}
	}
	if got := len(m.handles); got != 1 {
		t.Errorf("open/delete_all cycled 5 times left %d handle slots, want 1", got)
	}
}

func TestHandleValidation(t *testing.T) {
	m := newTestModule(t)
	res, err := m.Open("ns")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := m.DeleteAll(res.Handle); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	// Out of range, negative, and a slot whose db was closed by DeleteAll.
	for _, h := range []int32{-1, 99, res.Handle} {
		if _, err := m.GetEntries(h); err == nil {
			t.Errorf("GetEntries(%d) succeeded, want an invalid-handle error", h)
		}
		if _, err := m.GetEntry(h, "k"); err == nil {
			t.Errorf("GetEntry(%d) succeeded, want an invalid-handle error", h)
		}
		if _, err := m.SetEntry(h, "k", "v"); err == nil {
			t.Errorf("SetEntry(%d) succeeded, want an invalid-handle error", h)
		}
		if _, err := m.DeleteEntry(h, "k"); err == nil {
			t.Errorf("DeleteEntry(%d) succeeded, want an invalid-handle error", h)
		}
	}
}

func TestEntryRoundTrip(t *testing.T) {
	m := newTestModule(t)
	res, err := m.Open("ns")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := res.Handle

	if _, err := m.SetEntry(h, "a", "1"); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	// Upsert, not a duplicate-key error.
	if _, err := m.SetEntry(h, "a", "2"); err != nil {
		t.Fatalf("SetEntry (update): %v", err)
	}
	got, err := m.GetEntry(h, "a")
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if got.Value != "2" {
		t.Errorf("value = %q, want the updated %q", got.Value, "2")
	}
	if got.Updated == 0 {
		t.Error("updated timestamp not set")
	}

	// A missing key is an error, not a zero entry — the consumer must be able
	// to tell "absent" from "present and empty".
	if _, err := m.GetEntry(h, "nope"); err == nil {
		t.Error("GetEntry of a missing key succeeded, want an error")
	}

	del, err := m.DeleteEntry(h, "a")
	if err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if !del.Ok || del.Count != 1 {
		t.Errorf("DeleteEntry = {ok:%v count:%d}, want {true 1}", del.Ok, del.Count)
	}
	// Deleting again reports no rows rather than failing.
	del, err = m.DeleteEntry(h, "a")
	if err != nil {
		t.Fatalf("second DeleteEntry: %v", err)
	}
	if del.Ok || del.Count != 0 {
		t.Errorf("re-DeleteEntry = {ok:%v count:%d}, want {false 0}", del.Ok, del.Count)
	}
}

func TestSetEntriesIsAtomic(t *testing.T) {
	m := newTestModule(t)
	res, err := m.Open("ns")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := res.Handle

	if _, err := m.SetEntries(h, []persist.Entry{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2", Updated: 42},
	}); err != nil {
		t.Fatalf("SetEntries: %v", err)
	}
	entries, err := m.GetEntries(h)
	if err != nil {
		t.Fatalf("GetEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Ordered by key, and an explicit Updated is preserved while a zero one is
	// stamped with now.
	if entries[0].Key != "a" || entries[1].Key != "b" {
		t.Errorf("entries not ordered by key: %v", entries)
	}
	if entries[0].Updated == 0 {
		t.Error("a zero Updated was not stamped")
	}
	if entries[1].Updated != 42 {
		t.Errorf("explicit Updated = %d, want 42", entries[1].Updated)
	}
}

// TestGetNamespacesIncludesUnopened pins that the listing merges the open
// handles with a scan of the db directory, so a namespace persisted by an
// earlier run is discoverable before anyone opens it.
func TestGetNamespacesIncludesUnopened(t *testing.T) {
	m := newTestModule(t)
	if _, err := m.Open("live"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Drop a file in as if a previous run had left it.
	if err := os.WriteFile(filepath.Join(m.dbDir, "stale.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// ...and one that must be ignored: not a .db, and an unsafe stem.
	if err := os.WriteFile(filepath.Join(m.dbDir, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dbDir, "bad name.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := m.GetNamespaces()
	if err != nil {
		t.Fatalf("GetNamespaces: %v", err)
	}
	want := map[string]bool{"live": true, "stale": true}
	if len(got) != len(want) {
		t.Fatalf("GetNamespaces = %v, want exactly %v", got, want)
	}
	for _, ns := range got {
		if !want[ns] {
			t.Errorf("GetNamespaces returned unexpected %q", ns)
		}
	}
}

// TestDeleteAllRemovesSidecars pins that the -wal/-shm files go too; leaving
// them behind resurrects committed data the next time the namespace is opened.
func TestDeleteAllRemovesSidecars(t *testing.T) {
	m := newTestModule(t)
	res, err := m.Open("ns")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := m.SetEntry(res.Handle, "k", "v"); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	if _, err := m.DeleteAll(res.Handle); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := filepath.Join(m.dbDir, "ns.db"+suffix)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after DeleteAll", filepath.Base(p))
		}
	}
	// Re-opening must give a fresh, empty namespace.
	res, err = m.Open("ns")
	if err != nil {
		t.Fatalf("re-Open after DeleteAll: %v", err)
	}
	entries, err := m.GetEntries(res.Handle)
	if err != nil {
		t.Fatalf("GetEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("re-opened namespace holds %d entries, want 0", len(entries))
	}
}

// TestDestroyIsIdempotent — the launcher's lifecycle contract allows Destroy
// after a Stop that never followed a Start, and doCleanup has re-run teardown
// steps before (launcher L-7).
func TestDestroyIsIdempotent(t *testing.T) {
	m := newTestModule(t)
	if _, err := m.Open("ns"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	m.Destroy()
	m.Destroy() // must not double-close
}
