// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"errors"
	"fmt"
	"math"
)

// Smooth paths through the route (D47).
//
// The visibility-graph route decides which side of each dead zone a leg
// passes; the path driven may instead be a smooth curve through the route's
// corner points. For bounded acceleration the time-optimal path is a smooth
// curve, not the shortest polyline: the shortest one needs a stop at every
// sharp corner, and the trajectory planner's blend arcs only round them on a
// radius of the blend tolerance.

// Shaper picks the geometry a leg drives.
type Shaper int

const (
	// ShaperRoute drives the route itself: its polyline, the corners
	// blended by the trajectory planner.
	ShaperRoute Shaper = iota
	// ShaperNatural drives a natural cubic spline through the route's corner
	// points: C2, chord-length parameters.
	ShaperNatural
	// ShaperCatmullRom drives a centripetal Catmull-Rom curve through them:
	// local (a key moves only its neighbourhood), no loops or cusps, staying
	// close to the polygon of its keys; C1 only.
	ShaperCatmullRom
)

func (s Shaper) String() string {
	switch s {
	case ShaperRoute:
		return "route"
	case ShaperNatural:
		return "natural"
	case ShaperCatmullRom:
		return "catmull-rom"
	}
	return fmt.Sprintf("Shaper(%d)", int(s))
}

// ErrNoCurve is returned by [Planner.Smooth] when no curve through the route
// keeps the clearance. The route itself still does: it is the safety net.
var ErrNoCurve = errors.New("no clear curve through the route")

const (
	// curveStep is the sample spacing along a curve, mm: fine against the
	// clearance and the arc chain's piece length, a few thousand samples on
	// the longest leg.
	curveStep = 0.25
	// pushMargin is how far beyond the clearance a key is pushed, mm, so a
	// refit lands on the clear side rather than on the boundary again.
	pushMargin = 0.02
	// maxPushes bounds the push-out loop. A curve that does not come clear
	// within it falls back to the route.
	maxPushes = 40
	// keyNearFactor: a shortfall farther along the curve than this many
	// clearances from every interior key gets a key of its own instead of
	// pushing a distant one.
	keyNearFactor = 3
)

// Smooth shapes r -- a route from this planner -- into a smooth curve with
// sh and returns it as a new Route: the same Waypoints, Shape sh, Curve the
// curve sampled every [curveStep] mm from start to goal, Length its length.
//
// The curve's keys are the route's corner points (Douglas-Peucker at the
// clearance). Where the curve falls short of the clearance -- measured to the
// zones AS DRAWN and to the drawn outer limit, the margin it really keeps --
// the key nearest the shortfall is pushed away from the obstacle (or, far
// from any key, a key is added there) and the curve refitted. If that never
// comes clear, Smooth returns [ErrNoCurve]: the route is the safety net, and
// a curve may improve a leg, never make it impossible.
//
// A straight route is returned as it is (Shape [ShaperRoute]): it has no
// corner to smooth. ShaperRoute returns r itself.
func (p *Planner) Smooth(r *Route, sh Shaper) (*Route, error) {
	var interp func([]Point, float64) []Point
	switch sh {
	case ShaperRoute:
		return r, nil
	case ShaperNatural:
		interp = naturalCubic
	case ShaperCatmullRom:
		interp = centripetalCR
	default:
		return nil, fmt.Errorf("pnproute: unknown shaper %v", sh)
	}
	if len(r.Waypoints) <= 2 {
		return r, nil
	}
	curve, ok := p.pushOut(simplifyDP(r.Waypoints, p.clearance), interp)
	if !ok {
		return nil, fmt.Errorf("pnproute: %v: %w", sh, ErrNoCurve)
	}
	return &Route{Waypoints: r.Waypoints, Shape: sh, Curve: curve, Length: polyLength(curve)}, nil
}

// pushOut fits interp through keys and moves keys until the curve keeps the
// clearance. It returns the clear curve, or false.
func (p *Planner) pushOut(keys []Point, interp func([]Point, float64) []Point) ([]Point, bool) {
	c := p.clearance
	for iter := 0; iter < maxPushes; iter++ {
		pts := interp(keys, curveStep)
		if !finitePoints(pts) {
			return nil, false
		}
		d, idx, q := p.curveClearance(pts, c)
		if d >= c-1e-9 {
			return pts, true
		}
		bad := pts[idx]
		away := unit(bad.sub(q))
		if d < 0 {
			away = away.scale(-1) // inside an obstacle: its nearest boundary point is ahead
		}
		push := c - d + pushMargin
		// The interior key whose position along the key polygon is nearest
		// to the shortfall's position along the curve.
		sBad := polyLength(pts[:idx+1])
		best, bestDS := -1, math.Inf(1)
		acc := 0.0
		for k := 1; k < len(keys)-1; k++ {
			acc += keys[k-1].dist(keys[k])
			if ds := math.Abs(acc - sBad); ds < bestDS {
				best, bestDS = k, ds
			}
		}
		if best < 0 || bestDS > keyNearFactor*c {
			// Far from every key: a key of its own, pushed clear, between
			// the keys the shortfall lies between.
			nk := bad.add(away.scale(push))
			ins := len(keys) - 1
			acc = 0
			for k := 1; k < len(keys); k++ {
				acc += keys[k-1].dist(keys[k])
				if acc >= sBad {
					ins = k
					break
				}
			}
			keys = append(keys[:ins:ins], append([]Point{nk}, keys[ins:]...)...)
			continue
		}
		keys[best] = keys[best].add(away.scale(push))
	}
	return nil, false
}

