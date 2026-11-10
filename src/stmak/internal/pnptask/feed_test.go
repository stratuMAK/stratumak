// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

import (
	"errors"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pnptask/dztest"
)

// The leg feed at the function level (pnp_rt.h, the leg feed;
// PNPTASK_RT_BOUNDARY.md §13.4): the cycle function run by hand against the
// fake mot provider, which records what it is fed.

// feedModule is rtModule with the fake provider's trajectory intake attached,
// and a first frame read.
func feedModule(t *testing.T) (*pnptaskModule, *dztest.FakeMot) {
	t.Helper()
	m, _ := rtModule(t)
	mot := dztest.NewFakeMot()
	mot.EnableTraj()
	m.deadzone.setMot(mot.Ptr())
	t.Cleanup(func() {
		m.deadzone.setMot(nil)
		mot.Free()
	})
	m.rt.invoke()
	m.rt.read()
	return m, mot
}

// feedLineID writes a line record for segment id, to X = id.
func feedLineID(t *testing.T, m *pnptaskModule, id int32) {
	t.Helper()
	if err := m.rt.feedLine(motctl.Pose{X: float64(id), Z: 5}, 10, 9, 100, motionTypeTraverse, id, 0, -1); err != nil {
		t.Fatalf("feedLine %d: %v", id, err)
	}
}

// runFeed runs the function once, reads its frame and returns what the fake
// was fed in that run.
func runFeed(t *testing.T, m *pnptaskModule, mot *dztest.FakeMot) []dztest.Fed {
	t.Helper()
	m.rt.invoke()
	m.rt.read()
	fed, err := mot.Take()
	if err != nil {
		t.Fatal(err)
	}
	return fed
}

// fedIDs is the segment ids of fed segments (a term cond is -1).
func fedIDs(fed []dztest.Fed) []int32 {
	ids := make([]int32, 0, len(fed))
	for _, f := range fed {
		if f.Kind == dztest.FedTermCond {
			ids = append(ids, -1)
			continue
		}
		ids = append(ids, f.ID)
	}
	return ids
}

// TestFeedOneRecordPerCycle: a commit's records go to motmod one per run, in
// the order written, carrying what motctl's calls carried; the frame counts a
// record fed from the next frame on, whose snapshot has seen it.
func TestFeedOneRecordPerCycle(t *testing.T) {
	m, mot := feedModule(t)

	if err := m.rt.feedTermCond(tpTermCondParabolic, 0.25); err != nil {
		t.Fatal(err)
	}
	feedLineID(t, m, 1)
	if err := m.rt.feedCircle(motctl.Pose{X: 2, Y: 3, Z: 5}, motctl.Cartesian{X: 2, Y: 1, Z: 5},
		motctl.Cartesian{Z: 1}, -1, 20, 15, 200, motionTypeTraverse, 2, 0); err != nil {
		t.Fatal(err)
	}
	if got := runFeed(t, m, mot); len(got) != 0 {
		t.Fatalf("fed before the commit: %v", fedIDs(got))
	}

	m.rt.commit()
	var all []dztest.Fed
	for run := 1; run <= 3; run++ {
		got := runFeed(t, m, mot)
		if len(got) != 1 {
			t.Fatalf("run %d fed %d records, want 1", run, len(got))
		}
		if m.rt.feedThrough() {
			t.Fatalf("run %d: the frame says through with its own record not yet seen", run)
		}
		if f := uint32(m.rt.in.fed); f != uint32(run-1) {
			t.Fatalf("run %d: frame fed = %d, want %d (the records the snapshot has seen)", run, f, run-1)
		}
		all = append(all, got...)
	}
	if got := runFeed(t, m, mot); len(got) != 0 || !m.rt.feedThrough() {
		t.Fatalf("after the last record: fed %v, through %v; want nothing and through", fedIDs(got), m.rt.feedThrough())
	}

	tc, ln, ci := all[0], all[1], all[2]
	if tc.Kind != dztest.FedTermCond || tc.Cond != tpTermCondParabolic || tc.Tolerance != 0.25 {
		t.Errorf("term cond: %+v", tc)
	}
	if ln.Kind != dztest.FedLine || ln.ID != 1 || ln.Pos[0] != 1 || ln.Pos[2] != 5 ||
		ln.Vel != 10 || ln.IniMaxvel != 9 || ln.Acc != 100 || ln.Turn != -1 || ln.MotionType != motionTypeTraverse {
		t.Errorf("line: %+v", ln)
	}
	if ci.Kind != dztest.FedCircle || ci.ID != 2 || ci.Pos[1] != 3 || ci.Center != [3]float64{2, 1, 5} ||
		ci.Normal != [3]float64{0, 0, 1} || ci.Turn != -1 || ci.Vel != 20 || ci.IniMaxvel != 15 || ci.Acc != 200 {
		t.Errorf("circle: %+v", ci)
	}
}

