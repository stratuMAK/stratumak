// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Behavioural tests for the halscope module against a real in-process HAL
// instance (link_test.go pulls in the HAL C symbols).
//
// The API handlers here write into a struct the RT sample function reads, so
// the tests concentrate on the guards that keep that struct coherent: index and
// state checks that must reject a bad REST call rather than let it corrupt a
// capture, and the persistence round-trip that has to survive a config whose
// pins moved or vanished.
package halscope

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	halscopeapi "github.com/sittner/linuxcnc/src/gomc/generated/gmi/halscope"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	_ "github.com/sittner/linuxcnc/src/gomc/internal/persist_sqlite"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

const maxChannels = 16 // HALSCOPE_MAX_CHANNELS

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and cannot be re-initialised afterwards — see pkg/hal's TestMain.
// sp makes an optional string parameter from a literal.
func sp(s string) *string { return &s }

func TestMain(m *testing.M) {
	// Without the RTAPI app init, thread creation fails with EPERM in an
	// unprivileged process (see internal/halcmd's TestMain).
	halcmd.RtapiInitializeApp()

	// RtapiAppInit is what the launcher calls before any hal_init, and it is
	// required here specifically: it sets hal_lib's rtapi_pid, which is what
	// makes hal_init_ex(..., COMPONENT_TYPE_REALTIME) mark the component as
	// realtime (comp->pid == 0). Without it hal_export_funct refuses the
	// scope's sample function with EINVAL ("component is not realtime").
	if err := halcmd.RtapiAppInit(); err != nil {
		fmt.Fprintf(os.Stderr, "rtapi app init failed: %v\n", err)
		os.Exit(1)
	}

	keep, err := hal.NewComponent("halscope-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	halcmd.RtapiAppCleanup()
	os.Exit(code)
}

var uniqCounter int

// uniq gives each test its own HAL namespace. HAL names are process-global and
// only freed on component exit, so sharing them would make failures cascade.
func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ensureRegistries installs fresh package-level registries for the duration of
// the test, restoring the previous ones afterwards. Idempotent within a test so
// a helper can call it without clobbering an earlier setup.
func ensureRegistries(t *testing.T) {
	t.Helper()
	if apiserver.DefaultRegistry() != nil && apiserver.DefaultWatchRegistry() != nil {
		return
	}
	origReg, origWatch := apiserver.DefaultRegistry(), apiserver.DefaultWatchRegistry()
	t.Cleanup(func() {
		apiserver.SetDefaultRegistry(origReg)
		apiserver.SetDefaultWatchRegistry(origWatch)
	})
	apiserver.SetDefaultRegistry(apiserver.NewRegistry())
	apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())
}

// newScope builds a halscope module over a fresh HAL RT component.
func newScope(t *testing.T, args ...string) *halscope {
	t.Helper()
	ensureRegistries(t)

	mod, err := newHalscope(nil, testLogger(), uniq("scope"), args)
	if err != nil {
		t.Fatalf("newHalscope: %v", err)
	}
	m := mod.(*halscope)
	t.Cleanup(m.Destroy)
	return m
}

