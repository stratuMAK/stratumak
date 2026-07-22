// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestFactory_NilIni pins the nil-INI guard.  The launcher passes ini == nil
// when it runs without an INI file (halrun mode); milltask is INI-driven
// throughout, and pkg/inifile's methods dereference the receiver immediately,
// so ini.WithNamespace(name) killed the process.  It must be a clean load
// error instead.
func TestFactory_NilIni(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mod, err := factory(nil, logger, "milltask", nil)
	if err == nil {
		t.Fatal("factory with a nil INI: want an error, got nil")
	}
	if mod != nil {
		t.Errorf("factory returned a module (%T) alongside the error", mod)
	}
	if !strings.Contains(err.Error(), "INI") {
		t.Errorf("error %q does not explain the missing INI", err)
	}
}
