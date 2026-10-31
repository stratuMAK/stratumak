// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// The curve shapers (D47). The hand-built scenes are one or two rectangles
// the route has to turn around, in an outer limit large enough that nothing
// but the rectangles decides the shape of the route.

func rect(x0, y0, x1, y1 float64) Polygon {
	return Polygon{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

func rectScene(zones ...Polygon) *Scene {
	s := &Scene{Outer: rect(0, 0, 1000, 600)}
	for _, z := range zones {
		s.Deadzones = append(s.Deadzones, Shape{Kind: ShapePolyline, Poly: z})
	}
	return s
}

func mustPlanner(t testing.TB, s *Scene, clearance float64, opts ...Option) *Planner {
	t.Helper()
	p, err := NewPlanner(s, clearance, opts...)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return p
}

func mustPlan(t testing.TB, p *Planner, a, b Point) *Route {
	t.Helper()
	r, err := p.Plan(a, b)
	if err != nil {
		t.Fatalf("Plan(%v, %v): %v", a, b, err)
	}
	return r
}

var allCurves = []Shaper{ShaperNatural, ShaperCatmullRom}

// assertCurve checks a smoothed route: from start to goal, its Path the
// curve, and clear by the planner's clearance -- by the shaper's own check
// and by the independent segment scan of Metrics.
func assertCurve(t *testing.T, p *Planner, r, c *Route, sh Shaper) {
	t.Helper()
	if c.Shape != sh || c.Curve == nil {
		t.Fatalf("%v: got shape %v with %d curve points", sh, c.Shape, len(c.Curve))
	}
	start, goal := r.Waypoints[0], r.Waypoints[len(r.Waypoints)-1]
	if c.Curve[0] != start || c.Curve[len(c.Curve)-1] != goal {
		t.Fatalf("%v: curve runs %v..%v, want %v..%v", sh, c.Curve[0], c.Curve[len(c.Curve)-1], start, goal)
	}
	for i := 1; i < len(c.Curve); i++ {
		if d := c.Curve[i-1].dist(c.Curve[i]); d > 2*curveStep || d < 1e-9 {
			t.Fatalf("%v: curve samples %d..%d are %.4f apart", sh, i-1, i, d)
		}
	}
	if math.Abs(c.Length-polyLength(c.Curve)) > 1e-9 {
		t.Errorf("%v: length %.6f is not the curve's %.6f", sh, c.Length, polyLength(c.Curve))
	}
	if d, _, _ := p.curveClearance(c.Curve, math.Inf(1)); d < p.clearance-1e-9 {
		t.Errorf("%v: curve keeps %.4f, want the clearance %g", sh, d, p.clearance)
	}
	p.Metrics(c)
	if c.MinClear < p.clearance-1e-6 {
		t.Errorf("%v: Metrics finds %.4f to a dead zone, want the clearance %g", sh, c.MinClear, p.clearance)
	}
}

// A route over a box becomes a curve through its corners that keeps the
// clearance and turns on far wider radii than the route's clearance-sized
// corner arcs.
func TestSmoothOverABox(t *testing.T) {
	const clearance = 5.0
	p := mustPlanner(t, rectScene(rect(400, 0, 600, 300)), clearance)
	r := mustPlan(t, p, Point{300, 100}, Point{700, 100})
	p.Metrics(r)
	for _, sh := range allCurves {
		c, err := p.Smooth(r, sh)
		if err != nil {
			t.Fatalf("%v: %v", sh, err)
		}
		assertCurve(t, p, r, c, sh)
		if c.MinRadius < 4*r.MinRadius {
			t.Errorf("%v: turns on %.1f mm, the route on %.1f -- no wider", sh, c.MinRadius, r.MinRadius)
		}
	}
}

// A straight route has nothing to smooth, and ShaperRoute is the route.
func TestSmoothStraightAndRoute(t *testing.T) {
	p := mustPlanner(t, rectScene(rect(400, 0, 600, 300)), 5)
	r := mustPlan(t, p, Point{100, 400}, Point{900, 450})
	for _, sh := range append(allCurves, ShaperRoute) {
		c, err := p.Smooth(r, sh)
		if err != nil || c != r || c.Shape != ShaperRoute || c.Curve != nil {
			t.Errorf("%v: straight route came back as %+v, %v", sh, c, err)
		}
	}
	bent := mustPlan(t, p, Point{300, 100}, Point{700, 100})
	if c, err := p.Smooth(bent, ShaperRoute); err != nil || c != bent {
		t.Errorf("ShaperRoute returned %+v, %v, want the route itself", c, err)
	}
	if _, err := p.Smooth(bent, Shaper(99)); err == nil {
		t.Error("an unknown shaper was accepted")
	}
}

// Keys that put the curve through a zone are pushed out until it clears.
func TestPushOut(t *testing.T) {
	const clearance = 5.0
	p := mustPlanner(t, rectScene(rect(400, 0, 600, 300)), clearance)
	keys := []Point{{300, 100}, {500, 310}, {700, 100}}
	for _, interp := range []func([]Point, float64) []Point{naturalCubic, centripetalCR} {
		raw := interp(append([]Point(nil), keys...), curveStep)
		if d, _, _ := p.curveClearance(raw, math.Inf(1)); d >= clearance {
			t.Fatalf("the raw curve keeps %.2f already; the test needs one that cuts the zone", d)
		}
		pts, ok := p.pushOut(append([]Point(nil), keys...), interp)
		if !ok {
			t.Fatal("push-out found no clear curve")
		}
		if d, _, _ := p.curveClearance(pts, math.Inf(1)); d < clearance-1e-9 {
			t.Errorf("pushed curve keeps %.3f", d)
		}
		if pts[0] != keys[0] || pts[len(pts)-1] != keys[2] {
			t.Errorf("push-out moved the ends: %v..%v", pts[0], pts[len(pts)-1])
		}
	}
}

// Where no curve comes clear, Smooth says so with ErrNoCurve -- the caller
// drives the route. A slot between two boxes no wider than twice the
// clearance plus a hair leaves the route (straight through the slot, with
// corners at both ends) clear, but nothing curved fits.
func TestSmoothNoCurve(t *testing.T) {
	const clearance = 5.0
	p := mustPlanner(t, rectScene(rect(300, 0, 700, 295), rect(300, 305.5, 700, 600)), clearance)
	r := mustPlan(t, p, Point{200, 100}, Point{800, 500})
	if len(r.Waypoints) < 3 {
		t.Fatalf("route %v does not turn", r.Waypoints)
	}
	for _, sh := range allCurves {
		c, err := p.Smooth(r, sh)
		if err == nil {
			// A curve may still come clear; then it must really be clear.
			assertCurve(t, p, r, c, sh)
			continue
		}
		if !errors.Is(err, ErrNoCurve) {
			t.Errorf("%v: %v, want ErrNoCurve", sh, err)
		}
	}
}

// Both interpolators pass through their keys and end exactly on them.
func TestInterpolatorsThroughKeys(t *testing.T) {
	keys := []Point{{0, 0}, {40, 30}, {45, 31}, {130, 0}}
	for name, interp := range map[string]func([]Point, float64) []Point{"natural": naturalCubic, "catmull-rom": centripetalCR} {
		pts := interp(keys, 0.25)
		if pts[0] != keys[0] || pts[len(pts)-1] != keys[len(keys)-1] {
			t.Errorf("%s: ends %v..%v", name, pts[0], pts[len(pts)-1])
		}
		for _, k := range keys {
			best := math.Inf(1)
			for _, q := range pts {
				best = math.Min(best, q.dist(k))
			}
			if best > 1e-6 {
				t.Errorf("%s: key %v missed by %.3g", name, k, best)
			}
		}
	}
}

// The natural spline is C2: its sampled turning rate changes smoothly, with
// no jump at the keys.
func TestNaturalCubicIsSmooth(t *testing.T) {
	pts := naturalCubic([]Point{{0, 0}, {40, 30}, {90, 35}, {130, 0}}, 0.25)
	k := pathCurvatures(pts)
	for i := 2; i < len(k)-2; i++ {
		if math.Abs(k[i]-k[i-1]) > 2e-4 {
			t.Fatalf("curvature jumps from %.5f to %.5f at %v", k[i-1], k[i], pts[i])
		}
	}
}

// The coating machine's drawings, when CoatV2conf is checked out next to
// this repository: every curve the shapers return on random legs keeps the
// clearance, and most legs get one.
func TestSmoothCoatDrawings(t *testing.T) {
	files, _ := filepath.Glob(coatDir() + "/deadzones-sphere-*.dxf")
	if len(files) == 0 {
		t.Skip("CoatV2conf not checked out next to this repository")
	}
	for _, f := range files {
		p := mustPlanner(t, loadScene(t, f), 5)
		rng := rand.New(rand.NewSource(7))
		lo, hi := boundsOf(p.boundary).min(), boundsOf(p.boundary).max()
		free := func() Point {
			for {
				pt := Point{lo.X + rng.Float64()*(hi.X-lo.X), lo.Y + rng.Float64()*(hi.Y-lo.Y)}
				if p.CheckPoint(pt) == nil {
					return pt
				}
			}
		}
		bent, curved := 0, 0
		for n := 0; n < 60; n++ {
			r, err := p.Plan(free(), free())
			if err != nil || len(r.Waypoints) < 3 {
				continue
			}
			bent++
			for _, sh := range allCurves {
				c, err := p.Smooth(r, sh)
				if err != nil {
					continue
				}
				curved++
				assertCurve(t, p, r, c, sh)
			}
		}
		if bent == 0 || curved < bent {
			t.Errorf("%s: %d curves for %d cornered legs", filepath.Base(f), curved, bent)
		}
	}
}

// coatDir is the coating machine's configuration, next to this repository.
func coatDir() string {
	if d := os.Getenv("COATV2CONF"); d != "" {
		return d
	}
	return "../../../../../CoatV2conf"
}

func (b box) min() Point { return Point{b.minX, b.minY} }
func (b box) max() Point { return Point{b.maxX, b.maxY} }