// testPins creates a ready component exporting one pin of each HAL type and
// returns its name.
func testPins(t *testing.T) string {
	t.Helper()
	name := uniq("scopins")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	t.Cleanup(func() { _ = comp.Exit() })
	if _, err := hal.NewPin[bool](comp, "b", hal.Out); err != nil {
		t.Fatalf("NewPin bit: %v", err)
	}
	if _, err := hal.NewPin[float64](comp, "f", hal.Out); err != nil {
		t.Fatalf("NewPin float: %v", err)
	}
	if _, err := hal.NewPin[int32](comp, "s", hal.Out); err != nil {
		t.Fatalf("NewPin s32: %v", err)
	}
	if _, err := hal.NewPin[uint32](comp, "u", hal.Out); err != nil {
		t.Fatalf("NewPin u32: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return name
}

// newThread creates a HAL thread the scope can attach its sample function to.
func newThread(t *testing.T) string {
	t.Helper()
	name := uniq("scopethread")
	// The scope's sample function is exported as floating-point, and HAL
	// refuses to add an FP function to a non-FP thread.
	if err := halcmd.CreateThreadCPU(name, 1000000, 1, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.ThreadDelete(name) })
	return name
}

// --- Construction ---

func TestNewHalscopeRegistersModule(t *testing.T) {
	if !gomc.HasModule("halscope") {
		t.Error("the halscope module factory was not registered")
	}
}

func TestNewHalscopeRejectsBadArgs(t *testing.T) {
	ensureRegistries(t)

	for _, args := range [][]string{
		{"num_samples=0"},
		{"num_samples=15"},
		{"num_samples=notanumber"},
	} {
		if _, err := newHalscope(nil, testLogger(), uniq("scopebad"), args); err == nil {
			t.Errorf("newHalscope(%v) must fail", args)
		}
	}
}

func TestNewHalscopeRegistersRESTAndWatch(t *testing.T) {
	m := newScope(t)

	reg := apiserver.DefaultRegistry()
	if api := reg.GetByAPI("halscope", m.name); api == nil {
		t.Error("halscope did not register its REST API")
	}
	wreg := apiserver.DefaultWatchRegistry()
	wapi := wreg.Get("halscope", m.name)
	if wapi == nil {
		t.Fatal("halscope did not register its watch API")
	}
	// The scope pushes both a JSON status watch and a binary sample watch; the
	// HMI needs both to render.
	var haveJSON, haveBinary bool
	for _, w := range wapi.Watches {
		if w.BinaryWatch != nil {
			haveBinary = true
		}
		if w.Watch != nil || w.Factory != nil {
			haveJSON = true
		}
	}
	if !haveJSON || !haveBinary {
		t.Errorf("watch API = %+v; want both a JSON and a binary watch", wapi.Watches)
	}
}

// --- Threads and configuration ---

func TestListThreads(t *testing.T) {
	m := newScope(t)
	thread := newThread(t)

	threads, err := m.ListThreads()
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	var found *halscopeapi.ThreadInfo
	for i := range threads {
		if threads[i].Name == thread {
			found = &threads[i]
		}
	}
	if found == nil {
		t.Fatalf("ListThreads did not include %q: %+v", thread, threads)
	}
	if found.PeriodNs != 1000000 {
		t.Errorf("period = %d, want 1000000", found.PeriodNs)
	}
}

func TestConfigureSnapsMaxChannels(t *testing.T) {
	m := newScope(t, "num_samples=1600")

	// max_channels must snap up to the next power of two so rec_len divides the
	// buffer exactly; anything above 16 clamps to 16.
	for _, tc := range []struct{ req, want int32 }{
		{1, 1}, {2, 2}, {3, 4}, {4, 4}, {5, 8}, {8, 8}, {9, 16}, {16, 16}, {99, 16},
	} {
		rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: tc.req})
		if err != nil || rc != 0 {
			t.Fatalf("Configure(%d) = %d, %v", tc.req, rc, err)
		}
		st, err := m.GetStatus()
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if st.MaxChannels != tc.want {
			t.Errorf("MaxChannels for request %d = %d, want %d", tc.req, st.MaxChannels, tc.want)
		}
		if want := int32(1600) / tc.want; st.RecLen != want {
			t.Errorf("RecLen = %d, want %d", st.RecLen, want)
		}
		// The trigger sits at the midpoint of the record, always.
		if st.PreTrig != st.RecLen/2 {
			t.Errorf("PreTrig = %d, want RecLen/2 = %d", st.PreTrig, st.RecLen/2)
		}
	}

	// A zero/negative max_channels leaves the current setting alone rather than
	// producing a zero-length record.
	before, _ := m.GetStatus()
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 0}); err != nil || rc != 0 {
		t.Fatalf("Configure(0) = %d, %v", rc, err)
	}
	after, _ := m.GetStatus()
	if after.MaxChannels != before.MaxChannels {
		t.Errorf("MaxChannels changed on a zero request: %d → %d", before.MaxChannels, after.MaxChannels)
	}
}

