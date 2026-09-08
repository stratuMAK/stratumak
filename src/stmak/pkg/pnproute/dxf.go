// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// pair is one DXF group code / value record.
type pair struct {
	code int
	val  string
}

// entity is a raw DXF entity: its type plus all its group-code pairs. An
// old-style POLYLINE also carries its VERTEX children, which the DXF format
// writes as separate entities up to a SEQEND.
type entity struct {
	typ   string
	pairs []pair
	verts []entity
}

// readPairs reads the input as a stream of (code, value) records.
func readPairs(r io.Reader) ([]pair, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var pairs []pair
	for sc.Scan() {
		codeLine := strings.TrimSpace(sc.Text())
		if !sc.Scan() {
			// A group code with no value line is a file cut off mid-record.
			// Failing loudly matters here: a truncated drawing that loads as
			// "whatever fit" is a dead zone smaller than the one drawn.
			return nil, fmt.Errorf("truncated: group code %q at end of input has no value", codeLine)
		}
		valLine := strings.TrimRight(sc.Text(), "\r")
		code, err := strconv.Atoi(codeLine)
		if err != nil {
			return nil, fmt.Errorf("bad group code %q: %w", codeLine, err)
		}
		pairs = append(pairs, pair{code, valLine})
	}
	return pairs, sc.Err()
}

