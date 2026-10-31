// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnproute

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Choosing the path a leg drives (D47, D50).

// PathOptions say how [Planner.PlanPath] shapes a leg.
type PathOptions struct {
	// Smooth enables the curve shapers. Off, PlanPath returns the route.
	Smooth bool
	// Limits are the machine's limits, for the requested speeds of a curve's
	// segments and for the TP model that chooses between the candidates.
	Limits Limits
	// BlendTolerance is the tolerance the route's corners are blended within
	// by the trajectory planner (pnptask's BLEND_TOLERANCE), for the model of
	// the route.
	BlendTolerance float64
}

// PlanPath plans a leg from start to goal and shapes it (D47, D50): of the
// route, the natural spline and the Catmull-Rom curve through it, the one the
// TP model says is fastest. The route is always a candidate and always clear,
// so a leg that has a route has a path. The chosen curve's segments carry
// their requested speed (Segment.Vel): the highest speed of the time-optimal
// profile along the stretch of the curve each piece replaces.
//
// Without o.Smooth, or for a straight leg, it returns the route ([Planner.Plan]).
// With o.Smooth the returned Route's Time is the model's time for it.
func (p *Planner) PlanPath(start, goal Point, o PathOptions) (*Route, error) {
	r, err := p.Plan(start, goal)
	if err != nil {
		return nil, err
	}
	if !o.Smooth {
		return r, nil
	}
	if !o.Limits.valid() {
		return nil, fmt.Errorf("pnproute: limits %+v are not usable for the TP model", o.Limits)
	}
	if o.BlendTolerance < 0 {
		return nil, fmt.Errorf("pnproute: blend tolerance %v is negative", o.BlendTolerance)
	}
	r.Time = tpPolyline(r.Waypoints, o.BlendTolerance, o.Limits).T
	if len(r.Waypoints) <= 2 {
		return r, nil
	}
	best := r
	for _, sh := range []Shaper{ShaperNatural, ShaperCatmullRom} {
		c, err := p.Smooth(r, sh)
		if errors.Is(err, ErrNoCurve) {
			continue
		}
		if err != nil {
			return nil, err
		}
		setSpeeds(c, o.Limits)
		if c.Time = tpChain(c.Segments, o.Limits).T; c.Time < best.Time {
			best = c
		}
	}
	return best, nil
}

// setSpeeds gives every segment of a curve the highest speed the
// time-optimal profile reaches along the stretch of the curve its piece
// replaces.
func setSpeeds(c *Route, L Limits) {
	pf := idealProfile(c.sm, L)
	for i := range c.Segments {
		s0, s1 := c.pieces[i][0], c.pieces[i][1]
		lo := sort.Search(len(c.sm), func(k int) bool { return c.sm[k].s >= s0-1e-9 })
		v := 0.0
		for k := lo; k < len(c.sm) && c.sm[k].s <= s1+1e-9; k++ {
			v = math.Max(v, pf.v[k])
		}
		c.Segments[i].Vel = v
	}
}
