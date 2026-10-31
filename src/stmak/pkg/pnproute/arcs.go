// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import "math"

// Curves as the trajectory planner gets them: tangent arc chains (D48).
//
// A curve is cut into pieces of about ArcPieceLength mm (earlier where its
// curvature jumps, never shorter than arcMinPiece) and each piece sent as a
// biarc -- two arcs matching position and direction at both ends -- so every
// join is tangent to ~1e-12 rad. That is not cosmetic: tpmod takes a join as
// tangent only when the acceleration the kink would need within one cycle at
// full speed stays below ARC_BLEND_KINK_RATIO (0.1) of the axis limits --
// about 1e-4 rad at pick-and-place speeds -- and blends anything more as a
// corner at half the acceleration of both segments (tp.c tpSetupTangent,
// tcGetOverallMaxAccel). A piece becomes a line only where its chord leaves
// the curve's direction by less than arcLineKink.

const (
	// ArcPieceLength is the curve length per biarc, mm. 10 mm keeps the
	// queue ahead of execution: motion takes one command per servo cycle,
	// and a piece at 900 mm/s lasts 11 ms.
	ArcPieceLength = 10.0
	// arcMinPiece is the shortest piece a curvature jump may cut, mm: a curve
	// whose curvature changes continuously would otherwise be cut into
	// sample-sized pieces, each held back by the TP's minimum segment
	// duration.
	arcMinPiece = 2.0
	// arcLineKink is the largest angle (rad) between a piece's chord and the
	// curve's direction at its ends for which it is sent as a line. Far
	// below the TP's tangency test, so a line never kinks the chain.
	arcLineKink = 1e-6
	// ArcEndLine is the straight line a curve ends in, mm. tpmod never
	// finalizes the last queued segment, so a last ARC keeps the initial
	// tangential ratio 0.5 (tcFinalizeLength -> tcUpdateCircleAccRatio only
	// runs when a next segment arrives) and brakes into the stop at half the
	// acceleration; a last line brakes at the full value (D40).
	ArcEndLine = 3.0
)

// SegmentKind tells a line from an arc.
type SegmentKind int

const (
	SegmentLine SegmentKind = iota
	SegmentArc
)

// Segment is one piece of the commanded geometry of a smoothed route: a line
// or a circular arc in the XY plane, tangent to its neighbours.
type Segment struct {
	Kind   SegmentKind
	Start  Point
	End    Point
	Center Point   // arc only
	Radius float64 // arc only
	Sweep  float64 // arc only: signed angle, positive counter-clockwise (rad)
	// Vel is the speed the segment is requested at, mm/s. Set by
	// [Planner.PlanPath] from the time-optimal profile along the curve; 0
	// where nothing set it.
	Vel float64
}

// Length is the segment's arc length.
func (s Segment) Length() float64 {
	if s.Kind == SegmentArc {
		return s.Radius * math.Abs(s.Sweep)
	}
	return s.Start.dist(s.End)
}

func (s Segment) prim() prim {
	if s.Kind == SegmentLine {
		return prim{kind: primLine, a: s.Start, b: s.End}
	}
	return prim{kind: primArc, a: s.Start, b: s.End, c: s.Center, r: s.Radius,
		a0: math.Atan2(s.Start.Y-s.Center.Y, s.Start.X-s.Center.X), sweep: s.Sweep}
}

func segmentOf(p prim) Segment {
	if p.kind == primArc {
		return Segment{Kind: SegmentArc, Start: p.a, End: p.b, Center: p.c, Radius: p.r, Sweep: p.sweep}
	}
	return Segment{Kind: SegmentLine, Start: p.a, End: p.b}
}

// curvePieces cuts a sampled curve into pieces for the TP, as sample indices
// (first 0, last len(sm)-1): a new piece every pieceLen, and where the
// curvature jumps -- but only once a piece has arcMinPiece.
func curvePieces(sm []sample, pieceLen float64) []int {
	minPiece := math.Min(arcMinPiece, pieceLen/2)
	bounds := []int{0}
	k0 := math.Abs(sm[0].kappa)
	for i := 1; i < len(sm)-1; i++ {
		k := math.Abs(sm[i].kappa)
		since := sm[i].s - sm[bounds[len(bounds)-1]].s
		jump := math.Abs(k-k0) > 0.2*math.Max(k, k0) && math.Abs(k-k0) > 1e-4 && since >= minPiece
		if jump || since >= pieceLen {
			bounds = append(bounds, i)
			k0 = k
		}
	}
	return append(bounds, len(sm)-1)
}

// arcFrom is the circular arc leaving p0 in direction t (unit) and ending at
// p1, or a line if p1 lies straight ahead. It returns the prim and the
// direction it ends in.
func arcFrom(p0, t, p1 Point) (prim, Point) {
	c := p1.sub(p0)
	l := norm(c)
	th := math.Atan2(cross2(t, c), dot(t, c)) // angle from t to the chord
	if l < 1e-12 || math.Abs(th) < 1e-9 {
		return prim{kind: primLine, a: p0, b: p1}, unit(c)
	}
	r := l / (2 * math.Abs(math.Sin(th)))
	n := Point{-t.Y, t.X}
	if th < 0 {
		n = n.scale(-1)
	}
	cen := p0.add(n.scale(r))
	end := Point{t.X*math.Cos(2*th) - t.Y*math.Sin(2*th), t.X*math.Sin(2*th) + t.Y*math.Cos(2*th)}
	return prim{kind: primArc, a: p0, b: p1, c: cen, r: r,
		a0: math.Atan2(p0.Y-cen.Y, p0.X-cen.X), sweep: 2 * th}, end
}

