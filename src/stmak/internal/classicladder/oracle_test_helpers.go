// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#include "classicladder_rt.h"
*/
import "C"

import (
	"fmt"
	"strings"
)

// Script execution against the stmak RT engine, in the same command language
// the 2.9 oracle in testdata/oracle understands, so a single script can drive
// both engines and the dumps can be compared line for line.
//
// Commands: "set <type> <offset> <value>", "scan <ms>", "prepare", "dump".

// dumpState renders the PLC variable state in the oracle's dump format.
func (m *classicladder) dumpState() string {
	rt := m.rt
	var b strings.Builder

	region := func(label string, varType, count int) {
		b.WriteString(label)
		for i := 0; i < count; i++ {
			fmt.Fprintf(&b, " %d", int(C.read_var_ext(rt, C.int(varType), C.int(i))))
		}
		b.WriteByte('\n')
	}

	region("BITS", C.CL_VAR_MEM_BIT, int(rt.sizes.nbr_bits))
	region("INPUTS", C.CL_VAR_PHYS_INPUT, int(rt.sizes.nbr_phys_inputs))
	region("OUTPUTS", C.CL_VAR_PHYS_OUTPUT, int(rt.sizes.nbr_phys_outputs))
	region("WORDS", C.CL_VAR_MEM_WORD, int(rt.sizes.nbr_words))
	region("TIMER_DONE", C.CL_VAR_TIMER_DONE, int(rt.sizes.nbr_timers))
	region("TIMER_RUNNING", C.CL_VAR_TIMER_RUNNING, int(rt.sizes.nbr_timers))
	region("TIMER_VALUE", C.CL_VAR_TIMER_VALUE, int(rt.sizes.nbr_timers))
	region("MONO_RUNNING", C.CL_VAR_MONOSTABLE_RUNNING, int(rt.sizes.nbr_monostables))
	region("COUNTER_VALUE", C.CL_VAR_COUNTER_VALUE, int(rt.sizes.nbr_counters))
	region("COUNTER_DONE", C.CL_VAR_COUNTER_DONE, int(rt.sizes.nbr_counters))
	region("TIMER_IEC_DONE", C.CL_VAR_TIMER_IEC_DONE, int(rt.sizes.nbr_timers_iec))
	region("TIMER_IEC_VALUE", C.CL_VAR_TIMER_IEC_VALUE, int(rt.sizes.nbr_timers_iec))
	region("ERROR_BITS", C.CL_VAR_ERROR_BIT, int(rt.sizes.nbr_error_bits))
	// The SFC state. Without these a project with a sequential section
	// compares identically whatever the two engines make of the chart.
	region("STEP_ACTIVITY", C.CL_VAR_STEP_ACTIVITY, C.CL_MAX_STEPS)
	region("STEP_TIME", C.CL_VAR_STEP_TIME, C.CL_MAX_STEPS)
	b.WriteString("END\n")
	return b.String()
}

// runScript executes an oracle script and returns everything the dumps
// produced, concatenated in order.
func (m *classicladder) runScript(script string) (string, error) {
	var out strings.Builder

	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "set":
			if len(fields) != 4 {
				return "", fmt.Errorf("bad set command: %q", line)
			}
			var varType, offset, value int
			if _, err := fmt.Sscan(fields[1], &varType); err != nil {
				return "", fmt.Errorf("bad set command: %q", line)
			}
			if _, err := fmt.Sscan(fields[2], &offset); err != nil {
				return "", fmt.Errorf("bad set command: %q", line)
			}
			if _, err := fmt.Sscan(fields[3], &value); err != nil {
				return "", fmt.Errorf("bad set command: %q", line)
			}
			C.write_var_ext(m.rt, C.int(varType), C.int(offset), C.int(value))
		case "scan":
			ms := 1
			if len(fields) > 1 {
				if _, err := fmt.Sscan(fields[1], &ms); err != nil {
					return "", fmt.Errorf("bad scan command: %q", line)
				}
			}
			C.cl_scan(m.rt, C.int(ms))
		case "prepare":
			C.cl_prepare_all_datas_before_run(m.rt)
		case "dump":
			out.WriteString(m.dumpState())
		default:
			return "", fmt.Errorf("unknown command: %q", line)
		}
	}
	return out.String(), nil
}

// prepareRun resets all dynamic state, as a transition to RUN does.
func (m *classicladder) prepareRunForTest() {
	C.cl_prepare_all_datas_before_run(m.rt)
}
