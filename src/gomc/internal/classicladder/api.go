// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

// API handler implementations — implements classicladderapi.ClassicladderCallbacks
// and ClassicladderWatchCallbacks.

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#include "classicladder_rt.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	api "github.com/sittner/linuxcnc/src/gomc/generated/gmi/classicladder"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// registerAPI registers REST + WebSocket watch endpoints using generated dispatch code.
func (m *classicladder) registerAPI(reg *apiserver.Registry, instance string) {
	apiserver.RegisterMeta(api.ClassicladderMeta)
	if err := api.RegisterClassicladderAPI(reg, instance, m); err != nil {
		m.logger.Error("register REST API failed", "err", err)
	}
}

func (m *classicladder) registerWatch(wreg *apiserver.WatchRegistry, instance string) {
	api.RegisterClassicladderWatch(wreg, instance, m, api.ClassicladderCommands(m))
}

// --- ClassicladderCallbacks implementation ---

func (m *classicladder) GetStatus() (*api.Status, error) {
	return m.buildStatus(), nil
}

func (m *classicladder) SetState(state api.LadderState) (int32, error) {
	if state == api.LadderState_RUN {
		// A start is a cold start: timers, counters and edge history are reset
		// before the first scan (2.9 PrepareAllDatasBeforeRun). Only on an
		// actual transition, though — resetting while the RT function is
		// already scanning would race it over data it reads unlocked. Because
		// the reset happens before RUN is published with a release store, the
		// scan cannot observe half-reset data.
		if m.getState() == C.CL_STATE_RUN {
			return 0, nil
		}
		m.mu.Lock()
		C.cl_prepare_all_datas_before_run(m.rt)
		m.mu.Unlock()
		m.setState(int(state))
		m.modbus.start()
	} else {
		m.setState(int(state))
		m.modbus.stop()
	}
	return 0, nil
}

func (m *classicladder) GetProgram() (*api.Program, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prog := m.buildProgram()
	return &prog, nil
}

func (m *classicladder) SetProgram(program api.Program) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Compile before applying anything. The realtime engine evaluates bytecode,
	// so an expression stored without being recompiled leaves the scan running
	// the previous program while the API reports the new one; and a program
	// applied before it is known to compile cannot be un-applied. Both are
	// avoided by making the compile a precondition.
	code, err := compileExprList(exprTexts(program.ArithmExprs))
	if err != nil {
		return -1, err
	}
	// Same reasoning for the rungs: check the whole upload before any of it
	// lands, so a refused program leaves the running one untouched.
	for i := range program.Rungs {
		if !program.Rungs[i].Used {
			continue
		}
		if err := m.validateRung(i, &program.Rungs[i]); err != nil {
			return -1, err
		}
	}
	m.applyProgram(&program)
	m.installExprCode(code)
	m.bumpGeneration()
	return 0, nil
}

// exprTexts pulls the expression strings out of the wire type.
func exprTexts(exprs []api.ArithmExpr) []string {
	out := make([]string, len(exprs))
	for i, e := range exprs {
		out[i] = e.Expr
	}
	return out
}

func (m *classicladder) GetRung(index int32) (*api.Rung, error) {
	if index < 0 || index >= int32(m.rt.sizes.nbr_rungs) {
		return nil, errInvalidIndex
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := m.rungToAPI(int(index))
	return &r, nil
}

func (m *classicladder) SetRung(index int32, rung api.Rung) (int32, error) {
	if index < 0 || index >= int32(m.rt.sizes.nbr_rungs) {
		return -1, errInvalidIndex
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Validate before applying: a half-written rung cannot be taken back, and
	// the scan may read it before the caller sees the error.
	if err := m.validateRung(int(index), &rung); err != nil {
		return -1, err
	}
	m.applyRung(int(index), &rung)
	m.bumpGeneration()
	return 0, nil
}

// InsertRung adds an empty rung after the given one and returns its index.
func (m *classicladder) InsertRung(afterIndex int32) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx, err := m.insertRungAfter(int(afterIndex))
	if err != nil {
		return -1, err
	}
	m.bumpGeneration()
	return int32(idx), nil
}

// DeleteRung unlinks a rung from its section and frees it.
func (m *classicladder) DeleteRung(index int32) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.deleteRung(int(index)); err != nil {
		return -1, err
	}
	m.bumpGeneration()
	return 0, nil
}

// AddSection creates a section and returns its index.
func (m *classicladder) AddSection(name string, language api.SectionLanguage, subRoutineNumber int32) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx, err := m.addSection(name, int(language), int(subRoutineNumber))
	if err != nil {
		return -1, err
	}
	m.bumpGeneration()
	return int32(idx), nil
}

// DeleteSection frees a section and the rungs it holds.
func (m *classicladder) DeleteSection(index int32) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.deleteSection(int(index)); err != nil {
		return -1, err
	}
	m.bumpGeneration()
	return 0, nil
}

func (m *classicladder) GetSection(index int32) (*api.Section, error) {
	if index < 0 || index >= int32(m.rt.sizes.nbr_sections) {
		return nil, errInvalidIndex
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sectionToAPI(int(index))
	return &s, nil
}

func (m *classicladder) SetSection(index int32, section api.Section) (int32, error) {
	if index < 0 || index >= int32(m.rt.sizes.nbr_sections) {
		return -1, errInvalidIndex
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// insert_rung and delete_rung exist so a client never has to write these
	// links, but this call can still reach them. A chain that never arrives at
	// lastRung is a scan that only stops when the runaway guard trips.
	if err := m.validateSectionChain(int(index), &section); err != nil {
		return -1, err
	}
	m.applySection(int(index), &section)
	m.bumpGeneration()
	return 0, nil
}

func (m *classicladder) GetVariables() (*api.Variables, error) {
	vars := m.buildVariables()
	return &vars, nil
}

// GetRungStates returns where the power is in every used rung.
func (m *classicladder) GetRungStates() ([]api.RungState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]api.RungState, 0, int(m.rt.sizes.nbr_rungs))
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		if rtRungs(m.rt)[i].used == 0 {
			continue
		}
		states = append(states, api.RungState{Rung: int32(i), Cells: m.rungStateCells(i)})
	}
	return states, nil
}