// biarc joins (a, ta) to (b, tb) with two tangent arcs (the equal-distance
// biarc): position and direction match at both ends. Degenerate cases fall
// back to the single arc leaving a in direction ta.
func biarc(a, ta, b, tb Point) []prim {
	v := b.sub(a)
	t := ta.add(tb)
	den := 2 * (1 - dot(ta, tb))
	var d float64
	if den < 1e-9 {
		vt := dot(v, tb)
		if vt <= 1e-12 {
			p, _ := arcFrom(a, ta, b)
			return []prim{p}
		}
		d = dot(v, v) / (4 * vt)
	} else {
		vt := dot(v, t)
		d = (-vt + math.Sqrt(vt*vt+den*dot(v, v))) / den
	}
	if !(d > 0) || math.IsInf(d, 0) {
		p, _ := arcFrom(a, ta, b)
		return []prim{p}
	}
	j := a.add(b).add(ta.sub(tb).scale(d)).scale(0.5)
	p1, tj := arcFrom(a, ta, j)
	p2, _ := arcFrom(j, tj, b)
	return []prim{p1, p2}
}

// chainPiece is one prim of an arc chain and the stretch of the curve (arc
// length s0..s1 along it) the piece it belongs to replaces.
type chainPiece struct {
	p      prim
	s0, s1 float64
}

// arcChain is a sampled curve as the TP gets it: per piece a biarc through
// the piece's ends with the curve's directions there -- or a line, where the
// chord leaves the curve's direction at both ends by less than arcLineKink.
// With endLine > 0 the chain ends in a straight line of that length in the
// curve's end direction, the arcs leading into its start (see ArcEndLine).
// With startLine > 0 it likewise starts with a straight line in the curve's
// start direction: what a Z corner arc turns into (D52). Either line is left
// out where the curve is not long enough for it (3× the lines together).
func arcChain(sm []sample, pieceLen, startLine, endLine float64) []chainPiece {
	b := curvePieces(sm, pieceLen)
	dir := func(i int) Point { return Point{math.Cos(sm[i].phi), math.Sin(sm[i].phi)} }
	n := len(sm) - 1
	total := sm[n].s
	type node struct {
		p, t Point
		s    float64
	}
	nodes := make([]node, 0, len(b)+1)
	for _, i := range b {
		nodes = append(nodes, node{sm[i].p, dir(i), sm[i].s})
	}
	if endLine > 0 && total > 3*endLine {
		// The last piece ends where the line starts, in the line's direction.
		tEnd := dir(n)
		lineStart := sm[n].p.sub(tEnd.scale(endLine))
		for len(nodes) > 2 && nodes[len(nodes)-2].s > total-endLine-math.Min(arcMinPiece, pieceLen/2) {
			nodes = append(nodes[:len(nodes)-2], nodes[len(nodes)-1])
		}
		nodes[len(nodes)-1] = node{lineStart, tEnd, total - endLine}
		nodes = append(nodes, node{sm[n].p, tEnd, total})
	}
	if startLine > 0 && total > 3*(startLine+endLine) {
		// The first piece starts where the line ends, in the line's
		// direction.
		t0 := dir(0)
		lineEnd := sm[0].p.add(t0.scale(startLine))
		for len(nodes) > 2 && nodes[1].s < startLine+math.Min(arcMinPiece, pieceLen/2) {
			nodes = append(nodes[:1], nodes[2:]...)
		}
		nodes = append(nodes[:1], append([]node{{lineEnd, t0, startLine}}, nodes[1:]...)...)
	}
	var out []chainPiece
	for k := 0; k+1 < len(nodes); k++ {
		a, z := nodes[k], nodes[k+1]
		chord := unit(z.p.sub(a.p))
		var ps []prim
		if math.Abs(cross2(a.t, chord)) < arcLineKink && math.Abs(cross2(z.t, chord)) < arcLineKink && dot(a.t, chord) > 0 {
			ps = []prim{{kind: primLine, a: a.p, b: z.p}}
		} else {
			ps = biarc(a.p, a.t, z.p, z.t)
		}
		for _, p := range ps {
			if p.length() > 1e-9 {
				out = append(out, chainPiece{p, a.s, z.s})
			}
		}
	}
	return out
}

// primDir is a prim's direction at its start or end, as an angle.
func primDir(p prim, end bool) float64 {
	if p.kind == primLine {
		return math.Atan2(p.b.Y-p.a.Y, p.b.X-p.a.X)
	}
	off := math.Pi / 2
	if p.sweep < 0 {
		off = -off
	}
	if end {
		return p.a0 + p.sweep + off
	}
	return p.a0 + off
}

// chainKink is the largest direction change where two prims of a chain meet:
// the TP takes a join as tangent only well below 1e-4 rad.
func chainKink(prims []prim) float64 {
	worst := 0.0
	for i := 1; i < len(prims); i++ {
		d := math.Abs(math.Remainder(primDir(prims[i], false)-primDir(prims[i-1], true), 2*math.Pi))
		worst = math.Max(worst, d)
	}
	return worst
}
