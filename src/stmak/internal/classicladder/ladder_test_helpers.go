// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

/*
#include "classicladder_rt.h"
*/
import "C"

// Helpers for driving the RT ladder engine from tests: build a rung cell by
// cell, run scans, and read variables back — all without HAL pins. Test files
// cannot use cgo, so every C name the tests need is re-exported here.

// Element types.
const (
	eleFree          = C.CL_ELE_FREE
	eleInput         = C.CL_ELE_INPUT
	eleInputNot      = C.CL_ELE_INPUT_NOT
	eleRisingInput   = C.CL_ELE_RISING_INPUT
	eleFallingInput  = C.CL_ELE_FALLING_INPUT
	eleConnection    = C.CL_ELE_CONNECTION
	eleTimer         = C.CL_ELE_TIMER
	eleMonostable    = C.CL_ELE_MONOSTABLE
	eleCounter       = C.CL_ELE_COUNTER
	eleTimerIEC      = C.CL_ELE_TIMER_IEC
	eleCompar        = C.CL_ELE_COMPAR
	eleOutput        = C.CL_ELE_OUTPUT
	eleOutputNot     = C.CL_ELE_OUTPUT_NOT
	eleOutputSet     = C.CL_ELE_OUTPUT_SET
	eleOutputReset   = C.CL_ELE_OUTPUT_RESET
	eleOutputJump    = C.CL_ELE_OUTPUT_JUMP
	eleOutputCall    = C.CL_ELE_OUTPUT_CALL
	eleOutputOperate = C.CL_ELE_OUTPUT_OPERATE
	eleUnusable      = C.CL_ELE_UNUSABLE
)

// Variable types.
const (
	varMemBit         = C.CL_VAR_MEM_BIT
	varMemWord        = C.CL_VAR_MEM_WORD
	varPhysInput      = C.CL_VAR_PHYS_INPUT
	varPhysOutput     = C.CL_VAR_PHYS_OUTPUT
	varStepActivity   = C.CL_VAR_STEP_ACTIVITY
	varErrorBit       = C.CL_VAR_ERROR_BIT
	varStepTime       = C.CL_VAR_STEP_TIME
	varPhysWordInput  = C.CL_VAR_PHYS_WORD_INPUT
	varPhysWordOutput = C.CL_VAR_PHYS_WORD_OUTPUT
	varPhysFloatIn    = C.CL_VAR_PHYS_FLOAT_INPUT
	varPhysFloatOut   = C.CL_VAR_PHYS_FLOAT_OUTPUT
	varTimerPreset    = C.CL_VAR_TIMER_PRESET
	varCounterValue   = C.CL_VAR_COUNTER_VALUE
	varCounterDone    = C.CL_VAR_COUNTER_DONE
	varTimerIECPreset = C.CL_VAR_TIMER_IEC_PRESET
)

// States and time bases.
const (
	stateStop     = C.CL_STATE_STOP
	stateRun      = C.CL_STATE_RUN
	timeBaseSecs  = C.CL_TIME_BASE_SECS
	timeBase100ms = C.CL_TIME_BASE_100MS
	timerIECOn    = C.CL_TIMER_IEC_TON
)

// ladderRT is a test PLC: one ladder section holding one or more rungs.
type ladderRT struct {
	rt *C.classicladder_rt_t
}

// newLadderRT allocates a PLC with a single main ladder section covering
// rungs [0, nbrRungs).
func newLadderRT(nbrRungs int) *ladderRT {
	rt := newTestRT()
	for i := 0; i < nbrRungs; i++ {
		rtRungs(rt)[i].used = 1
		rtRungs(rt)[i].prev_rung = C.int(i - 1)
		rtRungs(rt)[i].next_rung = C.int(i + 1)
	}
	rtRungs(rt)[nbrRungs-1].next_rung = C.int(nbrRungs - 1)

	sec := &rtSections(rt)[0]
	sec.used = 1
	sec.language = C.CL_SECTION_LADDER
	sec.sub_routine_number = -1
	sec.first_rung = 0
	sec.last_rung = C.int(nbrRungs - 1)
	return &ladderRT{rt: rt}
}

func (l *ladderRT) free() { freeTestRT(l.rt) }

// setSubRoutine turns section `sec` into a sub-routine numbered `nbr`,
// covering rungs [first, last].
func (l *ladderRT) setSubRoutine(sec, nbr, first, last int) {
	s := &rtSections(l.rt)[sec]
	s.used = 1
	s.language = C.CL_SECTION_LADDER
	s.sub_routine_number = C.int(nbr)
	s.first_rung = C.int(first)
	s.last_rung = C.int(last)
}

// setMainSection makes section `sec` a main ladder section over [first, last].
func (l *ladderRT) setMainSection(sec, first, last int) {
	s := &rtSections(l.rt)[sec]
	s.used = 1
	s.language = C.CL_SECTION_LADDER
	s.sub_routine_number = -1
	s.first_rung = C.int(first)
	s.last_rung = C.int(last)
}

// put places an element at (x=col, y=row) of a rung.
func (l *ladderRT) put(rung, col, row, eleType, varType, varNum int) {
	e := &rtRungs(l.rt)[rung].elements[col][row]
	e._type = C.int16_t(eleType)
	e.var_type = C.int32_t(varType)
	e.var_num = C.int32_t(varNum)
}

