// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
)

// Defaults for the planner's numeric knobs. They are exported so a caller can
// state a value relative to the default instead of hard-coding a number.
const (
	// DefaultSegmentSamples is how many interior points of a segment are tested
	// for containment in an obstacle.
	DefaultSegmentSamples = 24
	// DefaultCoreErode is how far an obstacle is shrunk for that interior test:
	// small against feature sizes, large against floating-point noise.
	DefaultCoreErode = 0.05
	// DefaultOffsetArcStep is the angle between two points of a rounded corner
	// in the outward offset, in radians (~11°).
	DefaultOffsetArcStep = 0.20
)

// Planning failures. They are distinguishable so a caller can tell a bad
// request (a station taught inside a dead zone) from a genuinely blocked world.
var (
	ErrOutsideLimit = errors.New("outside the outer limit")
	ErrInDeadzone   = errors.New("inside a dead zone or its clearance margin")
	ErrNoRoute      = errors.New("no collision-free route")
)

// Option tunes a Planner's geometry handling.
type Option func(*plannerOptions)

type plannerOptions struct {
	segSamples    int
	coreErode     float64
	offsetArcStep float64
}

// WithSegmentSamples sets how many interior points along a segment are tested
// against obstacle interiors (default [DefaultSegmentSamples]). Fewer samples
// make graph construction faster and the collision test coarser.
func WithSegmentSamples(n int) Option {
	return func(o *plannerOptions) { o.segSamples = n }
}

// WithCoreErode sets the numerical erosion of an obstacle used for the interior
// sample test (default [DefaultCoreErode]). It is the width of the band along
// an obstacle edge in which travel still counts as grazing rather than as a
// hit; it is not a safety margin — that is the clearance.
func WithCoreErode(v float64) Option {
	return func(o *plannerOptions) { o.coreErode = v }
}

// WithOffsetArcStep sets the angular resolution of the rounded corners in the
// outward offset, in radians (default [DefaultOffsetArcStep]). Coarser steps
// shrink the visibility graph — and with it the planning time — at the cost of
// slightly cutting the corner arcs. The cut is inward, toward the obstacle, so
// a coarse step eats into the clearance: keep it well below 0.5 rad.
func WithOffsetArcStep(rad float64) Option {
	return func(o *plannerOptions) { o.offsetArcStep = rad }
}

// Route is a planned path between two points.
type Route struct {
	Waypoints []Point   // polyline from start to goal, both included
	Length    float64   // arc length
	Curv      []float64 // per-waypoint curvature (1/radius), 0 at the ends
	MinRadius float64   // smallest turn radius, the speed bottleneck; Inf if straight
	MinClear  float64   // closest the route comes to any dead zone; Inf if there are none
}

// obstacle pairs a dead zone's offset edge polygon with an eroded core used for
// robust interior sampling, plus its bounding box for cheap rejection.
type obstacle struct {
	edge, core Polygon
	bounds     box
}

// arc is one edge of the visibility graph.
type arc struct {
	to int32
	w  float64
}

// Planner is a scene plus one clearance, with everything that does not depend
// on the query precomputed: the offset obstacles, the eroded boundary, the
// usable static nodes and the visibility graph over them. Building one is the
// expensive step; [Planner.Plan] then only wires the two query points into that
// graph.
//
// A Planner is immutable once built and safe for concurrent use.
type Planner struct {
	scene     *Scene
	clearance float64
	opt       plannerOptions

	boundary  Polygon // outer limit eroded by clearance
	obstacles []obstacle
	nodes     []Point // static graph nodes (usable offset-obstacle vertices)
	adj       [][]arc // visibility adjacency over nodes
}

