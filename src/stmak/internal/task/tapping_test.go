// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func (m *recordingMotion) FeedScaleEnable(e int32) error {
	m.event(fmt.Sprintf("fs=%d", e))
	return nil
}

func (m *recordingMotion) SpindleScaleEnable(s, e int32) error {
	m.event(fmt.Sprintf("ss%d=%d", s, e))
	return nil
}

func (m *recordingMotion) TapCycleEnable(e int32) error {
	m.event(fmt.Sprintf("tap=%d", e))
	return nil
}

// TestTapping_G84ReachesMotionAsOneCycle: a G84 reaches motion as a tap cycle
// around the whole tap -- in and out -- and the cycle's own override disables
// and re-enables stay out of motion: motion suspends both overrides for the
// cycle's segments itself (TAP_ACTIVE), so there is no enable for an abort to
// drop from the queue and leave an override switched off.
//
// M51 is the other half: the speed-override calls used to stop at the canon's
// flag, so M51 P0 never reached motion.
func TestTapping_G84ReachesMotionAsOneCycle(t *testing.T) {
	mot := runNGCViaInterpRec(t, `G21 G90 G94 G17
S500 M3
G0 X0 Y0 Z5
G98 G84 X10 Y0 Z-5 R2 F500
G80
M51 P0
G1 X20 F100
M51 P1
M2
`)
	ev := mot.eventLog()

	start := strings.Index(ev, "tap=1")
	end := strings.Index(ev, "tap=0")
	if start < 0 || end < start {
		t.Fatalf("no tap=1 ... tap=0 bracket in %q", ev)
	}
	inside := ev[start:end]
	if n := strings.Count(inside, "line"); n < 2 {
		t.Errorf("tap cycle %q holds %d lines, want the feed in and the feed out", inside, n)
	}
	if strings.Contains(inside, "fs=") || strings.Contains(inside, "ss0=") {
		t.Errorf("override enables sent inside the tap cycle: %q", inside)
	}
	if strings.Count(ev, "tap=1") != 1 || strings.Count(ev, "tap=0") != 1 {
		t.Errorf("one hole, want one cycle: %q", ev)
	}

	// M51 P0 / P1 after the cycle, around the G1, in program order.
	after := ev[end:]
	off := strings.Index(after, "ss0=0")
	on := strings.Index(after, "ss0=1")
	if off < 0 || on < off || !strings.Contains(after[off:on], "line") {
		t.Errorf("want ss0=0 line ss0=1 after the cycle (M51 P0, G1, M51 P1), got %q", after)
	}
}

// TestTapping_ResetInsideCycleRestoresOverrides: an interpreter reset with the
// read-ahead inside a tap cycle never sees the cycle's closing calls. The
// canon flags have to go back to what the cycle found -- the next synch reads
// them into the interpreter as the M48-M51 state -- including an override the
// program had disabled before the cycle.
func TestTapping_ResetInsideCycleRestoresOverrides(t *testing.T) {
	task, mot := newBlendCanonTask(t)
	c := task.canon

	c.DisableSpeedOverride(1) // M51 P0 $1 before the cycle: stays off
	c.StartTappingCycle()
	c.DisableFeedOverride()
	c.DisableSpeedOverride(0)
	c.OnReset()
	task.DrainQueue()
	task.StopSequencer()

	s := c.state
	if s.tapping {
		t.Error("still inside a tap cycle after the reset")
	}
	if !s.feedOverrideEnabled || !s.speedOverrideEnabled[0] {
		t.Errorf("after reset: feed=%v speed0=%v, want both restored to enabled",
			s.feedOverrideEnabled, s.speedOverrideEnabled[0])
	}
	if s.speedOverrideEnabled[1] {
		t.Error("after reset: speed1 enabled, want it left disabled as the program had it")
	}
	if got := mot.eventLog(); got != "ss1=0 tap=1" {
		t.Errorf("motion saw %q, want %q (nothing of the cycle's overrides)", got, "ss1=0 tap=1")
	}
}

// gateCmd holds the sequencer at a known command until the test lets it go,
// so a pause can be placed inside a tap cycle deterministically.
type gateCmd struct{ entered, release chan struct{} }

func (c *gateCmd) Execute(*Task) error { close(c.entered); <-c.release; return nil }
func (c *gateCmd) Wait() WaitType      { return WaitNone }
func (c *gateCmd) String() string      { return "Gate" }

// markCmd records that the sequencer executed it.
type markCmd struct {
	name string
	mot  *recordingMotion
}

func (c *markCmd) Execute(*Task) error { c.mot.event(c.name); return nil }
func (c *markCmd) Wait() WaitType      { return WaitNone }
func (c *markCmd) String() string      { return "Mark(" + c.name + ")" }

// TestTapping_SequencerDefersPauseAndStep: inside a tap cycle the sequencer
// must not stop between commands. Motion runs the cycle's feed to the end
// under a pause; a sequencer that paused meanwhile would hold the spindle stop
// or reversal that follows it, and the spindle would keep turning with Z
// standing at the bottom of the hole. Single-step did exactly that on every
// G84: it paused after the feed in. The pause or step stays latched and takes
// effect after the command that ends the cycle.
func TestTapping_SequencerDefersPauseAndStep(t *testing.T) {
	for _, mode := range []string{"pause", "step"} {
		t.Run(mode, func(t *testing.T) {
			task, mot := newBlendCanonTask(t)
			defer task.StopSequencer()
			mark := func(n string) QueuedCmd { return &markCmd{n, mot} }
			events := mot.eventLog
			gate := &gateCmd{make(chan struct{}), make(chan struct{})}

			for _, c := range []QueuedCmd{
				&TapCycleCmd{Active: true},
				gate,
				&LinearMoveCmd{Pos: Pose{Z: -5}, Vel: 5, IniMaxVel: 10, Acc: 100, MotionType: 2, ID: 1},
				mark("reversal"),
				&TapCycleCmd{Active: false},
				mark("after"),
			} {
				if err := task.EnqueueCmd(c); err != nil {
					t.Fatal(err)
				}
			}
			<-gate.entered
			task.mu.Lock()
			task.interpState = InterpReading
			if mode == "pause" {
				_ = task.doPauseLocked()
			} else {
				task.stepping = true
				task.mu.Unlock()
			}
			close(gate.release)

			waitUntil(t, "the cycle to finish under the "+mode, func() bool {
				return strings.Contains(events(), "tap=0")
			})
			time.Sleep(100 * time.Millisecond) // "after" must not come
			if got := events(); !strings.Contains(got, "line reversal tap=0") || strings.Contains(got, "after") {
				t.Fatalf("events %q: want the whole cycle and nothing after it", got)
			}

			task.mu.Lock()
			_ = task.doResumeLocked()
			waitUntil(t, "the sequencer to go on after resume", func() bool {
				return strings.Contains(events(), "after")
			})
		})
	}
}

func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !pred() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
