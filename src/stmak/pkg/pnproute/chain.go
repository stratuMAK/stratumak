// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"fmt"
	"math"
	"strings"
)

// Loops chained from LINE and ARC entities.
//
// CAD users routinely draw an outline as separate LINE and ARC entities that
// meet end to end rather than as one polyline. On the two recognized layers
// such entities are collected per layer and chained by endpoint coincidence
// into closed loops, which then pass exactly the validation a polyline does.

const (
	// chainJoinEps is the distance below which two segment endpoints are the
	// same point, in drawing units. It is deliberately far above the geometry
	// epsilon (geomEps, dupEps = 1e-9): a CAD export joins consecutive
	// entities to ~1e-9, but a join a user made by hand — snapping one entity
	// onto another's endpoint — is routinely off by 1e-6..1e-4, and refusing
	// those would refuse most hand-drawn outlines. 1e-3 is a micrometre on a
	// metric drawing and 25 µm on an inch one: an order above the sloppiest
	// join expected, and three orders below any clearance that matters. The
	// join keeps one of the two points, so the loaded ring carries no jog.
	chainJoinEps = 1e-3

	// minArcSegments is the fewest straight segments an ARC is discretized
	// into, whatever its sweep. A short arc scaled from the per-circle count
	// alone could end up as one or two segments; a few keep the corner
	// rounded rather than chamfered.
	minArcSegments = 4
)

// chainSeg is one LINE or ARC awaiting chaining: its two endpoints as drawn
// plus, for an arc, what is needed to discretize it once its direction in the
// loop is known. desc names the entity in errors.
type chainSeg struct {
	a, b Point
	arc  *arcGeom
	desc string
}

// arcGeom is a DXF ARC: counter-clockwise from start by sweep (both radians).
type arcGeom struct {
	center Point
	r      float64
	start  float64
	sweep  float64
}

// parseLine reads a LINE entity (group codes 10/20 start, 11/21 end). A LINE's
// coordinates are world coordinates whatever its extrusion says — the 210
// group only orients its thickness — so no OCS check applies.
func parseLine(e entity) (chainSeg, error) {
	// Endpoints are required, not defaulted: a missing or malformed code
	// falling back to 0 would drag one end onto an axis and reshape the
	// loop the zone guards.
	ax, okax := valFloatOK(e, 10)
	ay, okay := valFloatOK(e, 20)
	if !okax || !okay {
		return chainSeg{}, fmt.Errorf("LINE has no start point (group codes 10/20 missing or malformed)")
	}
	bx, okbx := valFloatOK(e, 11)
	by, okby := valFloatOK(e, 21)
	if !okbx || !okby {
		return chainSeg{}, fmt.Errorf("LINE starting at (%.3f,%.3f) has no end point (group codes 11/21 missing or malformed)", ax, ay)
	}
	s := chainSeg{a: Point{ax, ay}, b: Point{bx, by}}
	s.desc = fmt.Sprintf("LINE from (%.3f,%.3f) to (%.3f,%.3f)", ax, ay, bx, by)
	if s.a.dist(s.b) < chainJoinEps {
		return chainSeg{}, fmt.Errorf("%s is shorter than the join tolerance %g", s.desc, chainJoinEps)
	}
	return s, nil
}

// parseArc reads an ARC entity (10/20 center, 40 radius, 50/51 start and end
// angle in degrees, counter-clockwise in the entity's OCS). Like a circle, an
// arc drawn in a nontrivial OCS is rejected: its coordinates would not be
// world coordinates.
func parseArc(e entity) (chainSeg, error) {
	cx, okx := valFloatOK(e, 10)
	cy, oky := valFloatOK(e, 20)
	if !okx || !oky {
		return chainSeg{}, fmt.Errorf("ARC has no center (group codes 10/20 missing or malformed)")
	}
	c := Point{cx, cy}
	r, okr := valFloatOK(e, 40)
	if !okr {
		return chainSeg{}, fmt.Errorf("ARC at (%.3f,%.3f) has no radius (group code 40 missing or malformed)", c.X, c.Y)
	}
	if r <= 0 {
		return chainSeg{}, fmt.Errorf("ARC at (%.3f,%.3f) has radius %.3f", c.X, c.Y, r)
	}
	startDeg, oks := valFloatOK(e, 50)
	endDeg, oke := valFloatOK(e, 51)
	if !oks || !oke {
		return chainSeg{}, fmt.Errorf("ARC at (%.3f,%.3f) has no start/end angle (group codes 50/51 missing or malformed)", c.X, c.Y)
	}
	if err := checkPlanar(e); err != nil {
		return chainSeg{}, fmt.Errorf("ARC at (%.3f,%.3f) %v", c.X, c.Y, err)
	}
	// DXF arcs always run counter-clockwise from the start angle, so an end
	// angle numerically below the start means the arc passes through 0°.
	sweepDeg := math.Mod(endDeg-startDeg, 360)
	if sweepDeg <= 0 {
		sweepDeg += 360
	}
	g := &arcGeom{center: c, r: r, start: startDeg * math.Pi / 180, sweep: sweepDeg * math.Pi / 180}
	s := chainSeg{
		a:    Point{c.X + r*math.Cos(g.start), c.Y + r*math.Sin(g.start)},
		b:    Point{c.X + r*math.Cos(g.start+g.sweep), c.Y + r*math.Sin(g.start+g.sweep)},
		arc:  g,
		desc: fmt.Sprintf("ARC at (%.3f,%.3f) r=%.3f %.1f°..%.1f°", c.X, c.Y, r, startDeg, endDeg),
	}
	if r*g.sweep < chainJoinEps {
		return chainSeg{}, fmt.Errorf("%s is shorter than the join tolerance %g", s.desc, chainJoinEps)
	}
	// An arc whose ends coincide (a full circle, or within the tolerance of
	// one) would chain onto itself; the drawing means a CIRCLE.
	if s.a.dist(s.b) < chainJoinEps {
		return chainSeg{}, fmt.Errorf("%s closes on itself (sweep %.1f°); draw a full circle as a CIRCLE", s.desc, sweepDeg)
	}
	return s, nil
}