// NewPlanner precomputes the visibility graph of scene at the given clearance.
// The clearance is kept from every dead zone and from the outer limit, and is
// also the radius the corners of the route are rounded to; pick it as the true
// safety margin plus the trajectory planner's blend tolerance.
func NewPlanner(s *Scene, clearance float64, opts ...Option) (*Planner, error) {
	if s == nil || len(s.Outer) < 3 {
		return nil, fmt.Errorf("pnproute: scene has no outer limit")
	}
	if clearance < 0 || math.IsNaN(clearance) {
		return nil, fmt.Errorf("pnproute: clearance %v is not a valid distance", clearance)
	}
	o := plannerOptions{
		segSamples:    DefaultSegmentSamples,
		coreErode:     DefaultCoreErode,
		offsetArcStep: DefaultOffsetArcStep,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.segSamples < 2 {
		return nil, fmt.Errorf("pnproute: segment samples must be at least 2, got %d", o.segSamples)
	}
	if o.coreErode <= 0 {
		return nil, fmt.Errorf("pnproute: core erosion must be positive, got %v", o.coreErode)
	}
	if o.offsetArcStep <= 0 {
		return nil, fmt.Errorf("pnproute: offset arc step must be positive, got %v", o.offsetArcStep)
	}

	p := &Planner{scene: s, clearance: clearance, opt: o}

	// Validate the scene here, not only in LoadDXF: a Scene is an exported
	// type, and a hand-built one (phase 2 constructs them around the DXF path)
	// must not silently produce a planner that guards nothing. The offset and
	// visibility machinery is only correct for simple convex rings (D7).
	outer := dedupeRing(s.Outer)
	if err := checkConvex(outer); err != nil {
		return nil, fmt.Errorf("pnproute: outer limit %v", err)
	}

	p.boundary = erodeConvex(outer, clearance)
	if err := usableBoundary(p.boundary, outer, clearance); err != nil {
		return nil, fmt.Errorf("pnproute: clearance %.3f is too large for the outer limit: %v", clearance, err)
	}

	p.obstacles = make([]obstacle, len(s.Deadzones))
	for i, dz := range s.Deadzones {
		poly := dedupeRing(dz.Poly)
		if err := checkConvex(poly); err != nil {
			return nil, fmt.Errorf("pnproute: dead zone %d %v", i, err)
		}
		var edge Polygon
		if dz.Kind == ShapeCircle {
			if dz.Radius <= 0 {
				return nil, fmt.Errorf("pnproute: dead zone %d: circle has radius %v", i, dz.Radius)
			}
			// A circle's offset is a bigger circle: taking it analytically keeps
			// the node count at the discretization instead of doubling it.
			edge = discretizeCircle(dz.Center, dz.Radius+clearance, len(poly))
		} else {
			edge = offsetConvexOut(poly, clearance, o.offsetArcStep)
		}
		p.obstacles[i] = obstacle{
			edge: edge,
			// The exact inward offset keeps the documented grazing band width
			// (coreErode) on every edge; a centroid-scaling approximation would
			// thin it on the long sides of elongated zones.
			core:   erodeConvex(edge, o.coreErode),
			bounds: boundsOf(edge),
		}
	}

	p.buildGraph()
	return p, nil
}

// usableBoundary verifies the eroded outer limit is still a real region: a
// clearance wider than the machine envelope turns the miter construction inside
// out rather than failing outright.
func usableBoundary(eroded, outer Polygon, clearance float64) error {
	if len(eroded) < 3 {
		return fmt.Errorf("nothing is left of it")
	}
	if clearance == 0 {
		return nil // nothing was eroded; the limit is the drawn one
	}
	if signedArea(eroded) <= 0 {
		return fmt.Errorf("the erosion turned it inside out")
	}
	if err := checkConvex(eroded); err != nil {
		return fmt.Errorf("the erosion collapsed it: %v", err)
	}
	for _, v := range eroded {
		if !pointInPolygon(v, outer) || penetrationDepth(v, outer) < clearance-1e-6 {
			return fmt.Errorf("the margin does not fit at (%.3f,%.3f)", v.X, v.Y)
		}
	}
	return nil
}

// buildGraph collects the usable static nodes and links every visible pair.
func (p *Planner) buildGraph() {
	for i, o := range p.obstacles {
		for _, v := range o.edge {
			if !pointInPolygon(v, p.boundary) {
				continue // clipped away by the eroded outer limit
			}
			if p.insideAnyObstacleExcept(v, i) {
				continue // buried in an overlapping zone; no route can use it
			}
			p.nodes = append(p.nodes, v)
		}
	}

	p.adj = make([][]arc, len(p.nodes))
	for i := 0; i < len(p.nodes); i++ {
		for j := i + 1; j < len(p.nodes); j++ {
			if !p.visible(p.nodes[i], p.nodes[j]) {
				continue
			}
			w := p.nodes[i].dist(p.nodes[j])
			p.adj[i] = append(p.adj[i], arc{int32(j), w})
			p.adj[j] = append(p.adj[j], arc{int32(i), w})
		}
	}
}

func (p *Planner) insideAnyObstacleExcept(pt Point, skip int) bool {
	for i, o := range p.obstacles {
		if i == skip || !o.bounds.contains(pt) {
			continue
		}
		if pointInPolygon(pt, o.edge) {
			return true
		}
	}
	return false
}

// visible reports whether a can see b: the segment stays inside the eroded
// boundary and clears every offset obstacle's interior.
func (p *Planner) visible(a, b Point) bool {
	if segCrossesBoundary(a, b, p.boundary, p.opt.segSamples) {
		return false
	}
	for _, o := range p.obstacles {
		if !o.bounds.overlapsSegment(a, b) {
			continue
		}
		if segBlockedByPolygon(a, b, o.edge, o.core, p.opt.segSamples) {
			return false
		}
	}
	return true
}

// Scene returns the scene the planner was built from.
func (p *Planner) Scene() *Scene { return p.scene }

// Clearance returns the safety margin the planner keeps.
func (p *Planner) Clearance() float64 { return p.clearance }

// Boundary returns the outer limit eroded by the clearance — the region a
// position must lie in to be reachable.
func (p *Planner) Boundary() Polygon { return p.boundary.Clone() }

// OffsetZones returns the dead zones grown by the clearance, in scene order.
func (p *Planner) OffsetZones() []Polygon {
	out := make([]Polygon, len(p.obstacles))
	for i, o := range p.obstacles {
		out[i] = o.edge.Clone()
	}
	return out
}

// NodeCount and EdgeCount report the size of the precomputed visibility graph,
// for startup diagnostics.
func (p *Planner) NodeCount() int { return len(p.nodes) }

func (p *Planner) EdgeCount() int {
	n := 0
	for _, a := range p.adj {
		n += len(a)
	}
	return n / 2
}

// CheckPoint reports whether pt is a usable start or goal: inside the eroded
// outer limit and outside every offset dead zone. Configuration validation uses
// it to reject taught positions before a job ever runs; the error wraps
// [ErrOutsideLimit] or [ErrInDeadzone].
func (p *Planner) CheckPoint(pt Point) error { return p.checkPoint(pt, "point") }

func (p *Planner) checkPoint(pt Point, role string) error {
	if !pointInPolygon(pt, p.boundary) {
		return fmt.Errorf("pnproute: %s (%.3f,%.3f) is %w", role, pt.X, pt.Y, ErrOutsideLimit)
	}
	for _, o := range p.obstacles {
		if o.bounds.contains(pt) && pointInPolygon(pt, o.edge) {
			return fmt.Errorf("pnproute: %s (%.3f,%.3f) is %w", role, pt.X, pt.Y, ErrInDeadzone)
		}
	}
	return nil
}

// Plan returns the shortest route from start to goal that keeps the planner's
// clearance from every dead zone and from the outer limit.
func (p *Planner) Plan(start, goal Point) (*Route, error) {
	if err := p.checkPoint(start, "start"); err != nil {
		return nil, err
	}
	if err := p.checkPoint(goal, "goal"); err != nil {
		return nil, err
	}

	// The straight segment, when free, is provably shortest (its length is the
	// Euclidean lower bound of any route), so the graph work can be skipped
	// entirely. This is the common case for a pick-and-place move that does
	// not cross a zone's clearance shadow.
	if p.visible(start, goal) {
		return p.assembleRoute([]Point{start, goal}, goal), nil
	}

	// Wire the two query points into the static graph: one segment test per
	// static node each, instead of rebuilding the graph. The direct start-goal
	// edge was ruled out above.
	n := len(p.nodes)
	startVis := make([]float64, n)
	goalVis := make([]float64, n)
	for i, nd := range p.nodes {
		startVis[i], goalVis[i] = -1, -1
		if p.visible(start, nd) {
			startVis[i] = start.dist(nd)
		}
		if p.visible(goal, nd) {
			goalVis[i] = goal.dist(nd)
		}
	}

	startIdx, goalIdx := n, n+1
	dist := make([]float64, n+2)
	prev := make([]int32, n+2)
	done := make([]bool, n+2)
	for i := range dist {
		dist[i] = math.Inf(1)
		prev[i] = -1
	}
	dist[startIdx] = 0

	pq := &priorityQueue{{node: int32(startIdx)}}
	heap.Init(pq)
	relax := func(u, v int, w float64) {
		if nd := dist[u] + w; nd < dist[v] {
			dist[v] = nd
			prev[v] = int32(u)
			heap.Push(pq, pqItem{node: int32(v), dist: nd})
		}
	}
	for pq.Len() > 0 {
		u := int(heap.Pop(pq).(pqItem).node)
		if done[u] {
			continue
		}
		done[u] = true
		if u == goalIdx {
			break
		}
		if u == startIdx {
			for v, w := range startVis {
				if w >= 0 {
					relax(u, v, w)
				}
			}
			continue
		}
		for _, a := range p.adj[u] {
			relax(u, int(a.to), a.w)
		}
		if w := goalVis[u]; w >= 0 {
			relax(u, goalIdx, w)
		}
	}

	if math.IsInf(dist[goalIdx], 1) {
		return nil, fmt.Errorf("pnproute: %w from (%.3f,%.3f) to (%.3f,%.3f)",
			ErrNoRoute, start.X, start.Y, goal.X, goal.Y)
	}

	var path []Point
	for at := goalIdx; at != -1; at = int(prev[at]) {
		switch at {
		case startIdx:
			path = append(path, start)
		case goalIdx:
			path = append(path, goal)
		default:
			path = append(path, p.nodes[at])
		}
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i] // backtracking built it goal-first
	}
	return p.assembleRoute(path, goal), nil
}

