// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// Tests for the naive-CAM detector (canon_naivecam.go), the port of 2.9
// emccanon's chained_points/see_segment/flush_segments. Each test drives REAL
// canon calls through the real canon -> sequencer -> motion boundary (same
// harness as the blend integration tests) and asserts WRITTEN rules from the
// 2.9 reference implementation.

import (
	"bytes"
	"log/slog"
	"os"
	"sync"
	"testing"
)

// naivecamMotion records the ordered event stream of motion calls (lines,
// circles, synced outputs) plus the ids of emitted lines, so tests can assert
// both merging and emission ORDER relative to sync-I/O.
type naivecamMotion struct {
	recordingMotion
	mu     sync.Mutex
	events []string
	ids    []int32
}

func (m *naivecamMotion) SetLine(pos Pose, vel, iniMaxvel, acc float64, mt int32, id int32, feedUpm float64, ij int32) error {
	m.mu.Lock()
	m.events = append(m.events, "line")
	m.ids = append(m.ids, id)
	m.mu.Unlock()
	return m.recordingMotion.SetLine(pos, vel, iniMaxvel, acc, mt, id, feedUpm, ij)
}

func (m *naivecamMotion) SetCircle(pos Pose, center, normal Cartesian, turn int32, vel, iniMaxvel, acc float64, mt int32, id int32, feedUpm float64) error {
	m.mu.Lock()
	m.events = append(m.events, "circle")
	m.mu.Unlock()
	return m.recordingMotion.SetCircle(pos, center, normal, turn, vel, iniMaxvel, acc, mt, id, feedUpm)
}

func (m *naivecamMotion) SetAoutSynched(index int32, startValue, endValue float64) error {
	m.mu.Lock()
	m.events = append(m.events, "aout")
	m.mu.Unlock()
	return nil
}

func (m *naivecamMotion) SetSpindlesync(sync float64, motionType int32) error {
	m.mu.Lock()
	if sync != 0 {
		m.events = append(m.events, "sync-on")
	} else {
		m.events = append(m.events, "sync-off")
	}
	m.mu.Unlock()
	return nil
}

func newNaivecamTask(t *testing.T) (*Task, *naivecamMotion) {
	t.Helper()
	mot := &naivecamMotion{}
	st := &testStatus{}
	st.inPosition.Store(true)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	task := NewTask(mot, &mockIO{}, st, logger)
	applyBlendLimits(task)
	task.maxAcceleration = 600
	task.numJoints = 3
	task.canon.UseLengthUnits(2) // CANON_UNITS_MM: prog units == mm, 1:1
	task.canon.SetFeedRate(600)  // 10 mm/s
	task.StartSequencer()
	return task, mot
}

func collectEvents(t *testing.T, task *Task, mot *naivecamMotion) ([]string, []int32, []recMove) {
	t.Helper()
	task.DrainQueue()
	task.StopSequencer()
	return mot.events, mot.ids, mot.moves
}

// enableNaivecam puts the canon in G64-continuous with the given Q tolerance
// (mm), like `G64 P<tol> Q<tol>`.
func enableNaivecam(c *Canon, tol float64) {
	c.SetMotionControlMode(CanonContinuous, tol)
	c.SetNaivecamTolerance(tol)
}