// rungStateCells packs one rung's dynamic state into the wire form.
//
// These fields are written by the RT scan without the lock, as the variable
// values are: what comes back is a snapshot of a running machine, which is the
// point of it. Nothing structural is read here — the caller holds the lock for
// that — so a scan landing mid-read can only mix cells from two consecutive
// scans, 100ms of animation apart.
func (m *classicladder) rungStateCells(idx int) []int32 {
	cr := &rtRungs(m.rt)[idx]
	cells := make([]int32, C.CL_RUNG_WIDTH*C.CL_RUNG_HEIGHT)
	for x := 0; x < C.CL_RUNG_WIDTH; x++ {
		for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
			e := &cr.elements[x][y]
			var v int32
			if e.dynamic_state != 0 {
				v |= api.CELL_STATE
			}
			if e.dynamic_input != 0 {
				v |= api.CELL_INPUT
			}
			if e.dynamic_output != 0 {
				v |= api.CELL_OUTPUT
			}
			cells[y*C.CL_RUNG_WIDTH+x] = v
		}
	}
	return cells
}

// --- Block parameters ---

// blockIndex bounds-checks the block number a setter was given.
func blockIndex(kind string, index int32, count C.int) (int, error) {
	if index < 0 || index >= int32(count) {
		return 0, fmt.Errorf("%w: %s %d does not exist; %d are configured",
			syscall.EINVAL, kind, index, int(count))
	}
	return int(index), nil
}

// checkBase refuses a time base this implementation does not know.
//
// The file parser is lenient and falls back to seconds, because a project that
// half-loads is more use than one that does not load at all. An API write is
// the opposite case: silently turning an unknown base into seconds would give
// the operator a timer that runs for a length they did not ask for.
func checkBase(base int32) (int, error) {
	switch base {
	case api.BASE_MINS, api.BASE_SECS, api.BASE_100MS:
		return baseMsFromID(int(base)), nil
	}
	return 0, fmt.Errorf("%w: time base %d is not one of BASE_MINS(%d), BASE_SECS(%d), BASE_100MS(%d)",
		syscall.EINVAL, base, api.BASE_MINS, api.BASE_SECS, api.BASE_100MS)
}

