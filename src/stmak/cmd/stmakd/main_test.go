// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runArgs calls run() with stderr captured, so a failing case reports what the
// user would actually have seen. Only argument-handling paths are exercised —
// every case here returns before launcher.Run(), so no HAL/RT environment is
// touched (RtapiInitializeApp lives in main(), not in an init()).
func runArgs(t *testing.T, args ...string) (code int, stderr string) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()

	code = run(args)
	_ = w.Close()
	stderr = <-done
	_ = r.Close()
	return code, stderr
}

func TestRun_Help(t *testing.T) {
	code, out := runArgs(t, "-h")
	if code != 0 {
		t.Errorf("run(-h) = %d, want 0", code)
	}
	if !strings.Contains(out, "stmakd: Run LinuxCNC") {
		t.Errorf("help output missing the usage banner:\n%s", out)
	}
	// Every documented flag must appear in the generated defaults block —
	// a flag that exists but is undiscoverable is a support burden.
	for _, f := range []string{"-D", "-p", "-d", "-r", "-l", "-k", "-t", "-m", "-f", "-serve", "-H"} {
		if !strings.Contains(out, "\n  "+f) {
			t.Errorf("help output does not document %s:\n%s", f, out)
		}
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	code, out := runArgs(t, "-nosuchflag")
	if code != 2 {
		t.Errorf("run(-nosuchflag) = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(out, "flag provided but not defined") {
		t.Errorf("stderr does not name the bad flag:\n%s", out)
	}
}

func TestRun_NoIniFile(t *testing.T) {
	code, out := runArgs(t)
	if code != 1 {
		t.Errorf("run() with no arguments = %d, want 1", code)
	}
	if !strings.Contains(out, "no INI file specified") {
		t.Errorf("stderr = %q, want the missing-INI diagnostic", out)
	}
}

// TestRun_LastUsedIniNotImplemented pins the -l behaviour: it is advertised in
// the usage text but not implemented, so it must fail loudly rather than fall
// through into a launch with an empty INI path.
func TestRun_LastUsedIniNotImplemented(t *testing.T) {
	for _, args := range [][]string{{"-l"}, {"-"}} {
		code, out := runArgs(t, args...)
		if code != 1 {
			t.Errorf("run(%v) = %d, want 1", args, code)
		}
		if !strings.Contains(out, "not yet implemented") {
			t.Errorf("run(%v) stderr = %q, want the not-implemented diagnostic", args, out)
		}
	}
}

func TestRun_InvalidHalLibDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	for name, path := range map[string]string{
		"missing directory": filepath.Join(dir, "nope"),
		"regular file":      file,
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runArgs(t, "-H", path, "some.ini")
			if code != 1 {
				t.Errorf("run(-H %s) = %d, want 1", path, code)
			}
			if !strings.Contains(out, "invalid directory specified with -H") {
				t.Errorf("stderr = %q, want the -H diagnostic", out)
			}
		})
	}
}

func TestRun_DebugLevelOutOfRange(t *testing.T) {
	for _, lvl := range []string{"-1", "4", "99"} {
		code, out := runArgs(t, "-d", lvl, "some.ini")
		if code != 1 {
			t.Errorf("run(-d %s) = %d, want 1", lvl, code)
		}
		if out == "" {
			t.Errorf("run(-d %s) rejected the level without a diagnostic", lvl)
		}
	}
}

// TestMultiFlag covers the -H accumulator: repeated flags append in order and
// String() renders them as a PATH-style list (it is what flag prints in usage
// and error messages).
func TestMultiFlag(t *testing.T) {
	var m multiFlag
	if got := m.String(); got != "" {
		t.Errorf("empty multiFlag.String() = %q, want \"\"", got)
	}
	for _, v := range []string{"/a", "/b", "/c"} {
		if err := m.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if got, want := m.String(), "/a:/b:/c"; got != want {
		t.Errorf("multiFlag.String() = %q, want %q", got, want)
	}
	if len(m) != 3 {
		t.Errorf("len(multiFlag) = %d, want 3", len(m))
	}

	var nilFlag *multiFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nil multiFlag.String() = %q, want \"\"", got)
	}
}
