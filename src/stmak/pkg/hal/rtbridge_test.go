// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests for the RT bridge: TriBuf, PinMap, Doorbell
// (docs/dev/PNPTASK_RT_BOUNDARY.md §3). The cyclic function that uses all three
// lives in internal/hallib/rtbridgetest.
package hal_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/internal/hallib/rtbridgetest"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// tortureFrame is big enough that a copy takes a while: a torn frame would
// show up as Fill not matching Seq.
type tortureFrame struct {
	Seq  uint64
	Fill [61]uint64
}

// TestTriBufWholeFrames hammers one buffer from a writer and a reader
// goroutine. The reader must only ever see whole frames, never time going
// backwards, a new frame exactly when fresh says so, and in the end the last
// one. The C exchange is the same code the realtime side runs.
func TestTriBufWholeFrames(t *testing.T) {
	b, err := hal.NewTriBuf[tortureFrame]()
	if err != nil {
		t.Fatalf("NewTriBuf: %v", err)
	}
	defer b.Free()

	const last = 200_000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var f tortureFrame
		for s := uint64(1); s <= last; s++ {
			f.Seq = s
			for i := range f.Fill {
				f.Fill[i] = s
			}
			b.Publish(&f)
		}
	}()

	var f tortureFrame
	var prev uint64
	fresh, stale := 0, 0
	deadline := time.Now().Add(20 * time.Second)
	for prev != last {
		if time.Now().After(deadline) {
			t.Fatalf("reader stuck at seq %d", prev)
		}
		isNew := b.Read(&f)
		for i, v := range f.Fill {
			if v != f.Seq {
				t.Fatalf("torn frame: seq %d, fill[%d] = %d", f.Seq, i, v)
			}
		}
		switch {
		case f.Seq < prev:
			t.Fatalf("time went backwards: seq %d after %d", f.Seq, prev)
		case isNew && f.Seq == prev:
			t.Fatalf("fresh read returned the same frame (seq %d)", f.Seq)
		case !isNew && f.Seq != prev:
			t.Fatalf("stale read returned a new frame: seq %d after %d", f.Seq, prev)
		}
		if isNew {
			fresh++
		} else {
			stale++
		}
		prev = f.Seq
	}
	wg.Wait()
	t.Logf("%d fresh reads, %d stale reads over %d publishes", fresh, stale, last)

	// Nothing published since: the next read is the same frame, not fresh.
	if b.Read(&f) || f.Seq != last {
		t.Errorf("read after the end: fresh or seq %d, want stale %d", f.Seq, last)
	}
}

// TestTriBufFrameInsideAStruct: a module keeps its frames inside a struct that
// also holds pointers (pnptask's rtIO holds the buffers themselves). Passing a
// pointer into such a struct to C makes cgo check the whole allocation and
// panic, so Publish and Read must never do that.
func TestTriBufFrameInsideAStruct(t *testing.T) {
	b, err := hal.NewTriBuf[tortureFrame]()
	if err != nil {
		t.Fatalf("NewTriBuf: %v", err)
	}
	defer b.Free()
	owner := &struct {
		buf   *hal.TriBuf[tortureFrame]
		out   tortureFrame
		in    tortureFrame
		names []string
	}{buf: b, names: []string{"x"}}

	owner.out.Seq = 3
	owner.buf.Publish(&owner.out)
	if !owner.buf.Read(&owner.in) || owner.in.Seq != 3 {
		t.Errorf("round trip through a struct with pointers: seq %d", owner.in.Seq)
	}
}

func TestTriBufNeverPublished(t *testing.T) {
	b, err := hal.NewTriBuf[tortureFrame]()
	if err != nil {
		t.Fatalf("NewTriBuf: %v", err)
	}
	defer b.Free()

	f := tortureFrame{Seq: 99}
	if b.Read(&f) {
		t.Error("fresh before anything was published")
	}
	if f.Seq != 0 {
		t.Errorf("seq = %d before anything was published, want the zero frame", f.Seq)
	}
}

// TestTriBufRefusesGoPointers: a frame is copied into C memory as bytes, where
// the collector cannot see what it points at.
func TestTriBufRefusesGoPointers(t *testing.T) {
	check := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, hal.ErrNotPlainData) {
			t.Errorf("%s: got %v, want ErrNotPlainData", name, err)
		}
	}
	_, err := hal.NewTriBuf[struct{ P *int }]()
	check("pointer field", err)
	_, err = hal.NewTriBuf[struct{ S []int }]()
	check("slice field", err)
	_, err = hal.NewTriBuf[struct{ S string }]()
	check("string field", err)
	_, err = hal.NewTriBuf[struct{ M map[int]int }]()
	check("map field", err)
	_, err = hal.NewTriBuf[struct{ P unsafe.Pointer }]()
	check("unsafe.Pointer field", err)
	_, err = hal.NewTriBuf[struct{ A [2]struct{ I any } }]()
	check("interface in a nested array", err)

	if _, err := hal.NewTriBuf[struct{}](); err == nil {
		t.Error("zero-size frame accepted")
	}
}

