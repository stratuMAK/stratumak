// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import "math"

// Naive CAM detector — the port of 2.9 emccanon.cc's chained_points /
// see_segment / flush_segments machinery (emccanon.cc:875-1030).
//
// Every STRAIGHT_FEED is buffered instead of emitted. While in G64-continuous
// mode with a Q tolerance (G64 P.. Q..), consecutive colinear-within-Q feed
// segments chain together and flush as ONE linear move to the last point, so
// dense micro-segment CAM output doesn't drown the TP. With Q unset (tol 0)
// every segment still passes through the one-deep buffer and flushes on the
// next canon call, preserving 2.9's emission order exactly.
//
// The ordering contract matters beyond G64 Q: every canon call that emits a
// queued command or reads/moves the endpoint must flushSegments() FIRST (the
// sync-I/O canons especially — a buffered move reordered past an M62/M67
// SET_AOUT would attach the output to the wrong motion), and abort/position
// resync must dropSegments() (2.9 GET_EXTERNAL_POSITION). state.endPoint is
// NOT advanced while a chain is open — it stays at the chain start until
// flush, which is what makes the merged move's vel/acc span the full chord.

// naivecamMaxChain caps the number of chained points per merged segment,
// matching 2.9's `chained_points.size() > 100` guard.
const naivecamMaxChain = 100

// chainedPt is one buffered straight-feed target (2.9 `struct pt`): the
// absolute-mm pose plus the source line and packed interp state tag captured
// when the segment was seen — the merged move carries the LAST point's
// line/tag (2.9 flush_segments uses pos.line_no / pos.tag).
type chainedPt struct {
	pos    Pose
	lineNo int32
	tag    []byte
}

// seeSegment buffers a straight-feed target, flushing first when it cannot
// chain onto the open buffer, and flushing after when the move involves
// rotary/UVW motion (2.9 see_segment). The changed-ABCUVW flags compare
// against the chain-start endpoint, exactly like 2.9.
func (c *Canon) seeSegment(lineno int32, pos Pose) {
	s := c.state
	changedABC := pos.A != s.endPoint.A || pos.B != s.endPoint.B || pos.C != s.endPoint.C
	changedUVW := pos.U != s.endPoint.U || pos.V != s.endPoint.V || pos.W != s.endPoint.W

	if len(c.chained) > 0 && !c.linkable(pos) {
		c.flushSegments()
	}
	c.chained = append(c.chained, chainedPt{pos: pos, lineNo: lineno, tag: s.currentTag})
	if changedABC || changedUVW {
		c.flushSegments()
	}
}

// linkable reports whether pos can chain onto the open buffer (2.9 linkable):
// only in G64-continuous with Q>0, buffer below the chain cap, no ABCUVW
// motion vs the last buffered point, not returning to the chain start, and
// every buffered point within Q of the chord endPoint→pos.
func (c *Canon) linkable(pos Pose) bool {
	s := c.state
	prev := c.chained[len(c.chained)-1]
	if s.motionMode != CanonContinuous || s.naivecamTol == 0 {
		return false
	}
	if len(c.chained) > naivecamMaxChain {
		return false
	}
	// Rotary/UVW motion never chains (2.9: tangent calculation limitation).
	if pos.A != prev.pos.A || pos.B != prev.pos.B || pos.C != prev.pos.C ||
		pos.U != prev.pos.U || pos.V != prev.pos.V || pos.W != prev.pos.W {
		return false
	}
	if pos.X == s.endPoint.X && pos.Y == s.endPoint.Y && pos.Z == s.endPoint.Z {
		return false
	}

	// Max deviation of every buffered point from the chord B→B+M.
	mx := pos.X - s.endPoint.X
	my := pos.Y - s.endPoint.Y
	mz := pos.Z - s.endPoint.Z
	mm := mx*mx + my*my + mz*mz
	for i := range c.chained {
		p := &c.chained[i].pos
		px := p.X - s.endPoint.X
		py := p.Y - s.endPoint.Y
		pz := p.Z - s.endPoint.Z
		t0 := (mx*px + my*py + mz*pz) / mm
		if t0 < 0 {
			t0 = 0
		}
		if t0 > 1 {
			t0 = 1
		}
		dx := px - t0*mx
		dy := py - t0*my
		dz := pz - t0*mz
		if dx*dx+dy*dy+dz*dz > s.naivecamTol*s.naivecamTol {
			return false
		}
	}
	return true
}

