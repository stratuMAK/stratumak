// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// Loading real drawings
// --------------------------------------------------------------------------

func TestLoadDXFFile_CADExport(t *testing.T) {
	scene, err := LoadDXFFile("testdata/cad_export.dxf")
	if err != nil {
		t.Fatalf("LoadDXFFile: %v", err)
	}
	if len(scene.Outer) != 4 {
		t.Fatalf("outer limit has %d vertices, want 4", len(scene.Outer))
	}
	min, max := scene.Bounds()
	if min != (Point{-1190, -790}) || max != (Point{-10, -10}) {
		t.Fatalf("bounds are %v..%v, want (-1190,-790)..(-10,-10)", min, max)
	}
	if len(scene.Deadzones) != 2 {
		t.Fatalf("got %d dead zones, want 2", len(scene.Deadzones))
	}
	if k := scene.Deadzones[0].Kind; k != ShapePolyline {
		t.Errorf("dead zone 0 is %q, want %q", k, ShapePolyline)
	}
	circle := scene.Deadzones[1]
	if circle.Kind != ShapeCircle || circle.Center != (Point{-900, -500}) || circle.Radius != 100 {
		t.Errorf("dead zone 1 is %+v, want a circle r=100 at (-900,-500)", circle)
	}
	if len(circle.Poly) != DefaultArcSegments {
		t.Errorf("circle discretized into %d segments, want %d", len(circle.Poly), DefaultArcSegments)
	}
}

// TestLoadDXF_Mixed covers what the CAD export does not: a non-axis-aligned
// convex outer limit, a rotated dead zone, an ellipse, an old-style POLYLINE
// with VERTEX children, spelling variants of the layer names, and an
// annotation entity that must be ignored rather than rejected.
func TestLoadDXF_Mixed(t *testing.T) {
	scene, err := LoadDXFFile("testdata/mixed.dxf")
	if err != nil {
		t.Fatalf("LoadDXFFile: %v", err)
	}
	if len(scene.Outer) != 8 {
		t.Fatalf("outer limit has %d vertices, want 8", len(scene.Outer))
	}
	if len(scene.Deadzones) != 3 {
		t.Fatalf("got %d dead zones, want 3", len(scene.Deadzones))
	}
	kinds := []ShapeKind{ShapePolyline, ShapeEllipse, ShapePolyline}
	for i, want := range kinds {
		if got := scene.Deadzones[i].Kind; got != want {
			t.Errorf("dead zone %d is %q, want %q", i, got, want)
		}
	}
	if n := len(scene.Deadzones[2].Poly); n != 3 {
		t.Errorf("the POLYLINE dead zone has %d vertices, want 3", n)
	}
	// The ellipse has a vertical major axis of 60 and ratio 0.5.
	eb := boundsOf(scene.Deadzones[1].Poly)
	if math.Abs((eb.maxY-eb.minY)-120) > 1 || math.Abs((eb.maxX-eb.minX)-60) > 1 {
		t.Errorf("ellipse spans %.1f x %.1f, want 60 x 120", eb.maxX-eb.minX, eb.maxY-eb.minY)
	}
	for i, dz := range scene.Deadzones {
		if err := checkConvex(dz.Poly); err != nil {
			t.Errorf("dead zone %d is not convex: %v", i, err)
		}
	}
}

func TestWithArcSegments(t *testing.T) {
	scene, err := LoadDXF(strings.NewReader(dxfFile(
		lwPolyline("outer limits", true, 0, 0, 100, 0, 100, 100, 0, 100),
		circleEnt("deadzones", 50, 50, 10),
	)), WithArcSegments(16))
	if err != nil {
		t.Fatalf("LoadDXF: %v", err)
	}
	if n := len(scene.Deadzones[0].Poly); n != 16 {
		t.Fatalf("circle discretized into %d segments, want 16", n)
	}
	if _, err := LoadDXF(strings.NewReader(dxfFile()), WithArcSegments(4)); err == nil {
		t.Fatal("WithArcSegments(4) was accepted")
	}
}

// --------------------------------------------------------------------------
// Rejected input — every one of these would otherwise plan against a world
// that is not the drawn one
// --------------------------------------------------------------------------

