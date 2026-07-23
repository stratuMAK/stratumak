//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halcmd

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

// TestThreadLifecycle covers newthread/start/stop/delthread against real HAL
// threads: creation, the listing and show output the REST/CLI surface, the
// cycle-counter helpers the launcher's shutdown ordering depends on, and
// deletion.
//
// The pool is reset to "no isolated cores" first so CreateThreadCPU asks for no
// affinity — otherwise it would inherit whatever pool state the cpupool tests
// left and try to pin to a core this machine may not have.
func TestThreadLifecycle(t *testing.T) {
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

	// The cycle counter advances as soon as the thread exists: hal_create_thread
	// starts the pthread, and start/stop gate whether the thread's *functions*
	// run (and the Running flag), not whether it cycles. The launcher relies on
	// this for its unload synchronisation — it waits for a cycle to pass before
	// freeing a component's pins, which must work whether or not the functions
	// are currently enabled.
	if err := WaitCycleAdvance(GetMaxCycleCount()); err != nil {
		t.Errorf("WaitCycleAdvance before StartThreads: %v", err)
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

// TestThreadCreateDeleteCycles is the regression test for a thread-teardown
// deadlock in the uspace non-RT scheduling path (do_thread_lock=1, which is
// what rtapi_initialize_app selects whenever RT hardening is unavailable).
//
// task_wrapper() acquired thread_lock at task start and never released it,
// relying on task_wait() to drop it when it observed the cooperative-exit flag.
// But the flag can also be set just after task_wait() re-acquired the lock and
// checked it — the task loop's own condition then ends the task with the lock
// still HELD, leaking it locked with its owner gone. The next thread created
// then blocked forever acquiring it in task_wrapper, so it never observed its
// own exit flag, so hal_thread_delete's pthread_join never returned: a hung
// controller on `delthread` after a previous thread had been deleted.
//
// Hitting the window needs a delete that races the task's wait rather than one
// that arrives while it is safely asleep, so the loop deletes at a spread of
// delays across the 1 ms period and then proves a fresh thread can still be
// created and deleted afterwards. Verified to hang before the fix.
func TestThreadCreateDeleteCycles(t *testing.T) {
	setPool(t, nil, false)

	const period = 2_000_000 // 2 ms
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("halcmd-test-cycle%d", i)
		if err := CreateThreadCPU(name, period, 0, -1); err != nil {
			t.Fatalf("cycle %d: CreateThreadCPU: %v", i, err)
		}
		// Walk the delete across the period so some deletes land while the
		// task sleeps and some land just as it wakes and re-takes the lock.
		time.Sleep(time.Duration(i%10) * 200 * time.Microsecond)
		// Before the fix this blocked forever once a previous cycle had
		// leaked thread_lock.
		if err := ThreadDelete(name); err != nil {
			t.Fatalf("cycle %d: ThreadDelete: %v", i, err)
		}
	}

	// A full start/stop/delete must still work after all that churn.
	const last = "halcmd-test-cycle-last"
	if err := CreateThreadCPU(last, period, 0, -1); err != nil {
		t.Fatalf("final CreateThreadCPU: %v", err)
	}
	if err := WaitCycleAdvance(GetMaxCycleCount()); err != nil {
		t.Errorf("final WaitCycleAdvance: %v", err)
	}
	if err := StartThreads(); err != nil {
		t.Fatalf("final StartThreads: %v", err)
	}
	if err := StopThreads(); err != nil {
		t.Fatalf("final StopThreads: %v", err)
	}
	if err := ThreadDelete(last); err != nil {
		t.Fatalf("final ThreadDelete: %v", err)
	}
}

// Thread periods used to be forced onto a multiple of the base period fixed by
// the first thread ever created: a shorter period was refused outright, and
// anything else was silently rounded. Both are gone — each task waits on its
// own absolute deadline, so a period is used exactly as requested.
func TestThreadPeriodIsUsedAsRequested(t *testing.T) {
	setPool(t, nil, false)

	// Every other test in this binary creates 2 ms threads, so a base period of
	// 2 ms is already established before this runs.
	for _, tc := range []struct {
		name   string
		period int64
		why    string
	}{
		{"halcmd-test-period-odd", 1_234_567, "not a multiple of any existing period"},
		{"halcmd-test-period-fast", 125_000, "faster than the first thread created"},
	} {
		if err := CreateThreadCPU(tc.name, tc.period, 0, -1); err != nil {
			t.Fatalf("CreateThreadCPU(%s, %s): %v", tc.name, tc.why, err)
		}
		t.Cleanup(func() { _ = ThreadDelete(tc.name) })

		res, err := Show("thread", tc.name)
		if err != nil || len(res.Threads) != 1 {
			t.Fatalf("Show thread %s = %+v / %v", tc.name, res, err)
		}
		if got := res.Threads[0].Period; got != tc.period {
			t.Errorf("thread %s period = %d; want exactly %d (%s)", tc.name, got, tc.period, tc.why)
		}
	}
}

// Creating a faster thread after a slower one is allowed but inverts rate
// monotonic scheduling, because HAL hands out priorities in creation order.
// That has to be reported: it is the one way the caller can silently end up
// with a fast thread that everything else preempts.
func TestThreadOrderWarning(t *testing.T) {
	setPool(t, nil, false)

	const slow = "halcmd-test-order-slow"
	if err := CreateThreadCPU(slow, 8_000_000, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU(%s): %v", slow, err)
	}
	t.Cleanup(func() { _ = ThreadDelete(slow) })

	// Faster than an existing thread: warned, and the offender is named so the
	// operator knows which thread to create first next time.
	w := ThreadOrderWarning("halcmd-test-order-fast", 1_000_000)
	if w == "" {
		t.Fatal("creating a thread faster than an existing one must warn")
	}
	if !strings.Contains(w, slow) {
		t.Errorf("warning %q does not name the slower thread %s", w, slow)
	}

	// Slower than everything that exists: correct rate monotonic order, silent.
	if w := ThreadOrderWarning("halcmd-test-order-slower", 16_000_000); w != "" {
		t.Errorf("creating the slowest thread must not warn, got %q", w)
	}
	// Equal periods are not an inversion either.
	if w := ThreadOrderWarning("halcmd-test-order-equal", 8_000_000); w != "" {
		t.Errorf("an equal period must not warn, got %q", w)
	}
}

// A CPU handed out for a thread that then fails to be created must go back into
// the pool — otherwise every failed newthread permanently burns an isolated
// core, and later threads co-locate for no reason.
func TestCreateThreadFailureReturnsCPUToPool(t *testing.T) {
	setPool(t, []int{3, 2}, false)

	const period = int64(2_000_000)

	const first = "halcmd-test-pool-first"
	if err := CreateThreadCPU(first, period, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU(%s): %v", first, err)
	}
	t.Cleanup(func() { _ = ThreadDelete(first) })

	// Fails inside hal_lib (duplicate name), after a CPU was already acquired:
	// acquireCPU has popped core 2 and moved lastAssigned by the time the C call
	// refuses the thread.
	if err := CreateThreadCPU(first, period, 0, -1); err == nil {
		t.Fatal("duplicate thread name must be refused")
	}

	// Both must be rolled back — this is what the assertion is really about.
	pool.mu.Lock()
	avail := append([]int(nil), pool.available...)
	last := pool.lastAssigned
	pool.mu.Unlock()
	if len(avail) != 1 || avail[0] != 2 {
		t.Errorf("free list = %v after a failed create; want [2] (the acquired core returned)", avail)
	}
	if last != 3 {
		t.Errorf("lastAssigned = %d after a failed create; want 3 (the last thread that really exists)", last)
	}

	// And the returned core is genuinely reusable, not just bookkeeping.
	const second = "halcmd-test-pool-second"
	if err := CreateThreadCPU(second, period, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU(%s): %v", second, err)
	}
	t.Cleanup(func() { _ = ThreadDelete(second) })
	pool.mu.Lock()
	assigned := pool.lastAssigned
	pool.mu.Unlock()
	if assigned != 2 {
		t.Errorf("second thread got cpu %d; want 2 (a free core, not a co-location)", assigned)
	}
}

// ===== process-lifetime entry points =====

// TestUnloadAllDoesNotSignalOurself covers the in-process behaviour of
// `halcmd unload all`. The shim walks the component list and SIGTERMs the
// *owning process* of each component whose pid is not our own; realtime
// components in our own process are left to the cmod infrastructure. So
// in-process it must be a no-op that leaves our components alive.
//
// The assertion that matters is the pid guard: if it ever inverted, the
// launcher would SIGTERM itself on `unload all` and the controller would die.
// A live component of ours exists here, so an inverted guard kills this test
// binary rather than passing quietly.
func TestUnloadAllDoesNotSignalOurself(t *testing.T) {
	before, err := ListComponents()
	if err != nil {
		t.Fatalf("ListComponents: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("no components to protect — the keep-alive should be present")
	}

	if err := UnloadAll(0); err != nil {
		t.Fatalf("UnloadAll: %v", err)
	}

	after, err := ListComponents()
	if err != nil {
		t.Fatalf("ListComponents after UnloadAll: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("UnloadAll changed our own component set: %v -> %v", before, after)
	}
	// exceptID is honoured the same way (nothing of ours is touched either way).
	if err := UnloadAll(FindCompID("halcmd-test-keepalive")); err != nil {
		t.Errorf("UnloadAll with an exceptID: %v", err)
	}
}

// `setexact_for_test_suite_only` used to ask HAL to treat the requested base
// period as exactly achievable instead of rounding thread periods to it. Thread
// periods are no longer rounded at all, so it is a no-op that must simply keep
// being accepted — 10 test configurations in tests/ still issue it, and it used
// to fail once any thread existed.
func TestSetExactIsAcceptedAfterThreadsExist(t *testing.T) {
	setPool(t, nil, false)

	const name = "halcmd-test-setexact"
	if err := CreateThreadCPU(name, 2_000_000, 0, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	defer func() { _ = ThreadDelete(name) }()

	if err := SetExact(); err != nil {
		t.Errorf("SetExact with threads already created: %v; want accepted as a no-op", err)
	}
}

// TestLockDLHandleNilIsSafe covers the mlock helpers' nil guard. They are
// called from the cmod loader over every dlopen handle; a nil handle (a module
// that failed to load) must be ignored rather than dereferenced, because this
// runs on the load/unload path of a live controller.
func TestLockDLHandleNilIsSafe(t *testing.T) {
	LockDLHandle(nil)
	UnlockDLHandle(nil)
}
