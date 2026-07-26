// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package realtime manages the LinuxCNC realtime environment (uspace only).
//
// This package corresponds to the uspace path of the legacy scripts/realtime.in
// bash script.  Kernel-module paths (RTAI, Xenomai) are intentionally not
// implemented here.
//
// In the gomc architecture there is nothing to bring up at this layer: RT
// modules load in-process via dlopen in the halcmd CGo shims, and HAL/RTAPI
// shared memory is in-process heap allocation: there is no SysV-shm or
// /dev/zero mmap to wait for, and 2.9's realtime.in ipcrm cleanup has no
// counterpart here.
// Start() is retained as the startup seam and error-return contract for the
// launcher; RT-correctness of the cyclic paths lives in cmod/* and is tracked
// in RT_HARDENING_CHECKLIST.md, not here.
package realtime

import (
	"log/slog"
	"os"
)

// Manager manages the LinuxCNC uspace realtime environment.
type Manager struct {
	logger *slog.Logger
}

// New returns a new Manager.  If logger is nil a default structured logger
// writing to stderr is used.
func New(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Manager{
		logger: logger,
	}
}

// Start marks the uspace realtime environment ready.
//
// There is no uspace precondition to validate here (RT modules load via dlopen,
// HAL shm is in-process heap).  The error return is kept so the launcher's
// startup contract does not change if a real precondition is added later.
func (m *Manager) Start() error {
	m.logger.Info("realtime environment ready (uspace)")
	return nil
}
