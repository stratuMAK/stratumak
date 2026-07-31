// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#include "classicladder_rt.h"
*/
import "C"

// Helpers for the dynamic-sizing tests. Test files cannot use cgo, so the
// sizes travel as a plain map keyed by the load-argument names.

// allocSizedRT builds a cl_sizes_t from arg-name keys (missing keys keep the
// test default 10; nbr_error_bits has no load argument and stays 10) and
// allocates an instance. Returns nil when the allocator refuses, so tests can
// probe the validation.
func allocSizedRT(sizes map[string]int) *ladderRT {
	cs := C.cl_sizes_t{
		nbr_rungs:        10,
		nbr_bits:         10,
		nbr_words:        10,
		nbr_timers:       10,
		nbr_monostables:  10,
		nbr_counters:     10,
		nbr_timers_iec:   10,
		nbr_phys_inputs:  10,
		nbr_phys_outputs: 10,
		nbr_arithm_expr:  10,
		nbr_sections:     10,
		nbr_symbols:      10,
		nbr_s32_in:       10,
		nbr_s32_out:      10,
		nbr_float_in:     10,
		nbr_float_out:    10,
		nbr_error_bits:   10,
	}
	for key, n := range sizes {
		v := C.int(n)
		switch key {
		case "numRungs":
			cs.nbr_rungs = v
		case "numBits":
			cs.nbr_bits = v
		case "numWords":
			cs.nbr_words = v
		case "numTimers":
			cs.nbr_timers = v
		case "numMonostables":
			cs.nbr_monostables = v
		case "numCounters":
			cs.nbr_counters = v
		case "numTimersIec":
			cs.nbr_timers_iec = v
		case "numPhysInputs":
			cs.nbr_phys_inputs = v
		case "numPhysOutputs":
			cs.nbr_phys_outputs = v
		case "numArithmExpr":
			cs.nbr_arithm_expr = v
		case "numSections":
			cs.nbr_sections = v
		case "numSymbols":
			cs.nbr_symbols = v
		case "numS32in":
			cs.nbr_s32_in = v
		case "numS32out":
			cs.nbr_s32_out = v
		case "numFloatIn":
			cs.nbr_float_in = v
		case "numFloatOut":
			cs.nbr_float_out = v
		}
	}
	rt := C.classicladder_rt_alloc(&cs)
	if rt == nil {
		return nil
	}
	C.classicladder_rt_init_data(rt)
	return &ladderRT{rt: rt}
}

// clSizeLimit re-exports the allocator's sanity cap for the tests.
const clSizeLimit = C.CL_SIZE_LIMIT

// maxSteps re-exports the fixed step count (the two step regions are always
// this long, whatever the configured sizes).
const maxSteps = C.CL_MAX_STEPS

// parseArgsForTest runs parseModuleArgs and flattens the cgo sizes struct
// into arg-name keys.
func parseArgsForTest(args []string) (map[string]int, string, int, error) {
	sizes, projectFile, slavePort, err := parseModuleArgs(args)
	if err != nil {
		return nil, "", 0, err
	}
	m := map[string]int{
		"numRungs":       int(sizes.nbr_rungs),
		"numBits":        int(sizes.nbr_bits),
		"numWords":       int(sizes.nbr_words),
		"numTimers":      int(sizes.nbr_timers),
		"numMonostables": int(sizes.nbr_monostables),
		"numCounters":    int(sizes.nbr_counters),
		"numTimersIec":   int(sizes.nbr_timers_iec),
		"numPhysInputs":  int(sizes.nbr_phys_inputs),
		"numPhysOutputs": int(sizes.nbr_phys_outputs),
		"numArithmExpr":  int(sizes.nbr_arithm_expr),
		"numSections":    int(sizes.nbr_sections),
		"numSymbols":     int(sizes.nbr_symbols),
		"numS32in":       int(sizes.nbr_s32_in),
		"numS32out":      int(sizes.nbr_s32_out),
		"numFloatIn":     int(sizes.nbr_float_in),
		"numFloatOut":    int(sizes.nbr_float_out),
	}
	return m, projectFile, slavePort, nil
}