// chainLoops joins the segments end to end into closed loops and returns each
// loop's ring, discretizing arcs on the way. Segments may be drawn in either
// direction and in any order. Every endpoint must meet exactly one other
// endpoint (within chainJoinEps): a lone endpoint is an open chain and three or
// more meeting is a branch — both are reported with the coordinates, since an
// outline that does not close is the drawing mistake this loader exists to
// catch rather than paper over.
//
// The loops come out in the order of their first segment in the file, each
// starting at that segment's start point and running in its direction.
func chainLoops(segs []chainSeg, arcSegments int) ([]Polygon, error) {
	n := len(segs)
	// Endpoint j is end j%2 (0: a, 1: b) of segment j/2.
	pt := func(j int) Point {
		if j%2 == 0 {
			return segs[j/2].a
		}
		return segs[j/2].b
	}
	// Cluster endpoints by coincidence. Segments are at least chainJoinEps
	// long, so an endpoint never clusters with its own segment's other end,
	// and n is the entity count of one layer — a few dozen — so the
	// quadratic scan is nothing.
	rep := make([]int, 2*n)
	members := make(map[int][]int, 2*n)
	for j := 0; j < 2*n; j++ {
		rep[j] = j
		for k := 0; k < j; k++ {
			if pt(k).dist(pt(j)) < chainJoinEps {
				rep[j] = rep[k]
				break
			}
		}
		members[rep[j]] = append(members[rep[j]], j)
	}
	// Every dangling end is listed, not only the first: a missing segment
	// leaves two, and seeing both shows the gap directly.
	partner := make([]int, 2*n)
	var open []string
	for j := 0; j < 2*n; j++ {
		if rep[j] != j {
			continue
		}
		m := members[j]
		p := pt(j)
		switch {
		case len(m) == 1:
			open = append(open, fmt.Sprintf("(%.3f,%.3f) of %s", p.X, p.Y, segs[j/2].desc))
			continue
		case len(m) > 2:
			return nil, fmt.Errorf("branch: %d segments meet at (%.3f,%.3f)", len(m), p.X, p.Y)
		}
		partner[m[0]], partner[m[1]] = m[1], m[0]
	}
	if len(open) > 0 {
		return nil, fmt.Errorf("do not close: nothing is joined to %s (join tolerance %g)",
			strings.Join(open, ", "), chainJoinEps)
	}

	// Every endpoint now has exactly one partner on another segment, so the
	// segments form disjoint cycles; walk each one from its first segment.
	visited := make([]bool, n)
	var loops []Polygon
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		var ring Polygon
		cur, forward := i, true
		for {
			if visited[cur] {
				// Unreachable with the 2-regular structure checked above;
				// a loud failure beats a silent, wrong ring.
				return nil, fmt.Errorf("internal error: chaining revisited %s", segs[cur].desc)
			}
			visited[cur] = true
			ring = appendSegPoints(ring, segs[cur], forward, arcSegments)
			exit := 2*cur + 1
			if !forward {
				exit = 2 * cur
			}
			next := partner[exit]
			cur, forward = next/2, next%2 == 0 // entering at a means running a->b
			if cur == i {
				break
			}
		}
		loops = append(loops, ring)
	}
	return loops, nil
}

// appendSegPoints appends the segment's start point and, for an arc, its
// intermediate vertices — everything but the end point, which the next
// segment in the loop contributes as its start.
//
// Arcs are discretized the way discretizeCircle does it: the vertices sit at
// radius r/cos(Δ/2) so that every edge is a tangent of the true arc and the
// ring contains it — the conservative direction for an obstacle. With the two
// endpoints pinned on the arc itself this means tangents at the endpoints
// too: the first edge leaves the start point along the arc's tangent there
// (so a LINE joining tangentially, as in a stadium, continues collinearly)
// and the intermediate vertices are the intersections of consecutive tangents
// spaced Δ apart, Δ = sweep/segments.
func appendSegPoints(ring Polygon, s chainSeg, forward bool, arcSegments int) Polygon {
	if forward {
		ring = append(ring, s.a)
	} else {
		ring = append(ring, s.b)
	}
	g := s.arc
	if g == nil {
		return ring
	}
	segs := int(math.Ceil(float64(arcSegments) * g.sweep / (2 * math.Pi)))
	if segs < minArcSegments {
		segs = minArcSegments
	}
	delta := g.sweep / float64(segs)
	rv := g.r / math.Cos(delta/2)
	for k := 0; k < segs; k++ {
		ang := g.start + delta*(float64(k)+0.5)
		if !forward {
			ang = g.start + g.sweep - delta*(float64(k)+0.5)
		}
		ring = append(ring, Point{g.center.X + rv*math.Cos(ang), g.center.Y + rv*math.Sin(ang)})
	}
	return ring
}