// assembleRoute turns a raw waypoint sequence into a Route with its metrics.
// A query point sitting on a graph node — or a start equal to the goal —
// would enter the path twice; the repeat is dropped, because a zero-length leg
// is not something the motion stream should ever see.
func (p *Planner) assembleRoute(path []Point, goal Point) *Route {
	kept := path[:1]
	for _, pt := range path[1:] {
		if pt.dist(kept[len(kept)-1]) >= 1e-9 {
			kept = append(kept, pt)
		}
	}
	if len(kept) > 1 {
		kept[len(kept)-1] = goal // keep the goal exact, node or not
	}

	curv := pathCurvatures(kept)
	route := &Route{
		Waypoints: kept,
		Curv:      curv,
		MinRadius: minRadius(curv),
		MinClear:  pathClearance(kept, p.scene.Deadzones, p.opt.segSamples),
	}
	for i := 1; i < len(kept); i++ {
		route.Length += kept[i-1].dist(kept[i])
	}
	return route
}

// pathClearance returns the closest the route comes to any true (un-offset)
// dead zone — the safety margin actually achieved — measured over whole
// segments, not just the waypoints. Negative if the route enters a zone.
func pathClearance(path []Point, zones []Shape, samples int) float64 {
	worst := math.Inf(1)
	for _, z := range zones {
		zb := boundsOf(z.Poly)
		nz := len(z.Poly)
		for i := 0; i+1 < len(path); i++ {
			a, b := path[i], path[i+1]
			// The gap is a lower bound on the *distance*, never on the
			// penetration depth: it is 0 whenever the boxes touch. So it may
			// only prune while it is positive — a positive gap proves this pair
			// cannot penetrate at all and cannot beat a running minimum below
			// it. Pruning on gap >= worst alone would stop all measurement as
			// soon as worst went negative and under-report the deepest cut,
			// zone-order dependently.
			if g := zb.gapToSegment(a, b); g > 0 && g >= worst {
				continue
			}
			d := math.Inf(1)
			for j := 0; j < nz; j++ {
				d = math.Min(d, segSegDistance(a, b, z.Poly[j], z.Poly[(j+1)%nz]))
			}
			pen, inside := 0.0, false
			for k := 0; k <= samples; k++ {
				t := float64(k) / float64(samples)
				pt := Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
				if pointInPolygon(pt, z.Poly) {
					inside = true
					pen = math.Max(pen, penetrationDepth(pt, z.Poly))
				}
			}
			if inside {
				d = -pen
			}
			worst = math.Min(worst, d)
		}
	}
	return worst
}

