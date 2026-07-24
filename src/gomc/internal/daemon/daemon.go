// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package daemon provides daemonization, PID file management, and syslog logging.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// DefaultPidFile is the default path for the PID file when -daemon is used
// without specifying a path.
const DefaultPidFile = "/var/run/gomc-server.pid"

// envSentinel is the environment variable used to detect the re-execed child.
const envSentinel = "_GOMC_DAEMONIZED"

// ErrAlreadyRunning is returned by Daemonize when the PID file names a process
// that is still alive.
var ErrAlreadyRunning = errors.New("daemon: already running")

// IsChild returns true if this process is the daemonized child (re-execed).
func IsChild() bool {
	return os.Getenv(envSentinel) == "1"
}

// Daemonize re-execs the current process in the background and writes the
// child PID to the given pidFile. The parent process exits after the child
// has started. This function only returns in the child process.
//
// The PID file is written by the PARENT only, so that it is guaranteed to
// exist by the time the parent exits (a supervisor that reads it immediately
// after the fork must not lose the race). The child does not rewrite it — the
// value would be identical, and a second writer opens a window where a child
// that fails and removes the file has it recreated behind its back.
//
// If the PID file already names a live process, Daemonize refuses to start and
// returns ErrAlreadyRunning instead of silently overwriting it: overwriting
// orphans the running instance (the recorded PID no longer refers to it, so
// nothing can stop it) while two servers fight over the same HAL shm and REST
// port. A stale PID file — no such process, or a PID recycled by an unrelated
// program — is overwritten as before.
func Daemonize(pidFile string) error {
	if IsChild() {
		// We are the child — remove the sentinel and continue. The parent
		// already wrote the PID file.
		_ = os.Unsetenv(envSentinel)
		return nil
	}

	if pid, running := pidFileRunning(pidFile); running {
		return fmt.Errorf("%w: %s names live process %d", ErrAlreadyRunning, pidFile, pid)
	}

	// Parent: re-exec ourselves with the sentinel set.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: resolving executable: %w", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), envSentinel+"=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: starting child: %w", err)
	}

	// Write the child's PID to the pidfile from the parent, then exit.
	pid := cmd.Process.Pid
	if err := writePidFile(pidFile, pid); err != nil {
		// Best effort — child is already running.
		fmt.Fprintf(os.Stderr, "daemon: writing pid file: %v\n", err)
	}

	os.Exit(0)
	return nil // unreachable
}

// writePidFile writes pid to path.
func writePidFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// readPidFile returns the PID recorded in path. It reports an error when the
// file is missing, empty, or does not contain a positive decimal integer.
func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("daemon: %s: malformed pid: %w", path, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("daemon: %s: invalid pid %d", path, pid)
	}
	return pid, nil
}

// pidFileRunning reports whether path names a process that is currently alive.
// A missing or malformed file, or a dead PID, reports false — that file is
// stale and may be overwritten.
func pidFileRunning(path string) (int, bool) {
	pid, err := readPidFile(path)
	if err != nil {
		return 0, false
	}
	return pid, processAlive(pid)
}

// processAlive reports whether pid exists. Signal 0 performs the permission and
// existence checks without delivering anything; EPERM means the process exists
// but is owned by someone else, which still counts as alive.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid) // never fails on Unix
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// RemovePidFile removes the PID file on shutdown. It removes the file only if
// it still records THIS process (or is unreadable/stale): after a crash, a
// replacement instance may already own the path, and deleting its PID file
// would leave the live server unsupervised.
func RemovePidFile(path string) {
	if pid, err := readPidFile(path); err == nil && pid != os.Getpid() && processAlive(pid) {
		return
	}
	_ = os.Remove(path)
}

// RedirectStdio redirects stdin, stdout, stderr to /dev/null (for daemon mode).
func RedirectStdio() error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	for _, fd := range []int{int(os.Stdin.Fd()), int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		// Dup3, not Dup2: arm64 has no dup2 syscall, so Go defines only Dup3
		// there (dup3 covers every architecture). The one behavioral gap is
		// oldfd == newfd — a no-op for dup2 but EINVAL for dup3 — which is
		// reachable here: started with stdin closed, OpenFile hands /dev/null
		// fd 0, and the fd already IS the redirection.
		if int(devNull.Fd()) == fd {
			continue
		}
		if err := syscall.Dup3(int(devNull.Fd()), fd, 0); err != nil {
			_ = devNull.Close()
			return fmt.Errorf("daemon: redirecting fd %d to /dev/null: %w", fd, err)
		}
	}
	_ = devNull.Close()
	return nil
}