// guardFrame is a Go-side frame for the binding guards: a PinMap only uses
// offsets, so it need not be a C type.
type guardFrame struct {
	B  uint8
	_  [3]uint8
	U  uint32
	F  float64
	B2 uint8
}

func TestPinMapGuards(t *testing.T) {
	comp, err := hal.NewComponent("test-pinmap-guards")
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	in, _ := hal.NewPin[bool](comp, "in", hal.In)
	in2, _ := hal.NewPin[bool](comp, "in2", hal.In)
	out, _ := hal.NewPin[bool](comp, "out", hal.Out)
	io, _ := hal.NewPin[bool](comp, "io", hal.IO)
	uout, _ := hal.NewPin[uint32](comp, "u", hal.Out)
	fout, _ := hal.NewPin[float64](comp, "f", hal.Out)

	var g guardFrame
	base := unsafe.Pointer(&g)
	size := unsafe.Sizeof(g)
	refused := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, hal.ErrPinMap) {
			t.Errorf("%s: got %v, want ErrPinMap", name, err)
		}
	}

	t.Run("in map", func(t *testing.T) {
		m, err := hal.NewPinMap(hal.In, size, 3)
		if err != nil {
			t.Fatalf("NewPinMap: %v", err)
		}
		defer m.Free()

		if err := hal.BindBit(m, in, base, &g.B); err != nil {
			t.Fatalf("BindBit: %v", err)
		}
		// Reading an output pin is fine for an In map.
		if err := hal.BindBit(m, out, base, &g.B2); err != nil {
			t.Errorf("BindBit of an Out pin into an In map: %v", err)
		}
		refused("same field twice", hal.BindBit(m, in2, base, &g.B))
		refused("same pin twice", hal.BindBit(m, in, base, (*uint8)(unsafe.Add(base, 1))))
		if err := hal.BindU32(m, uout, base, &g.U); err != nil {
			t.Fatalf("third binding: %v", err)
		}
		refused("map full", hal.BindBit(m, in2, base, (*uint8)(unsafe.Add(base, 2))))
		if m.Len() != 3 {
			t.Errorf("Len = %d after the refusals, want 3", m.Len())
		}
	})

	t.Run("out map", func(t *testing.T) {
		m, err := hal.NewPinMap(hal.Out, size, 8)
		if err != nil {
			t.Fatalf("NewPinMap: %v", err)
		}
		defer m.Free()

		refused("In pin", hal.BindBit(m, in, base, &g.B))
		refused("IO pin", hal.BindBit(m, io, base, &g.B))
		if err := hal.BindBit(m, out, base, &g.B); err != nil {
			t.Fatalf("BindBit: %v", err)
		}
		refused("same pin twice", hal.BindBit(m, out, base, &g.B2))

		var elsewhere uint8
		refused("field outside the frame", hal.BindBit(m, out, base, &elsewhere))

		// A float at offset 4 of the frame: inside it, but not 8-aligned.
		mis := (*float64)(unsafe.Add(base, 4))
		refused("misaligned field", hal.BindFloat(m, fout, base, mis))
		if err := hal.BindFloat(m, fout, base, &g.F); err != nil {
			t.Errorf("BindFloat aligned: %v", err)
		}
	})

	t.Run("constructor", func(t *testing.T) {
		if _, err := hal.NewPinMap(hal.IO, size, 1); err == nil {
			t.Error("IO map accepted")
		}
		if _, err := hal.NewPinMap(hal.In, 0, 1); err == nil {
			t.Error("zero frame size accepted")
		}
		if _, err := hal.NewPinMap(hal.In, size, 0); err == nil {
			t.Error("zero capacity accepted")
		}
	})
}

// bridgePins is one component with one pin of each type in each direction.
type bridgePins struct {
	comp      *hal.Component
	inB, outB *hal.Pin[bool]
	inS, outS *hal.Pin[int32]
	inU, outU *hal.Pin[uint32]
	inF, outF *hal.Pin[float64]
}

