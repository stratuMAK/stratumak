// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package emccalib

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/calibreg"
)

// TestNewEmccalib_NilIni pins the nil-INI guard.  The launcher passes
// ini == nil when it runs without an INI file (halrun mode), and pkg/inifile's
// methods dereference the receiver immediately, so the provenance lookup over
// ini.Sections killed the process.  The tunables themselves come from
// calibreg, so they must still be built — only their source file/line is
// unavailable.  A registered mapping is required for the loop to run at all.
func TestNewEmccalib_NilIni(t *testing.T) {
	calibreg.Reset()
	t.Cleanup(calibreg.Reset)
	calibreg.Record(calibreg.IniPinMapping{
		Section:  "JOINT_0",
		Key:      "MAX_VELOCITY",
		Pin:      "joint.0.max-vel",
		IniValue: 12.5,
	})

	if apiserver.DefaultRegistry() == nil {
		apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mod, err := newEmccalib(nil, logger, uniq("emccalib-nilini-test"), nil)
	if err != nil {
		t.Fatalf("newEmccalib with a nil INI: %v", err)
	}
	e, ok := mod.(*emccalib)
	if !ok {
		t.Fatalf("newEmccalib returned %T, want *emccalib", mod)
	}
	if len(e.index) != 1 {
		t.Fatalf("built %d tunables, want 1", len(e.index))
	}
}
