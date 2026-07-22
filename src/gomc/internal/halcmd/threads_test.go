//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halcmd

import (
	"strings"
	"testing"
)

// TestParseCPUList covers the sysfs CPU-list parser. Its input is
// /sys/devices/system/cpu/isolated (and the per-CPU thread_siblings_list), so a
// mis-parse silently changes which cores RT threads are pinned to — the list
// syntax mixes single CPUs and inclusive ranges.
func TestParseCPUList(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []int
	}{
		{"", nil},
		{"0", []int{0}},
		{"2,3", []int{2, 3}},
		{"2-5", []int{2, 3, 4, 5}},
		{"0,2-4,7", []int{0, 2, 3, 4, 7}},
		{"3-3", []int{3}},
		{" 1 , 2 ", []int{1, 2}},
		// Degenerate/hostile input must be skipped, not panic or mis-count.
		{"1,,2", []int{1, 2}},
		{"a-b", nil},
		{"5-1", nil}, // reversed range yields nothing
		{"notanumber", nil},
	} {
		got := parseCPUList(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseCPUList(%q) = %v; want %v", tc.in, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("parseCPUList(%q) = %v; want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

// requireRT skips a test that needs real HAL threads when the process lacks RT
// scheduling privileges: rtapi_task_start() then fails with EPERM and thread
// creation cannot succeed at all. On an RT-capable machine (and in the runtests
// suite, which runs the same paths end-to-end) these tests run for real.
func requireRT(t *testing.T) {
	t.Helper()
	if !rtapiIsRealtime() {
		t.Skip("no POSIX realtime scheduling privileges — HAL threads cannot be created")
	}
}

// TestThreadLifecycle covers newthread/start/stop/delthread against real HAL
// threads: creation, the listing and show output the REST/CLI surface, the
// cycle-counter helpers the launcher's shutdown ordering depends on, and
// deletion.
//
// The pool is reset to "no isolated cores" first so CreateThreadCPU asks for no
// affinity — otherwise it would inherit whatever pool state the cpupool tests
// left and try to pin to a core this machine may not have.
func TestThreadLifecycle(t *testing.T) {
	requireRT(t)
	setPool(t, nil, false)

	const name = "halcmd-test-thread"
	const period = int64(2_000_000) // 2 ms
	if err := CreateThreadCPU(name, period, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			_ = StopThreads()
			_ = ThreadDelete(name)
		}
	})

	threads, err := List("thread", name)
	if err != nil {
		t.Fatalf("List thread: %v", err)
	}
	if len(threads) != 1 || threads[0] != name {
		t.Fatalf("List thread = %v; want exactly [%s]", threads, name)
	}

	res, err := Show("thread", name)
	if err != nil {
		t.Fatalf("Show thread: %v", err)
	}
	if len(res.Threads) != 1 {
		t.Fatalf("Show thread = %+v; want exactly one", res.Threads)
	}
	th := res.Threads[0]
	if th.Name != name {
		t.Errorf("thread Name = %q; want %q", th.Name, name)
	}
	if th.Period != period {
		t.Errorf("thread Period = %d; want %d", th.Period, period)
	}
	if th.FP {
		t.Errorf("thread FP = true; want false (created with usesFP=0)")
	}
	if th.Running {
		t.Error("thread Running = true before StartThreads")
	}
	if len(th.Functs) != 0 {
		t.Errorf("thread Functs = %v; want none", th.Functs)
	}

	// A duplicate name must be refused.
	if err := CreateThreadCPU(name, period, 0, -1); err == nil {
		t.Error("CreateThreadCPU with a duplicate name must fail")
	}

	// The cycle counter only advances while the threads run, so the pre-start
	// wait must time out rather than block forever.
	base := GetMaxCycleCount()
	if err := WaitCycleAdvance(base); err == nil {
		t.Error("WaitCycleAdvance on stopped threads must time out")
	}

	if err := StartThreads(); err != nil {
		t.Fatalf("StartThreads: %v", err)
	}
	if res, err := Show("thread", name); err != nil {
		t.Fatalf("Show thread: %v", err)
	} else if !res.Threads[0].Running {
		t.Error("thread Running = false after StartThreads")
	}
	if err := WaitCycleAdvance(GetMaxCycleCount()); err != nil {
		t.Errorf("WaitCycleAdvance on running threads: %v", err)
	}

	if err := StopThreads(); err != nil {
		t.Fatalf("StopThreads: %v", err)
	}
	if err := ThreadDelete(name); err != nil {
		t.Fatalf("ThreadDelete: %v", err)
	}
	deleted = true

	if err := ThreadDelete(name); err == nil {
		t.Error("ThreadDelete on an unknown thread must fail")
	}
	if threads, err := List("thread", name); err != nil || len(threads) != 0 {
		t.Errorf("List thread after delete = %v, %v; want empty", threads, err)
	}
}

// TestAddFDelFErrors covers the addf/delf rejection paths. The success paths
// need a component that exports a realtime function, which only a loaded
// cmod/comp does — those are covered end-to-end by the runtests HAL bucket.
func TestAddFDelFErrors(t *testing.T) {
	requireRT(t)
	setPool(t, nil, false)

	const thread = "halcmd-test-addf-thread"
	if err := CreateThreadCPU(thread, 2_000_000, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	t.Cleanup(func() { _ = ThreadDelete(thread) })

	if err := AddF("nosuch.funct", thread, -1); err == nil {
		t.Error("AddF with an unknown funct must fail")
	}
	if err := AddF("nosuch.funct", "nosuchthread", -1); err == nil {
		t.Error("AddF with an unknown thread must fail")
	}
	if err := DelF("nosuch.funct", thread); err == nil {
		t.Error("DelF with an unknown funct must fail")
	}
}

// TestDelFunctsByComp: a component that exports no realtime functions must
// report zero removals rather than an error. The launcher calls this on every
// module unload, so the empty case is the common one.
func TestDelFunctsByComp(t *testing.T) {
	id := FindCompID("halcmd-test-keepalive")
	if id == 0 {
		t.Fatal("keep-alive component not found")
	}
	n, err := DelFunctsByComp(id)
	if err != nil {
		t.Fatalf("DelFunctsByComp: %v", err)
	}
	if n != 0 {
		t.Errorf("DelFunctsByComp = %d; want 0 for a component with no functs", n)
	}
}

// TestLockStatusStringAllBits renders every lock bit at once, covering the
// per-bit lines that `halrun -f file | grep lock` matches byte for byte.
func TestLockStatusStringAllBits(t *testing.T) {
	t.Cleanup(func() { _ = halSetLock(0) })
	if err := halSetLock(15); err != nil {
		t.Fatalf("halSetLock: %v", err)
	}
	got := LockStatusString()
	for _, want := range []string{
		"HAL locking status:",
		"current lock value 15 (0f)",
		"HAL_LOCK_LOAD    - loading of new components is locked",
		"HAL_LOCK_CONFIG  - link and addf is locked",
		"HAL_LOCK_PARAMS  - setting params is locked",
		"HAL_LOCK_RUN     - running/stopping HAL is locked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("LockStatusString is missing %q:\n%s", want, got)
		}
	}
}
