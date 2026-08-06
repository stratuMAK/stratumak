// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

//go:build gcov

// Flush gcov counters for the C compiled into this binary.
//
// Without this, a COVERAGE=1 build reports nothing for the C half. libgcov
// writes its .gcda files from an atexit handler, and the Go runtime does not
// run C atexit handlers: it terminates with the exit_group syscall directly.
// So every counter for the C linked in through cgo -- HAL, RTAPI, the motion
// controller, the interpreter, posemath -- is discarded at exit, silently and
// with a full set of .gcno files on disk to suggest otherwise.
//
// The cmod components are unaffected and were never the problem: stmakd
// dlclose()s them during its ordered shutdown, and that runs their
// destructors, which is a flush path Go cannot bypass.
//
// Built only under -tags gcov, which src/stmak/Submakefile passes for
// COVERAGE=1 alone, so this file is not part of any normal or packaged build.
package main

/*
void __gcov_dump(void);
*/
import "C"

func gcovDump() { C.__gcov_dump() }