// SetTimer sets an old-style timer's preset and time base.
//
// The engine counts these in milliseconds while the API and the .clp file
// count them in base units, so the preset is scaled on the way in — the same
// conversion set_program does.
func (m *classicladder) SetTimer(index int32, preset int32, base int32) (int32, error) {
	i, err := blockIndex("timer", index, m.rt.sizes.nbr_timers)
	if err != nil {
		return -1, err
	}
	baseMs, err := checkBase(base)
	if err != nil {
		return -1, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rtTimers(m.rt)[i].base = C.int(baseMs)
	rtTimers(m.rt)[i].preset = C.int(int(preset) * baseMs)
	m.bumpGeneration()
	return 0, nil
}

// SetMonostable sets a monostable's pulse length and time base.
func (m *classicladder) SetMonostable(index int32, preset int32, base int32) (int32, error) {
	i, err := blockIndex("monostable", index, m.rt.sizes.nbr_monostables)
	if err != nil {
		return -1, err
	}
	baseMs, err := checkBase(base)
	if err != nil {
		return -1, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rtMonostables(m.rt)[i].base = C.int(baseMs)
	rtMonostables(m.rt)[i].preset = C.int(int(preset) * baseMs)
	m.bumpGeneration()
	return 0, nil
}

// SetCounter sets a counter's preset. Counters have no time base.
func (m *classicladder) SetCounter(index int32, preset int32) (int32, error) {
	i, err := blockIndex("counter", index, m.rt.sizes.nbr_counters)
	if err != nil {
		return -1, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rtCounters(m.rt)[i].preset = C.int(preset)
	m.bumpGeneration()
	return 0, nil
}

// SetTimerIec sets an IEC timer's preset, time base and mode.
//
// Unlike the old-style timer this one counts in base units, so the preset is
// stored as given.
func (m *classicladder) SetTimerIec(index int32, preset int32, base int32, mode api.TimerIECMode) (int32, error) {
	i, err := blockIndex("IEC timer", index, m.rt.sizes.nbr_timers_iec)
	if err != nil {
		return -1, err
	}
	baseMs, err := checkBase(base)
	if err != nil {
		return -1, err
	}
	switch mode {
	case api.TimerIECMode_TON, api.TimerIECMode_TOF, api.TimerIECMode_TP:
	default:
		return -1, fmt.Errorf("%w: IEC timer mode %d is not TON(%d), TOF(%d) or TP(%d)",
			syscall.EINVAL, mode, api.TimerIECMode_TON, api.TimerIECMode_TOF, api.TimerIECMode_TP)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rtTimersIec(m.rt)[i].base = C.int(baseMs)
	rtTimersIec(m.rt)[i].preset = C.int(preset)
	rtTimersIec(m.rt)[i].timer_mode = C.char(mode)
	m.bumpGeneration()
	return 0, nil
}

func (m *classicladder) SetVariable(varType int32, offset int32, value int32) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	C.write_var_ext(m.rt, C.int(varType), C.int(offset), C.int(value))
	return 0, nil
}

// LoadProject loads a .clp project.  The path arrives over REST, so it is
// resolved and contained by the shared rule (internal/pathres) rather than
// opened as given.
//
// The whole-program rewrite is bracketed the way 2.9's classicladder.c
// brackets every load (StopRunIfRunning / RunBackIfStopped): the lock-free
// scan must not walk a program mid-teardown, and a load left in RUN must not
// inherit the old program's timer and edge state — Start() prepares before
// running, and so does this.
func (m *classicladder) LoadProject(path string) (int32, error) {
	path, err := pathres.Resolve(path, pathres.Read)
	if err != nil {
		return -1, fmt.Errorf("classicladder: load_project: %w", err)
	}
	return m.loadProjectResolved(path)
}

// loadProjectResolved is LoadProject after path containment — split out so
// tests can drive the bracket semantics with plain file paths.
func (m *classicladder) loadProjectResolved(path string) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wasRun := m.stopRunIfRunning()
	m.modbus.stop()
	if err := m.loadCLPFile(path); err != nil {
		// loadCLPFile fails only before it touches the program (its stated
		// invariant), so the running project is intact — put it all back.
		m.modbus.start()
		m.runBackIfStopped(wasRun)
		return -1, err
	}
	m.projectFile = path
	C.cl_prepare_all_datas_before_run(m.rt)
	m.bumpGeneration()
	m.modbus.start()
	m.runBackIfStopped(wasRun)
	return 0, nil
}

// SaveProject writes a .clp project.  This is a REST-reachable *write*, so the
// target must resolve inside the allowed roots (internal/pathres).
func (m *classicladder) SaveProject(path string) (int32, error) {
	path, err := pathres.Resolve(path, pathres.Write)
	if err != nil {
		return -1, fmt.Errorf("classicladder: save_project: %w", err)
	}
	// Full lock, not RLock: a successful save-as becomes the current project,
	// like 2.9's save dialog, and Status reports it.
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveCLPFile(path); err != nil {
		return -1, err
	}
	m.projectFile = path
	return 0, nil
}

// GetSequential returns the SFC chart. Steps and transitions come back in full,
// unused slots included: transitions name steps by index, so compacting the
// arrays would break every reference in them. Live activity is not here — it
// reaches clients through Variables, with the rest of the running state.
func (m *classicladder) GetSequential() (*api.Sequential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seq := api.Sequential{
		Steps:       make([]api.Step, C.CL_MAX_STEPS),
		Transitions: make([]api.Transition, C.CL_MAX_TRANSITIONS),
		Comments:    make([]api.SeqComment, C.CL_MAX_SEQ_COMMENTS),
	}

	for i := range seq.Steps {
		s := &m.rt.steps[i]
		seq.Steps[i] = api.Step{
			Used:       s.num_page >= 0,
			InitStep:   s.init_step != 0,
			StepNumber: int32(s.step_number),
			Page:       int32(s.num_page),
			PosiX:      int32(s.posi_x),
			PosiY:      int32(s.posi_y),
		}
	}

	for i := range seq.Transitions {
		tr := &m.rt.transitions[i]
		t := api.Transition{
			Used:         tr.num_page >= 0,
			VarTypeCondi: int32(tr.var_type_condi),
			VarNumCondi:  int32(tr.var_num_condi),
			Page:         int32(tr.num_page),
			PosiX:        int32(tr.posi_x),
			PosiY:        int32(tr.posi_y),
		}
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			t.StepsToActivate[j] = int32(tr.num_step_to_activ[j])
			t.StepsToDeactivate[j] = int32(tr.num_step_to_desactiv[j])
			t.TransLinkedForStart[j] = int32(tr.num_trans_linked_for_start[j])
			t.TransLinkedForEnd[j] = int32(tr.num_trans_linked_for_end[j])
		}
		seq.Transitions[i] = t
	}

	for i := range seq.Comments {
		c := &m.rt.seq_comments[i]
		seq.Comments[i] = api.SeqComment{
			Used:    c.num_page >= 0,
			Page:    int32(c.num_page),
			PosiX:   int32(c.posi_x),
			PosiY:   int32(c.posi_y),
			Comment: C.GoString(&c.comment[0]),
		}
	}

	return &seq, nil
}

// SetSequential replaces the SFC chart. The whole chart arrives at once because
// one edit can rewrite the links of several elements — see the API notes — and
// it is checked before any of it lands, so a refused chart leaves the running
// one untouched.
func (m *classicladder) SetSequential(seq api.Sequential) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateSequential(&seq); err != nil {
		return -1, err
	}
	m.applySequential(&seq)
	m.bumpGeneration()
	return 0, nil
}

func (m *classicladder) GetSymbols() ([]api.Symbol, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	symbols := make([]api.Symbol, 0)
	for i := 0; i < int(m.rt.sizes.nbr_symbols); i++ {
		s := &rtSymbols(m.rt)[i]
		vn := C.GoString(&s.var_name[0])
		if vn == "" {
			continue
		}
		symbols = append(symbols, api.Symbol{
			VarName: vn,
			Symbol:  C.GoString(&s.symbol[0]),
			Comment: C.GoString(&s.comment[0]),
		})
	}
	return symbols, nil
}

func (m *classicladder) SetSymbols(symbols []api.Symbol) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Clear existing
	for i := 0; i < int(m.rt.sizes.nbr_symbols); i++ {
		rtSymbols(m.rt)[i].var_name[0] = 0
	}
	for i, sym := range symbols {
		if i >= int(m.rt.sizes.nbr_symbols) {
			break
		}
		s := &rtSymbols(m.rt)[i]
		copyStringToC(&s.var_name[0], sym.VarName, C.CL_LGT_VAR_NAME)
		copyStringToC(&s.symbol[0], sym.Symbol, C.CL_LGT_SYMBOL_STRING)
		copyStringToC(&s.comment[0], sym.Comment, C.CL_LGT_SYMBOL_COMMENT)
	}
	m.bumpGeneration()
	return 0, nil
}