// curveClearance is the smallest clearance over pts, the index where it
// occurs and the nearest obstacle point there. Obstacles farther than limit
// are not measured: a result at or above limit only says the points keep
// limit (pass +Inf for the exact value).
func (p *Planner) curveClearance(pts []Point, limit float64) (min float64, idx int, q Point) {
	min = math.Inf(1)
	for i, pt := range pts {
		if d, qq := p.pointClearance(pt, math.Min(min, limit)); d < min {
			min, idx, q = d, i, qq
		}
	}
	return min, idx, q
}

// pointClearance is the signed distance from pt to the nearest obstacle as
// drawn -- positive outside every dead zone and inside the outer limit --
// and the nearest obstacle point. Zones whose bounding box is farther than
// limit are skipped (pass +Inf for an exact value).
func (p *Planner) pointClearance(pt Point, limit float64) (float64, Point) {
	best, at := nearestOnRing(pt, p.outer)
	if !pointInPolygon(pt, p.outer) {
		best = -best
	}
	for _, z := range p.drawn {
		if z.bounds.distTo(pt) > math.Min(best, limit) {
			continue
		}
		d, q := nearestOnRing(pt, z.edge)
		if pointInPolygon(pt, z.edge) {
			d = -d
		}
		if d < best {
			best, at = d, q
		}
	}
	return best, at
}

// nearestOnRing is the distance from pt to ring's boundary and the nearest
// boundary point.
func nearestOnRing(pt Point, ring Polygon) (float64, Point) {
	best, at := math.Inf(1), Point{}
	n := len(ring)
	for i := 0; i < n; i++ {
		a, b := ring[i], ring[(i+1)%n]
		d, t := pointSegDistanceT(pt, a, b)
		if d < best {
			best, at = d, Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
		}
	}
	return best, at
}

// distTo is the distance from pt to the box, 0 inside it.
func (b box) distTo(pt Point) float64 {
	dx := math.Max(math.Max(b.minX-pt.X, pt.X-b.maxX), 0)
	dy := math.Max(math.Max(b.minY-pt.Y, pt.Y-b.maxY), 0)
	return math.Hypot(dx, dy)
}

func polyLength(pts []Point) float64 {
	l := 0.0
	for i := 1; i < len(pts); i++ {
		l += pts[i-1].dist(pts[i])
	}
	return l
}

func finitePoints(pts []Point) bool {
	for _, pt := range pts {
		if math.IsNaN(pt.X) || math.IsNaN(pt.Y) || math.IsInf(pt.X, 0) || math.IsInf(pt.Y, 0) {
			return false
		}
	}
	return len(pts) > 0
}

// dedupePoints drops consecutive points closer than eps, keeping the last
// point exact.
func dedupePoints(pts []Point, eps float64) []Point {
	out := make([]Point, 0, len(pts))
	for _, pt := range pts {
		if len(out) > 0 && out[len(out)-1].dist(pt) < eps {
			continue
		}
		out = append(out, pt)
	}
	if len(out) > 1 {
		out[len(out)-1] = pts[len(pts)-1]
	}
	return out
}