// TestFeedNothingBeyondFeedEnd: records written after the last commit wait
// for the next one.
func TestFeedNothingBeyondFeedEnd(t *testing.T) {
	m, mot := feedModule(t)
	feedLineID(t, m, 1)
	feedLineID(t, m, 2)
	m.rt.commit()
	feedLineID(t, m, 3) // not committed

	var ids []int32
	for run := 0; run < 6; run++ {
		ids = append(ids, fedIDs(runFeed(t, m, mot))...)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("fed %v, want [1 2]", ids)
	}
	m.rt.commit()
	if got := fedIDs(runFeed(t, m, mot)); len(got) != 1 || got[0] != 3 {
		t.Fatalf("after the next commit: fed %v, want [3]", got)
	}
}

// TestFeedCancelSkips: a cancel voids what is not fed yet; the feed goes on
// with what is written after it.
func TestFeedCancelSkips(t *testing.T) {
	m, mot := feedModule(t)
	for id := int32(1); id <= 5; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()
	runFeed(t, m, mot)
	runFeed(t, m, mot) // 1 and 2 are out

	m.rt.cancelFeed()
	m.rt.commit()
	for run := 0; run < 4; run++ {
		if got := runFeed(t, m, mot); len(got) != 0 {
			t.Fatalf("fed %v after the cancel", fedIDs(got))
		}
	}
	if !m.rt.feedThrough() {
		t.Error("a cancelled feed is not through")
	}

	feedLineID(t, m, 6)
	m.rt.commit()
	if got := fedIDs(runFeed(t, m, mot)); len(got) != 1 || got[0] != 6 {
		t.Fatalf("after the cancel: fed %v, want [6]", got)
	}
}

// TestFeedEstopSkips: under estop nothing of a feed goes to motmod, and the
// feed is through at once; after the estop a new leg feeds as usual.
func TestFeedEstopSkips(t *testing.T) {
	m, mot := feedModule(t)
	for id := int32(1); id <= 3; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()
	runFeed(t, m, mot) // 1 is out

	m.pins.estopOn.Set(true)
	for run := 0; run < 4; run++ {
		if got := runFeed(t, m, mot); len(got) != 0 {
			t.Fatalf("fed %v under estop", fedIDs(got))
		}
	}
	if !m.rt.feedThrough() {
		t.Error("under estop the feed is not through")
	}

	m.pins.estopOn.Set(false)
	m.rt.cancelFeed() // the loop's teardown
	feedLineID(t, m, 4)
	m.rt.commit()
	if got := fedIDs(runFeed(t, m, mot)); len(got) != 1 || got[0] != 4 {
		t.Fatalf("after the estop: fed %v, want [4]", got)
	}
}