// connectTop marks the cell as joined to the cell above it (a vertical link).
func (l *ladderRT) connectTop(rung, col, row int) {
	rtRungs(l.rt)[rung].elements[col][row].connected_with_top = 1
}

// putBlock places a multi-cell block whose head sits at (col, row) — the right
// column of its footprint — and fills the body cells with ELE_UNUSABLE.
func (l *ladderRT) putBlock(rung, col, row, eleType, varNum, width, height int) {
	l.put(rung, col, row, eleType, 0, varNum)
	for x := col - width + 1; x <= col; x++ {
		for y := row; y < row+height; y++ {
			if x == col && y == row {
				continue
			}
			rtRungs(l.rt)[rung].elements[x][y]._type = C.CL_ELE_UNUSABLE
		}
	}
}

// scan runs one PLC scan accounting for ms elapsed milliseconds.
func (l *ladderRT) scan(ms int) { C.cl_scan(l.rt, C.int(ms)) }

// scanN runs n scans of ms milliseconds each.
func (l *ladderRT) scanN(n, ms int) {
	for i := 0; i < n; i++ {
		l.scan(ms)
	}
}

func (l *ladderRT) prepareRun() { C.cl_prepare_all_datas_before_run(l.rt) }

func (l *ladderRT) readVar(varType, offset int) int {
	return int(C.read_var_ext(l.rt, C.int(varType), C.int(offset)))
}

func (l *ladderRT) writeVar(varType, offset, value int) {
	C.write_var_ext(l.rt, C.int(varType), C.int(offset), C.int(value))
}

func (l *ladderRT) bit(offset int) bool {
	return l.readVar(C.CL_VAR_MEM_BIT, offset) != 0
}

func (l *ladderRT) input(offset int, on bool) {
	v := 0
	if on {
		v = 1
	}
	l.writeVar(C.CL_VAR_PHYS_INPUT, offset, v)
}

func (l *ladderRT) output(offset int) bool {
	return l.readVar(C.CL_VAR_PHYS_OUTPUT, offset) != 0
}

// state reports the PLC run state (the engine stops itself on a runaway).
func (l *ladderRT) state() int { return int(l.rt.state) }

func (l *ladderRT) setState(s int) { l.rt.state = C.int(s) }

// --- Block parameter access (the RT structures hold milliseconds) ---

func (l *ladderRT) setTimer(idx, baseMs, presetMs int) {
	rtTimers(l.rt)[idx].base = C.int(baseMs)
	rtTimers(l.rt)[idx].preset = C.int(presetMs)
}

func (l *ladderRT) timerValue(idx int) int { return int(rtTimers(l.rt)[idx].value) }

func (l *ladderRT) timerDone(idx int) bool { return rtTimers(l.rt)[idx].output_done != 0 }

func (l *ladderRT) setTimerRaw(idx, value int, done bool) {
	rtTimers(l.rt)[idx].value = C.int(value)
	if done {
		rtTimers(l.rt)[idx].output_done = 1
	} else {
		rtTimers(l.rt)[idx].output_done = 0
	}
}

func (l *ladderRT) setMonostable(idx, baseMs, presetMs int) {
	rtMonostables(l.rt)[idx].base = C.int(baseMs)
	rtMonostables(l.rt)[idx].preset = C.int(presetMs)
}

func (l *ladderRT) setCounterPreset(idx, preset int) {
	rtCounters(l.rt)[idx].preset = C.int(preset)
}

func (l *ladderRT) setCounterRaw(idx, value int, done bool) {
	rtCounters(l.rt)[idx].value = C.int(value)
	if done {
		rtCounters(l.rt)[idx].output_done = 1
	} else {
		rtCounters(l.rt)[idx].output_done = 0
	}
}

func (l *ladderRT) counterValue(idx int) int { return int(rtCounters(l.rt)[idx].value) }

func (l *ladderRT) counterDone(idx int) bool { return rtCounters(l.rt)[idx].output_done != 0 }

func (l *ladderRT) setTimerIEC(idx, baseMs, preset, mode int) {
	rtTimersIec(l.rt)[idx].base = C.int(baseMs)
	rtTimersIec(l.rt)[idx].preset = C.int(preset)
	rtTimersIec(l.rt)[idx].timer_mode = C.char(mode)
}

func (l *ladderRT) setTimerIECRaw(idx, value int, output bool) {
	rtTimersIec(l.rt)[idx].value = C.int(value)
	if output {
		rtTimersIec(l.rt)[idx].output = 1
	} else {
		rtTimersIec(l.rt)[idx].output = 0
	}
}

func (l *ladderRT) timerIECValue(idx int) int { return int(rtTimersIec(l.rt)[idx].value) }

func (l *ladderRT) timerIECOutput(idx int) bool { return rtTimersIec(l.rt)[idx].output != 0 }

func (l *ladderRT) timerPresetMs(idx int) int { return int(rtTimers(l.rt)[idx].preset) }

// compileInto compiles an expression and installs it at the given index.
// kind is 0 for a compare and 1 for an operate expression.
func (l *ladderRT) compileInto(idx int, expr string, kind int) error {
	ce, err := compileExpression(expr, kind)
	if err != nil {
		return err
	}
	rtCompiledExprs(l.rt)[idx] = ce
	return nil
}