// entitiesInSection collects the entities of the ENTITIES section. Sections are
// tracked by their 0/SECTION + 2/<name> header rather than by the name alone,
// so a value that merely reads "ENTITIES" elsewhere in the file cannot open one.
//
// An ENTITIES section still open at end of input is an error: DXF closes every
// section with ENDSEC, so a missing one means the file was truncated between
// records — and the entities already collected may be an incomplete picture of
// the drawing (a dead zone missing entirely is as unsafe as one loaded small).
func entitiesInSection(pairs []pair) ([]entity, error) {
	var ents []entity
	var cur *entity
	inEntities, expectName := false, false

	flush := func() {
		if cur != nil {
			ents = append(ents, *cur)
			cur = nil
		}
	}
	for _, p := range pairs {
		if p.code == 0 {
			switch strings.ToUpper(strings.TrimSpace(p.val)) {
			case "SECTION":
				flush()
				inEntities, expectName = false, true
				continue
			case "ENDSEC":
				flush()
				inEntities, expectName = false, false
				continue
			}
		}
		if expectName {
			if p.code == 2 {
				inEntities = strings.EqualFold(strings.TrimSpace(p.val), "ENTITIES")
				expectName = false
			}
			continue
		}
		if !inEntities {
			continue
		}
		if p.code == 0 {
			typ := strings.ToUpper(strings.TrimSpace(p.val))
			switch {
			case typ == "VERTEX" && cur != nil && cur.typ == "POLYLINE":
				cur.verts = append(cur.verts, entity{typ: typ})
			case typ == "SEQEND":
				flush()
			default:
				flush()
				cur = &entity{typ: typ}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if cur.typ == "POLYLINE" && len(cur.verts) > 0 {
			v := &cur.verts[len(cur.verts)-1]
			v.pairs = append(v.pairs, p)
			continue
		}
		cur.pairs = append(cur.pairs, p)
	}
	if inEntities {
		return nil, fmt.Errorf("truncated: the ENTITIES section is not closed with ENDSEC")
	}
	flush()
	return ents, nil
}

func valString(e entity, code int) string {
	for _, p := range e.pairs {
		if p.code == code {
			return p.val
		}
	}
	return ""
}

// valFloatOK reads a float group code and reports whether it was present and
// parseable. Geometry-defining codes (a circle's center, say) go through this
// rather than a defaulting accessor: a default of 0 would silently relocate
// the shape to the origin instead of failing.
func valFloatOK(e entity, code int) (float64, bool) {
	for _, p := range e.pairs {
		if p.code == code {
			if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil {
				return v, true
			}
			return 0, false
		}
	}
	return 0, false
}

// valFloatOpt reads a genuinely optional float group code: an absent code
// yields def, but a present-yet-malformed value is still an error — optional
// never licenses the loader to guess at a value the file does carry.
func valFloatOpt(e entity, code int, def float64) (float64, error) {
	for _, p := range e.pairs {
		if p.code == code {
			v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
			if err != nil {
				return 0, fmt.Errorf("has a malformed value %q for group code %d", p.val, code)
			}
			return v, nil
		}
	}
	return def, nil
}

// valIntOpt is valFloatOpt for integer group codes.
func valIntOpt(e entity, code int, def int) (int, error) {
	for _, p := range e.pairs {
		if p.code == code {
			v, err := strconv.Atoi(strings.TrimSpace(p.val))
			if err != nil {
				return 0, fmt.Errorf("has a malformed value %q for group code %d", p.val, code)
			}
			return v, nil
		}
	}
	return def, nil
}

// polylineVertices extracts the vertices of an LWPOLYLINE (10/20 pairs on the
// entity) or of an old-style POLYLINE (10/20 on its VERTEX children) together
// with the closed flag (group code 70, bit 1).
func polylineVertices(e entity) (Polygon, bool, error) {
	if err := checkPlanar(e); err != nil {
		return nil, false, err
	}
	// A malformed closed flag must not read as "open": the flag decides whether
	// the ring is a polygon at all, so a value the file carries but the loader
	// cannot read is rejected, not guessed at.
	closed := false
	for _, p := range e.pairs {
		if p.code == 70 {
			f, err := strconv.Atoi(strings.TrimSpace(p.val))
			if err != nil {
				return nil, false, fmt.Errorf("has a malformed flags value %q (group code 70)", p.val)
			}
			closed = f&1 == 1
		}
	}
	if e.typ == "POLYLINE" {
		poly := make(Polygon, 0, len(e.verts))
		for i, v := range e.verts {
			if err := checkPlanar(v); err != nil {
				return nil, false, err
			}
			if err := checkNoBulge(v.pairs); err != nil {
				return nil, false, err
			}
			// Coordinates are required, not defaulted: a missing or malformed
			// value falling back to 0 would silently drag the vertex onto an
			// axis and reshape the ring the zone guards.
			x, okx := valFloatOK(v, 10)
			y, oky := valFloatOK(v, 20)
			if !okx || !oky {
				return nil, false, fmt.Errorf("vertex %d has a missing or malformed coordinate (group codes 10/20)", i)
			}
			poly = append(poly, Point{x, y})
		}
		return poly, closed, nil
	}
	if err := checkNoBulge(e.pairs); err != nil {
		return nil, false, err
	}
	var poly Polygon
	var x float64
	var haveX bool
	for _, p := range e.pairs {
		switch p.code {
		case 10, 20:
			// Reject, don't guess: reading a corrupted coordinate as 0 would
			// silently move this vertex onto an axis — a rectangle with one
			// corrupted corner loads as a triangle guarding half the area.
			v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
			if err != nil {
				return nil, false, fmt.Errorf("vertex %d has a malformed coordinate %q (group code %d)", len(poly), p.val, p.code)
			}
			if p.code == 10 {
				x, haveX = v, true
			} else if haveX {
				poly = append(poly, Point{x, v})
				haveX = false
			}
		}
	}
	// An LWPOLYLINE declares its vertex count (group code 90). A mismatch
	// means vertices were lost — a truncated or corrupt export — and a
	// polygon smaller than declared is a dead zone that guards less than the
	// drawing shows. The count is optional in principle, but one the file
	// carries and the loader cannot read is an error like any other.
	want, err := valIntOpt(e, 90, -1)
	if err != nil {
		return nil, false, err
	}
	if want >= 0 && want != len(poly) {
		return nil, false, fmt.Errorf("declares %d vertices but carries %d (truncated or corrupt file?)", want, len(poly))
	}
	return poly, closed, nil
}

// checkNoBulge rejects arc segments inside a polyline. Reading their bulge as a
// straight chord would silently shrink the shape, and for a dead zone that is a
// safety hazard — better to make the export fix it.
func checkNoBulge(pairs []pair) error {
	for _, p := range pairs {
		if p.code != 42 {
			continue
		}
		// A malformed bulge must not read as "no bulge": if the value really
		// was an arc, the straight chord silently shrinks the shape.
		v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
		if err != nil {
			return fmt.Errorf("has a malformed bulge value %q (group code 42)", p.val)
		}
		if math.Abs(v) > 1e-12 {
			return fmt.Errorf("has a bulge (arc) segment; explode arcs into straight segments before exporting")
		}
	}
	return nil
}

// checkPlanar rejects entities drawn in a nontrivial object coordinate system
// (extrusion direction other than +Z, group codes 210/220/230). Their
// group-code coordinates are OCS, not world coordinates: a mirrored OCS (-Z)
// flips the shape about the Y axis, and a tilted one places it somewhere else
// entirely — either way the loaded dead zone would silently guard the wrong
// region.
func checkPlanar(e entity) error {
	ex, ey, ez := 0.0, 0.0, 1.0 // the default extrusion is +Z (OCS == WCS)
	for _, p := range e.pairs {
		var dst *float64
		switch p.code {
		case 210:
			dst = &ex
		case 220:
			dst = &ey
		case 230:
			dst = &ez
		default:
			continue
		}
		// A malformed component must not fall back to the +Z default: if the
		// entity really is in an OCS, its coordinates are not world coordinates
		// and the shape would silently load somewhere else.
		v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
		if err != nil {
			return fmt.Errorf("has a malformed extrusion value %q (group code %d)", p.val, p.code)
		}
		*dst = v
	}
	// Only the direction matters (any positive multiple of +Z leaves OCS equal
	// to WCS), so normalize before comparing.
	l := math.Sqrt(ex*ex + ey*ey + ez*ez)
	const eps = 1e-9
	if l < eps || math.Abs(ex)/l > eps || math.Abs(ey)/l > eps || ez < 0 {
		return fmt.Errorf("uses an object coordinate system (extrusion %g,%g,%g); re-export it in world coordinates", ex, ey, ez)
	}
	return nil
}
