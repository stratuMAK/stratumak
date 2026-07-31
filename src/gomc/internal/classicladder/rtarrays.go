// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#include "classicladder_rt.h"
*/
import "C"

import "unsafe"

// The size-configurable PLC arrays live in one C-allocated block behind
// pointer fields of classicladder_rt_t (see classicladder_rt_alloc). These
// helpers are the only way Go code views them: each wraps the pointer in a
// slice of the configured length, so ordinary Go indexing keeps its bounds
// check against the real allocation. The memory is C-owned and lives until
// classicladder_rt_free, so the slices are safe to hold while rt is.

func rtRungs(rt *C.classicladder_rt_t) []C.cl_rung_t {
	return unsafe.Slice(rt.rungs, int(rt.sizes.nbr_rungs))
}

func rtSections(rt *C.classicladder_rt_t) []C.cl_section_t {
	return unsafe.Slice(rt.sections, int(rt.sizes.nbr_sections))
}

func rtArithmExprs(rt *C.classicladder_rt_t) []C.cl_arithm_expr_t {
	return unsafe.Slice(rt.arithm_exprs, int(rt.sizes.nbr_arithm_expr))
}

func rtCompiledExprs(rt *C.classicladder_rt_t) []C.cl_compiled_expr_t {
	return unsafe.Slice(rt.compiled_exprs, int(rt.sizes.nbr_arithm_expr))
}

func rtTimers(rt *C.classicladder_rt_t) []C.cl_timer_t {
	return unsafe.Slice(rt.timers, int(rt.sizes.nbr_timers))
}

func rtMonostables(rt *C.classicladder_rt_t) []C.cl_monostable_t {
	return unsafe.Slice(rt.monostables, int(rt.sizes.nbr_monostables))
}

func rtCounters(rt *C.classicladder_rt_t) []C.cl_counter_t {
	return unsafe.Slice(rt.counters, int(rt.sizes.nbr_counters))
}

func rtTimersIec(rt *C.classicladder_rt_t) []C.cl_timer_iec_t {
	return unsafe.Slice(rt.timers_iec, int(rt.sizes.nbr_timers_iec))
}

func rtVarBits(rt *C.classicladder_rt_t) []C.char {
	return unsafe.Slice(rt.var_bits, int(rt.var_bits_count))
}

func rtVarWords(rt *C.classicladder_rt_t) []C.int32_t {
	return unsafe.Slice(rt.var_words, int(rt.var_words_count))
}

func rtVarFloats(rt *C.classicladder_rt_t) []C.double {
	return unsafe.Slice(rt.var_floats, int(rt.var_floats_count))
}

func rtSymbols(rt *C.classicladder_rt_t) []C.cl_symbol_t {
	return unsafe.Slice(rt.symbols, int(rt.sizes.nbr_symbols))
}

func rtHalInputs(rt *C.classicladder_rt_t) []*C.hal_bit_t {
	return unsafe.Slice(rt.hal_inputs, int(rt.sizes.nbr_phys_inputs))
}

func rtHalOutputs(rt *C.classicladder_rt_t) []*C.hal_bit_t {
	return unsafe.Slice(rt.hal_outputs, int(rt.sizes.nbr_phys_outputs))
}

func rtHalS32Inputs(rt *C.classicladder_rt_t) []*C.hal_s32_t {
	return unsafe.Slice(rt.hal_s32_inputs, int(rt.sizes.nbr_s32_in))
}

func rtHalS32Outputs(rt *C.classicladder_rt_t) []*C.hal_s32_t {
	return unsafe.Slice(rt.hal_s32_outputs, int(rt.sizes.nbr_s32_out))
}

func rtHalFloatInputs(rt *C.classicladder_rt_t) []*C.hal_float_t {
	return unsafe.Slice(rt.hal_float_inputs, int(rt.sizes.nbr_float_in))
}

func rtHalFloatOutputs(rt *C.classicladder_rt_t) []*C.hal_float_t {
	return unsafe.Slice(rt.hal_float_outputs, int(rt.sizes.nbr_float_out))
}
