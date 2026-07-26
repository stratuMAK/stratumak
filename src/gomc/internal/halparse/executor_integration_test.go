//go:build cgo

// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Executor tests against a real in-process HAL instance. The companion
// executor_test.go is `!cgo` and can only prove that each token type reaches
// the C shim (ErrNoCGO); these run the commands for real and check what they
// did to HAL — which is the whole job of a .hal file.
package halparse

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	halcmd "github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	hal "github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary —
// the in-process HAL segment cannot be re-initialised once the last component
// exits (see pkg/hal's TestMain).
func TestMain(m *testing.M) {
	keep, err := hal.NewComponent("halparse-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

var execCounter int

// execComp creates a ready HAL component with a unique name and the pins the
// executor tests drive: "b" (bit io), "f" (float io), "s" (s32 out) and two
// s32 inputs "si"/"si2". A signal that any pin writes cannot be `sets`, so the
// value-through-a-link assertions use the input pins and the writer pin only
// appears where a real link is being checked.
func execComp(t *testing.T) *hal.Component {
	t.Helper()
	execCounter++
	name := fmt.Sprintf("hpx%d", execCounter)
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	t.Cleanup(func() { _ = comp.Exit() })
	if _, err := hal.NewPin[bool](comp, "b", hal.IO); err != nil {
		t.Fatalf("NewPin bit: %v", err)
	}
	if _, err := hal.NewPin[float64](comp, "f", hal.IO); err != nil {
		t.Fatalf("NewPin float: %v", err)
	}
	if _, err := hal.NewPin[int32](comp, "s", hal.Out); err != nil {
		t.Fatalf("NewPin s32 out: %v", err)
	}
	if _, err := hal.NewPin[int32](comp, "si", hal.In); err != nil {
		t.Fatalf("NewPin s32 in: %v", err)
	}
	if _, err := hal.NewPin[int32](comp, "si2", hal.In); err != nil {
		t.Fatalf("NewPin s32 in2: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return comp
}

// runHAL parses content as a HAL file and executes it against live HAL.
func runHAL(t *testing.T, content string) error {
	t.Helper()
	res, err := NewSingleFileParser(nil, nil).ParseContent("test.hal", content)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, content)
	}
	return res.Execute()
}

// TestExecuteHALFile is the end-to-end contract: a HAL file's commands are
// parsed and then actually applied to HAL, in order. It covers the executor's
// signal, link, value, alias and query dispatch in one realistic file.
func TestExecuteHALFile(t *testing.T) {
	c := execComp(t)
	n := c.Name()
	sig := n + "-sig"

	content := strings.Join([]string{
		"newsig " + sig + " s32",
		"net " + sig + " " + n + ".si => " + n + ".si2",
		"setp " + n + ".f 1.25",
		"setp " + n + ".b TRUE",
		"sets " + sig + " 21",
		"alias pin " + n + ".f " + n + "-alias",
		"getp " + n + ".f",
		"gets " + sig,
		"ptype " + n + ".f",
		"stype " + sig,
		"list pin " + n + ".*",
		"show sig " + sig,
	}, "\n")

	t.Cleanup(func() {
		_ = halcmd.UnAlias("pin", n+"-alias")
		_ = halcmd.UnlinkP(n + ".si")
		_ = halcmd.UnlinkP(n + ".si2")
		_ = halcmd.DelSig(sig)
	})
	if err := runHAL(t, content); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, err := halcmd.GetP(n + ".f"); err != nil || got != "1.25" {
		t.Errorf("setp float: GetP = %q, %v; want %q", got, err, "1.25")
	}
	if got, err := halcmd.GetP(n + ".b"); err != nil || got != "TRUE" {
		t.Errorf("setp bit: GetP = %q, %v; want %q", got, err, "TRUE")
	}
	// The net linked both pins, so sets is visible through each of them.
	for _, pin := range []string{n + ".si", n + ".si2"} {
		if got, err := halcmd.GetP(pin); err != nil || got != "21" {
			t.Errorf("net + sets: GetP(%s) = %q, %v; want %q", pin, got, err, "21")
		}
	}
	if got, err := halcmd.GetP(n + "-alias"); err != nil || got != "1.25" {
		t.Errorf("alias: GetP = %q, %v; want %q", got, err, "1.25")
	}

	// unalias / unlinkp / delsig undo it, again through the executor.
	if err := runHAL(t, strings.Join([]string{
		"unalias pin " + n + "-alias",
		"unlinkp " + n + ".si",
		"unlinkp " + n + ".si2",
		"delsig " + sig,
	}, "\n")); err != nil {
		t.Fatalf("Execute teardown: %v", err)
	}
	if _, err := halcmd.GetP(n + "-alias"); err == nil {
		t.Error("unalias did not remove the alias")
	}
	if _, err := halcmd.GetS(sig); err == nil {
		t.Error("delsig did not remove the signal")
	}
}

// TestExecuteLinkForms covers the three link spellings a .hal file may use —
// linkps, linksp and the deprecated linkpp, which the executor deliberately
// treats as linkps with the second name as the signal.
func TestExecuteLinkForms(t *testing.T) {
	c := execComp(t)
	n := c.Name()
	sig := n + "-lsig"

	writerSig := n + "-wsig"
	t.Cleanup(func() {
		_ = halcmd.UnlinkP(n + ".s")
		_ = halcmd.UnlinkP(n + ".si")
		_ = halcmd.UnlinkP(n + ".si2")
		_ = halcmd.DelSig(sig)
		_ = halcmd.DelSig(writerSig)
	})
	if err := runHAL(t, strings.Join([]string{
		"newsig " + sig + " s32",
		"linkps " + n + ".si " + sig,
		"linksp " + sig + " " + n + ".si2",
		"sets " + sig + " 5",
	}, "\n")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, pin := range []string{n + ".si", n + ".si2"} {
		if got, err := halcmd.GetP(pin); err != nil || got != "5" {
			t.Errorf("linkps/linksp: GetP(%s) = %q, %v; want %q", pin, got, err, "5")
		}
	}

	if err := runHAL(t, "unlinkp "+n+".si2\nlinkpp "+n+".si2 "+sig); err != nil {
		t.Fatalf("Execute linkpp: %v", err)
	}
	if got, err := halcmd.GetP(n + ".si2"); err != nil || got != "5" {
		t.Errorf("linkpp: GetP = %q, %v; want %q", got, err, "5")
	}

	// An output pin joins a signal too — checked through the link the pin
	// reports rather than by writing the (now driven) signal.
	if err := runHAL(t, "newsig "+writerSig+" s32\nlinkps "+n+".s "+writerSig); err != nil {
		t.Fatalf("Execute writer link: %v", err)
	}
	res, err := halcmd.Show("pin", n+".s")
	if err != nil {
		t.Fatalf("Show pin: %v", err)
	}
	if len(res.Pins) != 1 || res.Pins[0].Signal != writerSig {
		t.Errorf("Show pin %s.s = %+v; want it linked to %q", n, res.Pins, writerSig)
	}
}

// TestExecuteLockUnlock covers the lock/unlock dispatch, including the
// unlock-clears-bits semantics executeToken implements inline (current &^ level)
// rather than delegating to halcmd.Unlock.
func TestExecuteLockUnlock(t *testing.T) {
	t.Cleanup(func() { _ = halcmd.SetLock(0) })

	if err := runHAL(t, "lock all"); err != nil {
		t.Fatalf("Execute lock all: %v", err)
	}
	locked := halcmd.GetLock()
	if locked == 0 {
		t.Fatal("lock all left the lock at 0")
	}

	if err := runHAL(t, "unlock tune"); err != nil {
		t.Fatalf("Execute unlock tune: %v", err)
	}
	if got := halcmd.GetLock(); got != locked&^3 {
		t.Errorf("after unlock tune GetLock = %d; want %d", got, locked&^3)
	}

	if err := runHAL(t, "unlock all"); err != nil {
		t.Fatalf("Execute unlock all: %v", err)
	}
	if got := halcmd.GetLock(); got != 0 {
		t.Errorf("after unlock all GetLock = %d; want 0", got)
	}
}

// TestExecuteStatusAndDebug covers the two commands with no HAL side effect to
// assert on: `status` prints the classic lock block and `debug` sets the log
// level. Both must dispatch without error.
func TestExecuteStatusAndDebug(t *testing.T) {
	t.Cleanup(func() {
		_ = halcmd.SetDebug(1)
		_ = halcmd.SetLock(0)
	})
	if err := runHAL(t, "status"); err != nil {
		t.Errorf("Execute status: %v", err)
	}
	if err := runHAL(t, "debug 3"); err != nil {
		t.Errorf("Execute debug 3: %v", err)
	}
}

// TestExecuteSaveToFile covers the save dispatch including the file argument,
// which is taken straight from the .hal line.
func TestExecuteSaveToFile(t *testing.T) {
	c := execComp(t)
	n := c.Name()
	sig := n + "-svsig"
	path := t.TempDir() + "/out.hal"

	t.Cleanup(func() { _ = halcmd.DelSig(sig) })
	if err := runHAL(t, "newsig "+sig+" float\nsave sig "+path); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the saved file: %v", err)
	}
	if !strings.Contains(string(data), "newsig "+sig) {
		t.Errorf("saved file is missing %q:\n%s", "newsig "+sig, data)
	}
}

// TestExecuteErrorCarriesLocation is what makes a broken HAL file debuggable:
// a command that fails at runtime must report the file and line it came from,
// and execution must stop there rather than run on.
func TestExecuteErrorCarriesLocation(t *testing.T) {
	c := execComp(t)
	n := c.Name()
	sig := n + "-errsig"

	content := strings.Join([]string{
		"newsig " + sig + " float",
		"setp nosuch.pin 1",   // line 2 — fails
		"setp " + n + ".f 99", // must not run
	}, "\n")
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	err := runHAL(t, content)
	if err == nil {
		t.Fatal("Execute of a failing command returned nil")
	}
	var execErr *ExecutionError
	if !errors.As(err, &execErr) {
		t.Fatalf("error is %T, not *ExecutionError: %v", err, err)
	}
	if execErr.Loc.Line != 2 {
		t.Errorf("error line = %d; want 2", execErr.Loc.Line)
	}
	if execErr.Loc.File != "test.hal" {
		t.Errorf("error file = %q; want test.hal", execErr.Loc.File)
	}
	if !strings.Contains(err.Error(), "test.hal:2:") {
		t.Errorf("error text %q does not carry file:line", err.Error())
	}
	// The command before the failure ran; the one after it did not.
	if _, err := halcmd.GetS(sig); err != nil {
		t.Errorf("the command before the failure did not run: %v", err)
	}
	if got, _ := halcmd.GetP(n + ".f"); got == "99" {
		t.Error("the command after the failure must not have run")
	}
}

// TestExecuteLoadIsRejected: `load` is the launcher's business (IterLoads), and
// executeToken must refuse it rather than silently skip it — a HAL file whose
// modules were never loaded would otherwise fail much later with a confusing
// "no such pin".
func TestExecuteLoadIsRejected(t *testing.T) {
	res, err := NewSingleFileParser(nil, nil).ParseContent("test.hal", "load nosuchmodule.so")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Loads are bucketed away from HALCmd, so Execute alone is a no-op...
	if err := res.Execute(); err != nil {
		t.Fatalf("Execute with only a load token: %v", err)
	}
	// ...but dispatching the token directly must be refused.
	if err := executeToken(res.Loads[0]); err == nil {
		t.Fatal("executeToken(load) returned nil")
	} else if !strings.Contains(err.Error(), "cannot be executed directly") {
		t.Errorf("error = %q; want the load-via-launcher message", err.Error())
	}
}

// TestIterLoadsAgainstParsedFile covers the launcher's entry point over a real
// parsed file: the default instance name derived from the module basename, the
// explicit <a,b> multi-instance form, and argument pass-through.
func TestIterLoadsAgainstParsedFile(t *testing.T) {
	res, err := NewSingleFileParser(nil, nil).ParseContent("test.hal", strings.Join([]string{
		"load /some/dir/mymod.so cfg=1 rate=5",
		"load othermod.so <a,b> x=2",
	}, "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	type call struct{ path, name string }
	var calls []call
	var args [][]string
	if err := res.IterLoads(func(path, name string, a []string) error {
		calls = append(calls, call{path, name})
		args = append(args, a)
		return nil
	}); err != nil {
		t.Fatalf("IterLoads: %v", err)
	}

	want := []call{
		{"/some/dir/mymod.so", "mymod"},
		{"othermod.so", "a"},
		{"othermod.so", "b"},
	}
	if len(calls) != len(want) {
		t.Fatalf("IterLoads made %d calls (%v); want %v", len(calls), calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v; want %+v", i, calls[i], want[i])
		}
	}
	if len(args[0]) != 2 || args[0][0] != "cfg=1" || args[0][1] != "rate=5" {
		t.Errorf("args[0] = %v; want [cfg=1 rate=5]", args[0])
	}
	// Both instances of a multi-name load get the same args.
	if len(args[1]) != 1 || args[1][0] != "x=2" || len(args[2]) != 1 || args[2][0] != "x=2" {
		t.Errorf("multi-instance args = %v / %v; want [x=2] each", args[1], args[2])
	}

	// A callback error is wrapped with the load line's location.
	iterErr := res.IterLoads(func(_, _ string, _ []string) error {
		return fmt.Errorf("boom")
	})
	if iterErr == nil {
		t.Fatal("IterLoads swallowed the callback error")
	}
	if !strings.Contains(iterErr.Error(), "test.hal:1:") {
		t.Errorf("IterLoads error %q does not carry file:line", iterErr.Error())
	}
}