// simplifyDP is Douglas-Peucker: the points of pts a polyline needs to stay
// within eps of it, first and last always kept.
func simplifyDP(pts []Point, eps float64) []Point {
	if len(pts) < 3 {
		return append([]Point(nil), pts...)
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	var rec func(i, j int)
	rec = func(i, j int) {
		best, at := -1.0, -1
		for k := i + 1; k < j; k++ {
			if d := pointSegDistance(pts[k], pts[i], pts[j]); d > best {
				best, at = d, k
			}
		}
		if at >= 0 && best > eps {
			keep[at] = true
			rec(i, at)
			rec(at, j)
		}
	}
	rec(0, len(pts)-1)
	var out []Point
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

// sampleLine samples the polyline through keys about ds apart, every key
// included: the straight-line case of the interpolators, sampled like a
// curve so its whole length gets checked.
func sampleLine(keys []Point, ds float64) []Point {
	var out []Point
	for i := 1; i < len(keys); i++ {
		n := int(math.Ceil(keys[i-1].dist(keys[i])/ds)) + 1
		for k := 0; k < n; k++ {
			t := float64(k) / float64(n)
			out = append(out, Point{keys[i-1].X + (keys[i].X-keys[i-1].X)*t, keys[i-1].Y + (keys[i].Y-keys[i-1].Y)*t})
		}
	}
	return append(out, keys[len(keys)-1])
}

// naturalCubic interpolates keys with a C2 cubic spline in chord-length
// parameterization (zero second derivative at both ends) and samples it every
// ds of parameter length, every key exact.
func naturalCubic(keys []Point, ds float64) []Point {
	keys = dedupePoints(keys, 1e-6) // a zero-length interval has no chord parameter
	m := len(keys)
	if m < 3 {
		return sampleLine(keys, ds)
	}
	d := make([]float64, m)
	for i := 1; i < m; i++ {
		d[i] = d[i-1] + keys[i-1].dist(keys[i])
	}
	h := make([]float64, m-1)
	for i := range h {
		h[i] = d[i+1] - d[i]
	}
	coef := func(y []float64) (b, c, dd []float64) {
		// The Thomas algorithm on the tridiagonal system for c, natural ends.
		c = make([]float64, m)
		lo, diag, up, rhs := make([]float64, m), make([]float64, m), make([]float64, m), make([]float64, m)
		diag[0], diag[m-1] = 1, 1
		for i := 1; i < m-1; i++ {
			lo[i], diag[i], up[i] = h[i-1], 2*(h[i-1]+h[i]), h[i]
			rhs[i] = 3 * ((y[i+1]-y[i])/h[i] - (y[i]-y[i-1])/h[i-1])
		}
		for i := 1; i < m; i++ {
			w := lo[i] / diag[i-1]
			diag[i] -= w * up[i-1]
			rhs[i] -= w * rhs[i-1]
		}
		c[m-1] = rhs[m-1] / diag[m-1]
		for i := m - 2; i >= 0; i-- {
			c[i] = (rhs[i] - up[i]*c[i+1]) / diag[i]
		}
		b, dd = make([]float64, m-1), make([]float64, m-1)
		for i := 0; i < m-1; i++ {
			b[i] = (y[i+1]-y[i])/h[i] - h[i]*(2*c[i]+c[i+1])/3
			dd[i] = (c[i+1] - c[i]) / (3 * h[i])
		}
		return b, c, dd
	}
	xs, ys := make([]float64, m), make([]float64, m)
	for i, k := range keys {
		xs[i], ys[i] = k.X, k.Y
	}
	bx, cx, dx := coef(xs)
	by, cy, dy := coef(ys)
	// Sampled per interval, so every key is a sample.
	out := []Point{keys[0]}
	for seg := 0; seg < m-1; seg++ {
		n := int(math.Ceil(h[seg] / ds))
		for k := 1; k <= n; k++ {
			t := h[seg] * float64(k) / float64(n)
			out = append(out, Point{
				X: xs[seg] + bx[seg]*t + cx[seg]*t*t + dx[seg]*t*t*t,
				Y: ys[seg] + by[seg]*t + cy[seg]*t*t + dy[seg]*t*t*t,
			})
		}
		out[len(out)-1] = keys[seg+1]
	}
	return out
}

// centripetalCR interpolates keys with a centripetal Catmull-Rom spline
// (alpha = 0.5, Barry-Goldman evaluation), the ends extended by mirrored
// phantom points, sampled about ds apart, through every key.
func centripetalCR(keys []Point, ds float64) []Point {
	keys = dedupePoints(keys, 1e-6)
	m := len(keys)
	if m < 3 {
		return sampleLine(keys, ds)
	}
	pts := make([]Point, 0, m+2)
	pts = append(pts, keys[0].scale(2).sub(keys[1]))
	pts = append(pts, keys...)
	pts = append(pts, keys[m-1].scale(2).sub(keys[m-2]))
	mix := func(a, b Point, ta, tb, t float64) Point {
		if tb == ta {
			return a
		}
		return a.scale((tb - t) / (tb - ta)).add(b.scale((t - ta) / (tb - ta)))
	}
	out := []Point{keys[0]}
	for i := 1; i+2 < len(pts); i++ {
		p0, p1, p2, p3 := pts[i-1], pts[i], pts[i+1], pts[i+2]
		t0 := 0.0
		t1 := t0 + math.Sqrt(p0.dist(p1))
		t2 := t1 + math.Sqrt(p1.dist(p2))
		t3 := t2 + math.Sqrt(p2.dist(p3))
		n := int(math.Ceil(p1.dist(p2)/ds)) + 1
		for k := 1; k <= n; k++ {
			t := t1 + (t2-t1)*float64(k)/float64(n)
			a1, a2, a3 := mix(p0, p1, t0, t1, t), mix(p1, p2, t1, t2, t), mix(p2, p3, t2, t3, t)
			b1, b2 := mix(a1, a2, t0, t2, t), mix(a2, a3, t1, t3, t)
			out = append(out, mix(b1, b2, t1, t2, t))
		}
		out[len(out)-1] = p2 // exactly through the key
	}
	return out
}