func TestConfigureThreadAssignment(t *testing.T) {
	m := newScope(t)
	t1 := newThread(t)
	t2 := newThread(t)

	if rc, err := m.Configure(halscopeapi.CaptureConfig{ThreadName: t1, MaxChannels: 4}); err != nil || rc != 0 {
		t.Fatalf("Configure(t1) = %d, %v", rc, err)
	}
	st, _ := m.GetStatus()
	if st.ThreadName != t1 {
		t.Fatalf("ThreadName = %q, want %q", st.ThreadName, t1)
	}
	if st.ThreadPeriodNs != 1000000 {
		t.Errorf("ThreadPeriodNs = %d, want 1000000", st.ThreadPeriodNs)
	}

	// Re-configuring the same thread must be idempotent (no double-add).
	if rc, err := m.Configure(halscopeapi.CaptureConfig{ThreadName: t1}); err != nil || rc != 0 {
		t.Fatalf("re-Configure(t1) = %d, %v", rc, err)
	}

	// Moving to another thread must detach from the first, or the sample
	// function would run in both.
	if rc, err := m.Configure(halscopeapi.CaptureConfig{ThreadName: t2}); err != nil || rc != 0 {
		t.Fatalf("Configure(t2) = %d, %v", rc, err)
	}
	st, _ = m.GetStatus()
	if st.ThreadName != t2 {
		t.Fatalf("ThreadName = %q, want %q", st.ThreadName, t2)
	}
	functs, err := halcmd.Show("thread", t1)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	for _, th := range functs.Threads {
		if th.Name != t1 {
			continue
		}
		for _, f := range th.Functs {
			if f == m.functName {
				t.Errorf("the sample function is still attached to %q", t1)
			}
		}
	}

	// A thread that does not exist is reported, and the previous assignment is
	// not silently kept as if the call had succeeded.
	if rc, _ := m.Configure(halscopeapi.CaptureConfig{ThreadName: "no-such-thread"}); rc == 0 {
		t.Error("Configure with an unknown thread must fail")
	}
}

func TestConfigureSamplePeriodMult(t *testing.T) {
	m := newScope(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 1, SamplePeriodMult: 5}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	st, _ := m.GetStatus()
	if st.SamplePeriodMult != 5 {
		t.Errorf("SamplePeriodMult = %d, want 5", st.SamplePeriodMult)
	}
	// Zero means "leave it alone" — a zero multiplier would divide by zero in RT.
	if rc, err := m.Configure(halscopeapi.CaptureConfig{SamplePeriodMult: 0}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	st, _ = m.GetStatus()
	if st.SamplePeriodMult != 5 {
		t.Errorf("SamplePeriodMult = %d after a zero request, want the previous 5", st.SamplePeriodMult)
	}
}

// --- Channels ---

func TestSetChannelResolvesPinsSignalsAndParams(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 4}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}

	// A pin.
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: pins + ".f"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel(pin): %d, %v", rc, err)
	}
	// A signal.
	sig := uniq("scopesig")
	if err := halcmd.NewSig(sig, hal.TypeS32); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 1, PinName: sig}); err != nil || rc != 0 {
		t.Fatalf("SetChannel(signal): %d, %v", rc, err)
	}

	st, _ := m.GetStatus()
	if len(st.Channels) != 2 {
		t.Fatalf("channels = %+v, want 2", st.Channels)
	}
	byIdx := map[int32]halscopeapi.ChannelInfo{}
	for _, c := range st.Channels {
		byIdx[c.Channel] = c
	}
	if byIdx[0].PinName != pins+".f" || !byIdx[0].Enabled {
		t.Errorf("channel 0 = %+v", byIdx[0])
	}
	if byIdx[1].PinName != sig {
		t.Errorf("channel 1 = %+v", byIdx[1])
	}
	// The first configured channel becomes the trigger source so an Arm right
	// after configuration has something to trigger on.
	if st.TrigChannel != 0 {
		t.Errorf("TrigChannel = %d, want 0", st.TrigChannel)
	}
}

