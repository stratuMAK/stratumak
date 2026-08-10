// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"container/heap"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// batteryPoints are free-space positions spread across the cad_export scene
// (outer limit -10..-1190 x, -10..-790 y; a rectangular dead zone
// [-600..-200] x [-400..-200] and a circle r=100 at (-900,-500)). They are the
// point set the prototype's gen_testcases.sh swept, lifted here as a
// table-driven battery.
var batteryPoints = []Point{
	{-50, -50}, {-1150, -50}, {-50, -750}, {-1150, -750},
	{-600, -50}, {-1150, -400}, {-600, -750}, {-80, -400},
	{-750, -480}, {-1060, -500}, {-900, -360}, {-900, -640},
	{-680, -300}, {-140, -300}, {-400, -140}, {-400, -460},
	{-300, -600}, {-1000, -150}, {-250, -100}, {-1050, -680},
}

func loadScene(t testing.TB, path string) *Scene {
	t.Helper()
	scene, err := LoadDXFFile(path)
	if err != nil {
		t.Fatalf("LoadDXFFile(%s): %v", path, err)
	}
	return scene
}

func newPlanner(t testing.TB, path string, clearance float64, opts ...Option) *Planner {
	t.Helper()
	p, err := NewPlanner(loadScene(t, path), clearance, opts...)
	if err != nil {
		t.Fatalf("NewPlanner(%s, %.1f): %v", path, clearance, err)
	}
	return p
}

// --------------------------------------------------------------------------
// Graph construction
// --------------------------------------------------------------------------

func TestNewPlannerBuildsGraph(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	if p.NodeCount() == 0 || p.EdgeCount() == 0 {
		t.Fatalf("empty visibility graph: %d nodes, %d edges", p.NodeCount(), p.EdgeCount())
	}
	// A circle is offset analytically, so its node count stays at the
	// discretization instead of doubling through the corner arcs.
	if got, want := len(p.OffsetZones()[1]), DefaultArcSegments; got != want {
		t.Errorf("offset circle has %d vertices, want %d", got, want)
	}
	// Every node must be a position a route may actually pass through: inside
	// the eroded limit, and not buried in another zone. (A node sits *on* its
	// own offset ring, where pointInPolygon is deliberately undecided, so it is
	// checked against the other obstacles only — the same rule buildGraph uses.)
	for i, n := range p.nodes {
		if !pointInPolygon(n, p.boundary) {
			t.Fatalf("graph node %d at %v is outside the eroded limit", i, n)
		}
		for j := range p.obstacles {
			if penetrationDepth(n, p.obstacles[j].edge) > 1e-9 && pointInPolygon(n, p.obstacles[j].edge) {
				t.Fatalf("graph node %d at %v is inside offset zone %d", i, n, j)
			}
		}
	}
	b := p.Boundary()
	if len(b) != 4 {
		t.Fatalf("eroded boundary has %d vertices, want 4", len(b))
	}
	if p.Clearance() != 10 || p.Scene() == nil {
		t.Fatalf("planner does not report its inputs back")
	}
}