func (m *classicladder) GetExpressions() ([]api.ArithmExpr, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exprs := make([]api.ArithmExpr, int(m.rt.sizes.nbr_arithm_expr))
	for i := 0; i < int(m.rt.sizes.nbr_arithm_expr); i++ {
		exprs[i] = api.ArithmExpr{
			Expr: C.GoString(&rtArithmExprs(m.rt)[i].expr[0]),
		}
	}
	return exprs, nil
}

func (m *classicladder) SetExpressions(exprs []api.ArithmExpr) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Compile first: the scan evaluates the bytecode, not the string.
	code, err := compileExprList(exprTexts(exprs))
	if err != nil {
		return -1, err
	}
	for i, expr := range exprs {
		if i >= int(m.rt.sizes.nbr_arithm_expr) {
			break
		}
		copyStringToC(&rtArithmExprs(m.rt)[i].expr[0], expr.Expr, C.CL_ARITHM_EXPR_SIZE)
	}
	m.installExprCode(code)
	m.bumpGeneration()
	return 0, nil
}

// --- ClassicladderWatchCallbacks implementation ---

func (m *classicladder) WatchStatus() (*api.Status, error) {
	return m.buildStatus(), nil
}

func (m *classicladder) WatchVariables() (*api.Variables, error) {
	vars := m.buildVariables()
	return &vars, nil
}

// WatchRungStates pushes the live cell state, keyed by rung index so the push
// loop can send only the rungs that moved.
func (m *classicladder) WatchRungStates() (map[string]api.RungState, error) {
	states, err := m.GetRungStates()
	if err != nil {
		return nil, err
	}
	out := make(map[string]api.RungState, len(states))
	for _, s := range states {
		out[strconv.Itoa(int(s.Rung))] = s
	}
	return out, nil
}

// --- Data conversion helpers ---

func (m *classicladder) buildStatus() *api.Status {
	// RLock for projectFile: LoadProject/SaveProject reassign the string, and
	// a torn Go string header is a crash, not a glitch.
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt := m.rt
	return &api.Status{
		State:             api.LadderState(m.getState()),
		ScanTimeUs:        int32(m.getScanTimeNs() / 1000),
		PeriodicRefreshMs: int32(rt.periodic_refresh_ms),
		Sizes:             m.buildSizes(),
		ProjectFile:       m.projectFile,
	}
}

func (m *classicladder) buildSizes() api.SizeInfo {
	s := m.rt.sizes
	return api.SizeInfo{
		NbrRungs:       int32(s.nbr_rungs),
		NbrBits:        int32(s.nbr_bits),
		NbrWords:       int32(s.nbr_words),
		NbrTimers:      int32(s.nbr_timers),
		NbrMonostables: int32(s.nbr_monostables),
		NbrCounters:    int32(s.nbr_counters),
		NbrTimersIec:   int32(s.nbr_timers_iec),
		NbrPhysInputs:  int32(s.nbr_phys_inputs),
		NbrPhysOutputs: int32(s.nbr_phys_outputs),
		NbrArithmExpr:  int32(s.nbr_arithm_expr),
		NbrSections:    int32(s.nbr_sections),
		NbrSymbols:     int32(s.nbr_symbols),
		NbrS32in:       int32(s.nbr_s32_in),
		NbrS32out:      int32(s.nbr_s32_out),
		NbrFloatIn:     int32(s.nbr_float_in),
		NbrFloatOut:    int32(s.nbr_float_out),
		NbrErrorBits:   int32(s.nbr_error_bits),
	}
}

func (m *classicladder) rungToAPI(idx int) api.Rung {
	cr := &rtRungs(m.rt)[idx]
	r := api.Rung{
		Used:     cr.used != 0,
		PrevRung: int32(cr.prev_rung),
		NextRung: int32(cr.next_rung),
		Label:    C.GoString(&cr.label[0]),
		Comment:  C.GoString(&cr.comment[0]),
	}
	for x := 0; x < C.CL_RUNG_WIDTH; x++ {
		for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
			e := &cr.elements[x][y]
			r.Elements[y*C.CL_RUNG_WIDTH+x] = api.Element{
				Type:             int32(e._type),
				ConnectedWithTop: int32(e.connected_with_top),
				VarType:          int32(e.var_type),
				VarNum:           int32(e.var_num),
			}
		}
	}
	return r
}

func (m *classicladder) sectionToAPI(idx int) api.Section {
	s := &rtSections(m.rt)[idx]
	return api.Section{
		Used:             s.used != 0,
		Name:             C.GoString(&s.name[0]),
		Language:         api.SectionLanguage(s.language),
		SubRoutineNumber: int32(s.sub_routine_number),
		FirstRung:        int32(s.first_rung),
		LastRung:         int32(s.last_rung),
		SequentialPage:   int32(s.sequential_page),
	}
}

func (m *classicladder) applyRung(idx int, r *api.Rung) {
	cr := &rtRungs(m.rt)[idx]
	if r.Used {
		cr.used = 1
	} else {
		cr.used = 0
	}
	cr.prev_rung = C.int(r.PrevRung)
	cr.next_rung = C.int(r.NextRung)
	copyStringToC(&cr.label[0], r.Label, C.CL_LGT_LABEL)
	copyStringToC(&cr.comment[0], r.Comment, C.CL_LGT_COMMENT)
	for x := 0; x < C.CL_RUNG_WIDTH; x++ {
		for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
			i := y*C.CL_RUNG_WIDTH + x
			e := r.Elements[i]
			cr.elements[x][y]._type = C.int16_t(e.Type)
			cr.elements[x][y].connected_with_top = C.int8_t(e.ConnectedWithTop)
			cr.elements[x][y].var_type = C.int32_t(e.VarType)
			cr.elements[x][y].var_num = C.int32_t(e.VarNum)
		}
	}
}

