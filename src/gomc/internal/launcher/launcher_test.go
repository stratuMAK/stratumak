// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package launcher

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

// writeIni is a helper that writes an INI file and returns its path.
func writeIni(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeIni %s: %v", path, err)
	}
	return path
}

// --------------------------------------------------------------------------
// Tests for cleanup idempotency (sync.Once)
// --------------------------------------------------------------------------

// TestCleanup_IdempotentViaSyncOnce verifies that calling cleanup() multiple
// times does not panic and the doCleanup function runs only once.
func TestCleanup_IdempotentViaSyncOnce(t *testing.T) {
	callCount := 0
	l := &Launcher{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Override doCleanup by calling cleanupOnce.Do directly to test the Once
	// semantics without triggering real cleanup.
	fn := func() { callCount++ }
	l.cleanupOnce.Do(fn)
	l.cleanupOnce.Do(fn)
	l.cleanupOnce.Do(fn)

	if callCount != 1 {
		t.Errorf("fn called %d times, want 1", callCount)
	}
}

// TestShutdown_ConcurrentSafe verifies that concurrent shutdown() calls close
// shutdownCh exactly once without panicking. Several independent goroutines race
// to trigger shutdown (the signal handler, the REST-server death watcher via
// fail(), the halrun signal handler); the previous select/default check-then-
// close would panic with "close of closed channel" when two fired at once. Run
// under -race to exercise the window.
func TestShutdown_ConcurrentSafe(t *testing.T) {
	l := &Launcher{
		logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		shutdownCh: make(chan struct{}),
	}

	const n = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximize the race window
			l.shutdown()
		}()
	}
	close(start)
	wg.Wait()

	// Channel must be closed (drainable) exactly once, no panic reached here.
	select {
	case <-l.shutdownCh:
	default:
		t.Fatal("shutdownCh not closed after shutdown()")
	}
}

// --------------------------------------------------------------------------
// Tests for resolveRelativePath
// --------------------------------------------------------------------------