func newBridgePins(t *testing.T, name string, rig *rtbridgetest.Rig, rt bool) *bridgePins {
	t.Helper()
	var comp *hal.Component
	var err error
	if rt {
		comp, err = hal.NewRTComponent(name)
	} else {
		comp, err = hal.NewComponent(name)
	}
	if err != nil {
		t.Fatalf("component: %v", err)
	}
	p := &bridgePins{comp: comp}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
	}
	p.inB, err = hal.NewPin[bool](comp, "in-b", hal.In)
	must(err)
	p.inS, err = hal.NewPin[int32](comp, "in-s", hal.In)
	must(err)
	p.inU, err = hal.NewPin[uint32](comp, "in-u", hal.In)
	must(err)
	p.inF, err = hal.NewPin[float64](comp, "in-f", hal.In)
	must(err)
	p.outB, err = hal.NewPin[bool](comp, "out-b", hal.Out)
	must(err)
	p.outS, err = hal.NewPin[int32](comp, "out-s", hal.Out)
	must(err)
	p.outU, err = hal.NewPin[uint32](comp, "out-u", hal.Out)
	must(err)
	p.outF, err = hal.NewPin[float64](comp, "out-f", hal.Out)
	must(err)
	must(rig.BindIn(p.inB, p.inS, p.inU, p.inF))
	must(rig.BindOut(p.outB, p.outS, p.outU, p.outF))
	return p
}

// TestRTBridgeDirect runs the bridge function by hand, cycle by cycle: pins
// in reach Go in one frame, Go's frame reaches the pins only when the function
// runs, and the doorbell rings for a change and only for a change.
func TestRTBridgeDirect(t *testing.T) {
	rig, err := rtbridgetest.New(4)
	if err != nil {
		t.Fatalf("rtbridgetest.New: %v", err)
	}
	defer rig.Free()
	p := newBridgePins(t, "test-rtbridge-direct", rig, false)
	defer func() { _ = p.comp.Exit() }()
	if err := p.comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	var rf rtbridgetest.RTFrame

	// First run: primes and rings once.
	rig.Invoke(1_000_000)
	if !rig.ToGo.Read(&rf) || rf.Cycle != 1 {
		t.Fatalf("first frame: cycle %d, want a fresh cycle 1", rf.Cycle)
	}
	if c := rig.Bell.Change(); c != 1 {
		t.Errorf("change = %d after the first run, want 1", c)
	}
	if cy := rig.Bell.Cycle(); cy != 1 {
		t.Errorf("cycle = %d after the first run, want 1", cy)
	}

	// Inputs change: all four arrive in one frame, and it rings.
	p.inB.Set(true)
	p.inS.Set(-5)
	p.inU.Set(7)
	p.inF.Set(2.5)
	rig.Invoke(1_000_000)
	rig.ToGo.Read(&rf)
	if rf.In.B != 1 || rf.In.S != -5 || rf.In.U != 7 || rf.In.F != 2.5 {
		t.Errorf("inputs in the frame: %+v", rf.In)
	}
	if c := rig.Bell.Change(); c != 2 {
		t.Errorf("change = %d after an input change, want 2", c)
	}

	// Nothing changes: the frame still comes, the bell does not ring.
	rig.Invoke(1_000_000)
	if !rig.ToGo.Read(&rf) || rf.Cycle != 3 {
		t.Errorf("idle run: cycle %d, want a fresh cycle 3", rf.Cycle)
	}
	if c := rig.Bell.Change(); c != 2 {
		t.Errorf("change = %d after an idle run, want still 2", c)
	}
	if rig.ToGo.Read(&rf) {
		t.Error("second read without a run reported a fresh frame")
	}

	// Go publishes: the pins follow only when the function runs.
	rig.ToRT.Publish(&rtbridgetest.GoFrame{Seq: 1, Out: rtbridgetest.Pins{B: 1, S: -9, U: 11, F: 4.25}})
	if p.outB.Get() || p.outU.Get() != 0 {
		t.Error("outputs changed before the function ran")
	}
	rig.Invoke(1_000_000)
	if !p.outB.Get() || p.outS.Get() != -9 || p.outU.Get() != 11 || p.outF.Get() != 4.25 {
		t.Errorf("outputs: b=%v s=%d u=%d f=%g", p.outB.Get(), p.outS.Get(), p.outU.Get(), p.outF.Get())
	}
	rig.ToGo.Read(&rf)
	if rf.GoSeq != 1 {
		t.Errorf("GoSeq = %d, want 1", rf.GoSeq)
	}
	if c := rig.Bell.Change(); c != 3 {
		t.Errorf("change = %d after Go's frame, want 3", c)
	}
}

