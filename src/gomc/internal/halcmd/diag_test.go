//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests that the reason a HAL call refused reaches the caller.
//
// hal_lib and the shims both used to report why they refused only to the log
// and return a bare errno, so "newthread loop1" on an existing thread, a bad
// pin type and a malformed name all arrived as "code -22". The _ex entry points
// hand the reason back through the call that produced it; these tests assert
// the text, not merely that an error occurred.
package halcmd_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	halcmd "github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	hal "github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// halDetail returns the reason carried by err, failing the test if err is not a
// *hal.Error at all.
func halDetail(t *testing.T, err error) string {
	t.Helper()
	var he *hal.Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not a *hal.Error", err)
	}
	return he.Detail
}

// TestErrorCarriesHalLibReason is the reproducer from the field: creating a
// thread whose name is already taken reported only "invalid argument ... (code
// -22)", while hal_lib knew it was a duplicate name.
func TestErrorCarriesHalLibReason(t *testing.T) {
	name := uniq("diagthread")
	if err := halcmd.CreateThreadCPU(name, 1000000, 0, -1); err != nil {
		t.Fatalf("first CreateThreadCPU(%q) = %v; want success", name, err)
	}
	t.Cleanup(func() { _ = halcmd.ThreadDelete(name) })

	err := halcmd.CreateThreadCPU(name, 1000000, 0, -1)
	if err == nil {
		t.Fatalf("second CreateThreadCPU(%q) succeeded; want a duplicate-name error", name)
	}

	want := "duplicate thread name " + name
	if detail := halDetail(t, err); detail != want {
		t.Errorf("Detail = %q; want %q", detail, want)
	}
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("error text %q does not carry hal_lib's reason %q", got, want)
	}
	// The code and the generic category must survive alongside it: the REST
	// layer and the hal.Err* sentinels key off them, not off the reason.
	if got := err.Error(); !strings.Contains(got, "code -22") {
		t.Errorf("error text %q lost the errno", got)
	}
}

// TestHalLibReasons covers the rest of the hal_lib entry points halcmd drives.
// Each of these is a mistake a hand-edited HAL file makes, and each used to be
// indistinguishable from the others at the caller.
func TestHalLibReasons(t *testing.T) {
	comp := uniq("reasons")
	testComp(t, comp)

	bitSig := uniq("bitsig")
	if err := halcmd.NewSig(bitSig, hal.TypeBit); err != nil {
		t.Fatalf("NewSig(%q) = %v", bitSig, err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(bitSig) })

	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "duplicate signal",
			run:  func() error { return halcmd.NewSig(bitSig, hal.TypeBit) },
			want: "duplicate signal '" + bitSig + "'",
		},
		{
			name: "delsig on an unknown signal",
			run:  func() error { return halcmd.DelSig("nosuchsig") },
			want: "signal 'nosuchsig' not found",
		},
		{
			name: "linkps with an unknown pin",
			run:  func() error { return halcmd.LinkPS("nosuch.pin", bitSig) },
			want: "pin 'nosuch.pin' not found",
		},
		{
			name: "linkps with an unknown signal",
			run:  func() error { return halcmd.LinkPS(comp+".b", "nosuchsig") },
			want: "signal 'nosuchsig' not found",
		},
		{
			name: "linkps type mismatch names both sides",
			run:  func() error { return halcmd.LinkPS(comp+".f", bitSig) },
			want: "type mismatch '" + comp + ".f' <- '" + bitSig + "'",
		},
		{
			// hal_lib resolves the function before the thread, so this reports
			// the function — which is the more useful of the two, and exactly
			// the distinction that was invisible when both were -EINVAL.
			name: "addf with an unknown function",
			run:  func() error { return halcmd.AddF(comp+".funct", "nosuchthread", -1) },
			want: "function '" + comp + ".funct' not found",
		},
		{
			name: "unlinkp with an unknown pin",
			run:  func() error { return halcmd.UnlinkP("nosuch.pin") },
			want: "pin 'nosuch.pin' not found",
		},
		{
			name: "delthread with an unknown thread",
			run:  func() error { return halcmd.ThreadDelete("nosuchthread") },
			want: "thread 'nosuchthread' not found",
		},
		{
			name: "alias on an unknown pin",
			run:  func() error { return halcmd.Alias("pin", "nosuch.pin", "somealias") },
			want: "pin 'nosuch.pin' not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("operation succeeded; want an error")
			}
			if detail := halDetail(t, err); !strings.Contains(detail, tc.want) {
				t.Errorf("Detail = %q; want it to contain %q", detail, tc.want)
			}
		})
	}
}

