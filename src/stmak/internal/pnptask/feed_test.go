// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pnptask/dztest"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/pnproute"
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

// TestMotctlCommandCancelsTheFeed: a motctl command -- an abort here -- goes
// out only once the cycle function has applied the cancel of the leg feed,
// so no record follows it into motmod.
func TestMotctlCommandCancelsTheFeed(t *testing.T) {
	m, mot := feedModule(t)
	for id := int32(1); id <= 5; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()
	runFeed(t, m, mot)
	runFeed(t, m, mot) // 1 and 2 are out

	var fedAfter []int32
	hook := homeHook{fakeMotion: newFakeMotion(m.cfg.NumJoints), at: func() {
		// The command is being sent: whatever is fed from here on follows it.
		m.rt.runRT = func() {
			m.rt.invoke()
			fed, _ := mot.Take()
			fedAfter = append(fedAfter, fedIDs(fed)...)
		}
	}}
	m.rt.runRT = func() { // the runs before the command is sent
		m.rt.invoke()
		_, _ = mot.Take()
	}
	cm := committedMotion{motionControl: hook, rt: m.rt}
	if err := cm.JointHome(-1); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 5; run++ {
		m.rt.runRT()
	}
	if len(fedAfter) != 0 {
		t.Fatalf("records %v went to motmod after the command", fedAfter)
	}
	m.rt.read()
	if !m.rt.feedThrough() {
		t.Error("the feed is not through after the cancel")
	}
}

// TestMotctlCommandDropsTheOldLegsRefusals: a motctl command ends the leg
// feed, and with it the refusals of that leg -- also one made in the run that
// still fed from Go's frame from before the cancel, after the loop read its
// last frame. None of them is left pending to fail what comes next.
func TestMotctlCommandDropsTheOldLegsRefusals(t *testing.T) {
	m, mot := feedModule(t)
	mot.Refuse(3, 4)
	for id := int32(1); id <= 4; id++ {
		feedLineID(t, m, id)
	}
	m.rt.commit()
	runFeed(t, m, mot)
	runFeed(t, m, mot) // 1 and 2 are out; the loop's frame is from the run of 2

	before := m.rt.sent
	stale := true
	m.rt.runRT = func() {
		if stale {
			// The first run after the commit took Go's frame before it was
			// published, and fed (and had refused) record 3.
			stale = false
			m.rt.toRT.Publish(&before)
			m.rt.invoke()
			m.rt.toRT.Publish(&m.rt.sent)
		} else {
			m.rt.invoke()
		}
		_, _ = mot.Take()
	}
	cm := committedMotion{motionControl: newFakeMotion(m.cfg.NumJoints), rt: m.rt}
	if err := cm.SetFree(); err != nil {
		t.Fatal(err)
	}
	m.rt.runRT()
	m.rt.read()
	if seq, id, _ := m.rt.feedErr(); seq != 1 || id != 3 {
		t.Fatalf("refusal: seq %d id %d, want 1 3 (the case did not happen)", seq, id)
	}
	if m.rt.feedErrPending() {
		t.Error("the old leg's refusal is pending after the motctl command")
	}
}

// --- the loop with the feed ----------------------------------------------------