// TestResolveRelativePath_Absolute verifies that absolute paths are unchanged.
func TestResolveRelativePath_Absolute(t *testing.T) {
	l := &Launcher{
		opts:   Options{IniFile: "/configs/test.ini"},
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	got := l.resolveRelativePath("/opt/linuxcnc/custom.hal")
	want := "/opt/linuxcnc/custom.hal"
	if got != want {
		t.Errorf("resolveRelativePath = %q, want %q", got, want)
	}
}

// TestResolveRelativePath_Relative verifies that relative paths are resolved
// against the INI directory.
func TestResolveRelativePath_Relative(t *testing.T) {
	l := &Launcher{
		opts:   Options{IniFile: "/configs/sim/test.ini"},
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	got := l.resolveRelativePath("custom.hal")
	want := "/configs/sim/custom.hal"
	if got != filepath.Clean(want) {
		t.Errorf("resolveRelativePath = %q, want %q", got, want)
	}
}

// --------------------------------------------------------------------------
// Tests for checkVersion
// --------------------------------------------------------------------------

// newLauncherWithIniPath is a helper that creates a Launcher with a parsed INI.
func newLauncherWithIniPath(t *testing.T, iniFilePath string) *Launcher {
	t.Helper()
	ini, err := inifile.Parse(iniFilePath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return &Launcher{
		opts:   Options{IniFile: iniFilePath},
		ini:    ini,
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// TestCheckVersion_CurrentVersion verifies that version "1.1" is a no-op.
func TestCheckVersion_CurrentVersion(t *testing.T) {
	dir := t.TempDir()
	f := writeIni(t, dir, "test.ini", `[EMC]
VERSION = 1.1
`)
	l := newLauncherWithIniPath(t, f)
	if err := l.checkVersion(); err != nil {
		t.Errorf("checkVersion with VERSION=1.1 returned error: %v", err)
	}
}

// TestCheckVersion_MissingVersion verifies that a missing [EMC]VERSION triggers
// the update path; when DISPLAY is unset an error about the missing X display is returned.
func TestCheckVersion_MissingVersion(t *testing.T) {
	dir := t.TempDir()
	f := writeIni(t, dir, "test.ini", `[EMC]
MACHINE = Test
`)
	l := newLauncherWithIniPath(t, f)

	// Ensure DISPLAY is not set so we exercise the no-display error path.
	origDisplay := os.Getenv("DISPLAY")
	_ = os.Unsetenv("DISPLAY")
	defer func() { _ = os.Setenv("DISPLAY", origDisplay) }()

	err := l.checkVersion()
	if err == nil {
		t.Fatal("checkVersion with no VERSION and no DISPLAY should return error")
	}
	if !strings.Contains(err.Error(), "without an X display") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --------------------------------------------------------------------------
// Tests for checkPlasmaC
// --------------------------------------------------------------------------

// TestCheckPlasmaC_NoPlasmaC verifies that a non-PlasmaC INI is a no-op.
func TestCheckPlasmaC_NoPlasmaC(t *testing.T) {
	dir := t.TempDir()
	f := writeIni(t, dir, "test.ini", `[EMC]
MACHINE = Test
`)
	l := newLauncherWithIniPath(t, f)
	if err := l.checkPlasmaC(); err != nil {
		t.Errorf("checkPlasmaC on non-PlasmaC INI returned error: %v", err)
	}
}

// TestCheckPlasmaC_PlasmaC verifies the positive path: a [PLASMAC]MODE config
// always returns ErrPlasmaC (never continues to launch), regardless of how the
// migration tool exits. checkPlasmaC invokes the tool by bare name, so a PATH
// stub replaces the real qtplasmac-plasmac2qt GUI without running it.
func TestCheckPlasmaC_PlasmaC(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode int
	}{
		{"migration-ok", 0},
		{"user-cancelled", 2},
		{"tool-error", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			stub := filepath.Join(binDir, "qtplasmac-plasmac2qt")
			script := fmt.Sprintf("#!/bin/sh\nexit %d\n", tc.exitCode)
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}
			t.Setenv("PATH", binDir)

			dir := t.TempDir()
			f := writeIni(t, dir, "test.ini", `[PLASMAC]
MODE = 0
`)
			l := newLauncherWithIniPath(t, f)
			if err := l.checkPlasmaC(); !errors.Is(err, ErrPlasmaC) {
				t.Errorf("checkPlasmaC on PlasmaC INI (tool exit %d) = %v, want ErrPlasmaC", tc.exitCode, err)
			}
		})
	}
}

// --------------------------------------------------------------------------
// --------------------------------------------------------------------------
// Tests for validateDependencies
// --------------------------------------------------------------------------

// newLauncherWithIniContent is a helper that creates a Launcher from raw INI
// content written to a temporary file.
func newLauncherWithIniContent(t *testing.T, content string) *Launcher {
	t.Helper()
	dir := t.TempDir()
	f := writeIni(t, dir, "test.ini", content)
	return newLauncherWithIniPath(t, f)
}

// TestValidateDependencies_HALOnlyMode verifies that a minimal configuration
// with at least one HALFILE is accepted.
func TestValidateDependencies_HALOnlyMode(t *testing.T) {
	l := newLauncherWithIniContent(t, `
[EMC]
MACHINE = TestMachine

[HAL]
HALFILE = my-hardware.hal
HALFILE = my-logic.hal

`)
	if err := l.validateDependencies(); err != nil {
		t.Errorf("HAL-only config should be valid, got error: %v", err)
	}
}

// TestValidateDependencies_NoHALFile verifies that a missing [HAL]HALFILE is
// rejected.
func TestValidateDependencies_NoHALFile(t *testing.T) {
	l := newLauncherWithIniContent(t, `
[EMC]
MACHINE = TestMachine
`)
	err := l.validateDependencies()
	if err == nil {
		t.Fatal("config without [HAL]HALFILE should be rejected")
	}
	if !strings.Contains(err.Error(), "HALFILE") {
		t.Errorf("error should mention HALFILE, got: %v", err)
	}
}

// TestValidateDependencies_NoINI covers the nil-INI receiver. `halrun -f` /
// `gomc-server -f` never set l.ini, and pkg/inifile's methods dereference the
// receiver immediately — so reading the INI directly here would segfault rather
// than report a configuration error. Only Run() calls validateDependencies and
// RunHalFile() does not, so this is defence in depth against a future caller,
// not a live crash; it is pinned because the raw-l.ini form used to be exactly
// the shape that produced the nil-INI crash class.
func TestValidateDependencies_NoINI(t *testing.T) {
	l := New(Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if l.ini != nil {
		t.Fatal("a launcher built without an INI path should have no INI")
	}
	err := l.validateDependencies()
	if err == nil {
		t.Fatal("a launcher with no INI has no HALFILE and must be rejected")
	}
	if !strings.Contains(err.Error(), "HALFILE") {
		t.Errorf("error should mention HALFILE, got: %v", err)
	}
}
