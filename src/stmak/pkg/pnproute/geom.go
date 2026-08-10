// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"fmt"
	"math"
)

// Point is a 2D point in machine coordinates.
type Point struct {
	X, Y float64
}

// Polygon is a closed ring of vertices (no repeated first/last point).
type Polygon []Point

// Clone returns an independent copy of the ring.
func (p Polygon) Clone() Polygon {
	out := make(Polygon, len(p))
	copy(out, p)
	return out
}

func (a Point) sub(b Point) Point     { return Point{a.X - b.X, a.Y - b.Y} }
func (a Point) add(b Point) Point     { return Point{a.X + b.X, a.Y + b.Y} }
func (a Point) scale(s float64) Point { return Point{a.X * s, a.Y * s} }
func (a Point) dist(b Point) float64 {
	dx, dy := a.X-b.X, a.Y-b.Y
	return math.Hypot(dx, dy)
}

func unit(v Point) Point {
	l := math.Hypot(v.X, v.Y)
	if l < 1e-12 {
		return Point{0, 0}
	}
	return Point{v.X / l, v.Y / l}
}

// pointSegDistanceT returns the distance from p to segment ab and the clamped
// parameter t of the closest point along ab.
func pointSegDistanceT(p, a, b Point) (float64, float64) {
	ab := b.sub(a)
	l2 := ab.X*ab.X + ab.Y*ab.Y
	if l2 < 1e-12 {
		return p.dist(a), 0
	}
	t := ((p.X-a.X)*ab.X + (p.Y-a.Y)*ab.Y) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return p.dist(Point{a.X + ab.X*t, a.Y + ab.Y*t}), t
}

// pointSegDistance is the distance from p to segment ab.
func pointSegDistance(p, a, b Point) float64 {
	d, _ := pointSegDistanceT(p, a, b)
	return d
}

// segSegDistance is the distance between segments ab and cd (0 if they touch).
func segSegDistance(a, b, c, d Point) float64 {
	if segIntersect(a, b, c, d) {
		return 0
	}
	return math.Min(
		math.Min(pointSegDistance(a, c, d), pointSegDistance(b, c, d)),
		math.Min(pointSegDistance(c, a, b), pointSegDistance(d, a, b)))
}

// penetrationDepth returns how far p sits from the nearest edge of poly. For a
// point inside a convex polygon this is its penetration depth.
func penetrationDepth(p Point, poly Polygon) float64 {
	n := len(poly)
	min := math.Inf(1)
	for i := 0; i < n; i++ {
		d := pointSegDistance(p, poly[i], poly[(i+1)%n])
		if d < min {
			min = d
		}
	}
	return min
}

func cross(o, a, b Point) float64 {
	return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
}

// pointInPolygon reports whether p is inside poly (ray casting). A point
// exactly on an edge may come out either way — ray casting cannot decide the
// boundary consistently, and it does not have to: every query the planner makes
// is separated from the polygons by the clearance offset.
func pointInPolygon(p Point, poly Polygon) bool {
	n := len(poly)
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		yi, yj := poly[i].Y, poly[j].Y
		xi, xj := poly[i].X, poly[j].X
		if (yi > p.Y) != (yj > p.Y) {
			xcross := (xj-xi)*(p.Y-yi)/(yj-yi) + xi
			if p.X < xcross {
				inside = !inside
			}
		}
	}
	return inside
}

// geomEps is the shared tolerance of the intersection predicates, as a length
// in machine units (mm): a point closer than this to a line counts as on it.
// The predicates compare *signed distances* (cross products normalized by the
// segment length), not raw cross products — a raw cross product scales with
// the square of the coordinate magnitude, so an absolute epsilon on it would
// make grazing/blocking decisions depend on where in the envelope the geometry
// sits.
const geomEps = 1e-9

// signedDist returns the signed distance of p from the line through a and b
// (positive on the left of a->b), or 0 when ab is degenerate.
func signedDist(a, b, p Point) float64 {
	l := a.dist(b)
	if l < geomEps {
		return 0
	}
	return cross(a, b, p) / l
}

// segProperIntersect reports whether segments ab and cd cross at a point that
// is interior to both. Touching at a shared endpoint or collinear grazing
// returns false (allowed under zero clearance).
func segProperIntersect(a, b, c, d Point) bool {
	d1 := signedDist(c, d, a)
	d2 := signedDist(c, d, b)
	d3 := signedDist(a, b, c)
	d4 := signedDist(a, b, d)
	const eps = geomEps
	return ((d1 > eps && d2 < -eps) || (d1 < -eps && d2 > eps)) &&
		((d3 > eps && d4 < -eps) || (d3 < -eps && d4 > eps))
}