// TestShimReasons covers the refusals that happen in gomc's own shims rather
// than in hal_lib. Those resolve names and parse values themselves, so hal_lib
// never sees the failure — they need their own reasons, not hal_lib's.
func TestShimReasons(t *testing.T) {
	comp := uniq("shimdiag")
	testComp(t, comp)

	sig := uniq("shimsig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig(%q) = %v", sig, err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "setp on an unknown pin names it",
			run:  func() error { return halcmd.SetP("nosuch.pin", "5") },
			want: "no pin or parameter named 'nosuch.pin'",
		},
		{
			name: "setp on an output pin says why",
			run:  func() error { return halcmd.SetP(comp+".b", "1") },
			want: "is an output pin",
		},
		{
			name: "sets on an unknown signal names it",
			run:  func() error { return halcmd.SetS("nosuchsig", "5") },
			want: "signal 'nosuchsig' not found",
		},
		{
			name: "unparsable value reports the value",
			run:  func() error { return halcmd.SetS(sig, "notanumber") },
			want: "'notanumber' is not a valid s32",
		},
		{
			name: "net names the pin it could not find",
			run:  func() error { return halcmd.Net(uniq("netsig"), "nosuch.pin") },
			want: "pin 'nosuch.pin' not found",
		},
		{
			name: "gets on an unknown signal names it",
			run:  func() error { _, err := halcmd.GetS("nosuchsig"); return err },
			want: "signal 'nosuchsig' not found",
		},
		{
			name: "getp on an unknown pin names it",
			run:  func() error { _, err := halcmd.GetP("nosuch.pin"); return err },
			want: "no pin or parameter named 'nosuch.pin'",
		},
		{
			name: "retain on an unknown signal names it",
			run:  func() error { return halcmd.Retain("nosuchsig") },
			want: "signal 'nosuchsig' not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("operation succeeded; want an error")
			}
			if detail := halDetail(t, err); !strings.Contains(detail, tc.want) {
				t.Errorf("Detail = %q; want it to contain %q", detail, tc.want)
			}
		})
	}
}

// halOp returns the operation name carried by err.
func halOp(t *testing.T, err error) string {
	t.Helper()
	var he *hal.Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not a *hal.Error", err)
	}
	return he.Op
}

// TestErrorNamesTheCommand asserts the error names the halcmd command the
// operator issued, not the C function underneath.
//
// The interesting cases are the last four: one Go wrapper serves both linkps
// and linksp, both alias and unalias, and both lock and unlock, so those verbs
// are passed in rather than hardcoded. Hardcoding either one would still pass a
// test that only checked the common commands.
func TestErrorNamesTheCommand(t *testing.T) {
	comp := uniq("opnames")
	testComp(t, comp)

	sig := uniq("opsig")
	if err := halcmd.NewSig(sig, hal.TypeBit); err != nil {
		t.Fatalf("NewSig(%q) = %v", sig, err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	for _, tc := range []struct {
		run func() error
		op  string
	}{
		{func() error { return halcmd.NewSig(sig, hal.TypeBit) }, "newsig"},
		{func() error { return halcmd.DelSig("nosuchsig") }, "delsig"},
		{func() error { return halcmd.SetP("nosuch.pin", "5") }, "setp"},
		{func() error { _, err := halcmd.GetP("nosuch.pin"); return err }, "getp"},
		{func() error { return halcmd.SetS("nosuchsig", "1") }, "sets"},
		{func() error { _, err := halcmd.GetS("nosuchsig"); return err }, "gets"},
		{func() error { return halcmd.Net(uniq("opnet"), "nosuch.pin") }, "net"},
		{func() error { return halcmd.AddF("nosuch.f", "nosuchthread", -1) }, "addf"},
		{func() error { return halcmd.DelF("nosuch.f", "nosuchthread") }, "delf"},
		{func() error { return halcmd.ThreadDelete("nosuchthread") }, "delthread"},
		{func() error { return halcmd.Retain("nosuchsig") }, "retain"},
		{func() error { return halcmd.Unretain("nosuchsig") }, "unretain"},
		{func() error { return halcmd.UnlinkP("nosuch.pin") }, "unlinkp"},
		// Shared wrappers: the verb must follow the command, not the wrapper.
		{func() error { return halcmd.LinkPS("nosuch.pin", sig) }, "linkps"},
		{func() error { return halcmd.LinkSP(sig, "nosuch.pin") }, "linksp"},
		{func() error { return halcmd.Alias("pin", "nosuch.pin", "al") }, "alias"},
		{func() error { return halcmd.UnAlias("pin", "nosuch.pin") }, "unalias"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("operation succeeded; want an error")
			}
			if got := halOp(t, err); got != tc.op {
				t.Errorf("Op = %q; want %q", got, tc.op)
			}
			if got := err.Error(); !strings.HasPrefix(got, tc.op+": ") {
				t.Errorf("error text %q does not lead with the command name", got)
			}
		})
	}
}