// newSmoothLegFixture is a job fixture whose job 10 -> 20 has a leg around
// the zone of zones_between.dxf, smoothed: a chain of arcs and lines, many
// records long.
func newSmoothLegFixture(t *testing.T) *jobFixture {
	t.Helper()
	f := newJobFixtureOpts(t, fixtureOpts{
		files: map[string]string{zonesA: "zones_between.dxf", zonesB: fixtureClear},
		tweak: func(c *Config) { c.SmoothPaths = true },
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.mot.resetCalls()
	return f
}

// TestLegFeedLoopActsMidLeg: the loop is not held while a leg goes out. Its
// machine-on drops as the first arc of a long leg reaches motion; the loop's
// teardown comes while records of that leg are still in the feed, and none
// of them follows the Abort.
func TestLegFeedLoopActsMidLeg(t *testing.T) {
	f := newSmoothLegFixture(t)
	var hooked bool
	var pending, movesAtAbort atomic.Int32
	pending.Store(-1)
	f.mot.onMove = func(mc moveCall) { // the control goroutine, f.mu held
		if mc.arc && !hooked {
			hooked = true
			f.m.pins.machineOn.Set(false)
		}
	}
	f.mot.onAbort = func() { // the control goroutine, after the cancel was applied
		if pending.Load() < 0 {
			pending.Store(int32(uint32(f.m.rt.out.feed_end) - uint32(f.m.rt.in.fed)))
			movesAtAbort.Store(int32(len(f.mot.moves)))
		}
	}

	startJobTo(f, 10, 20)
	f.eventually("the abort", func() bool { return pending.Load() >= 0 })
	f.cycles(20)
	if p := pending.Load(); p <= 0 {
		t.Errorf("the abort came with %d records of the leg outstanding: the loop waited for the leg to go out", p)
	}
	if n, at := len(f.mot.moveList()), int(movesAtAbort.Load()); n != at {
		t.Errorf("%d moves reached motion after the Abort", n-at)
	}
}

// TestLegFeedRefusedSegmentEndsTheJob: a segment motmod refuses ends the job
// with MOTION_ERROR, and nothing of the leg behind it reaches motion. After
// Quittieren the next job runs: the loop's cancel let go of the held feed.
func TestLegFeedRefusedSegmentEndsTheJob(t *testing.T) {
	f := newSmoothLegFixture(t)
	f.feed.Refuse(3, 4) // EMCMOT_COMMAND_BAD_EXEC

	f.runJob(10, 20, 0)
	if id := f.errorID(); id != errMotionError {
		t.Fatalf("error-id %d (%v), want MOTION_ERROR", id, id)
	}
	for _, m := range f.mot.moveList() {
		if m.id >= 3 {
			t.Errorf("segment %d reached motion after segment 3 was refused", m.id)
		}
	}
	if f.bit("busy") {
		t.Error("busy still up")
	}

	f.feed.Refuse(0, 0)
	f.clearError()
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("the job after the refusal")
	if len(f.mot.moveList()) == 0 {
		t.Error("no move reached motion in the job after the refusal")
	}
}

// TestLegFeedRefusesALegThatDoesNotFit: a leg with more segments than the
// ring has room for is refused with MOTION_ERROR before any of it is written;
// what is still in the ring counts against the room.
func TestLegFeedRefusesALegThatDoesNotFit(t *testing.T) {
	m, _ := feedModule(t)
	c := newControl(m)
	legOf := func(n int) *leg {
		return &leg{route: &pnproute.Route{Waypoints: make([]pnproute.Point, n)}}
	}

	if err := c.checkLegRoom(legOf(feedCap - legFeedExtra)); err != nil {
		t.Fatalf("a leg that fits the empty ring: %v", err)
	}
	var f *pnpFault
	if err := c.checkLegRoom(legOf(feedCap - legFeedExtra + 1)); !errors.As(err, &f) || f.id != errMotionError {
		t.Fatalf("a leg one record too long: %v, want MOTION_ERROR", err)
	}
	if m.rt.out.feed_end != 0 {
		t.Fatalf("records written for a refused leg: feed_end %d", m.rt.out.feed_end)
	}

	feedLineID(t, m, 1) // one record not yet fed
	if err := c.checkLegRoom(legOf(feedCap - legFeedExtra)); err == nil {
		t.Error("a leg that fits only an empty ring accepted with a record outstanding")
	}
}

// TestLegFeedLegRefusedInPlanningDoesNotMove: a leg whose planning fails at a
// middle segment (out of the axis limits) ends the job with MOTION_ERROR, and
// none of its segments written before the bad one reaches motion -- not with
// the job's last commit, and not later.
func TestLegFeedLegRefusedInPlanningDoesNotMove(t *testing.T) {
	f := newSmoothLegFixture(t)
	f.m.limits.AxisMinPos[0] = 95 // the leg bulges to X = 86.9

	f.runJob(10, 20, 0)
	f.requireError("a leg out of the limits", errMotionError)
	if f.bit("busy") {
		t.Fatal("busy still up")
	}
	f.cycles(300)
	for _, m := range f.mot.moveList() {
		t.Errorf("a move reached motion from a leg refused in planning: %+v", m.pos)
	}
}

// TestLegFeedParkRefusedInPlanningDoesNotMove: the same for a park, whose
// leg around the zone bulges to X = 85.3.
func TestLegFeedParkRefusedInPlanningDoesNotMove(t *testing.T) {
	f := newJobFixtureOpts(t, fixtureOpts{
		files: map[string]string{zonesA: "zones_between.dxf", zonesB: fixtureClear},
		tweak: func(c *Config) {
			c.SmoothPaths = true
			c.HasPark = true
			c.Park.X, c.Park.Y = 100, 380
			c.ParkZ = 15
		},
	})
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.m.limits.AxisMinPos[0] = 95
	f.mot.resetCalls()
	f.press(f.m.pins.park)
	f.eventually("the park's error", func() bool { return f.bit("error") })
	f.eventually("busy to clear", func() bool { return !f.bit("busy") })
	f.requireError("a park out of the limits", errMotionError)
	f.cycles(300)
	for _, m := range f.mot.moveList() {
		if m.pos.X != 100 || m.pos.Y != 100 {
			t.Errorf("a move reached motion from a park refused in planning: %+v", m.pos)
		}
	}
}

// TestLegFeedRefusalWithMachineOffIsDropped: a refusal that comes in the
// frame in which machine-on drops is not the fault (MACHINE_OFF is), and it
// does not come back later: the next switch-on enables, and no MOTION_ERROR
// is latched.
func TestLegFeedRefusalWithMachineOffIsDropped(t *testing.T) {
	f := newSmoothLegFixture(t)
	f.mot.onMove = func(mc moveCall) { // the control goroutine, f.mu held
		if mc.id == 3 {
			f.m.pins.machineOn.Set(false)
		}
	}
	f.feed.Refuse(4, 4)

	f.runJob(10, 20, 0)
	f.requireError("machine-on dropped mid-leg", errMachineOff)

	f.mot.onMove = nil
	f.feed.Refuse(0, 0)
	f.clearError()
	f.m.pins.machineOn.Set(true)
	f.cycles(300)
	if f.bit("error") {
		t.Fatalf("switching on again latched %s", f.errorID())
	}
	if !f.bit("machine-is-on") {
		t.Error("machine-is-on stays low after switching on again")
	}
}