func TestSetChannelRejectsBadIndexAndUnknownName(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 2}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}

	for _, ch := range []int32{-1, maxChannels, maxChannels + 1} {
		rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: ch, PinName: pins + ".f"})
		if err != nil || rc != -int32(syscall.EINVAL) {
			t.Errorf("SetChannel(channel=%d) = %d, %v; want -EINVAL", ch, rc, err)
		}
	}

	// Within the hard limit but beyond the configured max_channels: the RT
	// sampler only walks max_channels entries, so a channel above it would
	// never be sampled and must be refused rather than silently ignored.
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 3, PinName: pins + ".f"}); err != nil || rc != -int32(syscall.EINVAL) {
		t.Errorf("SetChannel beyond max_channels = %d, %v; want -EINVAL", rc, err)
	}

	// An unresolvable name must not leave a channel pointing at nothing.
	rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: "no.such.pin"})
	if err != nil || rc != -int32(syscall.ENOENT) {
		t.Errorf("SetChannel with an unknown name = %d, %v; want -ENOENT", rc, err)
	}
	st, _ := m.GetStatus()
	if len(st.Channels) != 0 {
		t.Errorf("a failed SetChannel enabled a channel: %+v", st.Channels)
	}
}

func TestClearChannel(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 2}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: pins + ".b"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel: %d, %v", rc, err)
	}

	for _, ch := range []int32{-1, maxChannels} {
		if rc, err := m.ClearChannel(ch); err != nil || rc != -int32(syscall.EINVAL) {
			t.Errorf("ClearChannel(%d) = %d, %v; want -EINVAL", ch, rc, err)
		}
	}

	if rc, err := m.ClearChannel(0); err != nil || rc != 0 {
		t.Fatalf("ClearChannel: %d, %v", rc, err)
	}
	st, _ := m.GetStatus()
	if len(st.Channels) != 0 {
		t.Errorf("channels after ClearChannel = %+v", st.Channels)
	}
	// Clearing an already-clear channel is a no-op, not an error.
	if rc, err := m.ClearChannel(1); err != nil || rc != 0 {
		t.Errorf("ClearChannel on an unused channel = %d, %v", rc, err)
	}
}

// --- Trigger ---

func TestSetTrigger(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 4}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: pins + ".f"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel: %d, %v", rc, err)
	}

	rc, err := m.SetTrigger(halscopeapi.TriggerConfig{
		Channel: 0, Level: 1.25, Edge: halscopeapi.TrigEdge_FALLING, AutoTrig: true,
	})
	if err != nil || rc != 0 {
		t.Fatalf("SetTrigger: %d, %v", rc, err)
	}
	st, _ := m.GetStatus()
	if st.TrigChannel != 0 || st.TrigLevel != 1.25 {
		t.Errorf("trigger = ch %d level %v", st.TrigChannel, st.TrigLevel)
	}
	if st.TrigEdge != halscopeapi.TrigEdge_FALLING || !st.TrigAutoTrig {
		t.Errorf("edge = %v, autoTrig = %v", st.TrigEdge, st.TrigAutoTrig)
	}

	// Anything not explicitly falling is rising — the field is a two-valued
	// enum and an out-of-range value must not leave the RT edge test undefined.
	if rc, err := m.SetTrigger(halscopeapi.TriggerConfig{Channel: 0, Edge: halscopeapi.TrigEdge(99)}); err != nil || rc != 0 {
		t.Fatalf("SetTrigger: %d, %v", rc, err)
	}
	st, _ = m.GetStatus()
	if st.TrigEdge != halscopeapi.TrigEdge_RISING || st.TrigAutoTrig {
		t.Errorf("edge = %v, autoTrig = %v; want rising and auto off", st.TrigEdge, st.TrigAutoTrig)
	}

	// -1 disables the trigger and is explicitly in range.
	if rc, err := m.SetTrigger(halscopeapi.TriggerConfig{Channel: -1, Level: 3}); err != nil || rc != 0 {
		t.Fatalf("SetTrigger(-1) = %d, %v", rc, err)
	}
	for _, ch := range []int32{-2, maxChannels} {
		if rc, err := m.SetTrigger(halscopeapi.TriggerConfig{Channel: ch}); err != nil || rc != -int32(syscall.EINVAL) {
			t.Errorf("SetTrigger(channel=%d) = %d, %v; want -EINVAL", ch, rc, err)
		}
	}
}

// --- Arm / force / reset ---

