// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"math"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// Convexity validation (D7): the shape rule the whole package rests on
// --------------------------------------------------------------------------

func TestCheckConvex(t *testing.T) {
	tests := []struct {
		name    string
		poly    Polygon
		wantErr string // substring; "" = must be accepted
	}{
		{
			name: "rectangle ccw",
			poly: Polygon{{0, 0}, {100, 0}, {100, 50}, {0, 50}},
		},
		{
			name: "rectangle cw",
			poly: Polygon{{0, 50}, {100, 50}, {100, 0}, {0, 0}},
		},
		{
			name: "triangle",
			poly: Polygon{{0, 0}, {10, 0}, {5, 8}},
		},
		{
			name: "collinear vertices are fine",
			poly: Polygon{{0, 0}, {50, 0}, {100, 0}, {100, 50}, {0, 50}},
		},
		{
			name: "discretized circle",
			poly: discretizeCircle(Point{3, -7}, 12, 96),
		},
		{
			name:    "concave L",
			poly:    Polygon{{0, 0}, {100, 0}, {100, 100}, {50, 100}, {50, 50}, {0, 50}},
			wantErr: "not convex",
		},
		{
			name:    "self-intersecting star",
			poly:    star(5, 100, 100),
			wantErr: "self-intersecting",
		},
		{
			name:    "bowtie",
			poly:    Polygon{{0, 0}, {100, 100}, {100, 0}, {0, 100}},
			wantErr: "degenerate",
		},
		{
			name:    "repeated vertex",
			poly:    Polygon{{0, 0}, {100, 0}, {100, 0}, {100, 50}},
			wantErr: "repeats its neighbour",
		},
		{
			name:    "two vertices",
			poly:    Polygon{{0, 0}, {100, 0}},
			wantErr: "at least 3 vertices",
		},
		{
			name:    "all collinear",
			poly:    Polygon{{0, 0}, {50, 0}, {100, 0}},
			wantErr: "degenerate",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkConvex(tc.poly)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("checkConvex: unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("checkConvex: want error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("checkConvex: want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// star returns a self-intersecting ring whose every turn has the same sign —
// the case the per-vertex convexity test alone cannot reject.
func star(points int, r1, r2 float64) Polygon {
	poly := make(Polygon, 0, points)
	for i := 0; i < points; i++ {
		// Skipping every other point of a regular polygon draws a pentagram.
		t := 2 * math.Pi * float64(i*2%points) / float64(points)
		poly = append(poly, Point{r1 * math.Cos(t), r2 * math.Sin(t)})
	}
	return poly
}

func TestDedupeRing(t *testing.T) {
	got := dedupeRing(Polygon{{0, 0}, {0, 0}, {10, 0}, {10, 10}, {10, 10}, {0, 0}})
	want := Polygon{{0, 0}, {10, 0}, {10, 10}}
	if len(got) != len(want) {
		t.Fatalf("dedupeRing: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeRing: got %v, want %v", got, want)
		}
	}
}

// --------------------------------------------------------------------------
// Offsetting: the safety margin the planner's correctness depends on
// --------------------------------------------------------------------------

func TestOffsetConvexOutKeepsUniformStandoff(t *testing.T) {
	const margin = 7.5
	for _, src := range []Polygon{
		{{0, 0}, {100, 0}, {100, 50}, {0, 50}},            // rectangle
		{{0, 0}, {60, 10}, {40, 70}},                      // skewed triangle
		{{0, 0}, {40, -20}, {90, 10}, {70, 60}, {10, 55}}, // irregular convex
	} {
		out := offsetConvexOut(src, margin, DefaultOffsetArcStep)
		if err := checkConvex(out); err != nil {
			t.Fatalf("offset of %v is not convex: %v", src, err)
		}
		// The corner arcs circumscribe the true offset, so every vertex sits
		// between margin and margin/cos(step/2) away from the source ring.
		maxStandoff := margin/math.Cos(DefaultOffsetArcStep/2) + 1e-9
		for _, p := range out {
			d := penetrationDepth(p, src)
			if pointInPolygon(p, src) {
				t.Fatalf("offset vertex %v fell inside the source polygon", p)
			}
			if d < margin-1e-9 || d > maxStandoff {
				t.Fatalf("offset vertex %v is %.6f from the source, want %.6f..%.6f",
					p, d, margin, maxStandoff)
			}
		}
		for _, p := range src {
			if !pointInPolygon(p, out) {
				t.Fatalf("source vertex %v is not inside the offset polygon", p)
			}
		}
		// The full margin survives between the vertices too — that is what the
		// circumscribed corner arcs buy, and what the clearance promise rests on.
		for i := range out {
			a, b := out[i], out[(i+1)%len(out)]
			mid := Point{(a.X + b.X) / 2, (a.Y + b.Y) / 2}
			if d := penetrationDepth(mid, src); d < margin-1e-9 {
				t.Fatalf("offset edge %d dips to %.6f from the source, want at least %.6f", i, d, margin)
			}
		}
	}
}

func TestErodeConvex(t *testing.T) {
	got := erodeConvex(Polygon{{0, 0}, {100, 0}, {100, 50}, {0, 50}}, 10)
	want := Polygon{{10, 10}, {90, 10}, {90, 40}, {10, 40}}
	if len(got) != len(want) {
		t.Fatalf("erodeConvex: got %d vertices, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i].X-want[i].X) > 1e-9 || math.Abs(got[i].Y-want[i].Y) > 1e-9 {
			t.Fatalf("erodeConvex: vertex %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// --------------------------------------------------------------------------
// Segment primitives
// --------------------------------------------------------------------------

func TestSegSegDistance(t *testing.T) {
	tests := []struct {
		name       string
		a, b, c, d Point
		want       float64
	}{
		{"parallel", Point{0, 0}, Point{10, 0}, Point{0, 5}, Point{10, 5}, 5},
		{"crossing", Point{0, 0}, Point{10, 10}, Point{0, 10}, Point{10, 0}, 0},
		{"touching endpoints", Point{0, 0}, Point{10, 0}, Point{10, 0}, Point{10, 10}, 0},
		{"apart, skew", Point{0, 0}, Point{10, 0}, Point{13, 4}, Point{20, 9}, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := segSegDistance(tc.a, tc.b, tc.c, tc.d); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("segSegDistance = %.9f, want %.9f", got, tc.want)
			}
		})
	}
}

func TestPointInPolygonEdgeCounts(t *testing.T) {
	sq := Polygon{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	if !pointInPolygon(Point{5, 5}, sq) {
		t.Fatal("centre point reported outside")
	}
	if pointInPolygon(Point{15, 5}, sq) {
		t.Fatal("outside point reported inside")
	}
	// Just off the edge is decided reliably in both directions; a point exactly
	// on it is not, and no caller depends on that (see pointInPolygon).
	if pointInPolygon(Point{-1e-6, 5}, sq) {
		t.Fatal("point just outside the edge reported inside")
	}
	if !pointInPolygon(Point{1e-6, 5}, sq) {
		t.Fatal("point just inside the edge reported outside")
	}
}

func TestSegBlockedByPolygon(t *testing.T) {
	sq := Polygon{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	core := inflate(sq, -DefaultCoreErode)
	tests := []struct {
		name    string
		a, b    Point
		blocked bool
	}{
		{"straight through", Point{-5, 5}, Point{15, 5}, true},
		{"clear of it", Point{-5, 15}, Point{15, 15}, false},
		{"along an edge", Point{0, -5}, Point{0, 15}, false},
		{"corner to corner", Point{0, 0}, Point{10, 10}, true},
		{"corner grazing", Point{-5, 5}, Point{5, -5}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := segBlockedByPolygon(tc.a, tc.b, sq, core, DefaultSegmentSamples); got != tc.blocked {
				t.Fatalf("segBlockedByPolygon = %v, want %v", got, tc.blocked)
			}
		})
	}
}

func TestBoxOverlapsSegment(t *testing.T) {
	b := boundsOf(Polygon{{0, 0}, {10, 0}, {10, 10}, {0, 10}})
	if b.overlapsSegment(Point{20, 20}, Point{30, 30}) {
		t.Fatal("segment far outside reported as overlapping")
	}
	if !b.overlapsSegment(Point{-5, 5}, Point{5, 5}) {
		t.Fatal("segment reaching into the box reported as not overlapping")
	}
	if got := b.gapToSegment(Point{20, 0}, Point{20, 10}); math.Abs(got-10) > 1e-9 {
		t.Fatalf("gapToSegment = %.6f, want 10", got)
	}
}