func TestNewPlannerRejectsBadInput(t *testing.T) {
	scene := loadScene(t, "testdata/cad_export.dxf")
	// Scene is an exported type, so NewPlanner must validate hand-built scenes
	// as strictly as LoadDXF validates drawings — an invalid zone that loads
	// silently is a zone that guards nothing.
	outer := Polygon{{0, 0}, {200, 0}, {200, 200}, {0, 200}}
	tests := []struct {
		name      string
		scene     *Scene
		clearance float64
		opts      []Option
		wantErr   string
	}{
		{"no scene", nil, 10, nil, "no outer limit"},
		{"negative clearance", scene, -1, nil, "not a valid distance"},
		{"NaN clearance", scene, math.NaN(), nil, "not a valid distance"},
		{"clearance wider than the machine", scene, 500, nil, "too large for the outer limit"},
		{"too few samples", scene, 10, []Option{WithSegmentSamples(1)}, "segment samples"},
		{"zero core erosion", scene, 10, []Option{WithCoreErode(0)}, "core erosion"},
		{"zero arc step", scene, 10, []Option{WithOffsetArcStep(0)}, "arc step"},
		{"hand-built concave outer limit",
			&Scene{Outer: Polygon{{0, 0}, {100, 0}, {100, 100}, {50, 100}, {50, 50}, {0, 50}}},
			10, nil, "outer limit is not convex"},
		{"hand-built circle without a ring",
			&Scene{Outer: outer, Deadzones: []Shape{{Kind: ShapeCircle, Center: Point{100, 100}, Radius: 30}}},
			10, nil, "dead zone 0"},
		{"hand-built zero-radius circle",
			&Scene{Outer: outer, Deadzones: []Shape{{Kind: ShapeCircle, Center: Point{100, 100},
				Poly: discretizeCircle(Point{100, 100}, 30, 32)}}},
			10, nil, "circle has radius"},
		{"hand-built concave dead zone",
			&Scene{Outer: outer, Deadzones: []Shape{{Kind: ShapePolyline,
				Poly: Polygon{{10, 10}, {40, 10}, {40, 40}, {25, 40}, {25, 25}, {10, 25}}}}},
			10, nil, "dead zone 0 is not convex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPlanner(tc.scene, tc.clearance, tc.opts...)
			if err == nil {
				t.Fatalf("NewPlanner accepted the input, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("NewPlanner: got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestPathClearanceOrderIndependent guards the bounding-box prune: the gap is
// a lower bound on the distance, never on the penetration depth, so it must
// stop pruning once the running minimum is negative — otherwise MinClear
// reports whichever penetration happens to be measured first.
func TestPathClearanceOrderIndependent(t *testing.T) {
	shallow := Shape{Kind: ShapePolyline, Poly: Polygon{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}
	deep := Shape{Kind: ShapePolyline, Poly: Polygon{{20, 0}, {60, 0}, {60, 40}, {20, 40}}}
	// The first leg cuts 0.5 into the shallow zone; the second ends at the
	// deep zone's center, 20 inside.
	path := []Point{{-5, 9.5}, {15, 9.5}, {40, 20}}

	a := pathClearance(path, []Shape{shallow, deep}, DefaultSegmentSamples)
	b := pathClearance(path, []Shape{deep, shallow}, DefaultSegmentSamples)
	if a != b {
		t.Fatalf("MinClear depends on zone order: %v vs %v", a, b)
	}
	if a > -19 {
		t.Fatalf("MinClear = %v, want about -20 (the deepest penetration)", a)
	}
}

// --------------------------------------------------------------------------
// The routing battery
// --------------------------------------------------------------------------

func TestPlanBattery(t *testing.T) {
	for _, clearance := range []float64{0, 10, 20} {
		p := newPlanner(t, "testdata/cad_export.dxf", clearance)
		for i := 0; i < len(batteryPoints); i++ {
			for j := i + 1; j < len(batteryPoints); j++ {
				start, goal := batteryPoints[i], batteryPoints[j]
				route, err := p.Plan(start, goal)
				if err != nil {
					t.Fatalf("clearance %.0f: Plan(%v, %v): %v", clearance, start, goal, err)
				}
				assertRoute(t, p, start, goal, route)
			}
		}
	}
}

// TestPlanClearanceSweep walks the prototype's parameter variations. A point
// too close to a wall for the requested clearance must be rejected as such —
// the same check that guards taught station positions at config load.
func TestPlanClearanceSweep(t *testing.T) {
	pairs := [][2]Point{
		{{-50, -50}, {-950, -650}},
		{{-1085, -680}, {-60, -60}},
		{{-50, -50}, {-500, -490}},
		{{-700, -100}, {-1100, -600}},
	}
	scene := loadScene(t, "testdata/cad_export.dxf")
	for _, clearance := range []float64{5, 20, 40} {
		p, err := NewPlanner(scene, clearance)
		if err != nil {
			t.Fatalf("NewPlanner(%.0f): %v", clearance, err)
		}
		for _, pair := range pairs {
			start, goal := pair[0], pair[1]
			route, err := p.Plan(start, goal)
			if blocked := p.CheckPoint(start) != nil || p.CheckPoint(goal) != nil; blocked {
				if !errors.Is(err, ErrOutsideLimit) && !errors.Is(err, ErrInDeadzone) {
					t.Fatalf("clearance %.0f: Plan(%v, %v) = %v, want the endpoint rejected",
						clearance, start, goal, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("clearance %.0f: Plan(%v, %v): %v", clearance, start, goal, err)
			}
			assertRoute(t, p, start, goal, route)
		}
	}
}

// assertRoute is the self-check the prototype's contact sheet did by eye: the
// route runs from start to goal, never enters a dead zone or leaves the limit,
// and keeps the clearance it promised.
func assertRoute(t *testing.T, p *Planner, start, goal Point, r *Route) {
	t.Helper()
	if len(r.Waypoints) < 2 {
		t.Fatalf("route has %d waypoints", len(r.Waypoints))
	}
	if r.Waypoints[0] != start || r.Waypoints[len(r.Waypoints)-1] != goal {
		t.Fatalf("route runs %v..%v, want %v..%v",
			r.Waypoints[0], r.Waypoints[len(r.Waypoints)-1], start, goal)
	}
	if direct := start.dist(goal); r.Length < direct-1e-9 {
		t.Fatalf("route length %.6f is shorter than the straight line %.6f", r.Length, direct)
	}
	sum := 0.0
	for i := 1; i < len(r.Waypoints); i++ {
		a, b := r.Waypoints[i-1], r.Waypoints[i]
		if a.dist(b) < 1e-9 {
			t.Fatalf("route repeats waypoint %d (%v)", i, a)
		}
		if !p.visible(a, b) {
			t.Fatalf("route leg %d (%v -> %v) is not collision-free", i, a, b)
		}
		sum += a.dist(b)
	}
	if math.Abs(sum-r.Length) > 1e-9 {
		t.Fatalf("route length %.9f does not match its legs (%.9f)", r.Length, sum)
	}
	// Offsets and arc discretization are both conservative, so the achieved
	// margin must reach the clearance — bar floating-point noise.
	if len(p.scene.Deadzones) > 0 && r.MinClear < p.clearance-1e-6 {
		t.Fatalf("route comes within %.3f of a dead zone, clearance is %.3f", r.MinClear, p.clearance)
	}
	if len(r.Curv) != len(r.Waypoints) {
		t.Fatalf("curvature has %d entries for %d waypoints", len(r.Curv), len(r.Waypoints))
	}
}

// TestPlanIsShortest checks the precomputed graph against a straightforward
// lazy-visibility Dijkstra over the same offset geometry — the prototype's
// algorithm, kept here as the reference the fast path must agree with.
func TestPlanIsShortest(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	pairs := [][2]Point{
		{{-50, -50}, {-1150, -750}},
		{{-1150, -50}, {-50, -750}},
		{{-750, -480}, {-1060, -500}},
		{{-900, -360}, {-900, -640}},
		{{-680, -300}, {-300, -600}},
		{{-250, -100}, {-1050, -680}},
	}
	for _, pair := range pairs {
		route, err := p.Plan(pair[0], pair[1])
		if err != nil {
			t.Fatalf("Plan(%v, %v): %v", pair[0], pair[1], err)
		}
		want := referenceLength(p, pair[0], pair[1])
		if math.Abs(route.Length-want) > 1e-6 {
			t.Errorf("Plan(%v, %v) length %.6f, reference %.6f", pair[0], pair[1], route.Length, want)
		}
	}
}

// referenceLength is a deliberately naive shortest-path search: all offset
// vertices as nodes, visibility computed on demand, no precomputation.
func referenceLength(p *Planner, start, goal Point) float64 {
	nodes := []Point{start, goal}
	for _, o := range p.obstacles {
		nodes = append(nodes, o.edge...)
	}
	n := len(nodes)
	dist := make([]float64, n)
	done := make([]bool, n)
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	dist[0] = 0
	pq := &priorityQueue{{node: 0}}
	heap.Init(pq)
	for pq.Len() > 0 {
		u := int(heap.Pop(pq).(pqItem).node)
		if done[u] {
			continue
		}
		done[u] = true
		if u == 1 {
			break
		}
		for v := 0; v < n; v++ {
			if v == u || done[v] || !p.visible(nodes[u], nodes[v]) {
				continue
			}
			if nd := dist[u] + nodes[u].dist(nodes[v]); nd < dist[v] {
				dist[v] = nd
				heap.Push(pq, pqItem{node: int32(v), dist: nd})
			}
		}
	}
	return dist[1]
}

// --------------------------------------------------------------------------
// Rejections and dead ends
// --------------------------------------------------------------------------

func TestPlanRejectsBadEndpoints(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	good := Point{-50, -50}
	tests := []struct {
		name        string
		start, goal Point
		want        error
	}{
		{"start outside the limit", Point{-5, -5}, good, ErrOutsideLimit},
		{"goal outside the limit", good, Point{-2000, -400}, ErrOutsideLimit},
		{"start in a dead zone", Point{-400, -300}, good, ErrInDeadzone},
		{"goal in a dead zone", good, Point{-900, -500}, ErrInDeadzone},
		{"goal in the clearance margin", good, Point{-900, -595}, ErrInDeadzone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Plan(tc.start, tc.goal)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Plan = %v, want %v", err, tc.want)
			}
		})
	}
	if err := p.CheckPoint(good); err != nil {
		t.Fatalf("CheckPoint(%v): %v", good, err)
	}
}

func TestPlanNoRoute(t *testing.T) {
	// A dead zone stretching across the full height of the envelope leaves the
	// two halves unconnected.
	scene, err := LoadDXF(strings.NewReader(dxfFile(
		lwPolyline("outer limits", true, 0, 0, 100, 0, 100, 100, 0, 100),
		lwPolyline("deadzones", true, 40, -10, 60, -10, 60, 110, 40, 110),
	)))
	if err != nil {
		t.Fatalf("LoadDXF: %v", err)
	}
	p, err := NewPlanner(scene, 2)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	if _, err := p.Plan(Point{10, 50}, Point{90, 50}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("Plan = %v, want %v", err, ErrNoRoute)
	}
	// Both halves are still routable on their own.
	if _, err := p.Plan(Point{10, 10}, Point{10, 90}); err != nil {
		t.Fatalf("Plan within one half: %v", err)
	}
}

func TestPlanTrivialCases(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	start := Point{-50, -50}

	route, err := p.Plan(start, start)
	if err != nil {
		t.Fatalf("Plan(start, start): %v", err)
	}
	if route.Length != 0 || len(route.Waypoints) != 1 || route.Waypoints[0] != start {
		t.Fatalf("degenerate route is %+v, want the single point %v", route.Waypoints, start)
	}

	// Nothing between the two: a straight line, no detour waypoints.
	goal := Point{-50, -750}
	route, err = p.Plan(start, goal)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(route.Waypoints) != 2 || math.Abs(route.Length-start.dist(goal)) > 1e-9 {
		t.Fatalf("clear line of sight produced %d waypoints (%.3f long)", len(route.Waypoints), route.Length)
	}
	if !math.IsInf(route.MinRadius, 1) {
		t.Fatalf("straight route reports a turn radius of %.3f", route.MinRadius)
	}
}

func TestPlanRoundsCorners(t *testing.T) {
	// Routing around the rectangular zone must bend on the offset corner arcs,
	// not on the raw corner: several waypoints, none tighter than the
	// clearance, and every one of them clear of the zone.
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	route, err := p.Plan(Point{-680, -300}, Point{-140, -300})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(route.Waypoints) < 4 {
		t.Fatalf("route around the zone has %d waypoints: %v", len(route.Waypoints), route.Waypoints)
	}
	if route.MinRadius < p.clearance-0.5 {
		t.Fatalf("min turn radius %.3f is tighter than the clearance %.3f", route.MinRadius, p.clearance)
	}
}

// --------------------------------------------------------------------------
// Determinism, concurrency and the D13 latency budget
// --------------------------------------------------------------------------

func TestPlanIsDeterministic(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	first, err := p.Plan(Point{-50, -50}, Point{-1150, -750})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := p.Plan(Point{-50, -50}, Point{-1150, -750})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if len(again.Waypoints) != len(first.Waypoints) || again.Length != first.Length {
			t.Fatalf("run %d returned a different route (%.9f vs %.9f)", i, again.Length, first.Length)
		}
	}
}

func TestPlanIsConcurrencySafe(t *testing.T) {
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	want, err := p.Plan(Point{-50, -50}, Point{-1150, -750})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := p.Plan(Point{-50, -50}, Point{-1150, -750})
			if err != nil {
				t.Errorf("Plan: %v", err)
				return
			}
			if got.Length != want.Length {
				t.Errorf("concurrent plan length %.9f, want %.9f", got.Length, want.Length)
			}
		}()
	}
	wg.Wait()
}

// TestPlanLatencyBudget guards D13: planning happens at the start-job edge and
// adds to the cycle time, so a plan must stay well under 100 ms.
func TestPlanLatencyBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	p := newPlanner(t, "testdata/cad_export.dxf", 10)
	var took []time.Duration
	for i := 0; i < len(batteryPoints); i++ {
		for j := i + 1; j < len(batteryPoints); j++ {
			t0 := time.Now()
			if _, err := p.Plan(batteryPoints[i], batteryPoints[j]); err != nil {
				t.Fatalf("Plan: %v", err)
			}
			took = append(took, time.Since(t0))
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	median, worst := took[len(took)/2], took[len(took)-1]
	t.Logf("plan time over %d routes: median %v, worst %v (graph: %d nodes, %d edges)",
		len(took), median, worst, p.NodeCount(), p.EdgeCount())
	if median > 100*time.Millisecond {
		t.Fatalf("median plan time %v exceeds the 100 ms budget", median)
	}
}

func BenchmarkNewPlanner(b *testing.B) {
	scene := loadScene(b, "testdata/cad_export.dxf")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewPlanner(scene, 10); err != nil {
			b.Fatalf("NewPlanner: %v", err)
		}
	}
}

func BenchmarkPlan(b *testing.B) {
	p := newPlanner(b, "testdata/cad_export.dxf", 10)
	start, goal := Point{-50, -50}, Point{-1150, -750}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Plan(start, goal); err != nil {
			b.Fatalf("Plan: %v", err)
		}
	}
}

func BenchmarkPlanBattery(b *testing.B) {
	p := newPlanner(b, "testdata/cad_export.dxf", 10)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for i := 0; i < len(batteryPoints); i++ {
			for j := i + 1; j < len(batteryPoints); j++ {
				if _, err := p.Plan(batteryPoints[i], batteryPoints[j]); err != nil {
					b.Fatalf("Plan: %v", err)
				}
			}
		}
	}
}
