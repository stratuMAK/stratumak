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
	assertChain(t, c, start, goal)
	if d, _, _ := p.curveClearance(c.Path(), math.Inf(1)); d < p.clearance-1e-9 {
		t.Errorf("%v: arc chain keeps %.4f, want the clearance %g", sh, d, p.clearance)
	}
	p.Metrics(c)
	if c.MinClear < p.clearance-1e-6 {
		t.Errorf("%v: Metrics finds %.4f to a dead zone, want the clearance %g", sh, c.MinClear, p.clearance)
	}
}

// assertChain checks a curve's arc chain: from start to goal, joined end to
// end and tangent at every join, ending in the closing line, close to the
// curve, and as long as it says.
func assertChain(t *testing.T, c *Route, start, goal Point) {
	t.Helper()
	segs := c.Segments
	if len(segs) == 0 {
		t.Fatal("no segments")
	}
	if segs[0].Start != start || segs[len(segs)-1].End != goal {
		t.Fatalf("chain runs %v..%v, want %v..%v", segs[0].Start, segs[len(segs)-1].End, start, goal)
	}
	prims := make([]prim, len(segs))
	sum := 0.0
	for i, sg := range segs {
		prims[i] = sg.prim()
		sum += sg.Length()
		if i > 0 && segs[i-1].End.dist(sg.Start) > 1e-9 {
			t.Fatalf("segments %d and %d do not join: %v / %v", i-1, i, segs[i-1].End, sg.Start)
		}
		if sg.Kind == SegmentArc {
			if d := math.Abs(sg.Center.dist(sg.Start) - sg.Radius); d > 1e-6*sg.Radius+1e-9 {
				t.Fatalf("segment %d starts %.3g off its circle", i, d)
			}
			if d := math.Abs(sg.Center.dist(sg.End) - sg.Radius); d > 1e-6*sg.Radius+1e-9 {
				t.Fatalf("segment %d ends %.3g off its circle", i, d)
			}
		}
	}
	if k := chainKink(prims); k > 1e-7 {
		t.Errorf("arc chain kinks by %.3g rad; the TP would blend that join", k)
	}
	if last := segs[len(segs)-1]; c.Length > 3*ArcEndLine && (last.Kind != SegmentLine || math.Abs(last.Length()-ArcEndLine) > 1e-9) {
		t.Errorf("chain does not end in a %g mm line: %+v", ArcEndLine, last)
	}
	if math.Abs(sum-c.Length) > 1e-9 {
		t.Errorf("length %.6f, segments add up to %.6f", c.Length, sum)
	}
	// Close to the curve, bar the closing line's cut of the curve's end.
	// Both run from start to goal, so the nearest curve segment is searched
	// in a window around the last one.
	worst, at := 0.0, 1
	for _, q := range c.Path() {
		if q.dist(goal) < 2*ArcEndLine {
			continue
		}
		best, bestAt := math.Inf(1), at
		for i := max(1, at-40); i < min(len(c.Curve), at+40); i++ {
			if d := pointSegDistance(q, c.Curve[i-1], c.Curve[i]); d < best {
				best, bestAt = d, i
			}
		}
		worst, at = math.Max(worst, best), bestAt
	}
	if worst > 0.1 {
		t.Errorf("arc chain departs %.3f mm from the curve", worst)
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
		sc, ok := p.pushOut(append([]Point(nil), keys...), interp)
		if !ok {
			t.Fatal("push-out found no clear curve")
		}
		pts := sc.curve
		if d, _, _ := p.curveClearance(sc.driven, math.Inf(1)); d < clearance-1e-9 {
			t.Errorf("pushed arc chain keeps %.3f", d)
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

// A biarc matches position and direction at both ends, and turns smoothly.
func TestBiarc(t *testing.T) {
	a, b := Point{0, 0}, Point{10, 3}
	ta, tb := unit(Point{1, 1}), unit(Point{1, -0.5})
	ps := biarc(a, ta, b, tb)
	if len(ps) != 2 || ps[0].a.dist(a) > 1e-9 || ps[1].b.dist(b) > 1e-9 || ps[0].b.dist(ps[1].a) > 1e-9 {
		t.Fatalf("biarc does not join a to b: %+v", ps)
	}
	sm := samplePrims(ps, 0.01)
	if d := math.Abs(math.Remainder(sm[0].phi-math.Atan2(ta.Y, ta.X), 2*math.Pi)); d > 1e-6 {
		t.Errorf("start direction off by %.3g rad", d)
	}
	if d := math.Abs(math.Remainder(sm[len(sm)-1].phi-math.Atan2(tb.Y, tb.X), 2*math.Pi)); d > 1e-6 {
		t.Errorf("end direction off by %.3g rad", d)
	}
	if k := chainKink(ps); k > 1e-9 {
		t.Errorf("the two arcs kink by %.3g rad", k)
	}
	// Parallel directions, and a target straight ahead.
	if ps := biarc(a, Point{1, 0}, Point{10, 2}, Point{1, 0}); chainKink(ps) > 1e-9 || ps[len(ps)-1].b.dist(Point{10, 2}) > 1e-9 {
		t.Errorf("S-biarc: %+v", ps)
	}
	for _, p := range biarc(a, Point{1, 0}, Point{10, 0}, Point{1, 0}) {
		if p.kind != primLine {
			t.Errorf("straight ahead: %+v, want lines", p)
		}
	}
}

// A curve whose curvature changes continuously is not cut into sample-sized
// TP pieces (each would be held back by the minimum segment duration).
func TestCurvePiecesHaveMinimumLength(t *testing.T) {
	keys := []Point{{0, 0}, {40, 30}, {90, 35}, {130, 0}}
	sm := samplePrims(filletize(centripetalCR(keys, curveStep), -1), curveStep)
	b := curvePieces(sm, ArcPieceLength)
	for k := 0; k+2 < len(b); k++ {
		if l := sm[b[k+1]].s - sm[b[k]].s; l < arcMinPiece-1e-9 || l > ArcPieceLength+curveStep {
			t.Errorf("piece %d is %.2f mm long", k, l)
		}
	}
}

// A blend arc takes the largest radius within the tolerance and half the
// shorter adjacent segment (blendmath.c blendComputeParameters).
func TestFilletRadius(t *testing.T) {
	pr := filletize([]Point{{0, 0}, {100, 0}, {100, 100}}, 3)
	if len(pr) != 3 || pr[1].kind != primArc {
		t.Fatalf("want line, arc, line; got %+v", pr)
	}
	if want := 3 / (math.Sqrt2 - 1); math.Abs(pr[1].r-want) > 1e-9 {
		t.Errorf("tolerance-bound R = %g, want %g", pr[1].r, want)
	}
	if r := filletize([]Point{{0, 0}, {4, 0}, {4, 100}}, 3)[1].r; math.Abs(r-2) > 1e-9 {
		t.Errorf("length-bound R = %g, want 2", r) // tan(45°) = 1, half of 4
	}
	if pr := filletize([]Point{{0, 0}, {4, 0}, {4, 100}}, 0); len(pr) != 3 || pr[1].kind != primStop {
		t.Errorf("tolerance 0: %+v, want a stop", pr)
	}
}