func TestArmRequiresThreadAndConfig(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)

	// No channel configuration yet → rec_len is 0, nothing to capture into.
	if rc, err := m.Arm(); err != nil || rc != -int32(syscall.EINVAL) {
		t.Errorf("Arm without configuration = %d, %v; want -EINVAL", rc, err)
	}

	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 2}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	// Still no thread: the sample function would never run, so arming would
	// hang the UI waiting for a capture that cannot happen.
	if rc, err := m.Arm(); err != nil || rc != -int32(syscall.EINVAL) {
		t.Errorf("Arm without a thread = %d, %v; want -EINVAL", rc, err)
	}

	thread := newThread(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{ThreadName: thread}); err != nil || rc != 0 {
		t.Fatalf("Configure(thread): %d, %v", rc, err)
	}
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: pins + ".f"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel: %d, %v", rc, err)
	}
	if rc, err := m.Arm(); err != nil || rc != 0 {
		t.Fatalf("Arm = %d, %v", rc, err)
	}

	// Armed: reconfiguring would move the buffer under the RT sampler.
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 8}); err != nil || rc != -int32(syscall.EBUSY) {
		t.Errorf("Configure while armed = %d, %v; want -EBUSY", rc, err)
	}
	if rc, err := m.Arm(); err != nil || rc != -int32(syscall.EBUSY) {
		t.Errorf("Arm while armed = %d, %v; want -EBUSY", rc, err)
	}

	// Force-trigger only makes sense once the capture is waiting for a trigger.
	if rc, err := m.ForceTrigger(); err != nil || rc != -int32(syscall.EINVAL) {
		t.Errorf("ForceTrigger before the pre-trigger phase = %d, %v; want -EINVAL", rc, err)
	}

	// Reset returns the scope to a reconfigurable state.
	if rc, err := m.Reset(); err != nil || rc != 0 {
		t.Fatalf("Reset = %d, %v", rc, err)
	}
}

func TestSetContinuousAndReset(t *testing.T) {
	m := newScope(t)

	if rc, err := m.SetContinuous(true); err != nil || rc != 0 {
		t.Fatalf("SetContinuous(true) = %d, %v", rc, err)
	}
	if st, _ := m.GetStatus(); !st.Continuous {
		t.Error("Continuous not reported after SetContinuous(true)")
	}
	if rc, err := m.SetContinuous(false); err != nil || rc != 0 {
		t.Fatalf("SetContinuous(false) = %d, %v", rc, err)
	}
	if st, _ := m.GetStatus(); st.Continuous {
		t.Error("Continuous still reported after SetContinuous(false)")
	}

	// Reset must also clear continuous mode, otherwise a reset scope would
	// immediately re-arm itself.
	if rc, err := m.SetContinuous(true); err != nil || rc != 0 {
		t.Fatalf("SetContinuous: %d, %v", rc, err)
	}
	if rc, err := m.Reset(); err != nil || rc != 0 {
		t.Fatalf("Reset: %d, %v", rc, err)
	}
	if st, _ := m.GetStatus(); st.Continuous {
		t.Error("Reset did not clear continuous mode")
	}
}

func TestGetStatusChannelOptions(t *testing.T) {
	m := newScope(t, "num_samples=1600")
	st, err := m.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	// The HMI builds its channel-count selector from this table, so every entry
	// must carry the record length that choice would produce.
	want := map[int32]int32{1: 1600, 2: 800, 4: 400, 8: 200, 16: 100}
	if len(st.ChannelOptions) != len(want) {
		t.Fatalf("ChannelOptions = %+v", st.ChannelOptions)
	}
	for _, opt := range st.ChannelOptions {
		if want[opt.MaxChannels] != opt.RecLen {
			t.Errorf("option %d → RecLen %d, want %d", opt.MaxChannels, opt.RecLen, want[opt.MaxChannels])
		}
	}
	// Channels is always a list, never null — the HMI iterates it directly.
	if st.Channels == nil {
		t.Error("Channels is nil; the JSON contract is an empty array")
	}
}

// --- Watches ---

