// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
/*
Package pnproute plans collision-free XY travel for the pick-and-place task.

A [Scene] is the world the machine moves in: one closed outer limit and a set of
dead zones the head must not enter, both read from a DXF drawing. A [Planner]
binds a scene to one clearance (safety margin) and answers shortest-route
queries between two points:

	scene, err := pnproute.LoadDXFFile("zones.dxf")
	pl, err := pnproute.NewPlanner(scene, 10.0)   // once, at startup
	route, err := pl.Plan(from, to)               // per job leg

# Method

Dead zones are grown and the outer limit shrunk by the clearance using a true
Minkowski offset: every edge moves by the clearance along its normal and every
convex corner is rounded with an arc of that radius. The shortest path around
the offset shapes — straight tangents plus corner arcs of radius = clearance —
is the route. Safety margin and corner rounding are therefore the same
operation: a larger clearance also buys gentler (faster) corners.

The route is a polyline; smoothing and timing are left to the trajectory
planner, which blends corners *inward*, toward the dead zone. Choose the
clearance as `true safety margin + blend tolerance` so the blended path still
clears the zone.

# Cost split

[NewPlanner] does the expensive work once: offsetting the shapes, discarding
nodes that no route can use, and building the full visibility graph over the
remaining static nodes. [Planner.Plan] only inserts the two query points — one
segment test per static node — and runs Dijkstra over the cached adjacency, so
per-job planning stays far below the pick-and-place cycle's latency budget.

A Planner is immutable once built and safe for concurrent use.

# Geometry rules

Both the outer limit and every dead-zone polyline must be closed and *convex*;
concave input is rejected at load time with a descriptive error. Circles and
ellipses are convex by construction and are discretized on load. Dead zones may
overlap: shortest paths only ever bend at convex corners, so the vertices of the
individual offset shapes still carry every corner the route can use.

Everything curved is approximated by polygons, and always in the conservative
direction: a discretized circle contains the drawn one, and the polygonal corner
arcs of the offset lie outside the true offset. The margin a route achieves is
therefore never smaller than the clearance asked for — it is a few tenths of a
percent larger, which [WithArcSegments] and [WithOffsetArcStep] trade against
graph size and planning time.

# Not in scope (v1)

  - Concave dead zones — decompose them into convex parts, or draw their convex
    hull.
  - Dead zones that appear, move or vanish at runtime. A Planner is built from a
    fixed scene; a changed world means a new Planner.
  - Arc output primitives. Routes are polylines only; corner rounding lives in
    the offset geometry and in the trajectory planner's blending.
*/
package pnproute
