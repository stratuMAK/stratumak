// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"bufio"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{AccX: 3000, AccY: 3000, VelX: 900, VelY: 900, VelMax: 1000, Cycle: 0.001}
}

// coatLimits are the coating machine's settings at the calibration run.
func coatLimits() Limits {
	return Limits{AccX: 2500, AccY: 3000, VelX: 900, VelY: 900, VelMax: 1000, Cycle: 0.001}
}

func near(t *testing.T, what string, got, want, relTol float64) {
	t.Helper()
	if math.Abs(got-want) > relTol*math.Abs(want) {
		t.Errorf("%s = %.5g, want %.5g (±%.2g%%)", what, got, want, relTol*100)
	}
}

// A straight move is a triangle below the speed cap and a trapezoid above
// it, in the ideal profile, the polyline model and the chain model alike.
func TestStraightLine(t *testing.T) {
	L := testLimits()
	for _, c := range []struct{ d, want float64 }{
		{100, 2 * math.Sqrt(100/3000.0)}, // triangle, peak 548 < 900
		{1000, 1000/900.0 + 900/3000.0},  // trapezoid
	} {
		poly := []Point{{0, 0}, {c.d, 0}}
		near(t, "ideal", idealProfile(samplePrims(filletize(poly, 3), curveStep), L).T, c.want, 2e-3)
		near(t, "polyline", tpPolyline(poly, 3, L).T, c.want, 2e-3)
		near(t, "chain", tpChain([]Segment{{Kind: SegmentLine, Start: poly[0], End: poly[1], Vel: 1000}}, L).T, c.want, 2e-3)
	}
}

// With tolerance 0 every corner is a stop, so a polyline takes the sum of
// its segments' moves.
func TestSharpCornersStop(t *testing.T) {
	L := testLimits()
	poly := []Point{{0, 0}, {100, 0}, {100, 100}}
	near(t, "stops", tpPolyline(poly, 0, L).T, 4*math.Sqrt(100/3000.0), 3e-3)
}

// pmCircleActualMaxVel: the split follows the segment's maximum speed. Below
// the 0.87 cutoff the turn takes what that speed needs and the rest goes
// along the path; above it the speed is capped at sqrt(0.87 a R) and the
// split is 0.5. With the requested speed as ini_maxvel (D49) an arc requested
// slowly gets most of the acceleration along the path.
func TestCircleLimits(t *testing.T) {
	v, at := circleLimits(100, 300, 300, 3000) // a_n = 900
	near(t, "v", v, 300, 1e-12)
	near(t, "a_t", at, 3000*math.Sqrt(1-0.09), 1e-12)
	v, at = circleLimits(10, 900, 900, 3000)
	near(t, "v capped", v, math.Sqrt(math.Sqrt(0.75)*3000*10), 1e-12)
	near(t, "a_t capped", at, 1500, 1e-12)
	L := testLimits()
	near(t, "ini_maxvel = requested", ArcMaxVel(100, 300, L), 300, 1e-12)
	near(t, "ini_maxvel capped", ArcMaxVel(10, 900, L), math.Sqrt(math.Sqrt(0.75)*3000*10), 1e-12)
	near(t, "ini_maxvel axis cap", ArcMaxVel(1e6, 2000, L), 900, 1e-12)
	_, at = circleLimits(100, 300, ArcMaxVel(100, 300, L), 3000)
	near(t, "a_t with ini_maxvel = feed", at, 3000*math.Sqrt(1-0.09), 1e-12)
}