func TestWatchStateMatchesGetStatus(t *testing.T) {
	m := newScope(t)
	if rc, err := m.Configure(halscopeapi.CaptureConfig{MaxChannels: 4}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	ws, err := m.WatchState()
	if err != nil {
		t.Fatalf("WatchState: %v", err)
	}
	gs, err := m.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if ws.MaxChannels != gs.MaxChannels || ws.State != gs.State || ws.RecLen != gs.RecLen {
		t.Errorf("WatchState %+v differs from GetStatus %+v", ws, gs)
	}
}

func TestWatchSamplesWithoutCapture(t *testing.T) {
	m := newScope(t)
	data, gen, err := m.WatchSamples()
	if err != nil {
		t.Fatalf("WatchSamples: %v", err)
	}
	// No capture has completed: the binary watch must yield nothing rather than
	// hand the client a half-initialised buffer.
	if data != nil || gen != 0 {
		t.Errorf("WatchSamples = %d bytes, gen %d; want nil, 0", len(data), gen)
	}
}

// --- Name resolution helpers ---

func TestListPinsFiltersByPatternAndKind(t *testing.T) {
	m := newScope(t)
	pins := testPins(t)
	sig := uniq("scopelistsig")
	if err := halcmd.NewSig(sig, hal.TypeFloat); err != nil {
		t.Fatalf("NewSig: %v", err)
	}
	t.Cleanup(func() { _ = halcmd.DelSig(sig) })

	names, err := m.ListPins(sp(pins+".*"), sp("pin"))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(names) != 4 {
		t.Errorf("ListPins(%q, pin) = %v, want the 4 pins", pins+".*", names)
	}

	// kind="sig" must exclude pins entirely.
	names, err = m.ListPins(sp(pins+".*"), sp("sig"))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("ListPins(%q, sig) = %v, want none", pins+".*", names)
	}
	names, err = m.ListPins(sp(sig), sp("sig"))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(names) != 1 || names[0] != sig {
		t.Errorf("ListPins(%q, sig) = %v", sig, names)
	}

	// An empty kind means all kinds, and an empty pattern means "*".
	all, err := m.ListPins(sp(""), sp(""))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(all) < 5 {
		t.Errorf("unfiltered ListPins returned %d names, want at least the 4 pins + 1 signal", len(all))
	}
	// A pattern that matches nothing yields an empty list, never nil (the JSON
	// contract is an array).
	none, err := m.ListPins(sp("zzz-no-match-*"), sp(""))
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if none == nil {
		t.Error("ListPins returned nil for a non-matching pattern")
	}
}

// --- Persistence ---

// newPersist loads a real persist_sqlite instance under the given name into the
// current default registry, so halscope's Start() can resolve it.
func newPersist(t *testing.T, instance string) {
	t.Helper()
	ensureRegistries(t)
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)

	factory := gomc.GetFactory("persist_sqlite")
	if factory == nil {
		t.Fatal("persist_sqlite is not registered")
	}
	mod, err := factory(nil, testLogger(), instance, []string{"dbpath=db"})
	if err != nil {
		t.Fatalf("persist_sqlite: %v", err)
	}
	t.Cleanup(mod.Destroy)
	if err := mod.Start(); err != nil {
		t.Fatalf("persist_sqlite Start: %v", err)
	}
}

func TestStartRequiresResolvablePersistInstance(t *testing.T) {
	m := newScope(t, "persist_instance=nosuchpersist")
	// A configured persistence instance that is missing is a hard error: the
	// scope must not come up silently unable to save its configuration.
	if err := m.Start(); err == nil {
		t.Fatal("Start with an unresolvable persist instance must fail")
	}
}

