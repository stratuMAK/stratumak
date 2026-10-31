// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"math"
	"sync"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
)

// Z corner arcs (D52): lift, travel and descent as one stream.

// zCornerOpts is a job fixture with Z_CORNER_RADIUS r, globally and on every
// station (as the loader resolves it; the stations hold their own copy of
// their section), then tweak.
func zCornerOpts(r float64, tweak func(*Config), prep func(*pnptaskModule)) fixtureOpts {
	return fixtureOpts{
		tweak: func(c *Config) {
			c.ZCornerRadius = r
			if tweak != nil {
				tweak(c)
			}
		},
		prep: func(_ *testing.T, m *pnptaskModule) {
			for _, tr := range m.world.trays {
				tr.cfg.ZCornerRadius = r
			}
			for _, p := range m.world.procs {
				p.cfg.ZCornerRadius = r
			}
			if prep != nil {
				prep(m)
			}
		},
	}
}

// zArcCheck walks a job's moves from the start pose and checks every Z corner
// arc and the stream around it; it returns the number of lift and descent
// arcs.
func zArcCheck(t *testing.T, moves []moveCall, start motctl.Pose, height float64) (lifts, descents int) {
	t.Helper()
	prev := start
	for i, m := range moves {
		cur := m.pos
		xyMoves := math.Hypot(cur.X-prev.X, cur.Y-prev.Y) > 1e-9
		if m.arc && m.normal.Z == 0 {
			n := math.Hypot(m.normal.X, m.normal.Y)
			if math.Abs(n-1) > 1e-9 || m.turn != 0 {
				t.Fatalf("move %d: Z corner arc with normal %+v, turn %d", i, m.normal, m.turn)
			}
			r := math.Abs(cur.Z - prev.Z)
			d0 := math.Sqrt(sq(prev.X-m.center.X) + sq(prev.Y-m.center.Y) + sq(prev.Z-m.center.Z))
			d1 := math.Sqrt(sq(cur.X-m.center.X) + sq(cur.Y-m.center.Y) + sq(cur.Z-m.center.Z))
			if math.Abs(d0-r) > 1e-9 || math.Abs(d1-r) > 1e-9 || math.Abs(math.Hypot(cur.X-prev.X, cur.Y-prev.Y)-r) > 1e-9 {
				t.Fatalf("move %d: not a quarter circle of radius %g: %+v from %+v", i, r, m, prev)
			}
			// The arc lies in the vertical plane of its horizontal direction:
			// the normal is perpendicular to it.
			if dx, dy := (cur.X-prev.X)/r, (cur.Y-prev.Y)/r; math.Abs(dx*m.normal.X+dy*m.normal.Y) > 1e-9 {
				t.Fatalf("move %d: normal %+v not perpendicular to the arc's direction", i, m.normal)
			}
			if m.termCond != tpTermCondParabolic {
				t.Errorf("move %d: Z corner arc queued with term cond %d", i, m.termCond)
			}
			if !(m.vel > 0) || m.iniMaxvel > m.vel+1e-9 || !(m.acc > 0) {
				t.Errorf("move %d: vel %g, ini_maxvel %g, acc %g", i, m.vel, m.iniMaxvel, m.acc)
			}
			// Tangent joins: a lift arc comes out of a vertical stroke (or a
			// standstill) and a descent arc goes into one, ending the leg.
			if cur.Z > prev.Z {
				lifts++
				if math.Abs(cur.Z-height) > 1e-9 {
					t.Errorf("move %d: lift arc ends at Z %.3f, not the travel height %g", i, cur.Z, height)
				}
				if i > 0 && moves[i-1].termCond == tpTermCondStop && !(moves[i-1].pos.X == prev.X && moves[i-1].pos.Y == prev.Y) {
					t.Errorf("move %d: lift arc after an exact stop", i)
				}
			} else {
				descents++
				if math.Abs(prev.Z-height) > 1e-9 {
					t.Errorf("move %d: descent arc starts at Z %.3f, not the travel height %g", i, prev.Z, height)
				}
				if i+1 >= len(moves) {
					t.Fatalf("move %d: the leg ends on its descent arc", i)
				}
				next := moves[i+1]
				if next.arc || next.pos.X != cur.X || next.pos.Y != cur.Y || next.pos.Z >= cur.Z || next.termCond != tpTermCondStop {
					t.Errorf("move %d: descent arc not followed by a vertical exact-stop stroke: %+v", i, next)
				}
			}
		} else if xyMoves && prev.Z < height-1e-9 && cur.Z == prev.Z {
			// Sideways below the travel height: only in a station (a low move
			// on a tray, D38), never out of a stroke.
			t.Errorf("move %d: XY move at Z %.3f below the travel height %g", i, prev.Z, height)
		}
		prev = cur
	}
	return lifts, descents
}