// The polyline model reproduces the legs measured on the coating machine
// (testdata/coat_calibration.legs): within 1 % each.
func TestCoatCalibration(t *testing.T) {
	f, err := os.Open("testdata/coat_calibration.legs")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 || strings.HasPrefix(fs[0], "#") {
			continue
		}
		meas, err := strconv.ParseFloat(fs[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		var wps []Point
		for _, w := range fs[2:] {
			xy := strings.Split(w, ",")
			x, _ := strconv.ParseFloat(xy[0], 64)
			y, _ := strconv.ParseFloat(xy[1], 64)
			wps = append(wps, Point{x, y})
		}
		near(t, fs[0]+" "+fs[2]+" -> "+fs[len(fs)-1], tpPolyline(wps, 3, coatLimits()).T*1000, meas, 0.01)
		n++
	}
	if n != 8 {
		t.Errorf("%d calibration legs, want 8", n)
	}
}

// The chain model of a curve requested at the ideal profile's speeds is
// never faster than the ideal profile itself, and a chain is not slower than
// its own curve by more than the TP's arc rules can explain.
func TestChainModelBounds(t *testing.T) {
	p := mustPlanner(t, rectScene(rect(400, 0, 600, 300)), 5)
	r := mustPlan(t, p, Point{300, 100}, Point{700, 100})
	L := testLimits()
	for _, sh := range allCurves {
		c, err := p.Smooth(r, sh)
		if err != nil {
			t.Fatal(err)
		}
		setSpeeds(c, L)
		ideal := idealProfile(c.sm, L).T
		tp := tpChain(c.Segments, L).T
		if tp < ideal*(1-1e-3) {
			t.Errorf("%v: TP model %.1f ms beats the time optimum %.1f ms", sh, tp*1000, ideal*1000)
		}
		if tp > 1.25*ideal {
			t.Errorf("%v: TP model %.1f ms, time optimum %.1f ms", sh, tp*1000, ideal*1000)
		}
		for i, sg := range c.Segments {
			if !(sg.Vel > 0) || sg.Vel > L.VelMax {
				t.Fatalf("%v: segment %d requested at %g mm/s", sh, i, sg.Vel)
			}
		}
	}
}

// PlanPath picks the fastest candidate by the model: never slower than the
// route, a curve on the box scene, the route when smoothing is off or the
// leg is straight.
func TestPlanPath(t *testing.T) {
	p := mustPlanner(t, rectScene(rect(400, 0, 600, 300)), 5)
	o := PathOptions{Smooth: true, Limits: testLimits(), BlendTolerance: 3}
	a, b := Point{300, 100}, Point{700, 100}
	got, err := p.PlanPath(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	route := mustPlan(t, p, a, b)
	tRoute := tpPolyline(route.Waypoints, 3, o.Limits).T
	if got.Shape == ShaperRoute || got.Time >= tRoute {
		t.Errorf("chose %v at %.1f ms, the route takes %.1f ms", got.Shape, got.Time*1000, tRoute*1000)
	}
	assertCurve(t, p, route, got, got.Shape)

	off := o
	off.Smooth = false
	if r, err := p.PlanPath(a, b, off); err != nil || r.Shape != ShaperRoute || r.Segments != nil {
		t.Errorf("smoothing off: %v %v", r.Shape, err)
	}
	if r, err := p.PlanPath(Point{100, 400}, Point{900, 450}, o); err != nil || r.Shape != ShaperRoute || len(r.Waypoints) != 2 {
		t.Errorf("straight leg: %+v %v", r, err)
	}
	if _, err := p.PlanPath(a, b, PathOptions{Smooth: true}); err == nil {
		t.Error("zero limits accepted")
	}
	if _, err := p.PlanPath(Point{500, 100}, b, o); err == nil {
		t.Error("a start inside a zone was planned")
	}
}

// On the coating machine's drawings the chosen path is never slower than
// the route by the model, keeps the clearance, and the whole choice -- plan,
// both shapers, the model -- stays inside D13's budget.
func TestPlanPathCoatDrawings(t *testing.T) {
	files, _ := filepath.Glob(coatDir() + "/deadzones-sphere-*.dxf")
	if len(files) == 0 {
		t.Skip("CoatV2conf not checked out next to this repository")
	}
	o := PathOptions{Smooth: true, Limits: coatLimits(), BlendTolerance: 3}
	var took []time.Duration
	shapes := map[Shaper]int{}
	var sumRoute, sumPath float64
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
		for n := 0; n < 60; n++ {
			a, b := free(), free()
			t0 := time.Now()
			got, err := p.PlanPath(a, b, o)
			dt := time.Since(t0)
			if err != nil {
				t.Fatalf("PlanPath(%v, %v): %v", a, b, err)
			}
			if len(got.Waypoints) <= 2 {
				continue
			}
			took = append(took, dt)
			shapes[got.Shape]++
			tRoute := tpPolyline(got.Waypoints, o.BlendTolerance, o.Limits).T
			if got.Time > tRoute+1e-9 {
				t.Errorf("%v -> %v: chose %v at %.1f ms, the route takes %.1f ms", a, b, got.Shape, got.Time*1000, tRoute*1000)
			}
			sumRoute += tRoute
			sumPath += got.Time
			if got.Shape != ShaperRoute {
				if d, _, _ := p.curveClearance(got.Path(), math.Inf(1)); d < p.clearance-1e-9 {
					t.Errorf("%v -> %v: %v keeps %.3f", a, b, got.Shape, d)
				}
			}
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("cornered legs: shapes %v, %.3f of the route's time by the model; PlanPath median %v, worst %v",
		shapes, sumPath/sumRoute, took[len(took)/2], took[len(took)-1])
	if shapes[ShaperNatural]+shapes[ShaperCatmullRom] <= shapes[ShaperRoute] {
		t.Errorf("curves chosen on fewer than half the cornered legs: %v", shapes)
	}
	if !testing.Short() && took[len(took)/2] > 50*time.Millisecond {
		t.Errorf("median PlanPath time %v", took[len(took)/2])
	}
}