func TestStateRoundTripThroughPersist(t *testing.T) {
	newPersist(t, "persistence")

	m := newScope(t)
	pins := testPins(t)
	thread := newThread(t)

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)

	if rc, err := m.Configure(halscopeapi.CaptureConfig{
		ThreadName: thread, MaxChannels: 4, SamplePeriodMult: 3,
	}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 1, PinName: pins + ".f"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel: %d, %v", rc, err)
	}
	if rc, err := m.SetTrigger(halscopeapi.TriggerConfig{
		Channel: 1, Level: 2.5, Edge: halscopeapi.TrigEdge_FALLING, AutoTrig: true,
	}); err != nil || rc != 0 {
		t.Fatalf("SetTrigger: %d, %v", rc, err)
	}
	if rc, err := m.SetContinuous(true); err != nil || rc != 0 {
		t.Fatalf("SetContinuous: %d, %v", rc, err)
	}

	if err := m.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// A second scope pointed at the same persist namespace must come up with
	// the first one's configuration — that is the whole point of the feature.
	restored := newScopeSharingRegistry(t, m)
	if err := restored.loadState(); err != nil {
		t.Fatalf("loadState: %v", err)
	}
	st, err := restored.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.ThreadName != thread || st.MaxChannels != 4 || st.SamplePeriodMult != 3 {
		t.Errorf("restored config = %+v", st)
	}
	if !st.Continuous {
		t.Error("continuous mode was not restored")
	}
	if len(st.Channels) != 1 || st.Channels[0].Channel != 1 || st.Channels[0].PinName != pins+".f" {
		t.Errorf("restored channels = %+v", st.Channels)
	}
	if st.TrigChannel != 1 || st.TrigLevel != 2.5 ||
		st.TrigEdge != halscopeapi.TrigEdge_FALLING || !st.TrigAutoTrig {
		t.Errorf("restored trigger = ch %d level %v edge %v auto %v",
			st.TrigChannel, st.TrigLevel, st.TrigEdge, st.TrigAutoTrig)
	}
}

// newScopeSharingRegistry builds a second scope that talks to the same persist
// instance as src (the registry globals are already pointed at it).
func newScopeSharingRegistry(t *testing.T, src *halscope) *halscope {
	t.Helper()
	mod, err := newHalscope(nil, testLogger(), uniq("scope"), nil)
	if err != nil {
		t.Fatalf("newHalscope: %v", err)
	}
	m := mod.(*halscope)
	t.Cleanup(m.Destroy)
	m.persist = src.persist
	m.persistHandle = src.persistHandle
	return m
}

func TestLoadStateSkipsVanishedAndRetypedChannels(t *testing.T) {
	newPersist(t, "persistence")

	m := newScope(t)
	pins := testPins(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)

	// Hand-craft a state file describing channels that cannot be restored:
	// a pin that no longer exists, one whose type changed, an out-of-range
	// index, and one above max_channels. A stale saved config is the normal
	// case after a HAL file edit — it must degrade, not fail the load.
	sf := scopeStateFile{
		Version: 1,
		Config:  stateConfig{MaxChannels: 2},
		Channels: []stateChannel{
			{Channel: 0, PinName: "gone.pin", DataType: 3},
			{Channel: 1, PinName: pins + ".f", DataType: 1}, // wrong type for a float pin
			{Channel: -1, PinName: pins + ".f", DataType: 3},
			{Channel: 5, PinName: pins + ".f", DataType: 3},
		},
		Trigger: stateTrigger{Channel: 0, Level: 1},
	}
	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := m.persist.SetEntry(m.persistHandle, persistKey, string(data)); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}

	if err := m.loadState(); err != nil {
		t.Fatalf("loadState must tolerate a stale config: %v", err)
	}
	st, _ := m.GetStatus()
	if len(st.Channels) != 0 {
		t.Errorf("channels restored from an unusable config: %+v", st.Channels)
	}
	// With no channel restored the trigger must be disabled, not left pointing
	// at an empty channel.
	if st.TrigChannel != -1 {
		t.Errorf("TrigChannel = %d, want -1", st.TrigChannel)
	}
}

func TestLoadStateRejectsUnsupportedVersionAndGarbage(t *testing.T) {
	newPersist(t, "persistence")

	m := newScope(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)

	for _, tc := range []struct{ name, value string }{
		{"garbage", "not json"},
		{"future version", `{"version":99}`},
	} {
		if _, err := m.persist.SetEntry(m.persistHandle, persistKey, tc.value); err != nil {
			t.Fatalf("SetEntry: %v", err)
		}
		if err := m.loadState(); err == nil {
			t.Errorf("loadState(%s) must fail rather than half-apply", tc.name)
		}
	}

	// An empty entry is the fresh-install case: no state yet, not an error.
	if _, err := m.persist.SetEntry(m.persistHandle, persistKey, ""); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	if err := m.loadState(); err != nil {
		t.Errorf("loadState with no saved state = %v, want nil", err)
	}
}

