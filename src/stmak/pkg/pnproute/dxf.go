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
			break
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
func entitiesInSection(pairs []pair) []entity {
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
	flush()
	return ents
}

func valString(e entity, code int) string {
	for _, p := range e.pairs {
		if p.code == code {
			return p.val
		}
	}
	return ""
}

func valFloat(e entity, code int) float64 { return valFloatDefault(e, code, 0) }

func valFloatDefault(e entity, code int, def float64) float64 {
	for _, p := range e.pairs {
		if p.code == code {
			if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil {
				return v
			}
		}
	}
	return def
}

// polylineVertices extracts the vertices of an LWPOLYLINE (10/20 pairs on the
// entity) or of an old-style POLYLINE (10/20 on its VERTEX children) together
// with the closed flag (group code 70, bit 1).
func polylineVertices(e entity) (Polygon, bool, error) {
	if err := checkPlanar(e); err != nil {
		return nil, false, err
	}
	closed := false
	for _, p := range e.pairs {
		if p.code == 70 {
			if f, err := strconv.Atoi(strings.TrimSpace(p.val)); err == nil {
				closed = f&1 == 1
			}
		}
	}
	if e.typ == "POLYLINE" {
		poly := make(Polygon, 0, len(e.verts))
		for _, v := range e.verts {
			if err := checkPlanar(v); err != nil {
				return nil, false, err
			}
			if err := checkNoBulge(v.pairs); err != nil {
				return nil, false, err
			}
			poly = append(poly, Point{valFloat(v, 10), valFloat(v, 20)})
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
		case 10:
			x, _ = strconv.ParseFloat(strings.TrimSpace(p.val), 64)
			haveX = true
		case 20:
			if haveX {
				y, _ := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
				poly = append(poly, Point{x, y})
				haveX = false
			}
		}
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
		if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil && math.Abs(v) > 1e-12 {
			return fmt.Errorf("has a bulge (arc) segment; explode arcs into straight segments before exporting")
		}
	}
	return nil
}

// checkPlanar rejects entities drawn in a mirrored object coordinate system
// (extrusion direction -Z). Their group-code coordinates are not world
// coordinates, and reading them as such mirrors the shape about the Y axis.
func checkPlanar(e entity) error {
	for _, p := range e.pairs {
		if p.code != 230 {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil && v < 0 {
			return fmt.Errorf("uses a mirrored extrusion direction (OCS); re-export it in world coordinates")
		}
	}
	return nil
}