func sq(v float64) float64 { return v * v }

// TestZCornerArcs: with Z_CORNER_RADIUS a tray -> proc job runs the descent
// into the tray slot, the lift out of it and the descent into the process
// station as corner arcs of the legs, with no stop between the lift and the
// travel -- the place's descent too, since the simulated fixture reports
// released while the head is on its way -- and the job ends with the head up.
func TestZCornerArcs(t *testing.T) {
	for _, smooth := range []bool{false, true} {
		f := newJobFixtureOpts(t, zCornerOpts(10, func(c *Config) { c.SmoothPaths = smooth }, nil))
		f.homed()
		f.selectTray(1)
		f.fillTray(0)
		f.mot.setPos(100, 100, 60)
		f.mot.resetCalls()
		f.runJob(10, 20, 0)
		f.requireOK("tray -> proc with Z corner arcs")

		moves := f.mot.moveList()
		// [PNPTASK_ROUTE_0]: the movement height for 10 -> 20 is 15 mm.
		const height = 15.0
		lifts, descents := zArcCheck(t, moves, motctl.Pose{X: 100, Y: 100, Z: 60}, height)
		if lifts != 1 || descents != 2 {
			t.Errorf("smooth %v: %d lift and %d descent arcs, want one lift (tray slot) and two descents (slot, station)", smooth, lifts, descents)
		}
		// The descent into the slot: the tray's stroke is 15 - 2.5 = 12.5 mm,
		// so the arc keeps its 10 mm and the stroke below it 2.5 mm. Into the
		// station: 15 - 5 = 10 mm, so the arc gets 9 and the stroke 1 mm.
		for i, m := range moves {
			if !m.arc || m.normal.Z != 0 || m.pos.Z >= moves[i-1].pos.Z {
				continue
			}
			wantArc, wantStroke := height-10, 2.5
			if m.pos.X == 300 && m.pos.Y == 200 {
				wantArc, wantStroke = height-9, 5
			}
			if math.Abs(m.pos.Z-wantArc) > 1e-9 || moves[i+1].pos.Z != wantStroke {
				t.Errorf("smooth %v: descent arc at (%g, %g) to Z %.3f, stroke to %.3f; want %g and %g",
					smooth, m.pos.X, m.pos.Y, m.pos.Z, moves[i+1].pos.Z, wantArc, wantStroke)
			}
		}
		last := moves[len(moves)-1]
		if last.pos.Z != height || last.termCond != tpTermCondStop || last.pos.X != 300 || last.pos.Y != 200 {
			t.Errorf("smooth %v: the job ends with %+v, want the lift at the station, an exact stop", smooth, last)
		}
	}
}

// TestZCornerArcRadiusPerLeg: a descent stroke shorter than the radius gets a
// smaller arc, leaving minDescentLine of straight stroke; a station override
// wins over the global radius; 0 at a station keeps its strokes separate.
func TestZCornerArcRadiusPerLeg(t *testing.T) {
	f := newJobFixtureOpts(t, zCornerOpts(50, nil, func(m *pnptaskModule) { m.world.procs[0].cfg.ZCornerRadius = 0 }))
	f.homed()
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setPos(100, 100, 60)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc, radius per leg")
	moves := f.mot.moveList()
	lifts, descents := zArcCheck(t, moves, motctl.Pose{X: 100, Y: 100, Z: 60}, 15)
	if lifts != 1 || descents != 1 {
		t.Fatalf("%d lift and %d descent arcs, want one each", lifts, descents)
	}
	for i, m := range moves {
		if !m.arc || m.normal.Z != 0 {
			continue
		}
		r := math.Abs(m.pos.Z - moves[i-1].pos.Z)
		if m.pos.Z < moves[i-1].pos.Z {
			if want := 15 - 2.5 - minDescentLine; math.Abs(r-want) > 1e-9 {
				t.Errorf("descent arc radius %.3f, want the stroke less %g: %g", r, minDescentLine, want)
			}
		} else if want := 15.0 - 2.5; math.Abs(r-want) > 1e-9 {
			t.Errorf("lift arc radius %.3f, want the whole stroke %g", r, want)
		}
	}
}