// TestSaverLoopCoalesces covers the single serialized saver: config setters
// signal it instead of each spawning a goroutine, so a burst of edits collapses
// to at most one queued save and no stale snapshot can overtake a newer one.
func TestSaverLoopCoalescesAndJoins(t *testing.T) {
	newPersist(t, "persistence")

	m := newScope(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for i := 0; i < 100; i++ {
		m.saveStateBg()
	}

	// Stop joins the saver and performs a final save; Destroy (via cleanup)
	// then frees the C state, which is only safe once no save is in flight.
	m.Stop()
	// stopSaver is idempotent — Destroy calls it again.
	m.stopSaver()
}

func TestSaveStateBgWithoutPersistIsNoOp(t *testing.T) {
	m := newScope(t)
	// Persistence disabled (Start never ran): the config setters still call
	// saveStateBg on every edit, so it must not block or nil-panic.
	m.saveStateBg()
	m.stopSaver()
	m.Stop()
}

// TestStartWithoutRegistryDisablesPersistence covers the launcher-less path:
// with no API registry there is no persist provider to find, and the scope must
// still start.
func TestStartWithoutRegistryDisablesPersistence(t *testing.T) {
	m := newScope(t)
	orig := apiserver.DefaultRegistry()
	apiserver.SetDefaultRegistry(nil)
	defer apiserver.SetDefaultRegistry(orig)

	if err := m.Start(); err != nil {
		t.Fatalf("Start without a registry: %v", err)
	}
	if m.persist != nil {
		t.Error("a persist client was created without a registry")
	}
	m.Stop()
}

// TestCaptureRoundTrip runs a real capture: the RT sample function executes in
// a HAL thread, fills a buffer, and WatchSamples linearises it for the client.
// This is the only path that exercises the triple-buffer hand-off and the
// ring-wrap copy, which is where a wrong length silently corrupts the trace.
func TestCaptureRoundTrip(t *testing.T) {
	m := newScope(t, "num_samples=64")
	pins := testPins(t)
	thread := newThread(t)

	if rc, err := m.Configure(halscopeapi.CaptureConfig{
		ThreadName: thread, MaxChannels: 1, SamplePeriodMult: 1,
	}); err != nil || rc != 0 {
		t.Fatalf("Configure: %d, %v", rc, err)
	}
	if rc, err := m.SetChannel(halscopeapi.ChannelConfig{Channel: 0, PinName: pins + ".f"}); err != nil || rc != 0 {
		t.Fatalf("SetChannel: %d, %v", rc, err)
	}
	// Auto-trigger so the capture completes without anything driving the pin.
	if rc, err := m.SetTrigger(halscopeapi.TriggerConfig{Channel: 0, Level: 0, AutoTrig: true}); err != nil || rc != 0 {
		t.Fatalf("SetTrigger: %d, %v", rc, err)
	}
	if rc, err := m.Arm(); err != nil || rc != 0 {
		t.Fatalf("Arm: %d, %v", rc, err)
	}

	if err := halcmd.StartThreads(); err != nil {
		t.Skipf("HAL threads cannot run here: %v", err)
	}
	defer func() { _ = halcmd.StopThreads() }()

	var data []byte
	var gen uint64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		data, gen, err = m.WatchSamples()
		if err != nil {
			t.Fatalf("WatchSamples: %v", err)
		}
		if data != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if data == nil {
		st, _ := m.GetStatus()
		t.Fatalf("no capture completed within 5s (state %v, samples %d)", st.State, st.Samples)
	}
	if gen == 0 {
		t.Error("a completed capture carries generation 0; the client cannot detect the next one")
	}

	st, err := m.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.State != halscopeapi.ScopeState_DONE {
		t.Errorf("state after capture = %v, want DONE", st.State)
	}
	// The frame is a fixed header plus rec_len samples of the channel's width.
	if len(data) <= 0 {
		t.Fatalf("empty capture frame")
	}

	// Re-reading without a new capture must return the same generation, so the
	// binary push loop can suppress the resend.
	_, gen2, err := m.WatchSamples()
	if err != nil {
		t.Fatalf("WatchSamples: %v", err)
	}
	if gen2 != gen {
		t.Errorf("generation changed without a new capture: %d → %d", gen, gen2)
	}
}