// flushSegments emits the open chain as one linear feed move to the last
// buffered point (2.9 flush_segments): vel/acc span the full chord from the
// chain-start endpoint; the move carries the LAST point's line number and
// state tag. The endpoint advances to the last point even when the move is
// dropped as zero-length (2.9 calls canonUpdateEndPoint unconditionally).
func (c *Canon) flushSegments() {
	if len(c.chained) == 0 {
		return
	}
	s := c.state
	last := c.chained[len(c.chained)-1]

	vel, iniMaxVel, acc, feed := c.feedLimits(s.endPoint, last.pos)
	// Zero-distance chains are dropped (StraightFeed's pre-buffer behavior,
	// 2.9's `if (vel && acc)`) — unless spindle-synched (G33/G95), where the
	// segment must still reach the TP for its sync semantics (2.9's
	// `|| synched` exception).
	synched := s.synched
	if acc > 0 || synched {
		cmd := &LinearMoveCmd{
			Pos:          last.pos,
			Vel:          vel,
			IniMaxVel:    iniMaxVel,
			Acc:          acc,
			MotionType:   2, // EMC_MOTION_TYPE_FEED
			ID:           c.allocSerialPinned(last.lineNo, last.tag),
			FeedMmPerMin: feed * 60,
			IndexerJ:     -1,
		}
		c.enqueue(cmd)
	}
	s.endPoint = last.pos
	c.dropSegments()
}

// dropSegments discards the open chain without emitting (2.9 drop_segments).
// Called on interp reset and machine-position resync (abort paths), where
// buffered readahead motion is stale.
func (c *Canon) dropSegments() {
	c.chained = c.chained[:0]
}

// lastChainedPos returns the current logical position: the last buffered
// point when a chain is open, else the endpoint (2.9 get_last_pos).
func (c *Canon) lastChainedPos() Pose {
	if n := len(c.chained); n > 0 {
		return c.chained[n-1].pos
	}
	return c.state.endPoint
}

// chordDeviation returns the max deviation of the arc (start s, end e, center
// c, sign of rotation as in ARC_FEED) from its straight chord, plus the arc
// midpoint — 2.9 emccanon chord_deviation. Used by ArcFeed to flatten arcs
// whose deviation is below the G64 Q tolerance into chained points.
func chordDeviation(sx, sy, ex, ey, cx, cy float64, rotation int32) (dev, mx, my float64) {
	th1 := math.Atan2(sy-cy, sx-cx)
	th2 := math.Atan2(ey-cy, ex-cx)
	r := math.Hypot(sy-cy, sx-cx)
	dth := th2 - th1

	if rotation < 0 {
		if dth >= -1e-5 {
			th2 -= 2 * math.Pi
		}
		// edge case where atan2 gives -pi and pi: a second iteration gets
		// these in the right order
		dth = th2 - th1
		if dth >= -1e-5 {
			th2 -= 2 * math.Pi
		}
	} else {
		if dth <= 1e-5 {
			th2 += 2 * math.Pi
		}
		dth = th2 - th1
		if dth <= 1e-5 {
			th2 += 2 * math.Pi
		}
	}

	included := math.Abs(th2 - th1)
	mid := (th2 + th1) / 2
	mx = cx + r*math.Cos(mid)
	my = cy + r*math.Sin(mid)
	dev = r * (1 - math.Cos(included/2))
	return dev, mx, my
}

// allocSerialPinned allocates a motion serial like allocSerial and pins the
// given state tag — plus the status codes decoded from it — on the entry. A
// merged segment flushes while a LATER source line is executing, so the
// flush-time id falls inside that later line's tagMotionRange bracket —
// without pinning, the merged move would report readahead modal state and
// restore the wrong state on abort. 2.9 stores the tag per chained point for
// the same reason (tag_and_send with pos.tag).
func (c *Canon) allocSerialPinned(lineno int32, tag []byte) int32 {
	id := c.allocSerial(lineno)
	c.task.pinMotionState(id, tag)
	return id
}
