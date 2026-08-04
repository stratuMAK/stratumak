// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func tmpPidFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stmakd.pid")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing pid file: %v", err)
		}
	}
	return path
}

// deadPID returns a PID that is (almost certainly) not in use: fork a trivial
// child, reap it, and reuse its number. Recycling would take the whole PID
// space, and nothing in this test allocates processes in between.
func deadPID(t *testing.T) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/true", []string{"/bin/true"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot spawn a throwaway process: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Fatalf("waiting for throwaway process: %v", err)
	}
	return proc.Pid
}

func TestReadPidFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{"plain", "1234\n", 1234, false},
		{"no newline", "1234", 1234, false},
		{"surrounding whitespace", "  1234  \n", 1234, false},
		{"empty", "", 0, true},
		{"not a number", "not-a-pid\n", 0, true},
		{"zero", "0\n", 0, true},
		{"negative", "-1\n", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// An empty content string means "no file at all" in tmpPidFile, which
			// is also a read error — exactly what we want for the empty case.
			path := tmpPidFile(t, tc.content)
			got, err := readPidFile(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readPidFile(%q) = %d, want error", tc.content, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readPidFile(%q): %v", tc.content, err)
			}
			if got != tc.want {
				t.Errorf("readPidFile(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

func TestReadPidFile_Missing(t *testing.T) {
	if _, err := readPidFile(filepath.Join(t.TempDir(), "absent.pid")); err == nil {
		t.Fatal("readPidFile on a missing file returned no error")
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false, want true")
	}
	if pid := deadPID(t); processAlive(pid) {
		t.Errorf("processAlive(%d) = true for a reaped process, want false", pid)
	}
	// PID 1 exists and is owned by root: the EPERM arm must still report alive,
	// otherwise a root-owned daemon looks dead to an unprivileged caller.
	if !processAlive(1) {
		t.Error("processAlive(1) = false, want true (EPERM still means alive)")
	}
}

// TestPidFileRunning_StaleIsOverwritable pins the staleness rule Daemonize
// depends on: only a PID file naming a LIVE process blocks a start.
func TestPidFileRunning_StaleIsOverwritable(t *testing.T) {
	live := tmpPidFile(t, strconv.Itoa(os.Getpid())+"\n")
	if pid, running := pidFileRunning(live); !running || pid != os.Getpid() {
		t.Errorf("pidFileRunning(live) = (%d, %v), want (%d, true)", pid, running, os.Getpid())
	}

	dead := tmpPidFile(t, strconv.Itoa(deadPID(t))+"\n")
	if _, running := pidFileRunning(dead); running {
		t.Error("pidFileRunning(dead pid) = true, want false (stale files are overwritable)")
	}

	garbage := tmpPidFile(t, "not-a-pid\n")
	if _, running := pidFileRunning(garbage); running {
		t.Error("pidFileRunning(malformed) = true, want false")
	}

	if _, running := pidFileRunning(filepath.Join(t.TempDir(), "absent.pid")); running {
		t.Error("pidFileRunning(missing) = true, want false")
	}
}

// TestDaemonize_RefusesWhenAlreadyRunning covers the parent-side guard: with a
// PID file naming a live process, Daemonize must fail instead of forking a
// second server and overwriting the record of the first (which would orphan it
// — nothing could stop it afterwards). It is safe to call here because the
// guard runs before any fork.
func TestDaemonize_RefusesWhenAlreadyRunning(t *testing.T) {
	path := tmpPidFile(t, strconv.Itoa(os.Getpid())+"\n")

	err := Daemonize(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Daemonize with a live pid file: got %v, want ErrAlreadyRunning", err)
	}

	// The existing file must be untouched.
	pid, readErr := readPidFile(path)
	if readErr != nil || pid != os.Getpid() {
		t.Errorf("pid file after refusal = (%d, %v), want (%d, nil)", pid, readErr, os.Getpid())
	}
}

func TestDaemonize_ChildReturnsWithoutRewriting(t *testing.T) {
	t.Setenv(envSentinel, "1")
	path := tmpPidFile(t, "")

	if err := Daemonize(path); err != nil {
		t.Fatalf("Daemonize in the child: %v", err)
	}
	if IsChild() {
		t.Error("sentinel still set after Daemonize; the child must clear it")
	}
	// The parent owns the PID file; the child must not create a second writer.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("child wrote the pid file (stat err = %v), want it left to the parent", err)
	}
}

func TestRemovePidFile(t *testing.T) {
	t.Run("removes own", func(t *testing.T) {
		path := tmpPidFile(t, strconv.Itoa(os.Getpid())+"\n")
		RemovePidFile(path)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("pid file survived removal (stat err = %v)", err)
		}
	})

	t.Run("removes stale", func(t *testing.T) {
		path := tmpPidFile(t, strconv.Itoa(deadPID(t))+"\n")
		RemovePidFile(path)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("stale pid file survived removal (stat err = %v)", err)
		}
	})

	t.Run("keeps another live instance", func(t *testing.T) {
		// PID 1 stands in for "a different, still-running server": a crashed
		// instance's late cleanup must not delete the replacement's pid file.
		path := tmpPidFile(t, "1\n")
		RemovePidFile(path)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("pid file of a live foreign process was removed: %v", err)
		}
	})

	t.Run("missing file is a no-op", func(t *testing.T) {
		RemovePidFile(filepath.Join(t.TempDir(), "absent.pid"))
	})
}

func TestWritePidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.pid")
	if err := writePidFile(path, 4242); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(data) != "4242\n" {
		t.Errorf("pid file content = %q, want %q", data, "4242\n")
	}
	if got, err := readPidFile(path); err != nil || got != 4242 {
		t.Errorf("round-trip = (%d, %v), want (4242, nil)", got, err)
	}
}
