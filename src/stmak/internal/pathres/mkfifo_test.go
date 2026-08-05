// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import "syscall"

// mkfifo creates a FIFO for the non-regular-file tests.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
