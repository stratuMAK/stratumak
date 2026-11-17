// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain fails the package when a test left one of a task's goroutines
// running. Every test that starts a sequencer, a program, an MDI, a filter or
// a monitor has to end it (Task.Shutdown, monitor.stop): a goroutine that
// outlives its test runs on into the next ones, where nothing orders it
// against what they do.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if leaked := leakedTaskGoroutines(); leaked != "" {
			fmt.Fprintf(os.Stderr, "FAIL: task goroutines still running after all tests:\n%s\n", leaked)
			code = 1
		}
	}
	os.Exit(code)
}

// leakedTaskGoroutines returns the stacks of this package's goroutines that
// are still running. A goroutine Shutdown waited for has run its last
// statement but may not have returned yet, so the check waits for the set to
// empty; the deadline only bounds a real leak, which never empties.
func leakedTaskGoroutines() string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		leaked := taskGoroutineStacks()
		if leaked == "" || time.Now().After(deadline) {
			return leaked
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func taskGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var leaked []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "leakedTaskGoroutines") {
			continue // this goroutine
		}
		if strings.Contains(g, "internal/task.(*Task).") || strings.Contains(g, "internal/task.(*monitor).") ||
			strings.Contains(g, "internal/task.(*mcodeHandler).") {
			leaked = append(leaked, g)
		}
	}
	return strings.Join(leaked, "\n\n")
}
