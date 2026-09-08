// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
//
// The in-process HAL data segment is created on the first hal_init and torn
// down when the last component exits; a subsequent hal_init in the same process
// then fails with EINVAL (HAL is not cleanly re-initializable after a full
// teardown — see the pkg/hal review). Keeping one component alive for the whole
// run means per-test create/exit cycles (and the Example) never drop the
// component count to zero, so each can init/exit freely.
//
// The two RTAPI initialisations ahead of it are what the launcher does before
// any hal_init, and the funct tests need both: rtapi_initialize_app falls back
// to SCHED_OTHER so an unprivileged process can create a thread at all, and
// RtapiAppInit sets hal_lib's rtapi_pid — which is what makes
// hal_init_ex(..., COMPONENT_TYPE_REALTIME) actually mark a component realtime
// (comp->pid == 0). Without it hal_export_funct refuses with "component is not
// realtime".
func TestMain(m *testing.M) {
	halcmd.RtapiInitializeApp()
	if err := halcmd.RtapiAppInit(); err != nil {
		fmt.Fprintf(os.Stderr, "rtapi app init failed: %v\n", err)
		os.Exit(1)
	}

	keep, err := hal.NewComponent("hal-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	halcmd.RtapiAppCleanup()
	os.Exit(code)
}

// TestPinGetSetRoundTrip verifies that Set() followed by Get() round-trips the
// value for every supported pin type (covers the per-type dereference paths in
// pin.go, including the HAL_PORT length-prefix framing for strings).
func TestPinGetSetRoundTrip(t *testing.T) {
	comp, err := hal.NewComponent("test-roundtrip")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	bp, err := hal.NewPin[bool](comp, "b", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[bool]: %v", err)
	}
	fp, err := hal.NewPin[float64](comp, "f", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[float64]: %v", err)
	}
	sp, err := hal.NewPin[int32](comp, "s", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[int32]: %v", err)
	}
	up, err := hal.NewPin[uint32](comp, "u", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[uint32]: %v", err)
	}
	strp, err := hal.NewPin[string](comp, "str", hal.Out)
	if err != nil {
		t.Fatalf("NewPin[string]: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	bp.Set(true)
	if got := bp.Get(); got != true {
		t.Errorf("bool pin: got %v, want true", got)
	}
	fp.Set(3.14)
	if got := fp.Get(); got != 3.14 {
		t.Errorf("float pin: got %v, want 3.14", got)
	}
	sp.Set(-42)
	if got := sp.Get(); got != -42 {
		t.Errorf("s32 pin: got %v, want -42", got)
	}
	up.Set(0xDEADBEEF)
	if got := up.Get(); got != 0xDEADBEEF {
		t.Errorf("u32 pin: got %v, want 0xDEADBEEF", got)
	}
	// A HAL_PORT-backed string pin has no backing buffer until it is netted to a
	// port signal allocated with a size. Standalone, hal_pin_port_new leaves the
	// port handle unbacked, so the write is dropped: Set() silently no-ops and
	// Get() reads back "". TrySet() surfaces that dropped write as an error —
	// this is the contract adsbridge relies on to fail an undeliverable ADS
	// string write instead of falsely ACKing it.
	if err := strp.TrySet("hello, world"); err == nil {
		t.Error("TrySet on unlinked string pin: got nil, want ErrPortWriteFailed")
	}
	strp.Set("hello, world") // fire-and-forget: must not panic
	if got := strp.Get(); got != "" {
		t.Errorf("unlinked string pin: got %q, want %q (no port buffer standalone)", got, "")
	}
}

// TestPinStringConcurrentWithSet is a regression test for the recursive
// read-lock deadlock that previously lived in Pin.String(): String() took an
// RLock and then called Get(), which RLocks again. Go's RWMutex forbids
// recursive read-locking and deadlocks when a Set() writer contends between the
// two RLocks. This test hammers String() concurrently with Set() and completes
// only if String() does not deadlock; run it under -race to also confirm the
// value accesses are properly synchronized.
func TestPinStringConcurrentWithSet(t *testing.T) {
	comp, err := hal.NewComponent("test-string-race")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	p, err := hal.NewPin[float64](comp, "v", hal.Out)
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	const iters = 2000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.Set(float64(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = p.String()
		}
	}()
	wg.Wait()
}
