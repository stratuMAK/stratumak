// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// DefaultArcSegments is how many straight segments a circle or a full ellipse
// is discretized into on load.
const DefaultArcSegments = 96

// ShapeKind records what a dead zone was drawn as. The planner works on Poly
// alone; the kind is kept for diagnostics and for drawing the scene.
type ShapeKind string

const (
	ShapePolyline ShapeKind = "polyline"
	ShapeCircle   ShapeKind = "circle"
	ShapeEllipse  ShapeKind = "ellipse"
)

// Shape is one loaded dead zone: the planning polygon plus what it came from.
type Shape struct {
	Kind   ShapeKind
	Poly   Polygon
	Center Point   // circle/ellipse
	Radius float64 // circle
}

// Scene is a validated world: one closed convex outer limit the head must stay
// inside, and the dead zones it must stay out of. A Scene carries no clearance
// — that belongs to the [Planner] built from it.
type Scene struct {
	Outer     Polygon
	Deadzones []Shape
}

// Bounds returns the lower-left and upper-right corner of the outer limit.
func (s *Scene) Bounds() (min, max Point) {
	b := boundsOf(s.Outer)
	return Point{b.minX, b.minY}, Point{b.maxX, b.maxY}
}

// LoadOption tunes DXF loading.
type LoadOption func(*loadOptions)

type loadOptions struct {
	arcSegments int
}

// WithArcSegments sets how many segments a circle or full ellipse is
// discretized into (default [DefaultArcSegments]). Fewer segments mean a
// smaller visibility graph and faster planning at the cost of a coarser
// approximation — the discretized polygon is inscribed, so it always sits
// *inside* the true circle and the offset makes up the difference.
func WithArcSegments(n int) LoadOption {
	return func(o *loadOptions) { o.arcSegments = n }
}

// Layer names recognized in the DXF. Case, spaces, hyphens and underscores are
// ignored, so "Outer Limits", "outer-limits" and "OUTERLIMIT" all match.
const (
	LayerOuterLimit = "outer limits"
	LayerDeadzones  = "deadzones"
)

// entity types that may sit on a recognized layer without describing geometry.
// Anything else there is an error: silently dropping a shape the drawing calls
// a dead zone is exactly the failure this package must not have.
// HATCH is deliberately NOT in this list: a filled hatch is the natural CAD
// idiom for marking an *area*, so one sitting on the dead-zone layer most
// likely IS the keep-out — silently ignoring it would load a drawing with
// fewer zones than it shows. It gets a dedicated error instead.
var annotationEntities = map[string]bool{
	"TEXT": true, "MTEXT": true, "ATTDEF": true, "ATTRIB": true,
	"DIMENSION": true, "LEADER": true, "MLEADER": true, "POINT": true,
	"VIEWPORT": true,
}

