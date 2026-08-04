// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	api "github.com/stratuMAK/stratumak/src/stmak/generated/gmi/classicladder"
	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
)

// Which HAL signal each physical ladder variable is wired to.
//
// Ported from 2.9 emc_mods.c ConvVarNameToHalSigName, which answers the question
// a reader of someone else's ladder actually has: this contact reads %I3, but
// what is %I3? The answer is in HAL rather than in the project, so it can only
// come from here.
//
// 2.9 returned a display string with a direction arrow baked in ("⇒gui-estop").
// This returns the signal and the direction separately and lets the client draw
// it, because the arrow is presentation and the wiring is not.

// GetHalLinks reports the HAL pin and connected signal for every physical
// variable. Variables with no pin — memory bits and words, timers, counters —
// are left out: they have no wiring, and saying so by omission beats a sentinel
// string that every client then has to recognise.
func (m *classicladder) GetHalLinks() ([]api.HalLink, error) {
	// One query for the whole component rather than one per pin. Pattern-matching
	// on the instance name is safe here because the names came from
	// createHALPins, which built them from the same name.
	signals := make(map[string]string)
	if res, err := halcmd.Show("pin", m.name+".*"); err == nil {
		for _, p := range res.Pins {
			signals[p.Name] = p.Signal
		}
	} else {
		// HAL introspection failing is not a reason to fail the request: the
		// wiring is an annotation, and the pins are still real. Report them
		// unconnected and let the caller show the ladder.
		m.logger.Warn("classicladder: could not read HAL pins for wiring", "err", err)
	}

	links := make([]api.HalLink, 0, len(m.halPins))
	for _, ref := range m.halPins {
		links = append(links, api.HalLink{
			VarType: int32(ref.varType),
			Offset:  int32(ref.offset),
			Pin:     ref.pin,
			Signal:  signals[ref.pin],
			IsInput: ref.isInput,
		})
	}
	return links, nil
}