func TestLoadDXF_Invalid(t *testing.T) {
	square := func(layer string) string {
		return lwPolyline(layer, true, 0, 0, 100, 0, 100, 100, 0, 100)
	}
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "no outer limit",
			body:    dxfFile(square("deadzones")),
			wantErr: "no closed polyline found",
		},
		{
			name:    "two outer limits",
			body:    dxfFile(square("outer limits"), lwPolyline("outer limits", true, 0, 0, 10, 0, 10, 10)),
			wantErr: "found 2",
		},
		{
			name:    "open outer polyline",
			body:    dxfFile(lwPolyline("outer limits", false, 0, 0, 100, 0, 100, 100, 0, 100)),
			wantErr: "outer limit polyline is not closed",
		},
		{
			name:    "concave outer limit",
			body:    dxfFile(lwPolyline("outer limits", true, 0, 0, 100, 0, 100, 100, 50, 100, 50, 50, 0, 50)),
			wantErr: "outer limit polyline is not convex",
		},
		{
			name:    "concave dead zone",
			body:    dxfFile(square("outer limits"), lwPolyline("deadzones", true, 10, 10, 40, 10, 40, 40, 25, 40, 25, 25, 10, 25)),
			wantErr: "dead zone polyline is not convex",
		},
		{
			name:    "open dead zone",
			body:    dxfFile(square("outer limits"), lwPolyline("deadzones", false, 10, 10, 40, 10, 40, 40)),
			wantErr: "dead zone polyline is not closed",
		},
		{
			name:    "bulge arc in a dead zone",
			body:    dxfFile(square("outer limits"), withPair(lwPolyline("deadzones", true, 10, 10, 40, 10, 40, 40), 42, "0.5")),
			wantErr: "bulge",
		},
		{
			name:    "mirrored extrusion",
			body:    dxfFile(square("outer limits"), withPair(circleEnt("deadzones", 50, 50, 10), 230, "-1.0")),
			wantErr: "object coordinate system",
		},
		{
			// A tilted OCS is as wrong as a mirrored one: the entity's 10/20
			// coordinates are not world coordinates, so the zone would load at
			// the wrong position and guard nothing.
			name: "tilted extrusion",
			body: dxfFile(square("outer limits"),
				withPair(withPair(circleEnt("deadzones", 50, 50, 10), 210, "0.707"), 230, "0.707")),
			wantErr: "object coordinate system",
		},
		{
			// A sweep of ~1e-7 rad discretizes into a sliver enclosing no
			// area; the same ring drawn as a polyline is already rejected, and
			// the ellipse branch must not be a way around that check.
			name:    "degenerate ellipse sweep",
			body:    dxfFile(square("outer limits"), ellipseEnt("deadzones", 50, 50, 30, 0, 0.5, 1.0, 1.0000001)),
			wantErr: "degenerate",
		},
		{
			name:    "unsupported entity on the dead-zone layer",
			body:    dxfFile(square("outer limits"), "0\nLINE\n8\ndeadzones\n10\n0.0\n20\n0.0\n11\n10.0\n21\n10.0\n"),
			wantErr: "unsupported entity LINE",
		},
		{
			// A filled hatch is the natural CAD idiom for a keep-out AREA;
			// whitelisting it as annotation would load fewer zones than drawn.
			name:    "hatch on the dead-zone layer",
			body:    dxfFile(square("outer limits"), "0\nHATCH\n8\ndeadzones\n"),
			wantErr: "HATCH",
		},
		{
			// Group code 90 declares the vertex count; fewer actual vertices
			// mean a truncated/corrupt export whose zone is smaller than drawn.
			name: "vertex count mismatch",
			body: dxfFile(square("outer limits"),
				"0\nLWPOLYLINE\n8\ndeadzones\n90\n4\n70\n1\n10\n10.0\n20\n10.0\n10\n40.0\n20\n10.0\n10\n40.0\n20\n40.0\n"),
			wantErr: "declares 4 vertices but carries 3",
		},
		{
			name:    "circle without a center",
			body:    dxfFile(square("outer limits"), "0\nCIRCLE\n8\ndeadzones\n40\n10.0\n"),
			wantErr: "no center",
		},
		{
			// ENTITIES never closed: the file was cut off between records, and
			// the entities collected so far may be an incomplete drawing.
			name:    "truncated between entities",
			body:    "0\nSECTION\n2\nENTITIES\n" + square("outer limits"),
			wantErr: "not closed with ENDSEC",
		},
		{
			name:    "truncated mid-record",
			body:    dxfFile(square("outer limits")) + "999\n",
			wantErr: "has no value",
		},
		{
			name:    "circle without a radius",
			body:    dxfFile(square("outer limits"), circleEnt("deadzones", 50, 50, 0)),
			wantErr: "radius",
		},
		{
			name:    "circle as the outer limit",
			body:    dxfFile(circleEnt("outer limits", 50, 50, 10)),
			wantErr: "must be a closed polyline",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadDXF(strings.NewReader(tc.body))
			if err == nil {
				t.Fatalf("LoadDXF accepted the drawing, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadDXF: got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadDXFFile_Missing(t *testing.T) {
	if _, err := LoadDXFFile("testdata/does-not-exist.dxf"); err == nil {
		t.Fatal("LoadDXFFile accepted a missing file")
	}
}

// --------------------------------------------------------------------------
// DXF text helpers
// --------------------------------------------------------------------------

func dxfFile(entities ...string) string {
	return "0\nSECTION\n2\nHEADER\n9\n$ACADVER\n1\nAC1015\n0\nENDSEC\n" +
		"0\nSECTION\n2\nENTITIES\n" + strings.Join(entities, "") + "0\nENDSEC\n0\nEOF\n"
}

// lwPolyline renders an LWPOLYLINE from flat x,y pairs.
func lwPolyline(layer string, closed bool, xy ...float64) string {
	flag := 0
	if closed {
		flag = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "0\nLWPOLYLINE\n8\n%s\n90\n%d\n70\n%d\n", layer, len(xy)/2, flag)
	for i := 0; i+1 < len(xy); i += 2 {
		fmt.Fprintf(&b, "10\n%f\n20\n%f\n", xy[i], xy[i+1])
	}
	return b.String()
}

func circleEnt(layer string, x, y, r float64) string {
	return fmt.Sprintf("0\nCIRCLE\n8\n%s\n10\n%f\n20\n%f\n40\n%f\n", layer, x, y, r)
}

// ellipseEnt renders an ELLIPSE from its center, major-axis endpoint (relative
// to the center), axis ratio and start/end parameters.
func ellipseEnt(layer string, cx, cy, mx, my, ratio, start, end float64) string {
	// %g, not %f: the start/end parameters need full precision (a degenerate
	// sweep like 1e-7 rad would round to zero decimals under %f).
	return fmt.Sprintf("0\nELLIPSE\n8\n%s\n10\n%g\n20\n%g\n11\n%g\n21\n%g\n40\n%g\n41\n%g\n42\n%g\n",
		layer, cx, cy, mx, my, ratio, start, end)
}

// withPair appends one group-code pair to an entity.
func withPair(ent string, code int, val string) string {
	return ent + fmt.Sprintf("%d\n%s\n", code, val)
}