// LoadDXF parses the ENTITIES section of a DXF drawing and returns the
// validated scene. Exactly one closed convex polyline must sit on the
// "outer limits" layer; closed convex polylines, circles and ellipses on the
// "deadzones" layer become dead zones. Everything on other layers is ignored.
func LoadDXF(r io.Reader, opts ...LoadOption) (*Scene, error) {
	o := loadOptions{arcSegments: DefaultArcSegments}
	for _, opt := range opts {
		opt(&o)
	}
	if o.arcSegments < 8 {
		return nil, fmt.Errorf("pnproute: arc segments must be at least 8, got %d", o.arcSegments)
	}

	pairs, err := readPairs(r)
	if err != nil {
		return nil, fmt.Errorf("pnproute: reading DXF: %w", err)
	}
	ents, err := entitiesInSection(pairs)
	if err != nil {
		return nil, fmt.Errorf("pnproute: reading DXF: %w", err)
	}

	scene := &Scene{}
	outerCount := 0
	for _, e := range ents {
		layer := normalizeLayer(valString(e, 8))
		isOuter := layer == "outerlimits" || layer == "outerlimit"
		isZone := layer == "deadzones" || layer == "deadzone"
		if !isOuter && !isZone {
			continue
		}
		what := "dead zone"
		if isOuter {
			what = "outer limit"
		}

		switch e.typ {
		case "LWPOLYLINE", "POLYLINE":
			poly, closed, err := polylineVertices(e)
			if err != nil {
				return nil, fmt.Errorf("pnproute: %s polyline %v", what, err)
			}
			if !closed {
				return nil, fmt.Errorf("pnproute: %s polyline is not closed", what)
			}
			poly = dedupeRing(poly)
			if err := checkConvex(poly); err != nil {
				return nil, fmt.Errorf("pnproute: %s polyline %v", what, err)
			}
			if isOuter {
				scene.Outer = poly
				outerCount++
				continue
			}
			scene.Deadzones = append(scene.Deadzones, Shape{Kind: ShapePolyline, Poly: poly})

		case "CIRCLE":
			if isOuter {
				return nil, fmt.Errorf("pnproute: the outer limit must be a closed polyline, found a CIRCLE on layer %q", LayerOuterLimit)
			}
			// The center is required, not defaulted: a missing group code
			// falling back to 0 would silently relocate the zone to the
			// origin and leave the real hazard unguarded.
			cx, okx := valFloatOK(e, 10)
			cy, oky := valFloatOK(e, 20)
			if !okx || !oky {
				return nil, fmt.Errorf("pnproute: dead-zone circle has no center (group codes 10/20 missing or malformed)")
			}
			c := Point{cx, cy}
			// The radius is required for the same reason: a malformed value
			// defaulting to 0 would be rejected as "radius 0" — a misdiagnosis
			// pointing the user at the wrong problem.
			r, okr := valFloatOK(e, 40)
			if !okr {
				return nil, fmt.Errorf("pnproute: dead-zone circle at (%.3f,%.3f) has no radius (group code 40 missing or malformed)", c.X, c.Y)
			}
			if r <= 0 {
				return nil, fmt.Errorf("pnproute: dead-zone circle at (%.3f,%.3f) has radius %.3f", c.X, c.Y, r)
			}
			if err := checkPlanar(e); err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone circle %v", err)
			}
			// Discretized rings pass the same validation as drawn polylines: a
			// dead zone that loads without error must actually guard its area.
			poly := dedupeRing(discretizeCircle(c, r, o.arcSegments))
			if err := checkConvex(poly); err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone circle at (%.3f,%.3f) %v", c.X, c.Y, err)
			}
			scene.Deadzones = append(scene.Deadzones, Shape{
				Kind: ShapeCircle, Center: c, Radius: r, Poly: poly,
			})

		case "ELLIPSE":
			if isOuter {
				return nil, fmt.Errorf("pnproute: the outer limit must be a closed polyline, found an ELLIPSE on layer %q", LayerOuterLimit)
			}
			if err := checkPlanar(e); err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse %v", err)
			}
			cx, okx := valFloatOK(e, 10)
			cy, oky := valFloatOK(e, 20)
			if !okx || !oky {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse has no center (group codes 10/20 missing or malformed)")
			}
			c := Point{cx, cy}
			// The major axis and ratio are required like the center is: a
			// malformed component defaulting to 0 would silently load a
			// different ellipse — axis (30,40) with a corrupt code 21 becomes
			// (30,0) — instead of failing the drawing.
			mx, okmx := valFloatOK(e, 11)
			my, okmy := valFloatOK(e, 21)
			if !okmx || !okmy {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) has no major-axis endpoint (group codes 11/21 missing or malformed)", c.X, c.Y)
			}
			majorRel := Point{mx, my}
			ratio, okr := valFloatOK(e, 40)
			if !okr {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) has no axis ratio (group code 40 missing or malformed)", c.X, c.Y)
			}
			if math.Hypot(majorRel.X, majorRel.Y) <= 0 || ratio <= 0 || ratio > 1 {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) has a degenerate axis definition", c.X, c.Y)
			}
			// The sweep parameters are genuinely optional — a full ellipse may
			// omit them — but a value the file carries and the loader cannot
			// read is still an error: guessing would close a partial arc into
			// the wrong ring.
			start, err := valFloatOpt(e, 41, 0)
			if err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) %v", c.X, c.Y, err)
			}
			end, err := valFloatOpt(e, 42, 2*math.Pi)
			if err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) %v", c.X, c.Y, err)
			}
			// A partial ellipse arc is closed by the chord between its ends;
			// that ring is still convex, and erring on the larger area is the
			// safe direction for an obstacle. The ring validation below catches
			// degenerate definitions the parameter checks cannot — e.g. a
			// near-zero sweep whose ring encloses no area.
			poly := dedupeRing(discretizeEllipse(c, majorRel, ratio, start, end, o.arcSegments))
			if err := checkConvex(poly); err != nil {
				return nil, fmt.Errorf("pnproute: dead-zone ellipse at (%.3f,%.3f) %v", c.X, c.Y, err)
			}
			scene.Deadzones = append(scene.Deadzones, Shape{
				Kind: ShapeEllipse, Center: c, Poly: poly,
			})

		default:
			if annotationEntities[e.typ] {
				continue
			}
			if e.typ == "HATCH" {
				return nil, fmt.Errorf("pnproute: a HATCH on the %s layer cannot be read; draw the region's outline as a closed polyline (the fill is only decoration)", what)
			}
			allowed := "closed polylines, circles and ellipses"
			if isOuter {
				allowed = "a closed polyline"
			}
			return nil, fmt.Errorf("pnproute: unsupported entity %s on the %s layer; only %s are read",
				e.typ, what, allowed)
		}
	}

	if scene.Outer == nil {
		return nil, fmt.Errorf("pnproute: no closed polyline found on layer %q", LayerOuterLimit)
	}
	if outerCount > 1 {
		return nil, fmt.Errorf("pnproute: expected exactly one shape on layer %q, found %d", LayerOuterLimit, outerCount)
	}
	return scene, nil
}

// LoadDXFFile loads a scene from a DXF file on disk.
func LoadDXFFile(path string, opts ...LoadOption) (*Scene, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pnproute: %w", err)
	}
	// Read-only: Close can only report an error about buffered writes, of
	// which there are none, so dropping it is explicit rather than global
	// (see .golangci.yml — no blanket errcheck exclusion for Close).
	defer func() { _ = f.Close() }()

	scene, err := LoadDXF(f, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w (in %s)", err, path)
	}
	return scene, nil
}

// normalizeLayer folds case and drops the separators CAD users vary on.
func normalizeLayer(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	for _, sep := range []string{" ", "-", "_"} {
		l = strings.ReplaceAll(l, sep, "")
	}
	return l
}