func (m *classicladder) applySection(idx int, s *api.Section) {
	sec := &rtSections(m.rt)[idx]
	if s.Used {
		sec.used = 1
	} else {
		sec.used = 0
	}
	copyStringToC(&sec.name[0], s.Name, C.CL_LGT_SECTION_NAME)
	sec.language = C.int(s.Language)
	sec.sub_routine_number = C.int(s.SubRoutineNumber)
	sec.first_rung = C.int(s.FirstRung)
	sec.last_rung = C.int(s.LastRung)
	sec.sequential_page = C.int(s.SequentialPage)
}

func (m *classicladder) applyProgram(prog *api.Program) {
	rt := m.rt

	for i, r := range prog.Rungs {
		if i >= int(rt.sizes.nbr_rungs) {
			break
		}
		m.applyRung(i, &r)
	}

	for i, s := range prog.Sections {
		if i >= int(rt.sizes.nbr_sections) {
			break
		}
		m.applySection(i, &s)
	}

	for i, sym := range prog.Symbols {
		if i >= int(rt.sizes.nbr_symbols) {
			break
		}
		s := &rtSymbols(rt)[i]
		copyStringToC(&s.var_name[0], sym.VarName, C.CL_LGT_VAR_NAME)
		copyStringToC(&s.symbol[0], sym.Symbol, C.CL_LGT_SYMBOL_STRING)
		copyStringToC(&s.comment[0], sym.Comment, C.CL_LGT_SYMBOL_COMMENT)
	}

	for i, expr := range prog.ArithmExprs {
		if i >= int(rt.sizes.nbr_arithm_expr) {
			break
		}
		copyStringToC(&rtArithmExprs(rt)[i].expr[0], expr.Expr, C.CL_ARITHM_EXPR_SIZE)
	}

	// Base travels over the API as an id (0=mins, 1=secs, 2=100ms), as it
	// does in the .clp file; the RT structures hold milliseconds. Old timers
	// and monostables additionally count in milliseconds, so their preset is
	// scaled by the base — IEC timers count in base units and are not.
	for i, t := range prog.TimersIec {
		if i >= int(rt.sizes.nbr_timers_iec) {
			break
		}
		rtTimersIec(rt)[i].preset = C.int(t.Preset)
		rtTimersIec(rt)[i].base = C.int(baseMsFromID(int(t.Base)))
		rtTimersIec(rt)[i].timer_mode = C.char(t.Mode)
	}

	for i, t := range prog.Timers {
		if i >= int(rt.sizes.nbr_timers) {
			break
		}
		base := baseMsFromID(int(t.Base))
		rtTimers(rt)[i].base = C.int(base)
		rtTimers(rt)[i].preset = C.int(int(t.Preset) * base)
	}

	for i, mo := range prog.Monostables {
		if i >= int(rt.sizes.nbr_monostables) {
			break
		}
		base := baseMsFromID(int(mo.Base))
		rtMonostables(rt)[i].base = C.int(base)
		rtMonostables(rt)[i].preset = C.int(int(mo.Preset) * base)
	}

	for i, c := range prog.Counters {
		if i >= int(rt.sizes.nbr_counters) {
			break
		}
		rtCounters(rt)[i].preset = C.int(c.Preset)
	}
}

func (m *classicladder) buildProgram() api.Program {
	rt := m.rt
	prog := api.Program{
		Sizes: m.buildSizes(),
	}

	prog.Rungs = make([]api.Rung, int(rt.sizes.nbr_rungs))
	for i := 0; i < int(rt.sizes.nbr_rungs); i++ {
		prog.Rungs[i] = m.rungToAPI(i)
	}

	prog.Sections = make([]api.Section, int(rt.sizes.nbr_sections))
	for i := 0; i < int(rt.sizes.nbr_sections); i++ {
		prog.Sections[i] = m.sectionToAPI(i)
	}

	prog.Symbols = make([]api.Symbol, int(rt.sizes.nbr_symbols))
	for i := 0; i < int(rt.sizes.nbr_symbols); i++ {
		s := &rtSymbols(rt)[i]
		prog.Symbols[i] = api.Symbol{
			VarName: C.GoString(&s.var_name[0]),
			Symbol:  C.GoString(&s.symbol[0]),
			Comment: C.GoString(&s.comment[0]),
		}
	}

	prog.ArithmExprs = make([]api.ArithmExpr, int(rt.sizes.nbr_arithm_expr))
	for i := 0; i < int(rt.sizes.nbr_arithm_expr); i++ {
		prog.ArithmExprs[i] = api.ArithmExpr{
			Expr: C.GoString(&rtArithmExprs(rt)[i].expr[0]),
		}
	}

	prog.TimersIec = make([]api.TimerIEC, int(rt.sizes.nbr_timers_iec))
	for i := 0; i < int(rt.sizes.nbr_timers_iec); i++ {
		t := &rtTimersIec(rt)[i]
		prog.TimersIec[i] = api.TimerIEC{
			Preset: int32(t.preset),
			Value:  int32(t.value),
			Base:   int32(baseIDFromMs(int(t.base))),
			Mode:   api.TimerIECMode(t.timer_mode),
			Input:  t.input != 0,
			Output: t.output != 0,
		}
	}

	prog.Timers = make([]api.Timer, int(rt.sizes.nbr_timers))
	for i := 0; i < int(rt.sizes.nbr_timers); i++ {
		t := &rtTimers(rt)[i]
		base := int(t.base)
		if base <= 0 {
			base = C.CL_TIME_BASE_SECS
		}
		prog.Timers[i] = api.Timer{
			Preset:        int32(int(t.preset) / base),
			Value:         int32(int(t.value) / base),
			Base:          int32(baseIDFromMs(base)),
			InputEnable:   t.input_enable != 0,
			InputControl:  t.input_control != 0,
			OutputDone:    t.output_done != 0,
			OutputRunning: t.output_running != 0,
		}
	}

	prog.Monostables = make([]api.Monostable, int(rt.sizes.nbr_monostables))
	for i := 0; i < int(rt.sizes.nbr_monostables); i++ {
		mo := &rtMonostables(rt)[i]
		base := int(mo.base)
		if base <= 0 {
			base = C.CL_TIME_BASE_SECS
		}
		prog.Monostables[i] = api.Monostable{
			Preset:        int32(int(mo.preset) / base),
			Value:         int32(int(mo.value) / base),
			Base:          int32(baseIDFromMs(base)),
			Input:         mo.input != 0,
			OutputRunning: mo.output_running != 0,
		}
	}

	prog.Counters = make([]api.Counter, int(rt.sizes.nbr_counters))
	for i := 0; i < int(rt.sizes.nbr_counters); i++ {
		c := &rtCounters(rt)[i]
		prog.Counters[i] = api.Counter{
			Preset:         int32(c.preset),
			Value:          int32(c.value),
			InputReset:     c.input_reset != 0,
			InputPreset:    c.input_preset != 0,
			InputCountUp:   c.input_count_up != 0,
			InputCountDown: c.input_count_down != 0,
			OutputDone:     c.output_done != 0,
			OutputEmpty:    c.output_empty != 0,
			OutputFull:     c.output_full != 0,
		}
	}

	return prog
}