// segIntersect reports whether segments ab and cd share any point, touching
// included. Used for distance metrics, where a shared endpoint means zero
// distance — unlike segProperIntersect, which answers the clearance question.
func segIntersect(a, b, c, d Point) bool {
	d1, d2 := signedDist(c, d, a), signedDist(c, d, b)
	d3, d4 := signedDist(a, b, c), signedDist(a, b, d)
	const eps = geomEps
	if ((d1 > eps && d2 < -eps) || (d1 < -eps && d2 > eps)) &&
		((d3 > eps && d4 < -eps) || (d3 < -eps && d4 > eps)) {
		return true
	}
	// Collinear / touching cases: an endpoint lying on the other segment.
	onSeg := func(p, q, r Point) bool { // r on segment pq, given collinearity
		return math.Min(p.X, q.X)-eps <= r.X && r.X <= math.Max(p.X, q.X)+eps &&
			math.Min(p.Y, q.Y)-eps <= r.Y && r.Y <= math.Max(p.Y, q.Y)+eps
	}
	return (math.Abs(d1) <= eps && onSeg(c, d, a)) ||
		(math.Abs(d2) <= eps && onSeg(c, d, b)) ||
		(math.Abs(d3) <= eps && onSeg(a, b, c)) ||
		(math.Abs(d4) <= eps && onSeg(a, b, d))
}

// segBlockedByPolygon reports whether segment ab passes through the interior
// of an obstacle. Proper edge crossings are tested against edge (the true
// polygon); interior sampling is tested against core (a slightly eroded copy)
// so that a chord lying exactly on a hull edge — i.e. travelling along the
// obstacle boundary between adjacent vertices — is treated as grazing (allowed)
// rather than as an interior hit. samples is how many interior points along the
// segment are tested: sampling (not just the midpoint) is needed because a
// chord can enter and leave a polygon through vertices only, which is not a
// proper edge crossing, while its midpoint happens to lie outside.
func segBlockedByPolygon(a, b Point, edge, core Polygon, samples int) bool {
	n := len(edge)
	for i := 0; i < n; i++ {
		c, d := edge[i], edge[(i+1)%n]
		if segProperIntersect(a, b, c, d) {
			return true
		}
	}
	for k := 1; k < samples; k++ {
		t := float64(k) / float64(samples)
		p := Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
		if pointInPolygon(p, core) {
			return true
		}
	}
	return false
}

// segCrossesBoundary reports whether segment ab exits the boundary polygon.
func segCrossesBoundary(a, b Point, boundary Polygon, samples int) bool {
	n := len(boundary)
	for i := 0; i < n; i++ {
		c, d := boundary[i], boundary[(i+1)%n]
		if segProperIntersect(a, b, c, d) {
			return true
		}
	}
	for k := 1; k < samples; k++ {
		t := float64(k) / float64(samples)
		p := Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
		if !pointInPolygon(p, boundary) {
			return true
		}
	}
	return false
}

// signedArea returns the signed area of a polygon (positive when CCW).
func signedArea(poly Polygon) float64 {
	s, n := 0.0, len(poly)
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		s += a.X*b.Y - b.X*a.Y
	}
	return s / 2
}

// ensureCCW returns poly wound counter-clockwise.
func ensureCCW(poly Polygon) Polygon {
	if signedArea(poly) >= 0 {
		return poly
	}
	out := make(Polygon, len(poly))
	for i, p := range poly {
		out[len(poly)-1-i] = p
	}
	return out
}