// gapToSegment is a lower bound on the distance from the box to segment ab:
// the gap between the box and the segment's own bounding box.
func (b box) gapToSegment(a, c Point) float64 {
	gap := func(lo1, hi1, lo2, hi2 float64) float64 {
		return math.Max(0, math.Max(lo2-hi1, lo1-hi2))
	}
	dx := gap(b.minX, b.maxX, math.Min(a.X, c.X), math.Max(a.X, c.X))
	dy := gap(b.minY, b.maxY, math.Min(a.Y, c.Y), math.Max(a.Y, c.Y))
	return math.Hypot(dx, dy)
}

// --- Dijkstra ----------------------------------------------------------------

type pqItem struct {
	node int32
	dist float64
}

type priorityQueue []pqItem

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].dist < pq[j].dist }
func (pq priorityQueue) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i] }
func (pq *priorityQueue) Push(x any)        { *pq = append(*pq, x.(pqItem)) }
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	it := old[n-1]
	*pq = old[:n-1]
	return it
}

// --- Curvature ---------------------------------------------------------------

// mengerCurvature is the curvature (1/circumradius) of the triangle abc.
func mengerCurvature(a, b, c Point) float64 {
	d1, d2, d3 := a.dist(b), b.dist(c), a.dist(c)
	if d1 < 1e-9 || d2 < 1e-9 || d3 < 1e-9 {
		return 0
	}
	area2 := math.Abs((b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X))
	return 2 * area2 / (d1 * d2 * d3)
}

// pathCurvatures returns per-point curvature (0 at the endpoints).
func pathCurvatures(pts []Point) []float64 {
	curv := make([]float64, len(pts))
	for i := 1; i < len(pts)-1; i++ {
		curv[i] = mengerCurvature(pts[i-1], pts[i], pts[i+1])
	}
	return curv
}

// minRadius returns the smallest turn radius along the path (Inf if straight).
func minRadius(curv []float64) float64 {
	maxK := 0.0
	for _, k := range curv {
		if k > maxK {
			maxK = k
		}
	}
	if maxK < 1e-9 {
		return math.Inf(1)
	}
	return 1 / maxK
}