// TestFeedRefusedHolds: a record motmod refuses is counted with its id and
// status, and nothing follows it until the loop's cancel moves feed_begin
// past it; the cancel voids the rest of that leg.
func TestFeedRefusedHolds(t *testing.T) {
	m, mot := feedModule(t)
	mot.Refuse(2, 4)
	for id := int32(1); id <= 3; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()

	var ids []int32
	for run := 0; run < 6; run++ {
		ids = append(ids, fedIDs(runFeed(t, m, mot))...)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("fed %v, want [1]: nothing after the refused record", ids)
	}
	seq, id, status := m.rt.feedErr()
	if seq != 1 || id != 2 || status != 4 {
		t.Fatalf("refusal: seq %d id %d status %d, want 1 2 4", seq, id, status)
	}
	if m.rt.feedThrough() {
		t.Fatal("a held feed is through")
	}

	// Without a cancel a new record does not get past the refusal either.
	feedLineID(t, m, 4)
	m.rt.commit()
	if got := runFeed(t, m, mot); len(got) != 0 {
		t.Fatalf("fed %v behind a refusal", fedIDs(got))
	}

	m.rt.cancelFeed()
	feedLineID(t, m, 5)
	m.rt.commit()
	if got := fedIDs(runFeed(t, m, mot)); len(got) != 1 || got[0] != 5 {
		t.Fatalf("after the cancel: fed %v, want [5]", got)
	}
	if seq, _, _ := m.rt.feedErr(); seq != 1 {
		t.Errorf("refusals counted %d, want 1", seq)
	}
}

// TestFeedRingWraps: the ring is reused round and round, in order, and a
// record that finds it full is refused rather than written over one not fed.
func TestFeedRingWraps(t *testing.T) {
	m, mot := feedModule(t)
	next, want := int32(1), int32(1)
	for round := 0; round < 3; round++ {
		for i := 0; i < feedCap*3/4; i++ {
			feedLineID(t, m, next)
			next++
		}
		m.rt.commit()
		for !m.rt.feedThrough() {
			for _, id := range fedIDs(runFeed(t, m, mot)) {
				if id != want {
					t.Fatalf("round %d: fed %d, want %d", round, id, want)
				}
				want++
			}
		}
	}
	if want != next {
		t.Fatalf("fed up to %d, written up to %d", want-1, next-1)
	}

	for i := 0; i < feedCap; i++ {
		feedLineID(t, m, next)
		next++
	}
	if err := m.rt.feedLine(motctl.Pose{}, 1, 1, 1, motionTypeTraverse, next, 0, -1); !errors.Is(err, errFeedFull) {
		t.Fatalf("a record into a full ring: %v, want errFeedFull", err)
	}
}

// TestFeedRingsAtTheEnd: the doorbell rings when a feed starts (the commit
// moved feed_end) and when it is through, not for every record.
func TestFeedRingsAtTheEnd(t *testing.T) {
	m, mot := feedModule(t)
	runFeed(t, m, mot)
	for id := int32(1); id <= 5; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()

	var rings []int
	for run := 0; run < 7; run++ {
		before := uint32(m.rt.in.change)
		runFeed(t, m, mot)
		if uint32(m.rt.in.change) != before {
			rings = append(rings, run)
		}
	}
	// run 0 applies the commit (not through any more) and feeds 1, runs 1-4
	// feed 2-5, run 5's frame has seen 5: through.
	if len(rings) != 2 || rings[0] != 0 || rings[1] != 5 {
		t.Fatalf("rang in runs %v, want [0 5]", rings)
	}
}

// TestFeedRefusalRings: a refusal rings in the run it happens.
func TestFeedRefusalRings(t *testing.T) {
	m, mot := feedModule(t)
	runFeed(t, m, mot)
	mot.Refuse(3, 4)
	for id := int32(1); id <= 5; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()
	runFeed(t, m, mot) // the commit's ring, 1 out
	runFeed(t, m, mot) // 2 out

	before := uint32(m.rt.in.change)
	runFeed(t, m, mot) // 3 refused
	if seq, _, _ := m.rt.feedErr(); seq != 1 {
		t.Fatalf("refusals %d, want 1", seq)
	}
	if uint32(m.rt.in.change) == before {
		t.Error("the refusal did not ring")
	}
}