func (m *classicladder) buildVariables() api.Variables {
	rt := m.rt
	nbits := int(rt.sizes.nbr_bits)
	nin := int(rt.sizes.nbr_phys_inputs)
	nout := int(rt.sizes.nbr_phys_outputs)
	nerr := int(rt.sizes.nbr_error_bits)

	var vars api.Variables
	vars.Bools.Bits = make([]bool, nbits)
	for i := 0; i < nbits; i++ {
		vars.Bools.Bits[i] = rtVarBits(rt)[i] != 0
	}
	vars.Bools.PhysInputs = make([]bool, nin)
	for i := 0; i < nin; i++ {
		vars.Bools.PhysInputs[i] = rtVarBits(rt)[nbits+i] != 0
	}
	vars.Bools.PhysOutputs = make([]bool, nout)
	for i := 0; i < nout; i++ {
		vars.Bools.PhysOutputs[i] = rtVarBits(rt)[nbits+nin+i] != 0
	}
	vars.Bools.ErrorBits = make([]bool, nerr)
	for i := 0; i < nerr; i++ {
		vars.Bools.ErrorBits[i] = rtVarBits(rt)[nbits+nin+nout+C.CL_MAX_STEPS+i] != 0
	}
	// Step activity is what makes an SFC chart legible while it runs: which
	// state the machine is in. Indexed by step number (the n in %Xn), which is
	// the author's numbering, not the step's slot.
	vars.Bools.StepActivity = make([]bool, C.CL_MAX_STEPS)
	for i := 0; i < C.CL_MAX_STEPS; i++ {
		vars.Bools.StepActivity[i] = rtVarBits(rt)[nbits+nin+nout+i] != 0
	}

	// Block outputs and values. They are read off the block structures rather
	// than the bit array because that is where the engine keeps them — the
	// same place read_var goes for %T0.D or %C0.V.
	ntimers := int(rt.sizes.nbr_timers)
	nmono := int(rt.sizes.nbr_monostables)
	ncount := int(rt.sizes.nbr_counters)
	niec := int(rt.sizes.nbr_timers_iec)

	vars.Bools.TimerDone = make([]bool, ntimers)
	vars.Bools.TimerRunning = make([]bool, ntimers)
	for i := 0; i < ntimers; i++ {
		vars.Bools.TimerDone[i] = rtTimers(rt)[i].output_done != 0
		vars.Bools.TimerRunning[i] = rtTimers(rt)[i].output_running != 0
	}
	vars.Bools.MonostableRunning = make([]bool, nmono)
	for i := 0; i < nmono; i++ {
		vars.Bools.MonostableRunning[i] = rtMonostables(rt)[i].output_running != 0
	}
	vars.Bools.CounterDone = make([]bool, ncount)
	vars.Bools.CounterEmpty = make([]bool, ncount)
	vars.Bools.CounterFull = make([]bool, ncount)
	for i := 0; i < ncount; i++ {
		vars.Bools.CounterDone[i] = rtCounters(rt)[i].output_done != 0
		vars.Bools.CounterEmpty[i] = rtCounters(rt)[i].output_empty != 0
		vars.Bools.CounterFull[i] = rtCounters(rt)[i].output_full != 0
	}
	vars.Bools.TimerIecDone = make([]bool, niec)
	for i := 0; i < niec; i++ {
		vars.Bools.TimerIecDone[i] = rtTimersIec(rt)[i].output != 0
	}

	nwords := int(rt.sizes.nbr_words)
	ns32in := int(rt.sizes.nbr_s32_in)
	ns32out := int(rt.sizes.nbr_s32_out)
	vars.Words.Words = make([]int32, nwords)
	for i := 0; i < nwords; i++ {
		vars.Words.Words[i] = int32(rtVarWords(rt)[i])
	}
	vars.Words.PhysWordInputs = make([]int32, ns32in)
	for i := 0; i < ns32in; i++ {
		vars.Words.PhysWordInputs[i] = int32(rtVarWords(rt)[nwords+i])
	}
	vars.Words.PhysWordOutputs = make([]int32, ns32out)
	for i := 0; i < ns32out; i++ {
		vars.Words.PhysWordOutputs[i] = int32(rtVarWords(rt)[nwords+ns32in+i])
	}
	vars.Words.StepTimes = make([]int32, C.CL_MAX_STEPS)
	for i := 0; i < C.CL_MAX_STEPS; i++ {
		vars.Words.StepTimes[i] = int32(rtVarWords(rt)[nwords+ns32in+ns32out+i])
	}

	// Timer and monostable values go over the wire in units of their base, the
	// way their presets do and the way read_var reports %T0.V. The engine counts
	// them in milliseconds; that stays inside it.
	vars.Words.TimerValues = make([]int32, ntimers)
	for i := 0; i < ntimers; i++ {
		vars.Words.TimerValues[i] = int32(C.read_var_ext(rt, C.CL_VAR_TIMER_VALUE, C.int(i)))
	}
	vars.Words.MonostableValues = make([]int32, nmono)
	for i := 0; i < nmono; i++ {
		vars.Words.MonostableValues[i] = int32(C.read_var_ext(rt, C.CL_VAR_MONOSTABLE_VALUE, C.int(i)))
	}
	vars.Words.CounterValues = make([]int32, ncount)
	for i := 0; i < ncount; i++ {
		vars.Words.CounterValues[i] = int32(rtCounters(rt)[i].value)
	}
	vars.Words.TimerIecValues = make([]int32, niec)
	for i := 0; i < niec; i++ {
		vars.Words.TimerIecValues[i] = int32(rtTimersIec(rt)[i].value)
	}

	nfin := int(rt.sizes.nbr_float_in)
	nfout := int(rt.sizes.nbr_float_out)
	vars.Floats.PhysFloatInputs = make([]float64, nfin)
	for i := 0; i < nfin; i++ {
		vars.Floats.PhysFloatInputs[i] = float64(rtVarFloats(rt)[i])
	}
	vars.Floats.PhysFloatOutputs = make([]float64, nfout)
	for i := 0; i < nfout; i++ {
		vars.Floats.PhysFloatOutputs[i] = float64(rtVarFloats(rt)[nfin+i])
	}

	return vars
}

