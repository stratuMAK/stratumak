// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

//go:build !gcov

package main

// gcovDump is a no-op in every build that is not instrumented for coverage.
// See gcov_dump.go for what the instrumented one does and why it is needed.
func gcovDump() {}