// TestZCornerArcLiftFlushedForAWait: a lift deferred for the next travel runs
// on its own when the head has to wait first -- at a busy station without a
// wait position the head waits up, not down at the tray.
func TestZCornerArcLiftFlushedForAWait(t *testing.T) {
	f := newJobFixtureOpts(t, zCornerOpts(10, nil, func(m *pnptaskModule) { m.world.procs[0].cfg.HasWait = false }))
	f.homed()
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setPos(100, 100, 60)
	f.setBit(f.m.pins.procs[0].busy, true)
	f.mot.resetCalls()
	startJobTo(f, 10, 20)
	f.eventually("the head waiting at the busy station's gate", func() bool {
		ms := f.mot.moveList()
		if len(ms) == 0 {
			return false
		}
		last := ms[len(ms)-1]
		// picked at the slot and lifted straight up, standing at the height
		return last.pos.X == 120 && last.pos.Y == 430 && last.pos.Z == 15 && !last.arc && f.bit("picker.0.close")
	})
	f.setBit(f.m.pins.procs[0].busy, false)
	f.awaitJob()
	f.requireOK("the job after the wait")
	lifts, _ := zArcCheck(t, f.mot.moveList(), motctl.Pose{X: 100, Y: 100, Z: 60}, 15)
	if lifts != 0 {
		t.Errorf("%d lift arcs; the lift before the wait has to be a stroke of its own", lifts)
	}
}

// TestZCornerArcThroughTheSceneGate: a leg out of a station in the movable
// region goes through the scene gate (D45); with the scene free the gate
// takes its lock without standing the deferred lift up on its own, so the
// lift still runs as the leg's corner arc.
func TestZCornerArcThroughTheSceneGate(t *testing.T) {
	o := zCornerOpts(10, nil, func(m *pnptaskModule) {
		m.world.procs[1].setHasMaterial(true)
		m.world.procs[2].setHasMaterial(true)
	})
	o.ini = sceneSections
	o.files = map[string]string{zonesA: fixtureOpen, zonesB: fixtureClear}
	f := newJobFixtureOpts(t, o)
	f.homed()
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.m.pins.deadzoneSelect.Set(0)
	f.mot.resetCalls()
	f.runJob(21, 10, 0)
	f.requireOK("station 21 -> tray through the scene gate")
	moves := f.mot.moveList()
	lifts, _ := zArcCheck(t, moves, motctl.Pose{X: 100, Y: 100, Z: 60}, f.m.cfg.MoveHeight)
	liftAt21 := false
	for i, m := range moves {
		if m.arc && m.normal.Z == 0 && i > 0 && m.pos.Z > moves[i-1].pos.Z &&
			moves[i-1].pos.X == 500 && moves[i-1].pos.Y == 100 {
			liftAt21 = true
		}
	}
	if !liftAt21 {
		t.Errorf("no lift arc out of station 21 (%d lift arcs in all): the gate stood the lift up on its own", lifts)
	}
}

// TestSeparateDescentOutsideTheSceneLock: without a corner arc the descent
// into a station in the movable region is a stroke of its own after the leg,
// and the scene gate's lock (D45) covers the leg only -- scene-busy is low
// while the head goes down, as before the descent was part of a travel.
func TestSeparateDescentOutsideTheSceneLock(t *testing.T) {
	f := newSceneFixture(t)
	var mu sync.Mutex
	busyAt := map[int]bool{} // move index -> scene-busy when dispatched
	f.mot.mu.Lock()
	f.mot.onMove = func(mc moveCall) {
		mu.Lock()
		busyAt[len(f.mot.moves)-1] = f.m.pins.sceneBusy.Get()
		mu.Unlock()
	}
	f.mot.mu.Unlock()
	f.mot.resetCalls()
	f.runJob(21, 10, 0)
	f.requireOK("station 21 -> tray")
	moves := f.mot.moveList()
	mu.Lock()
	defer mu.Unlock()
	descents := 0
	for i, m := range moves {
		// the stroke down into station 21 (500, 100), Z_PICK 5
		if m.pos.X == 500 && m.pos.Y == 100 && m.pos.Z == 5 {
			descents++
			if busyAt[i] {
				t.Errorf("move %d: the descent into station 21 dispatched under the scene lock", i)
			}
		}
	}
	if descents != 1 {
		t.Fatalf("%d descents into station 21, want 1: %+v", descents, moves)
	}
}

// TestZCornerArcPlaceWaitsForReleased: the descent into a process station may
// only start with the fixture open. A fixture that reports released only
// after the head has arrived gets no descent arc: the leg ends at the travel
// height, and the descent is a stroke of its own, dispatched after released
// came.
func TestZCornerArcPlaceWaitsForReleased(t *testing.T) {
	o := zCornerOpts(10, nil, nil)
	o.sim = func(s *machineSim) { s.fixtureLag = 40 }
	f := newJobFixtureOpts(t, o)
	f.homed()
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setPos(100, 100, 60)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("tray -> proc with a slow fixture")
	moves := f.mot.moveList()
	_, descents := zArcCheck(t, moves, motctl.Pose{X: 100, Y: 100, Z: 60}, 15)
	if descents != 1 {
		t.Errorf("%d descent arcs, want only the tray slot's", descents)
	}
	f.sim.mu.Lock()
	released := f.sim.releasedAt[0]
	f.sim.mu.Unlock()
	found := false
	for i, m := range moves {
		if m.pos.X == 300 && m.pos.Y == 200 && m.pos.Z == 5 {
			found = true
			prev := moves[i-1]
			if m.arc || prev.pos.Z != 15 || prev.termCond != tpTermCondStop || prev.arc {
				t.Errorf("descent into the station %+v after %+v, want a stroke after an exact stop at the travel height", m, prev)
			}
			if m.at.Before(released) {
				t.Errorf("the descent was dispatched %v before released came", released.Sub(m.at))
			}
		}
	}
	if !found {
		t.Fatalf("no descent into the station: %+v", moves)
	}
}