// --- Utility functions ---

func copyStringToC(dst *C.char, src string, maxLen C.int) {
	cstr := C.CString(src)
	C.strncpy(dst, cstr, C.size_t(maxLen-1))
	p := (*C.char)(unsafe.Pointer(uintptr(unsafe.Pointer(dst)) + uintptr(maxLen-1)))
	*p = 0
	C.free(unsafe.Pointer(cstr))
}

var errInvalidIndex = fmt.Errorf("invalid index")

// --- Modbus API handlers ---

func (m *classicladder) GetModbusComParams() (*api.ModbusComParams, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg := m.modbus.cfg
	return &api.ModbusComParams{
		SerialPort:        cfg.SerialPort,
		SerialSpeed:       int32(cfg.SerialSpeed),
		SerialDataBits:    int32(cfg.SerialDataBits),
		SerialStopBits:    int32(cfg.SerialStopBits),
		SerialParity:      int32(cfg.SerialParity),
		SerialUseRts:      cfg.SerialUseRTS,
		ElementOffset:     int32(cfg.ElementOffset),
		TimeInterFrame:    int32(cfg.TimeInterFrame),
		TimeOutReceipt:    int32(cfg.TimeOutReceipt),
		TimeAfterTransmit: int32(cfg.TimeAfterTransmit),
		DebugLevel:        int32(cfg.DebugLevel),
		MapCoilRead:       int32(cfg.MapCoilRead),
		MapCoilWrite:      int32(cfg.MapCoilWrite),
		MapInputs:         int32(cfg.MapInputs),
		MapHolding:        int32(cfg.MapHolding),
		MapRegisterRead:   int32(cfg.MapRegisterRead),
		MapRegisterWrite:  int32(cfg.MapRegisterWrite),
	}, nil
}

func (m *classicladder) SetModbusComParams(params api.ModbusComParams) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wasRunning := m.modbus.running
	if wasRunning {
		m.modbus.stop()
	}
	m.modbus.cfg.SerialPort = params.SerialPort
	m.modbus.cfg.SerialSpeed = int(params.SerialSpeed)
	m.modbus.cfg.SerialDataBits = int(params.SerialDataBits)
	m.modbus.cfg.SerialStopBits = int(params.SerialStopBits)
	m.modbus.cfg.SerialParity = int(params.SerialParity)
	m.modbus.cfg.SerialUseRTS = params.SerialUseRts
	m.modbus.cfg.ElementOffset = int(params.ElementOffset)
	m.modbus.cfg.TimeInterFrame = int(params.TimeInterFrame)
	m.modbus.cfg.TimeOutReceipt = int(params.TimeOutReceipt)
	m.modbus.cfg.TimeAfterTransmit = int(params.TimeAfterTransmit)
	m.modbus.cfg.DebugLevel = int(params.DebugLevel)
	m.modbus.cfg.MapCoilRead = int(params.MapCoilRead)
	m.modbus.cfg.MapCoilWrite = int(params.MapCoilWrite)
	m.modbus.cfg.MapInputs = int(params.MapInputs)
	m.modbus.cfg.MapHolding = int(params.MapHolding)
	m.modbus.cfg.MapRegisterRead = int(params.MapRegisterRead)
	m.modbus.cfg.MapRegisterWrite = int(params.MapRegisterWrite)
	if wasRunning {
		m.modbus.start()
	}
	return 0, nil
}