// offsetConvexOut returns the true outward Minkowski offset of a convex polygon
// by margin: every edge is pushed out by margin along its outward normal and
// each convex corner is rounded with an arc of radius margin, arcStep radians
// per arc point. This keeps a uniform stand-off from the whole boundary —
// unlike centroid scaling, which under-offsets edges relative to corners — and
// rounds corners to radius=margin as a side effect.
func offsetConvexOut(poly Polygon, margin, arcStep float64) Polygon {
	if margin <= 0 || len(poly) < 3 {
		return poly.Clone()
	}
	p := ensureCCW(poly)
	n := len(p)
	normal := func(i int) Point { // outward normal of edge p[i]->p[i+1] (CCW)
		d := p[(i+1)%n].sub(p[i])
		return unit(Point{d.Y, -d.X})
	}
	var out Polygon
	for i := 0; i < n; i++ {
		nPrev := normal((i - 1 + n) % n) // edge arriving at vertex i
		nNext := normal(i)               // edge leaving vertex i
		a0 := math.Atan2(nPrev.Y, nPrev.X)
		a1 := math.Atan2(nNext.Y, nNext.X)
		delta := a1 - a0
		for delta <= -math.Pi {
			delta += 2 * math.Pi
		}
		for delta > math.Pi {
			delta -= 2 * math.Pi
		}
		steps := int(math.Abs(delta)/arcStep) + 1
		// Circumscribe the corner arc instead of inscribing it: with the arc
		// points pushed out to margin/cos(step/2), the chords between them run
		// tangent to the true arc and the offset keeps the full margin
		// everywhere. Inscribed points would leave the chords cutting a sagitta
		// deep into the safety margin the caller asked for.
		r := margin / math.Cos(math.Abs(delta)/float64(steps)/2)
		for k := 0; k <= steps; k++ {
			ang := a0 + delta*float64(k)/float64(steps)
			out = append(out, Point{p[i].X + r*math.Cos(ang), p[i].Y + r*math.Sin(ang)})
		}
	}
	return out
}

// erodeConvex returns the inward offset of a convex polygon by margin: each edge
// moved inward along its normal, corners mitered. Used to keep the path a margin
// inside the outer limit.
func erodeConvex(poly Polygon, margin float64) Polygon {
	if margin <= 0 || len(poly) < 3 {
		return poly.Clone()
	}
	p := ensureCCW(poly)
	n := len(p)
	type line struct{ pt, dir Point }
	lines := make([]line, n)
	for i := 0; i < n; i++ {
		a, b := p[i], p[(i+1)%n]
		d := b.sub(a)
		inN := unit(Point{-d.Y, d.X}) // inward normal for CCW
		lines[i] = line{a.add(inN.scale(margin)), unit(d)}
	}
	out := make(Polygon, n)
	for i := 0; i < n; i++ {
		prev, cur := lines[(i-1+n)%n], lines[i]
		if pt, ok := lineIntersect(prev.pt, prev.dir, cur.pt, cur.dir); ok {
			out[i] = pt
		} else {
			out[i] = cur.pt
		}
	}
	return out
}

// lineIntersect returns the intersection of lines (p1,dir d1) and (p2,dir d2).
func lineIntersect(p1, d1, p2, d2 Point) (Point, bool) {
	den := d1.X*d2.Y - d1.Y*d2.X
	if math.Abs(den) < 1e-12 {
		return Point{}, false
	}
	t := ((p2.X-p1.X)*d2.Y - (p2.Y-p1.Y)*d2.X) / den
	return Point{p1.X + d1.X*t, p1.Y + d1.Y*t}, true
}

// discretizeCircle returns a polygon approximating a circle. The polygon
// circumscribes it — its edges run tangent to the circle at distance r — so the
// approximation errs on the side of a slightly larger obstacle. An inscribed
// polygon would be the unsafe direction: it would model a dead zone smaller
// than the one drawn.
func discretizeCircle(center Point, r float64, segments int) Polygon {
	rv := r / math.Cos(math.Pi/float64(segments)) // vertex radius
	poly := make(Polygon, segments)
	for i := 0; i < segments; i++ {
		t := 2 * math.Pi * float64(i) / float64(segments)
		poly[i] = Point{center.X + rv*math.Cos(t), center.Y + rv*math.Sin(t)}
	}
	return poly
}

// discretizeEllipse returns a polygon approximating a DXF ELLIPSE defined by a
// center, the endpoint of the major axis relative to the center, the ratio of
// minor to major axis, and start/end parameters (radians). Both axes are grown
// by the same factor the circle case uses, which to first order covers the
// chord sagitta at the sharp end too, so the polygon contains the true ellipse.
func discretizeEllipse(center, majorRel Point, ratio, start, end float64, segments int) Polygon {
	grow := 1 / math.Cos(math.Pi/float64(segments))
	major := math.Hypot(majorRel.X, majorRel.Y) * grow
	minor := major * ratio
	rot := math.Atan2(majorRel.Y, majorRel.X)
	if end <= start {
		end += 2 * math.Pi
	}
	// A full ellipse closes on itself, so t=end would repeat t=start and the
	// loop stops one short. A partial arc does NOT: its ring is closed by the
	// chord between the two ends, so the t=end vertex must be emitted —
	// dropping it would close the chord from the second-to-last sample and
	// carve the arc's whole last slice out of the dead zone.
	last := segments - 1
	if full := math.Abs((end-start)-2*math.Pi) < 1e-9; !full {
		last = segments
	}
	poly := make(Polygon, 0, last+1)
	for i := 0; i <= last; i++ {
		t := start + (end-start)*float64(i)/float64(segments)
		ex := major * math.Cos(t)
		ey := minor * math.Sin(t)
		poly = append(poly, Point{
			center.X + ex*math.Cos(rot) - ey*math.Sin(rot),
			center.Y + ex*math.Sin(rot) + ey*math.Cos(rot),
		})
	}
	return poly
}

