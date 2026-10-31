// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import "math"

// Executable geometry: lines, tangent arcs and stops, and their samples.
//
// A polyline as the trajectory planner runs it (corners blended into arcs),
// a sampled curve with its curvature rebuilt, and an arc chain all become
// prims; the TP model and the clearance check work on their samples.

// prim is one piece of an executable path: a straight line, a circular arc,
// or a stop marker (a sharp corner the motion has to come to rest at).
type prim struct {
	kind  primKind
	a, b  Point   // endpoints
	c     Point   // arc center
	r     float64 // arc radius
	a0    float64 // arc start angle
	sweep float64 // signed arc angle, positive = counter-clockwise
	seg   int     // line: index of the source polyline segment
	vtx   int     // arc/stop: index of the source polyline vertex
}

type primKind int

const (
	primLine primKind = iota
	primArc
	primStop
)

func (p prim) length() float64 {
	switch p.kind {
	case primLine:
		return p.a.dist(p.b)
	case primArc:
		return p.r * math.Abs(p.sweep)
	}
	return 0
}

func dot(a, b Point) float64    { return a.X*b.X + a.Y*b.Y }
func cross2(a, b Point) float64 { return a.X*b.Y - a.Y*b.X }
func norm(a Point) float64      { return math.Hypot(a.X, a.Y) }
func lerp(a, b Point, t float64) Point {
	return Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
}

// filletize turns a polyline into lines joined by tangent arcs, sized the way
// the trajectory planner sizes a blend arc (blendmath.c
// blendComputeParameters): the largest radius whose deviation from the corner
// stays within tol and whose tangent length takes at most half of either
// adjacent segment (BLEND_DIST_FRACTION). tol < 0 drops the tolerance bound
// (the polyline is a sampled curve: rebuild its curvature); tol == 0 keeps
// every corner sharp, a stop.
func filletize(poly []Point, tol float64) []prim {
	pts := dedupePoints(poly, 1e-9)
	var out []prim
	if len(pts) < 2 {
		return out
	}
	cur := pts[0]
	line := func(to Point, seg int) {
		if cur.dist(to) > 1e-12 {
			out = append(out, prim{kind: primLine, a: cur, b: to, seg: seg})
		}
		cur = to
	}
	for i := 1; i < len(pts)-1; i++ {
		u := unit(pts[i].sub(pts[i-1]))
		w := unit(pts[i+1].sub(pts[i]))
		cr, dt := cross2(u, w), dot(u, w)
		defl := math.Atan2(math.Abs(cr), dt)
		if defl < 1e-9 {
			continue // collinear: the line runs on
		}
		if tol == 0 || defl > math.Pi-1e-9 {
			line(pts[i], i-1)
			out = append(out, prim{kind: primStop, a: pts[i], b: pts[i], vtx: i})
			continue
		}
		th := math.Tan(defl / 2)
		lmax := 0.5 * math.Min(pts[i-1].dist(pts[i]), pts[i].dist(pts[i+1]))
		r := lmax / th
		if tol > 0 {
			r = math.Min(r, tol/(1/math.Cos(defl/2)-1))
		}
		d := r * th
		start, end := pts[i].sub(u.scale(d)), pts[i].add(w.scale(d))
		n := Point{-u.Y, u.X}
		sweep := defl
		if cr < 0 {
			n, sweep = n.scale(-1), -defl
		}
		c := start.add(n.scale(r))
		line(start, i-1)
		out = append(out, prim{kind: primArc, a: start, b: end, c: c, r: r,
			a0: math.Atan2(start.Y-c.Y, start.X-c.X), sweep: sweep, vtx: i})
		cur = end
	}
	line(pts[len(pts)-1], len(pts)-2)
	return out
}

// sample is a point on a path with its arc length, tangent direction, signed
// curvature (positive turning left) and the prim it belongs to.
type sample struct {
	s     float64
	p     Point
	phi   float64
	kappa float64
	stop  bool
	prim  int
}

// samplePrims samples prims at most h apart. Where two prims meet, the shared
// sample keeps the larger curvature, so a speed limit is never missed.
func samplePrims(prims []prim, h float64) []sample {
	var out []sample
	s := 0.0
	push := func(sm sample, first bool) {
		if len(out) > 0 && first {
			last := &out[len(out)-1]
			if math.Abs(sm.kappa) > math.Abs(last.kappa) {
				last.kappa = sm.kappa
			}
			return
		}
		if len(out) > 0 {
			s += out[len(out)-1].p.dist(sm.p)
		}
		sm.s = s
		out = append(out, sm)
	}
	for pi, pm := range prims {
		switch pm.kind {
		case primStop:
			if len(out) > 0 {
				out[len(out)-1].stop = true
			}
		case primLine:
			phi := math.Atan2(pm.b.Y-pm.a.Y, pm.b.X-pm.a.X)
			k := int(math.Max(1, math.Ceil(pm.length()/h)))
			for j := 0; j <= k; j++ {
				push(sample{p: lerp(pm.a, pm.b, float64(j)/float64(k)), phi: phi, prim: pi}, j == 0)
			}
		case primArc:
			k := int(math.Max(1, math.Ceil(pm.length()/h)))
			kap, off := 1/pm.r, math.Pi/2
			if pm.sweep < 0 {
				kap, off = -kap, -off
			}
			for j := 0; j <= k; j++ {
				t := pm.a0 + pm.sweep*float64(j)/float64(k)
				p := Point{pm.c.X + pm.r*math.Cos(t), pm.c.Y + pm.r*math.Sin(t)}
				push(sample{p: p, phi: t + off, kappa: kap, prim: pi}, j == 0)
			}
		}
	}
	return out
}

func samplePoints(sm []sample) []Point {
	out := make([]Point, len(sm))
	for i, s := range sm {
		out[i] = s.p
	}
	return out
}