func (m *classicladder) GetModbusRequests() ([]api.ModbusRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	reqs := make([]api.ModbusRequest, len(m.modbus.cfg.Requests))
	for i, r := range m.modbus.cfg.Requests {
		reqs[i] = api.ModbusRequest{
			SlaveAddr:          r.SlaveAddr,
			TypeReq:            api.ModbusReqType(r.TypeReq),
			FirstModbusElement: int32(r.FirstModbusElement),
			NbrModbusElements:  int32(r.NbrModbusElements),
			LogicInverted:      r.LogicInverted,
			OffsetVarMapped:    int32(r.OffsetVarMapped),
		}
	}
	return reqs, nil
}

func (m *classicladder) SetModbusRequests(requests []api.ModbusRequest) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wasRunning := m.modbus.running
	if wasRunning {
		m.modbus.stop()
	}
	m.modbus.cfg.Requests = make([]modbusRequest, len(requests))
	for i, r := range requests {
		m.modbus.cfg.Requests[i] = modbusRequest{
			SlaveAddr:          r.SlaveAddr,
			TypeReq:            int(r.TypeReq),
			FirstModbusElement: int(r.FirstModbusElement),
			NbrModbusElements:  int(r.NbrModbusElements),
			LogicInverted:      r.LogicInverted,
			OffsetVarMapped:    int(r.OffsetVarMapped),
		}
	}
	if wasRunning {
		m.modbus.start()
	}
	return 0, nil
}

func (m *classicladder) GetModbusStatus() (*api.ModbusStatus, error) {
	m.modbus.mu.Lock()
	defer m.modbus.mu.Unlock()
	return &api.ModbusStatus{
		Running:    m.modbus.running,
		CurrentReq: int32(m.modbus.currentReq),
		FrameCount: int32(m.modbus.frameCount),
		ErrorCount: int32(m.modbus.errorCount),
		SlavePort:  int32(m.slavePort),
	}, nil
}

// GetVarClasses serves the variable naming table, so a client can format and
// parse names locally instead of asking per variable.
func (m *classicladder) GetVarClasses() ([]api.VarClass, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.varClasses(), nil
}

// GetExpressionsText returns the expression table in the form an operator reads.
//
// Converting between the written and stored forms needs the variable naming
// rules, and those are checked against the original implementation here. Serving
// the converted text means a client displays what it is given rather than
// carrying a second parser that can drift from this one.
func (m *classicladder) GetExpressionsText() ([]api.ExprText, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]api.ExprText, int(m.rt.sizes.nbr_arithm_expr))
	for i := range out {
		stored := C.GoString(&rtArithmExprs(m.rt)[i].expr[0])
		if stored != "" {
			out[i].Text = m.exprToNames(stored)
		}
	}
	return out, nil
}

// SetExpressionsText replaces the expression table from written form.
func (m *classicladder) SetExpressionsText(texts []api.ExprText) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Parse the whole table first: one unknown name refuses the batch, the same
	// way one uncompilable expression does, so a rejected edit leaves the
	// running program alone.
	stored := make([]string, len(texts))
	for i, t := range texts {
		if t.Text == "" {
			continue
		}
		s, err := m.namesToExpr(t.Text)
		if err != nil {
			return -1, fmt.Errorf("%w: expr[%d] %q: %w", syscall.EINVAL, i, t.Text, err)
		}
		if len(s) > C.CL_ARITHM_EXPR_SIZE-1 {
			return -1, fmt.Errorf("%w: expr[%d] %q: too long once converted (%d chars)",
				syscall.EINVAL, i, t.Text, len(s))
		}
		stored[i] = s
	}

	code, err := compileExprList(stored)
	if err != nil {
		return -1, err
	}
	for i := 0; i < len(stored) && i < int(m.rt.sizes.nbr_arithm_expr); i++ {
		copyStringToC(&rtArithmExprs(m.rt)[i].expr[0], stored[i], C.CL_ARITHM_EXPR_SIZE)
	}
	m.installExprCode(code)
	m.bumpGeneration()
	return 0, nil
}

// SetExpressionText replaces one expression from written form.
//
// Only this entry is parsed and compiled, and only this entry's code is
// installed. The whole-table call cannot do that — it is given a whole table,
// so it has to stand behind all of it — but it means a project loaded through
// the lenient file path, which keeps an expression it could not compile and
// leaves it inert, would refuse every later edit until that one was fixed.
// Editing a rung's compare should not be blocked by a broken expression three
// rungs away.
func (m *classicladder) SetExpressionText(index int32, text string) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := int32(m.rt.sizes.nbr_arithm_expr)
	if index < 0 || index >= n {
		return -1, fmt.Errorf("%w: expression %d does not exist; %d are configured",
			syscall.EINVAL, index, n)
	}

	if text == "" {
		rtArithmExprs(m.rt)[index].expr[0] = 0
		rtCompiledExprs(m.rt)[index].valid = 0
		rtCompiledExprs(m.rt)[index].len = 0
		m.bumpGeneration()
		return 0, nil
	}

	stored, err := m.namesToExpr(text)
	if err != nil {
		return -1, fmt.Errorf("%w: expr[%d] %q: %w", syscall.EINVAL, index, text, err)
	}
	if len(stored) > C.CL_ARITHM_EXPR_SIZE-1 {
		return -1, fmt.Errorf("%w: expr[%d] %q: too long once converted (%d chars)",
			syscall.EINVAL, index, text, len(stored))
	}
	code, err := compileOne(stored)
	if err != nil {
		return -1, fmt.Errorf("%w: expr[%d] %q: %w", syscall.EINVAL, index, text, err)
	}

	copyStringToC(&rtArithmExprs(m.rt)[index].expr[0], stored, C.CL_ARITHM_EXPR_SIZE)
	rtCompiledExprs(m.rt)[index] = code
	m.bumpGeneration()
	return 0, nil
}