// --- Bounding boxes ----------------------------------------------------------

// box is an axis-aligned bounding box, used to skip segment tests against
// obstacles the segment cannot possibly touch.
type box struct {
	minX, minY, maxX, maxY float64
}

func boundsOf(poly Polygon) box {
	b := box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range poly {
		b.minX = math.Min(b.minX, p.X)
		b.minY = math.Min(b.minY, p.Y)
		b.maxX = math.Max(b.maxX, p.X)
		b.maxY = math.Max(b.maxY, p.Y)
	}
	return b
}

func (b box) contains(p Point) bool {
	return p.X >= b.minX && p.X <= b.maxX && p.Y >= b.minY && p.Y <= b.maxY
}

// overlapsSegment reports whether the bounding box of segment ab overlaps b.
// A false answer proves the segment misses the polygon; a true one proves
// nothing and the exact tests follow.
func (b box) overlapsSegment(a, c Point) bool {
	return math.Max(a.X, c.X) >= b.minX && math.Min(a.X, c.X) <= b.maxX &&
		math.Max(a.Y, c.Y) >= b.minY && math.Min(a.Y, c.Y) <= b.maxY
}

// --- Validation --------------------------------------------------------------

const (
	// convexEps is the sine of the smallest turn counted as a real turn; below
	// it a vertex is collinear, which convex rings are allowed to be (arc
	// discretization produces plenty of near-collinear vertices).
	convexEps = 1e-9
	// dupEps is the distance below which two ring vertices are the same point.
	dupEps = 1e-9
	// turnEps is how far the total turning of a simple ring may stray from 2π.
	turnEps = 1e-6
)

// dedupeRing drops consecutive duplicate vertices, including a closing vertex
// that repeats the first one. CAD exports produce both.
func dedupeRing(poly Polygon) Polygon {
	out := make(Polygon, 0, len(poly))
	for _, p := range poly {
		if len(out) > 0 && out[len(out)-1].dist(p) < dupEps {
			continue
		}
		out = append(out, p)
	}
	for len(out) > 1 && out[0].dist(out[len(out)-1]) < dupEps {
		out = out[:len(out)-1]
	}
	return out
}

// checkConvex verifies that poly is a simple, non-degenerate, convex ring —
// the only shape class the planner's offsetting and visibility graph are valid
// for (D7). It names the offending vertex so a drawing mistake is easy to find.
func checkConvex(poly Polygon) error {
	n := len(poly)
	if n < 3 {
		return fmt.Errorf("needs at least 3 vertices, has %d", n)
	}
	area := signedArea(poly)
	if math.Abs(area) < dupEps {
		// Either a sliver or a ring whose lobes cancel out, like a bowtie.
		return fmt.Errorf("is degenerate: it encloses no area")
	}
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	total := 0.0
	for i := 0; i < n; i++ {
		prev, cur, next := poly[(i-1+n)%n], poly[i], poly[(i+1)%n]
		in, out := cur.sub(prev), next.sub(cur)
		lIn, lOut := math.Hypot(in.X, in.Y), math.Hypot(out.X, out.Y)
		if lIn < dupEps || lOut < dupEps {
			return fmt.Errorf("vertex %d (%.3f,%.3f) repeats its neighbour", i, cur.X, cur.Y)
		}
		sin := (in.X*out.Y - in.Y*out.X) / (lIn * lOut)
		cos := (in.X*out.X + in.Y*out.Y) / (lIn * lOut)
		if sin*sign < -convexEps {
			return fmt.Errorf("is not convex: vertex %d (%.3f,%.3f) turns the wrong way", i, cur.X, cur.Y)
		}
		total += math.Atan2(sin, cos)
	}
	// A simple ring turns by exactly ±2π. A self-intersecting one (a star, say)
	// can keep every single turn on the convex side while winding twice, so the
	// per-vertex test alone would pass it.
	if math.Abs(math.Abs(total)-2*math.Pi) > turnEps {
		return fmt.Errorf("is self-intersecting: total turn is %.1f°, expected 360°", total*180/math.Pi)
	}
	return nil
}