// Colinear-within-Q micro segments merge into ONE move to the LAST point,
// carrying the LAST point's line number (2.9 flush_segments: pos.line_no,
// merged chord).
func TestNaivecam_MergesColinearFeeds(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 0.1)

	c.StraightFeed(11, 1, 0.01, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(12, 2, -0.01, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(13, 3, 0, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	events, ids, moves := collectEvents(t, task, mot)
	nLines := 0
	for _, e := range events {
		if e == "line" {
			nLines++
		}
	}
	if nLines != 1 {
		t.Fatalf("expected 1 merged line, got %d (events %v, moves %+v)", nLines, events, moves)
	}
	checkPos(t, moves[0], "line", 3, 0, 0)
	if got := task.lookupMotionLine(ids[0]); got != 13 {
		t.Errorf("merged move line number = %d, want 13 (the LAST chained point's line)", got)
	}
}

// A point whose chord deviation exceeds Q breaks the chain: the buffered
// prefix flushes as one move, the new point starts a fresh chain.
func TestNaivecam_DeviationBreaksChain(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 0.1)

	c.StraightFeed(1, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(2, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	// Chord (0,0)->(3,5) leaves the buffered points ~2.6mm off: not linkable.
	c.StraightFeed(3, 3, 5, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	_, _, moves := collectEvents(t, task, mot)
	if len(moves) != 2 {
		t.Fatalf("expected 2 moves (chain broken), got %d: %+v", len(moves), moves)
	}
	checkPos(t, moves[0], "line", 2, 0, 0)
	checkPos(t, moves[1], "line", 3, 5, 0)
}

// Synchronized I/O flushes the chain FIRST (2.9 SET_AUX_OUTPUT_VALUE /
// SET_MOTION_OUTPUT_BIT flush before appending): the buffered motion must
// reach motion before the synced output, or the output attaches to the wrong
// segment — the emission-ordering contract shared with the M62-M68 path.
func TestNaivecam_SyncIOFlushesChainFirst(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 10)

	c.StraightFeed(1, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(2, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	c.SetMotionOutputValue(0, 7) // M67 E0 Q7
	c.StraightFeed(3, 3, 0, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	events, _, moves := collectEvents(t, task, mot)
	want := []string{"line", "aout", "line"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	checkPos(t, moves[0], "line", 2, 0, 0)
	checkPos(t, moves[1], "line", 3, 0, 0)
}

// In the XY plane under G64 Q, an arc whose chord deviation is below Q is
// flattened into chained points instead of emitted as a circle (2.9 ARC_FEED
// head), merging with surrounding straight segments.
func TestNaivecam_ArcFlattening(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	c.SelectPlane(CanonPlaneXY)
	enableNaivecam(c, 1.0)

	c.StraightFeed(1, 10, 0, 0, 0, 0, 0, 0, 0, 0)
	// 180-degree arc from (10,0) to (10.2,0), radius 0.1: deviation from the
	// chord is 0.1 < Q=1 -> flattened, no circle emitted.
	c.ArcFeed(2, 10.2, 0, 10.1, 0, 1, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(3, 20, 0, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	events, _, moves := collectEvents(t, task, mot)
	for _, e := range events {
		if e == "circle" {
			t.Fatalf("arc under Q tolerance must be flattened, got a circle (events %v)", events)
		}
	}
	last := moves[len(moves)-1]
	checkPos(t, last, "line", 20, 0, 0)
}

// An arc whose deviation exceeds Q emits normally as a circle, flushing the
// open chain first so ordering is preserved.
func TestNaivecam_BigArcStaysArc(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	c.SelectPlane(CanonPlaneXY)
	enableNaivecam(c, 0.1)

	c.StraightFeed(1, 10, 0, 0, 0, 0, 0, 0, 0, 0)
	// Radius-5 half circle: deviation 5 >> Q=0.1.
	c.ArcFeed(2, 20, 0, 15, 0, 1, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	events, _, _ := collectEvents(t, task, mot)
	want := []string{"line", "circle"}
	if len(events) != 2 || events[0] != want[0] || events[1] != want[1] {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// Without G64 Q (tolerance 0) every feed emits separately in order — the
// one-deep buffer must not change the emitted stream.
func TestNaivecam_QZeroPassthrough(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	c.SetMotionControlMode(CanonContinuous, 0.5)
	// naivecamTol stays 0

	c.StraightFeed(1, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(2, 2, 1, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(3, 3, 0, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	_, _, moves := collectEvents(t, task, mot)
	if len(moves) != 3 {
		t.Fatalf("expected 3 separate moves with Q=0, got %d: %+v", len(moves), moves)
	}
	checkPos(t, moves[0], "line", 1, 0, 0)
	checkPos(t, moves[1], "line", 2, 1, 0)
	checkPos(t, moves[2], "line", 3, 0, 0)
}

// The chain caps at naivecamMaxChain points (2.9: size() > 100), so unbounded
// colinear input still flushes periodically.
func TestNaivecam_ChainCap(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 1.0)

	for i := 1; i <= 205; i++ {
		c.StraightFeed(int32(i), float64(i)*0.1, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	c.Finish()

	_, _, moves := collectEvents(t, task, mot)
	if len(moves) < 2 {
		t.Fatalf("expected the 100-point cap to split the chain, got %d moves", len(moves))
	}
	last := moves[len(moves)-1]
	checkPos(t, last, "line", 20.5, 0, 0)
}

// The merged move carries the LAST chained point's interp state tag, pinned so
// a later line's tagMotionRange bracket cannot overwrite it (restore_from_tag
// must restore the modal state of the line that produced the motion, not the
// line executing when the chain happened to flush).
func TestNaivecam_TagPinning(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	s := c.state
	enableNaivecam(c, 1.0)

	s.currentTag = []byte{1}
	c.StraightFeed(1, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	s.currentTag = []byte{2}
	c.StraightFeed(2, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	// Flush happens "during line 3": the id is allocated now, inside what would
	// be line 3's tagMotionRange bracket.
	s.currentTag = []byte{3}
	startID := c.serial()
	c.Dwell(0)
	task.tagMotionRange(startID, c.serial(), nil, nil, nil)

	_, ids, _ := collectEvents(t, task, mot)
	if len(ids) != 1 {
		t.Fatalf("expected 1 merged move, got ids %v", ids)
	}
	info, ok := task.motionInfoAndPrune(ids[0])
	if !ok {
		t.Fatalf("no motionMap entry for id %d", ids[0])
	}
	if !bytes.Equal(info.Tag, []byte{2}) {
		t.Errorf("merged move tag = %v, want [2] (LAST chained point's tag, pinned)", info.Tag)
	}
	if info.LineNo != 2 {
		t.Errorf("merged move line = %d, want 2", info.LineNo)
	}
}

// Zero-length chains are dropped without emission but still advance the
// endpoint (2.9 flush_segments calls canonUpdateEndPoint unconditionally).
func TestNaivecam_ZeroLengthDropped(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 0.1)

	c.StraightFeed(1, 0, 0, 0, 0, 0, 0, 0, 0, 0) // zero-distance
	c.Finish()
	c.StraightFeed(2, 5, 0, 0, 0, 0, 0, 0, 0, 0)
	c.Finish()

	_, _, moves := collectEvents(t, task, mot)
	if len(moves) != 1 {
		t.Fatalf("expected the zero-length move dropped, got %d moves: %+v", len(moves), moves)
	}
	checkPos(t, moves[0], "line", 5, 0, 0)
}

// A zero-length feed is KEPT when spindle-synched — the segment must still
// reach the TP for its sync semantics (2.9 flush_segments:
// `(vel && acc) || canon.spindle[n].synched`). G95 counts as synched: 2.9's
// SET_FEED_RATE starts velocity-mode sync whenever feed_mode != 0.
func TestNaivecam_ZeroLengthKeptWhenG95Synched(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 0.1)

	c.SetFeedMode(0, 1)                          // G95 units-per-rev
	c.SetFeedRate(3.0)                           // per-rev F word → StartSpeedFeedSynch(…, 1)
	c.StraightFeed(1, 0, 0, 0, 0, 0, 0, 0, 0, 0) // zero-distance, synched
	c.Finish()

	events, _, moves := collectEvents(t, task, mot)
	if len(moves) != 1 {
		t.Fatalf("expected the zero-length synched move kept, got %d moves: %+v", len(moves), moves)
	}
	want := []string{"sync-on", "line"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Fatalf("expected events %v (F word starts velocity sync before the move), got %v", want, events)
	}
}

// A traverse under G95 is sync-bracketed: stop sync, traverse, restart sync
// (2.9 STRAIGHT_TRAVERSE) — the rapid must not run spindle-synched.
func TestG95_TraverseSyncBracket(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon

	c.SetFeedMode(0, 1)                               // G95
	c.SetFeedRate(3.0)                                // sync-on
	c.StraightFeed(1, 5, 0, 0, 0, 0, 0, 0, 0, 0)      // synched feed
	c.StraightTraverse(2, 10, 0, 0, 0, 0, 0, 0, 0, 0) // bracketed rapid
	c.Finish()

	events, _, _ := collectEvents(t, task, mot)
	want := []string{"sync-on", "line", "sync-off", "line", "sync-on"}
	if len(events) != len(want) {
		t.Fatalf("expected events %v, got %v", want, events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("expected events %v, got %v", want, events)
		}
	}
}