// TestRTBridgeOnThread runs the bridge function on a real HAL thread: Wait
// wakes on an input change, the reader never sees time go backwards, and after
// the unload barrier Wait reports the function stalled.
func TestRTBridgeOnThread(t *testing.T) {
	rig, err := rtbridgetest.New(4)
	if err != nil {
		t.Fatalf("rtbridgetest.New: %v", err)
	}
	p := newBridgePins(t, "test-rtbridge-thread", rig, true)
	defer func() { _ = p.comp.Exit() }()
	if err := p.comp.ExportFunct("cycle", rtbridgetest.Funct(), rig.Arg(), true, false); err != nil {
		t.Fatalf("ExportFunct: %v", err)
	}
	if err := p.comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	const thread = "test-rtbridge-servo"
	if err := halcmd.CreateThreadCPU(thread, 1_000_000, 1, -1); err != nil {
		t.Fatalf("CreateThreadCPU: %v", err)
	}
	defer func() { _ = halcmd.ThreadDelete(thread) }()
	if err := halcmd.AddF("test-rtbridge-thread.cycle", thread, -1); err != nil {
		t.Fatalf("AddF: %v", err)
	}
	if err := halcmd.StartThreads(); err != nil {
		t.Fatalf("StartThreads: %v", err)
	}
	defer func() { _ = halcmd.StopThreads() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The first run rings.
	seen, err := rig.Bell.Wait(ctx, 0, 0, time.Second)
	if err != nil {
		t.Fatalf("Wait for the first run: %v", err)
	}

	// An input change wakes the waiter, and the frame it then reads has it.
	p.inU.Set(42)
	if seen, err = rig.Bell.Wait(ctx, seen, 0, time.Second); err != nil {
		t.Fatalf("Wait for the input change: %v", err)
	}
	var rf rtbridgetest.RTFrame
	rig.ToGo.Read(&rf)
	if rf.In.U != 42 {
		t.Errorf("frame after the wake: in.u = %d, want 42", rf.In.U)
	}

	// Go's frame reaches the pin within a few servo cycles.
	rig.ToRT.Publish(&rtbridgetest.GoFrame{Seq: 7, Out: rtbridgetest.Pins{U: 99}})
	if _, err = rig.Bell.Wait(ctx, seen, 0, time.Second); err != nil {
		t.Fatalf("Wait for Go's frame: %v", err)
	}
	if got := p.outU.Get(); got != 99 {
		t.Errorf("out-u = %d after the wake, want 99", got)
	}

	// A reader polling much faster than the thread: never backwards, fresh
	// exactly when the cycle moved.
	prev := uint32(0)
	end := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(end) {
		isNew := rig.ToGo.Read(&rf)
		cy := uint32(rf.Cycle)
		switch {
		case prev != 0 && cy < prev:
			t.Fatalf("cycle went backwards: %d after %d", cy, prev)
		case prev != 0 && isNew && cy == prev:
			t.Fatalf("fresh read with the same cycle %d", cy)
		case prev != 0 && !isNew && cy != prev:
			t.Fatalf("stale read with a new cycle %d after %d", cy, prev)
		}
		prev = cy
	}

	// The unload sequence, in the launcher's order; then the bell says stalled.
	if _, err := halcmd.DelFunctsByComp(p.comp.ID()); err != nil {
		t.Fatalf("DelFunctsByComp: %v", err)
	}
	if err := halcmd.WaitCycleAdvance(); err != nil {
		t.Fatalf("WaitCycleAdvance: %v", err)
	}
	_, err = rig.Bell.Wait(ctx, rig.Bell.Change(), 0, 50*time.Millisecond)
	if !errors.Is(err, hal.ErrRTStalled) {
		t.Errorf("Wait after the unload: got %v, want ErrRTStalled", err)
	}

	rig.Free()
}

func TestDoorbellWait(t *testing.T) {
	d, err := hal.NewDoorbell()
	if err != nil {
		t.Fatalf("NewDoorbell: %v", err)
	}
	defer d.Free()

	// A count that already differs returns at once.
	if c, err := d.Wait(context.Background(), 5, 0, 0); err != nil || c != 0 {
		t.Errorf("Wait(seen=5) = %d, %v; want 0, nil at once", c, err)
	}

	// Nothing rings: the context ends the wait.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := d.Wait(ctx, 0, 0, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait with nothing ringing: got %v, want DeadlineExceeded", err)
	}

	// Nothing ticks either: the stall timeout ends it.
	if _, err := d.Wait(context.Background(), 0, 0, 20*time.Millisecond); !errors.Is(err, hal.ErrRTStalled) {
		t.Errorf("Wait with no cycles: got %v, want ErrRTStalled", err)
	}
}
