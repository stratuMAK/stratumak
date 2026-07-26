// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package persist_sqlite

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// TestNewPersistSQLite_NilIni pins the nil-INI guard.  The launcher passes
// ini == nil when it runs without an INI file (halrun mode), and pkg/inifile's
// methods dereference the receiver immediately, so filepath.Dir(ini.
// SourceFile()) killed the process before the db directory was even chosen.
// An explicit absolute dbpath must be honoured unchanged.
func TestNewPersistSQLite_NilIni(t *testing.T) {
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	root := t.TempDir()
	pathres.SetDefaultForTest(t, root)
	dir := filepath.Join(root, "db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mod, err := newPersistSQLite(nil, logger, "persist-nilini-test", []string{"dbpath=" + dir})
	if err != nil {
		t.Fatalf("newPersistSQLite with a nil INI: %v", err)
	}
	m, ok := mod.(*module)
	if !ok {
		t.Fatalf("newPersistSQLite returned %T, want *module", mod)
	}
	if m.dbDir != dir {
		t.Errorf("dbDir = %q, want %q", m.dbDir, dir)
	}
	mod.Destroy()
}

// TestNewPersistSQLite_NilIniRelativeDbpath covers the other branch: with no
// INI to anchor it, a relative dbpath resolves against the working directory.
func TestNewPersistSQLite_NilIniRelativeDbpath(t *testing.T) {
	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	root := t.TempDir()
	t.Chdir(root)
	pathres.SetDefaultForTest(t, root)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mod, err := newPersistSQLite(nil, logger, "persist-nilini-rel-test", []string{"dbpath=state"})
	if err != nil {
		t.Fatalf("newPersistSQLite with a nil INI: %v", err)
	}
	m := mod.(*module)
	if m.dbDir != filepath.Join(root, "state") {
		t.Errorf("dbDir = %q, want %q", m.dbDir, filepath.Join(root, "state"))
	}
	mod.Destroy()
}
