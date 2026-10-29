// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"math"
	"testing"
)

// The corner radius (WithCornerRadius). The scenes are built by hand: one
// rectangle the route has to turn around, in an outer limit large enough that
// nothing but the rectangle decides the shape of the route.

func rect(x0, y0, x1, y1 float64) Polygon {
	return Polygon{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

func cornerScene(zones ...Polygon) *Scene {
	s := &Scene{Outer: rect(0, 0, 1000, 600)}
	for _, z := range zones {
		s.Deadzones = append(s.Deadzones, Shape{Kind: ShapePolyline, Poly: z})
	}
	return s
}

func mustPlanner(t *testing.T, s *Scene, clearance float64, opts ...Option) *Planner {
	t.Helper()
	p, err := NewPlanner(s, clearance, opts...)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return p
}

func mustPlan(t *testing.T, p *Planner, a, b Point) *Route {
	t.Helper()
	r, err := p.Plan(a, b)
	if err != nil {
		t.Fatalf("Plan(%v, %v): %v", a, b, err)
	}
	return p.Metrics(r)
}

// A route around a corner turns on the corner radius and still keeps the
// clearance: the circle passes the corner at exactly the clearance, and the
// offset zone it is laid over is unchanged.
func TestCornerRadiusTurnsWide(t *testing.T) {
	const clearance = 5.0
	s := cornerScene(rect(400, 0, 600, 300))
	// From the left, low, to the right, low: over the zone, around two corners.
	a, b := Point{300, 100}, Point{700, 100}

	tight := mustPlan(t, mustPlanner(t, s, clearance), a, b)
	for _, radius := range []float64{25, 50, 100} {
		wide := mustPlan(t, mustPlanner(t, s, clearance, WithCornerRadius(radius)), a, b)
		if wide.MinClear < clearance-1e-6 {
			t.Errorf("radius %g: route comes %.3f from the zone, want at least the clearance %g",
				radius, wide.MinClear, clearance)
		}
		// The polyline's curvature estimate runs a little below the true arc's.
		if wide.MinRadius < 0.95*radius {
			t.Errorf("radius %g: route turns on %.1f", radius, wide.MinRadius)
		}
		if tight.MinRadius > 1.05*clearance {
			t.Errorf("without a radius the route turns on %.1f, want the clearance %g", tight.MinRadius, clearance)
		}
	}
}

// Below the clearance a radius changes nothing: the offset zone's own corner
// arc already is the clearance.
func TestCornerRadiusBelowClearance(t *testing.T) {
	s := cornerScene(rect(400, 0, 600, 300))
	p := mustPlanner(t, s, 10, WithCornerRadius(4))
	if len(p.shapers) != 0 || p.tight != nil {
		t.Errorf("radius below the clearance built %d corner circles", len(p.shapers))
	}
}

// A passage narrower than the corner circles let through must not lose the
// route: it is planned as if no radius were set.
func TestCornerRadiusNarrowPassageFallsBack(t *testing.T) {
	const clearance = 5.0
	// Two zones from wall to wall, leaving a 30 mm gap between them at x 480..510.
	s := cornerScene(rect(300, 0, 480, 600), rect(510, 0, 700, 600))
	s.Deadzones[0].Poly = rect(300, 0, 480, 400)
	s.Deadzones[1].Poly = rect(510, 200, 700, 600)
	// Top left to bottom right: through the gap, turning around both corners.
	a, b := Point{400, 500}, Point{600, 100}
	tight := mustPlan(t, mustPlanner(t, s, clearance), a, b)
	wp := mustPlanner(t, s, clearance, WithCornerRadius(100))
	// The circles alone really do close the gap -- or this tests nothing.
	alone := *wp
	alone.tight = nil
	if _, err := alone.Plan(a, b); err == nil {
		t.Fatalf("the corner circles leave the gap open; the test needs a narrower one")
	}
	wide := mustPlan(t, wp, a, b)
	if math.Abs(wide.Length-tight.Length) > 1e-6 {
		t.Errorf("fallback route is %.3f long, the plain one %.3f", wide.Length, tight.Length)
	}
	if wide.MinClear < clearance-1e-6 {
		t.Errorf("fallback route comes %.3f from a zone", wide.MinClear)
	}
}

// A station inside a corner circle -- close to a zone corner, outside its
// clearance -- is still a legal point, and a route can start and end there.
func TestCornerRadiusPointInsideCircle(t *testing.T) {
	const clearance = 5.0
	s := cornerScene(rect(400, 100, 600, 300))
	p := mustPlanner(t, s, clearance, WithCornerRadius(50))
	// 6 mm above the top edge, 10 mm in from the top-left corner: outside the
	// clearance, inside the corner circle (centre 45 mm in from the corner on
	// the bisector, radius 50, so it bulges past the edge near the corner).
	near := Point{410, 306}
	if p.insideShaper(near, -1) < 0 {
		t.Fatalf("test point %v is not inside a corner circle", near)
	}
	if err := p.CheckPoint(near); err != nil {
		t.Fatalf("point inside a corner circle refused: %v", err)
	}
	for _, other := range []Point{{700, 500}, {300, 50}, {700, 50}} {
		r := mustPlan(t, p, near, other)
		if r.MinClear < clearance-1e-6 {
			t.Errorf("route %v -> %v comes %.3f from the zone", near, other, r.MinClear)
		}
		mustPlan(t, p, other, near)
	}
}

// A corner closer than the clearance to the outer limit gets no circle: no
// route passes between it and the limit, and a circle there would only take
// the room beside it.
func TestCornerRadiusSkipsCornersAtTheLimit(t *testing.T) {
	// Standing on the bottom wall: only the two top corners are free.
	p := mustPlanner(t, cornerScene(rect(400, 0, 600, 300)), 5, WithCornerRadius(50))
	if len(p.shapers) != 2 {
		t.Errorf("zone on the wall got %d corner circles, want 2", len(p.shapers))
	}
}

// The safety-relevant views of the planner do not change with a radius.
func TestCornerRadiusLeavesZonesAlone(t *testing.T) {
	s := cornerScene(rect(400, 100, 600, 300))
	plain := mustPlanner(t, s, 5)
	wide := mustPlanner(t, s, 5, WithCornerRadius(80))
	pz, wz := plain.OffsetZones(), wide.OffsetZones()
	if len(pz) != len(wz) {
		t.Fatalf("offset zones: %d vs %d", len(pz), len(wz))
	}
	for i := range pz {
		if len(pz[i]) != len(wz[i]) {
			t.Fatalf("offset zone %d: %d vs %d vertices", i, len(pz[i]), len(wz[i]))
		}
		for k := range pz[i] {
			if pz[i][k] != wz[i][k] {
				t.Fatalf("offset zone %d vertex %d moved: %v vs %v", i, k, pz[i][k], wz[i][k])
			}
		}
	}
}

func TestCornerRadiusInvalid(t *testing.T) {
	for _, r := range []float64{-1, math.NaN()} {
		if _, err := NewPlanner(cornerScene(), 5, WithCornerRadius(r)); err == nil {
			t.Errorf("corner radius %v accepted", r)
		}
	}
}

// A straight route stays straight: it has no corner to turn wide, so a corner
// circle it passes through must not bend it.
func TestCornerRadiusKeepsStraightRoutes(t *testing.T) {
	s := cornerScene(rect(400, 100, 600, 300))
	// Just above the top edge, past both top corners, inside their circles.
	a, b := Point{300, 310}, Point{700, 310}
	r := mustPlan(t, mustPlanner(t, s, 5, WithCornerRadius(50)), a, b)
	if len(r.Waypoints) != 2 {
		t.Errorf("straight route bent into %d waypoints: %v", len(r.Waypoints), r.Waypoints)
	}
}