// TestZCornerArcLiftAfterAFailedJob: a job that fails between a deferred lift
// and the travel it was waiting for still takes the head up -- here wait-abort
// is already raised when the job reaches the busy station's gate, after the
// pick at the tray.
func TestZCornerArcLiftAfterAFailedJob(t *testing.T) {
	f := newJobFixtureOpts(t, zCornerOpts(10, nil, func(m *pnptaskModule) { m.world.procs[0].cfg.HasWait = false }))
	f.homed()
	f.selectTray(1)
	f.fillTray(0)
	f.mot.setPos(100, 100, 60)
	f.setBit(f.m.pins.procs[0].busy, true)
	f.setBit(f.m.pins.waitAbort, true)
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireError("wait-abort at the gate", errWaitAborted)
	moves := f.mot.moveList()
	last := moves[len(moves)-1]
	if last.arc || last.pos.X != 120 || last.pos.Y != 430 || last.pos.Z != 15 || last.termCond != tpTermCondStop {
		t.Errorf("the failed job ended with %+v, want the lift at the tray slot to the travel height", last)
	}
}

// TestZCornerArcSwap: the place of a swap (§8, PROBE_SWAP on the coating
// machine) runs as one stream too. The remover lifts out of the station only
// to its NEAR_HEIGHT, the fixture stays open, and the shift by the picker
// offset turns into the lift and into the placer's descent on corner arcs --
// the descent's at once, the fixture already reporting released. The arcs are
// capped by the strokes and by a quarter of the 30 mm shift.
func TestZCornerArcSwap(t *testing.T) {
	o := zCornerOpts(10, nil, func(m *pnptaskModule) {
		m.world.procs[0].setHasMaterial(true)
		m.world.procs[0].cfg.NearHeight, m.world.procs[0].cfg.HasNear = 12, true
	})
	o.args = []string{"pickers=2"}
	o.ini = secondProcSection
	f := newJobFixtureOpts(t, o)
	f.homed()
	f.m.pins.pickers[1].yOffset.Set(-30)
	f.mot.setPos(100, 100, 60)
	f.selectTray(1)
	f.fillTray(0)
	f.eventually("has-material published", func() bool { return f.bit("proc.20.has-material") })
	f.mot.resetCalls()
	f.runJob(10, 20, 0)
	f.requireOK("tray -> occupied station, swapped, with Z corner arcs")

	// Picker 1 takes the occupant out with the machine at (300, 230); picker
	// 0 places with it at (300, 200). Near height 12, station Z_PICK 5.
	moves := f.mot.moveList()
	var lift, desc = -1, -1
	for i := 1; i < len(moves); i++ {
		m, p := moves[i], moves[i-1].pos
		if !m.arc || m.normal.Z != 0 {
			continue
		}
		if p.X == 300 && p.Y == 230 && p.Z == 5 && m.pos.Z == 12 && m.pos.Y == 223 {
			lift = i // straight from the bottom: the stroke (7) leaves no line
		}
		if m.pos.X == 300 && m.pos.Y == 200 && m.pos.Z < p.Z {
			desc = i
		}
	}
	// Lift: min(10, stroke 12 - 5 = 7, 30/4 = 7.5) = 7 -- hence from Z 5.
	if lift < 0 {
		t.Fatalf("no 7 mm lift arc out of the removal at (300, 230): %+v", moves)
	}
	// Descent: min(10, stroke 12 - 5 - 1 = 6, 7.5) = 6.
	if desc < 0 || moves[desc].pos.Z != 6 || moves[desc+1].pos.Z != 5 || moves[desc+1].termCond != tpTermCondStop {
		t.Fatalf("no 6 mm descent arc into the station at (300, 200), then the stroke: %+v", moves)
	}
	for i := lift; i < desc; i++ {
		if moves[i].termCond == tpTermCondStop {
			t.Errorf("move %d stops between the swap's lift and the place's descent: %+v", i, moves[i])
		}
	}
	// The fixture opened once, for the removal, and stayed open for the place.
	if n := f.sim.releaseRiseCount(0); n != 1 {
		t.Errorf("the fixture was opened %d times, want 1", n)
	}
}