// TestNoReasonFallsBack is the negative half: with nothing reported, the error
// must still render its generic per-code text rather than degrade to a bare
// code, and must not render empty brackets.
//
// This is asserted on the constructor because there is no longer a HAL
// operation reachable from this package that fails without saying why. The
// property still has to hold — hal_lib gains error paths over time, and any new
// one that forgets its buffer lands here.
func TestNoReasonFallsBack(t *testing.T) {
	err := hal.CodeError("hal_shim_example", -22, "")
	if err == nil {
		t.Fatal("CodeError with a failing code returned nil")
	}
	if detail := halDetail(t, err); detail != "" {
		t.Errorf("Detail = %q; want empty", detail)
	}
	got := err.Error()
	if !strings.Contains(got, "invalid argument") {
		t.Errorf("error text %q lost the generic per-code message", got)
	}
	if strings.Contains(got, "[]") {
		t.Errorf("error text %q rendered an empty detail", got)
	}
	if err := hal.CodeError("hal_shim_example", 0, ""); err != nil {
		t.Errorf("CodeError(..., 0, ...) = %v; want nil", err)
	}
}

// TestSuccessLeavesNoReason asserts a successful call does not pick up a reason
// from an earlier failure. The buffer belongs to the call, so this holds by
// construction — the test is here because it would not hold under a scheme that
// recovered the message from a shared slot afterwards.
func TestSuccessLeavesNoReason(t *testing.T) {
	sig := uniq("cleansig")

	if err := halcmd.DelSig("nosuchsig"); err == nil {
		t.Fatal("DelSig on an unknown signal succeeded; want an error")
	}
	if err := halcmd.NewSig(sig, hal.TypeBit); err != nil {
		t.Fatalf("NewSig after a failed call = %v; want success", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	// A later, different failure reports only its own reason.
	err := halcmd.SetS("nosuchsig", "1")
	if err == nil {
		t.Fatal("SetS on an unknown signal succeeded; want an error")
	}
	if detail := halDetail(t, err); !strings.Contains(detail, "signal 'nosuchsig' not found") {
		t.Errorf("Detail = %q; want this call's own reason", detail)
	}
}

// TestReasonsUnderConcurrency is what the out-param buys over a shared slot:
// concurrent callers each get their own reason with no locking, ordering or
// thread-affinity requirement anywhere in the wrappers.
func TestReasonsUnderConcurrency(t *testing.T) {
	const workers = 8
	const rounds = 20

	names := make([]string, workers)
	for i := range names {
		names[i] = uniq("racethread")
		if err := halcmd.CreateThreadCPU(names[i], 1000000, 0, -1); err != nil {
			t.Fatalf("CreateThreadCPU(%q) = %v; want success", names[i], err)
		}
		n := names[i]
		t.Cleanup(func() { _ = halcmd.ThreadDelete(n) })
	}

	var wg sync.WaitGroup
	errs := make(chan string, workers*rounds)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				err := halcmd.CreateThreadCPU(name, 1000000, 0, -1)
				if err == nil {
					errs <- "duplicate CreateThreadCPU(" + name + ") succeeded"
					return
				}
				var he *hal.Error
				if !errors.As(err, &he) {
					errs <- "not a *hal.Error: " + err.Error()
					return
				}
				if he.Detail != "duplicate thread name "+name {
					errs <- "misattributed reason for " + name + ": " + he.Detail
					return
				}
			}
		}(names[i])
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
