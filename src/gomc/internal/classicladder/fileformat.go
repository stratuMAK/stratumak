// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

// CLP file format parser/writer for Classic Ladder .clp project files.
//
// Format: a concatenated multi-file archive with marker-delimited sections:
//   _FILES_CLASSICLADDER
//   _FILE-<name>
//   ...content...
//   _/FILE-<name>
//   _/FILES_CLASSICLADDER

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#include "classicladder_rt.h"
#include <stdlib.h>
*/
import "C"

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// loadCLPFile parses a .clp file and applies it to the RT instance.
//
// Invariant the callers rely on: every error return sits BEFORE the wipe
// below, so a failed load leaves the current program untouched. The parsers
// past that point are lenient by design (2.9's were too) — they skip what
// they cannot read instead of erroring out of a half-wiped project.
//
// Callers mutate the whole program in place, so they must hold m.mu AND have
// the scan stopped and settled (stopRunIfRunning) if the PLC was running.
func (m *classicladder) loadCLPFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Split the archive into sub-files
	files, err := splitCLPArchive(f)
	if err != nil {
		return err
	}

	// Parse general.txt for sizes (already configured at init, just validate)
	if content, ok := files["general.txt"]; ok {
		m.parseGeneral(content)
	}

	// Initialize all data before loading
	C.classicladder_rt_init_data(m.rt)

	// Clear all rungs
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		rtRungs(m.rt)[i].used = 0
	}
	// Clear all sections
	for i := 0; i < int(m.rt.sizes.nbr_sections); i++ {
		rtSections(m.rt)[i].used = 0
	}

	// Load sections
	if content, ok := files["sections.csv"]; ok {
		m.parseSections(content)
	}

	// Load rungs
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		name := fmt.Sprintf("rung_%d.csv", i)
		if content, ok := files[name]; ok {
			m.parseRung(i, content)
		}
	}

	// Load timers IEC
	if content, ok := files["timers_iec.csv"]; ok {
		m.parseTimersIEC(content)
	}

	// Load timers
	if content, ok := files["timers.csv"]; ok {
		m.parseTimers(content)
	}

	// Load monostables
	if content, ok := files["monostables.csv"]; ok {
		m.parseMonostables(content)
	}

	// Load counters
	if content, ok := files["counters.csv"]; ok {
		m.parseCounters(content)
	}

	// Load arithmetic expressions
	if content, ok := files["arithmetic_expressions.csv"]; ok {
		m.parseArithmExprs(content)
	}

	// Load symbols
	if content, ok := files["symbols.csv"]; ok {
		m.parseSymbols(content)
	}

	// Load sequential data
	if content, ok := files["sequential.csv"]; ok {
		m.parseSequential(content)
	}

	// Load Modbus configuration
	if content, ok := files["com_params.txt"]; ok {
		m.parseComParams(content)
	}
	if content, ok := files["modbusioconf.csv"]; ok {
		m.parseModbusIOConf(content)
	}

	// Compile all arithmetic expressions to bytecode
	if errs := m.compileAllExpressions(); len(errs) > 0 {
		for _, e := range errs {
			m.logger.Warn("expression compile error", "error", e)
		}
	}

	// '^' changed meaning against 2.9: there its (broken) power operator
	// consumed every caret, here it is XOR and power is spelled '**'. The
	// expression still compiles and runs, so this warning is the only trace
	// an old project gets that its values moved.
	for i := 0; i < int(m.rt.sizes.nbr_arithm_expr); i++ {
		expr := C.GoString(&rtArithmExprs(m.rt)[i].expr[0])
		if strings.Contains(expr, "^") {
			m.logger.Warn("expression uses '^', which is XOR here but was power under 2.9; use '**' for power",
				"index", i, "expr", expr)
		}
	}

	m.logger.Info("loaded CLP project", "path", path)
	return nil
}

// saveCLPFile writes the current RT state to a .clp file.
func (m *classicladder) saveCLPFile(path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		// A failed Close on a file opened for writing means a lost final
		// flush (truncated/corrupt program file); surface it if nothing
		// earlier already failed.
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	w := bufio.NewWriter(f)
	_, _ = fmt.Fprintln(w, "_FILES_CLASSICLADDER")

	// symbols.csv
	m.writeFileSection(w, "symbols.csv", m.emitSymbols())

	// com_params.txt
	m.writeFileSection(w, "com_params.txt", m.emitComParams())

	// modbusioconf.csv
	m.writeFileSection(w, "modbusioconf.csv", m.emitModbusIOConf())

	// timers_iec.csv
	m.writeFileSection(w, "timers_iec.csv", m.emitTimersIEC())

	// timers.csv
	m.writeFileSection(w, "timers.csv", m.emitTimers())

	// counters.csv
	m.writeFileSection(w, "counters.csv", m.emitCounters())

	// monostables.csv
	m.writeFileSection(w, "monostables.csv", m.emitMonostables())

	// sections.csv
	m.writeFileSection(w, "sections.csv", m.emitSections())

	// arithmetic_expressions.csv
	m.writeFileSection(w, "arithmetic_expressions.csv", m.emitArithmExprs())

	// rungs
	for i := 0; i < int(m.rt.sizes.nbr_rungs); i++ {
		if rtRungs(m.rt)[i].used != 0 {
			name := fmt.Sprintf("rung_%d.csv", i)
			m.writeFileSection(w, name, m.emitRung(i))
		}
	}

	// sequential.csv
	m.writeFileSection(w, "sequential.csv", m.emitSequential())

	// general.txt
	m.writeFileSection(w, "general.txt", m.emitGeneral())

	_, _ = fmt.Fprintln(w, "_/FILES_CLASSICLADDER")
	// bufio.Writer errors are sticky; Flush surfaces any write failure above.
	return w.Flush()
}

// --- Archive splitting ---

func splitCLPArchive(f *os.File) (map[string]string, error) {
	files := make(map[string]string)
	scanner := bufio.NewScanner(f)

	var currentFile string
	var content strings.Builder
	inArchive := false

	for scanner.Scan() {
		line := scanner.Text()

		if line == "_FILES_CLASSICLADDER" {
			inArchive = true
			continue
		}
		if line == "_/FILES_CLASSICLADDER" {
			break
		}
		if !inArchive {
			continue
		}

		if strings.HasPrefix(line, "_FILE-") {
			currentFile = line[6:]
			content.Reset()
			continue
		}
		if strings.HasPrefix(line, "_/FILE-") {
			files[currentFile] = content.String()
			currentFile = ""
			continue
		}
		if currentFile != "" {
			if content.Len() > 0 {
				content.WriteByte('\n')
			}
			content.WriteString(line)
		}
	}
	return files, scanner.Err()
}

// --- Section parsers ---

func (m *classicladder) parseGeneral(content string) {
	// Sizes bind at module creation (they decide the allocation and the HAL
	// pin count), so the file's SIZE_NBR_* entries are not applied — but a
	// project that wants more than the module has will load truncated, and
	// that deserves a loud pointer at the load line.
	sizeOf := map[string]C.int{
		"SIZE_NBR_RUNGS":        m.rt.sizes.nbr_rungs,
		"SIZE_NBR_BITS":         m.rt.sizes.nbr_bits,
		"SIZE_NBR_WORDS":        m.rt.sizes.nbr_words,
		"SIZE_NBR_TIMERS":       m.rt.sizes.nbr_timers,
		"SIZE_NBR_MONOSTABLES":  m.rt.sizes.nbr_monostables,
		"SIZE_NBR_COUNTERS":     m.rt.sizes.nbr_counters,
		"SIZE_NBR_TIMERS_IEC":   m.rt.sizes.nbr_timers_iec,
		"SIZE_NBR_PHYS_INPUTS":  m.rt.sizes.nbr_phys_inputs,
		"SIZE_NBR_PHYS_OUTPUTS": m.rt.sizes.nbr_phys_outputs,
		"SIZE_NBR_ARITHM_EXPR":  m.rt.sizes.nbr_arithm_expr,
		"SIZE_NBR_SECTIONS":     m.rt.sizes.nbr_sections,
		"SIZE_NBR_SYMBOLS":      m.rt.sizes.nbr_symbols,
	}
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		v, _ := strconv.Atoi(val)
		switch key {
		case "PERIODIC_REFRESH":
			m.rt.periodic_refresh_ms = C.int(v)
		default:
			if have, ok := sizeOf[key]; ok && C.int(v) > have {
				m.logger.Warn("project wants more than the module was configured with; loading truncated",
					"key", key, "project", v, "configured", int(have))
			}
		}
	}
}

func (m *classicladder) parseSections(content string) {
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			continue
		}
		// The section index is the first field, not the line position: a
		// project may leave earlier section slots unused.
		idx := atoi(parts[0])
		if idx < 0 || idx >= int(m.rt.sizes.nbr_sections) {
			continue
		}
		sec := &rtSections(m.rt)[idx]
		sec.language = C.int(atoi(parts[1]))
		sec.sub_routine_number = C.int(atoi(parts[2]))
		sec.first_rung = C.int(atoi(parts[3]))
		sec.last_rung = C.int(atoi(parts[4]))
		if len(parts) > 5 {
			sec.sequential_page = C.int(atoi(parts[5]))
		}
		// Published last, mirroring the module's publish-order contract.
		sec.used = 1
	}
	// Parse section names from #NAMEnnn= comments
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "#NAME") {
			// #NAME000=Prog1 — but stay lenient about the shape: a truncated
			// or hand-edited line must not take the loader down with it
			// (line[5:8] here used to panic on anything shorter than 8 bytes).
			nameStart := strings.Index(line, "=")
			if nameStart <= 5 {
				continue
			}
			num := atoi(line[5:nameStart])
			if num >= 0 && num < int(m.rt.sizes.nbr_sections) {
				name := line[nameStart+1:]
				copyGoStringToC(&rtSections(m.rt)[num].name[0], name, C.CL_LGT_SECTION_NAME)
			}
		}
	}
}

func (m *classicladder) parseRung(idx int, content string) {
	rung := &rtRungs(m.rt)[idx]
	// The used flag is published last (see the loadCLPFile invariant): loads
	// run scan-settled today, but a half-filled rung must never be one
	// forgotten bracket away from being walked.
	y := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" {
			continue
		}
		if line[0] == '#' {
			// Parse metadata
			if strings.HasPrefix(line, "#VER=") {
				continue
			}
			if strings.HasPrefix(line, "#LABEL=") {
				copyGoStringToC(&rung.label[0], line[7:], C.CL_LGT_LABEL)
			}
			if strings.HasPrefix(line, "#COMMENT=") {
				copyGoStringToC(&rung.comment[0], line[9:], C.CL_LGT_COMMENT)
			}
			if strings.HasPrefix(line, "#PREVRUNG=") {
				rung.prev_rung = C.int(atoi(line[10:]))
			}
			if strings.HasPrefix(line, "#NEXTRUNG=") {
				rung.next_rung = C.int(atoi(line[10:]))
			}
			continue
		}
		if line[0] == ';' {
			continue
		}
		// Parse element row: "Type-ConnTop-VarType/VarNum , ..."
		if y >= C.CL_RUNG_HEIGHT {
			break
		}
		elements := strings.Split(line, ",")
		for x := 0; x < C.CL_RUNG_WIDTH && x < len(elements); x++ {
			elem := strings.TrimSpace(elements[x])
			parseElement(elem, &rung.elements[x][y])
		}
		y++
	}
	rung.used = 1
}

func parseElement(s string, e *C.cl_element_t) {
	// Format: "Type-ConnTop-VarType/VarNum"
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "-")
	if len(parts) < 3 {
		return
	}
	e._type = C.int16_t(atoi(parts[0]))
	e.connected_with_top = C.int8_t(atoi(parts[1]))
	// VarType/VarNum
	varParts := strings.Split(parts[2], "/")
	if len(varParts) >= 2 {
		e.var_type = C.int32_t(atoi(varParts[0]))
		e.var_num = C.int32_t(atoi(varParts[1]))
	}
}

// Time bases travel through the .clp file and the API as an id (0=minutes,
// 1=seconds, 2=100ms) but are held in milliseconds in the RT structures, the
// way 2.9 does it. baseMsFromID/baseIDFromMs convert between the two.

func baseMsFromID(id int) int {
	switch id {
	case C.CL_BASE_MINS:
		return C.CL_TIME_BASE_MINS
	case C.CL_BASE_100MS:
		return C.CL_TIME_BASE_100MS
	default:
		return C.CL_TIME_BASE_SECS
	}
}

func baseIDFromMs(ms int) int {
	switch ms {
	case C.CL_TIME_BASE_MINS:
		return C.CL_BASE_MINS
	case C.CL_TIME_BASE_100MS:
		return C.CL_BASE_100MS
	default:
		return C.CL_BASE_SECS
	}
}

// timers_iec.csv holds "base_id,preset,mode"; the IEC preset counts in base
// units, so it is stored unscaled.
func (m *classicladder) parseTimersIEC(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 || idx >= int(m.rt.sizes.nbr_timers_iec) {
			break
		}
		t := &rtTimersIec(m.rt)[idx]
		t.base = C.int(baseMsFromID(atoi(parts[0])))
		t.preset = clampI32(atoi(parts[1]))
		t.timer_mode = C.char(atoi(parts[2]))
		idx++
	}
}

// timers.csv holds "base_id,preset_in_base_units"; the old timer counts down
// in milliseconds, so the preset is scaled by the base.
func (m *classicladder) parseTimers(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 || idx >= int(m.rt.sizes.nbr_timers) {
			break
		}
		t := &rtTimers(m.rt)[idx]
		base := baseMsFromID(atoi(parts[0]))
		t.base = C.int(base)
		t.preset = clampI32(atoi(parts[1]) * base)
		idx++
	}
}

func (m *classicladder) parseMonostables(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 || idx >= int(m.rt.sizes.nbr_monostables) {
			break
		}
		mo := &rtMonostables(m.rt)[idx]
		base := baseMsFromID(atoi(parts[0]))
		mo.base = C.int(base)
		mo.preset = clampI32(atoi(parts[1]) * base)
		idx++
	}
}

func (m *classicladder) parseCounters(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if idx >= int(m.rt.sizes.nbr_counters) {
			break
		}
		rtCounters(m.rt)[idx].preset = C.int(atoi(line))
		idx++
	}
}

// arithmetic_expressions.csv has two forms. The current one (#VER=2.0) writes
// "NNNN,<expr>" for each non-empty expression, so that a sparse table keeps its
// numbering — the COMPARE and OPERATE elements refer to expressions by index,
// and renumbering them silently repoints every one of those elements. The older
// form is a bare expression per line, numbered by position.
func (m *classicladder) parseArithmExprs(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		expr := line
		if line[0] >= '0' && line[0] <= '9' {
			comma := strings.IndexByte(line, ',')
			if comma < 0 {
				continue
			}
			idx = atoi(line[:comma])
			expr = line[comma+1:]
		}
		if idx < 0 || idx >= int(m.rt.sizes.nbr_arithm_expr) {
			continue
		}
		copyGoStringToC(&rtArithmExprs(m.rt)[idx].expr[0], expr, C.CL_ARITHM_EXPR_SIZE)
		idx++
	}
}

func (m *classicladder) parseSymbols(content string) {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if idx >= int(m.rt.sizes.nbr_symbols) {
			break
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		s := &rtSymbols(m.rt)[idx]
		copyGoStringToC(&s.var_name[0], parts[0], C.CL_LGT_VAR_NAME)
		copyGoStringToC(&s.symbol[0], parts[1], C.CL_LGT_SYMBOL_STRING)
		copyGoStringToC(&s.comment[0], parts[2], C.CL_LGT_SYMBOL_COMMENT)
		idx++
	}
}

// --- Emitters (for save) ---

func (m *classicladder) emitGeneral() string {
	var b strings.Builder
	rt := m.rt
	fmt.Fprintf(&b, "PERIODIC_REFRESH=%d\n", int(rt.periodic_refresh_ms))
	fmt.Fprintf(&b, "SIZE_NBR_RUNGS=%d\n", int(rt.sizes.nbr_rungs))
	fmt.Fprintf(&b, "SIZE_NBR_BITS=%d\n", int(rt.sizes.nbr_bits))
	fmt.Fprintf(&b, "SIZE_NBR_WORDS=%d\n", int(rt.sizes.nbr_words))
	fmt.Fprintf(&b, "SIZE_NBR_TIMERS=%d\n", int(rt.sizes.nbr_timers))
	fmt.Fprintf(&b, "SIZE_NBR_MONOSTABLES=%d\n", int(rt.sizes.nbr_monostables))
	fmt.Fprintf(&b, "SIZE_NBR_COUNTERS=%d\n", int(rt.sizes.nbr_counters))
	fmt.Fprintf(&b, "SIZE_NBR_TIMERS_IEC=%d\n", int(rt.sizes.nbr_timers_iec))
	fmt.Fprintf(&b, "SIZE_NBR_PHYS_INPUTS=%d\n", int(rt.sizes.nbr_phys_inputs))
	fmt.Fprintf(&b, "SIZE_NBR_PHYS_OUTPUTS=%d\n", int(rt.sizes.nbr_phys_outputs))
	fmt.Fprintf(&b, "SIZE_NBR_ARITHM_EXPR=%d\n", int(rt.sizes.nbr_arithm_expr))
	fmt.Fprintf(&b, "SIZE_NBR_SECTIONS=%d\n", int(rt.sizes.nbr_sections))
	fmt.Fprintf(&b, "SIZE_NBR_SYMBOLS=%d\n", int(rt.sizes.nbr_symbols))
	return b.String()
}

func (m *classicladder) emitSections() string {
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=1.0")
	for i := 0; i < int(m.rt.sizes.nbr_sections); i++ {
		sec := &rtSections(m.rt)[i]
		if sec.used == 0 {
			continue
		}
		fmt.Fprintf(&b, "#NAME%03d=%s\n", i, C.GoString(&sec.name[0]))
	}
	for i := 0; i < int(m.rt.sizes.nbr_sections); i++ {
		sec := &rtSections(m.rt)[i]
		if sec.used == 0 {
			continue
		}
		fmt.Fprintf(&b, "%03d,%d,%d,%d,%d,%d\n", i,
			int(sec.language), int(sec.sub_routine_number),
			int(sec.first_rung), int(sec.last_rung), int(sec.sequential_page))
	}
	return b.String()
}

func (m *classicladder) emitRung(idx int) string {
	rung := &rtRungs(m.rt)[idx]
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=2.0")
	fmt.Fprintf(&b, "#LABEL=%s\n", C.GoString(&rung.label[0]))
	fmt.Fprintf(&b, "#COMMENT=%s\n", C.GoString(&rung.comment[0]))
	fmt.Fprintf(&b, "#PREVRUNG=%d\n", int(rung.prev_rung))
	fmt.Fprintf(&b, "#NEXTRUNG=%d\n", int(rung.next_rung))
	for y := 0; y < C.CL_RUNG_HEIGHT; y++ {
		for x := 0; x < C.CL_RUNG_WIDTH; x++ {
			if x > 0 {
				b.WriteString(" , ")
			}
			e := &rung.elements[x][y]
			fmt.Fprintf(&b, "%d-%d-%d/%d",
				int(e._type), int(e.connected_with_top),
				int(e.var_type), int(e.var_num))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *classicladder) emitTimersIEC() string {
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=1.0")
	for i := 0; i < int(m.rt.sizes.nbr_timers_iec); i++ {
		t := &rtTimersIec(m.rt)[i]
		fmt.Fprintf(&b, "%d,%d,%d\n",
			baseIDFromMs(int(t.base)), int(t.preset), int(t.timer_mode))
	}
	return b.String()
}

func (m *classicladder) emitTimers() string {
	var b strings.Builder
	for i := 0; i < int(m.rt.sizes.nbr_timers); i++ {
		t := &rtTimers(m.rt)[i]
		base := int(t.base)
		if base <= 0 {
			base = C.CL_TIME_BASE_SECS
		}
		fmt.Fprintf(&b, "%d,%d\n", baseIDFromMs(base), int(t.preset)/base)
	}
	return b.String()
}

func (m *classicladder) emitMonostables() string {
	var b strings.Builder
	for i := 0; i < int(m.rt.sizes.nbr_monostables); i++ {
		mo := &rtMonostables(m.rt)[i]
		base := int(mo.base)
		if base <= 0 {
			base = C.CL_TIME_BASE_SECS
		}
		fmt.Fprintf(&b, "%d,%d\n", baseIDFromMs(base), int(mo.preset)/base)
	}
	return b.String()
}

func (m *classicladder) emitCounters() string {
	var b strings.Builder
	for i := 0; i < int(m.rt.sizes.nbr_counters); i++ {
		fmt.Fprintf(&b, "%d\n", int(rtCounters(m.rt)[i].preset))
	}
	return b.String()
}

func (m *classicladder) emitArithmExprs() string {
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=2.0")
	for i := 0; i < int(m.rt.sizes.nbr_arithm_expr); i++ {
		expr := C.GoString(&rtArithmExprs(m.rt)[i].expr[0])
		if expr != "" {
			// Index-prefixed: skipping the empty slots without saying which
			// index each survivor had would renumber them on reload.
			fmt.Fprintf(&b, "%04d,%s\n", i, expr)
		}
	}
	return b.String()
}

func (m *classicladder) emitSymbols() string {
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=1.0")
	for i := 0; i < int(m.rt.sizes.nbr_symbols); i++ {
		s := &rtSymbols(m.rt)[i]
		vn := C.GoString(&s.var_name[0])
		if vn == "" {
			continue
		}
		fmt.Fprintf(&b, "%s,%s,%s\n", vn,
			C.GoString(&s.symbol[0]), C.GoString(&s.comment[0]))
	}
	return b.String()
}

// --- Helpers ---

func (m *classicladder) writeFileSection(w *bufio.Writer, name, content string) {
	// bufio.Writer errors are sticky and surfaced by the caller's Flush.
	_, _ = fmt.Fprintf(w, "_FILE-%s\n", name)
	if content != "" {
		_, _ = w.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			_ = w.WriteByte('\n')
		}
	}
	_, _ = fmt.Fprintf(w, "_/FILE-%s\n", name)
}

func copyGoStringToC(dst *C.char, src string, maxLen int) {
	copyStringToC(dst, src, C.int(maxLen))
}

// clampI32 bounds a file-supplied count to what the int32 RT fields hold: a
// corrupt preset becomes the nearest representable value, not a truncated
// surprise (atoi already saturates instead of erroring, this finishes the
// job for the 32-bit fields).
func clampI32(v int) C.int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return C.int(v)
}

func atoi(s string) int {
	s = strings.TrimSpace(s)
	v, _ := strconv.Atoi(s)
	return v
}

func (m *classicladder) parseSequential(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		switch line[0] {
		case 'S':
			// Step: S<idx>,<init>,<stepnumber>,<page>,<x>,<y>
			parts := strings.Split(line[1:], ",")
			if len(parts) < 6 {
				continue
			}
			idx := atoi(parts[0])
			if idx < 0 || idx >= C.CL_MAX_STEPS {
				continue
			}
			step := &m.rt.steps[idx]
			step.init_step = C.char(atoi(parts[1]))
			step.step_number = C.int(atoi(parts[2]))
			step.num_page = C.int8_t(atoi(parts[3]))
			step.posi_x = C.char(atoi(parts[4]))
			step.posi_y = C.char(atoi(parts[5]))

		case 'T':
			// Transition: T<idx>,<activ0..9>,<desactiv0..9>,<linked_start0..9>,<linked_end0..9>,<page>,<x>,<y>
			parts := strings.Split(line[1:], ",")
			expected := 1 + 4*C.CL_MAX_SWITCHS + 3
			if len(parts) < expected {
				continue
			}
			idx := atoi(parts[0])
			if idx < 0 || idx >= C.CL_MAX_TRANSITIONS {
				continue
			}
			trans := &m.rt.transitions[idx]
			// Out-of-range references are dropped and the list compacted:
			// -1 terminates these lists, so leaving a stray entry in place
			// would either count as present in the all-upstream-steps check
			// (the shape behind the fires-forever transitions) or, replaced
			// by -1 in the middle, cut off the valid entries behind it.
			readList := func(p int, limit int, dst *[C.CL_MAX_SWITCHS]C.int16_t) {
				j := 0
				for k := 0; k < C.CL_MAX_SWITCHS; k++ {
					v := atoi(parts[p+k])
					if v >= 0 && v < limit {
						dst[j] = C.int16_t(v)
						j++
					}
				}
				for ; j < C.CL_MAX_SWITCHS; j++ {
					dst[j] = -1
				}
			}
			p := 1
			readList(p, C.CL_MAX_STEPS, &trans.num_step_to_activ)
			p += C.CL_MAX_SWITCHS
			readList(p, C.CL_MAX_STEPS, &trans.num_step_to_desactiv)
			p += C.CL_MAX_SWITCHS
			readList(p, C.CL_MAX_TRANSITIONS, &trans.num_trans_linked_for_start)
			p += C.CL_MAX_SWITCHS
			readList(p, C.CL_MAX_TRANSITIONS, &trans.num_trans_linked_for_end)
			p += C.CL_MAX_SWITCHS
			trans.num_page = C.int8_t(atoi(parts[p]))
			p++
			trans.posi_x = C.char(atoi(parts[p]))
			p++
			trans.posi_y = C.char(atoi(parts[p]))

		case 'C':
			// Condition: C<trans_idx>,0,<vartype>/<varoffset>
			parts := strings.Split(line[1:], ",")
			if len(parts) < 3 {
				continue
			}
			idx := atoi(parts[0])
			if idx < 0 || idx >= C.CL_MAX_TRANSITIONS {
				continue
			}
			trans := &m.rt.transitions[idx]
			// parts[2] is "type/offset"
			varParts := strings.Split(strings.TrimSpace(parts[2]), "/")
			if len(varParts) >= 2 {
				trans.var_type_condi = C.int(atoi(varParts[0]))
				trans.var_num_condi = C.int(atoi(varParts[1]))
			}

		case 'N':
			// Comment: N<idx>,<page>,<x>,<y>,<text>
			parts := strings.SplitN(line[1:], ",", 5)
			if len(parts) < 4 {
				continue
			}
			idx := atoi(parts[0])
			if idx < 0 || idx >= C.CL_MAX_SEQ_COMMENTS {
				continue
			}
			sc := &m.rt.seq_comments[idx]
			sc.num_page = C.int8_t(atoi(parts[1]))
			sc.posi_x = C.char(atoi(parts[2]))
			sc.posi_y = C.char(atoi(parts[3]))
			if len(parts) >= 5 {
				copyGoStringToC(&sc.comment[0], parts[4], C.CL_SEQ_COMMENT_LGT)
			}
		}
	}
	// Activate init steps
	C.cl_prepare_sequential(m.rt)
}

func (m *classicladder) parseComParams(content string) {
	cfg := &m.modbus.cfg
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx > 0 {
			key := line[:idx]
			val := strings.TrimSpace(line[idx+1:])
			switch key {
			case "MODBUS_MASTER_SERIAL_PORT":
				cfg.SerialPort = val
			case "MODBUS_MASTER_SERIAL_SPEED":
				cfg.SerialSpeed = atoi(val)
			case "MODBUS_MASTER_SERIAL_DATABITS":
				cfg.SerialDataBits = atoi(val)
			case "MODBUS_MASTER_SERIAL_STOPBITS":
				cfg.SerialStopBits = atoi(val)
			case "MODBUS_MASTER_SERIAL_PARITY":
				cfg.SerialParity = atoi(val)
			case "MODBUS_ELEMENT_OFFSET":
				cfg.ElementOffset = atoi(val)
			case "MODBUS_MASTER_SERIAL_USE_RTS_TO_SEND":
				cfg.SerialUseRTS = atoi(val) != 0
			case "MODBUS_MASTER_TIME_INTER_FRAME":
				cfg.TimeInterFrame = atoi(val)
			case "MODBUS_MASTER_TIME_OUT_RECEIPT":
				cfg.TimeOutReceipt = atoi(val)
			case "MODBUS_MASTER_TIME_AFTER_TRANSMIT":
				cfg.TimeAfterTransmit = atoi(val)
			case "MODBUS_DEBUG_LEVEL":
				cfg.DebugLevel = atoi(val)
			case "MODBUS_MAP_COIL_READ":
				cfg.MapCoilRead = atoi(val)
			case "MODBUS_MAP_COIL_WRITE":
				cfg.MapCoilWrite = atoi(val)
			case "MODBUS_MAP_INPUT":
				cfg.MapInputs = atoi(val)
			case "MODBUS_MAP_HOLDING":
				cfg.MapHolding = atoi(val)
			case "MODBUS_MAP_REGISTER_READ":
				cfg.MapRegisterRead = atoi(val)
			case "MODBUS_MAP_REGISTER_WRITE":
				cfg.MapRegisterWrite = atoi(val)
			}
		}
	}
}

func (m *classicladder) parseModbusIOConf(content string) {
	var reqs []modbusRequest
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}

		var req modbusRequest
		// If line contains a dot, first field is IP address (comma-separated from rest)
		if strings.Contains(line, ".") {
			idx := strings.IndexByte(line, ',')
			if idx < 0 {
				continue
			}
			req.SlaveAddr = line[:idx]
			parts := strings.Split(line[idx+1:], ",")
			if len(parts) < 5 {
				continue
			}
			req.TypeReq = atoi(parts[0])
			req.FirstModbusElement = atoi(parts[1])
			req.NbrModbusElements = atoi(parts[2])
			req.LogicInverted = atoi(parts[3]) != 0
			req.OffsetVarMapped = atoi(parts[4])
		} else {
			// Serial: all numeric, first field is slave number
			parts := strings.Split(line, ",")
			if len(parts) < 6 {
				continue
			}
			req.SlaveAddr = strings.TrimSpace(parts[0])
			req.TypeReq = atoi(parts[1])
			req.FirstModbusElement = atoi(parts[2])
			req.NbrModbusElements = atoi(parts[3])
			req.LogicInverted = atoi(parts[4]) != 0
			req.OffsetVarMapped = atoi(parts[5])
		}
		reqs = append(reqs, req)
	}
	m.modbus.cfg.Requests = reqs
}

func (m *classicladder) emitComParams() string {
	cfg := &m.modbus.cfg
	if cfg.SerialPort == "" && len(cfg.Requests) == 0 {
		return ""
	}
	var b strings.Builder
	if cfg.SerialPort != "" {
		fmt.Fprintf(&b, "MODBUS_MASTER_SERIAL_PORT=%s\n", cfg.SerialPort)
		fmt.Fprintf(&b, "MODBUS_MASTER_SERIAL_SPEED=%d\n", cfg.SerialSpeed)
		if cfg.SerialDataBits != 0 {
			fmt.Fprintf(&b, "MODBUS_MASTER_SERIAL_DATABITS=%d\n", cfg.SerialDataBits)
		}
		if cfg.SerialStopBits != 0 {
			fmt.Fprintf(&b, "MODBUS_MASTER_SERIAL_STOPBITS=%d\n", cfg.SerialStopBits)
		}
		fmt.Fprintf(&b, "MODBUS_MASTER_SERIAL_PARITY=%d\n", cfg.SerialParity)
	}
	fmt.Fprintf(&b, "MODBUS_ELEMENT_OFFSET=%d\n", cfg.ElementOffset)
	if cfg.SerialUseRTS {
		fmt.Fprintln(&b, "MODBUS_MASTER_SERIAL_USE_RTS_TO_SEND=1")
	} else {
		fmt.Fprintln(&b, "MODBUS_MASTER_SERIAL_USE_RTS_TO_SEND=0")
	}
	fmt.Fprintf(&b, "MODBUS_MASTER_TIME_INTER_FRAME=%d\n", cfg.TimeInterFrame)
	fmt.Fprintf(&b, "MODBUS_MASTER_TIME_OUT_RECEIPT=%d\n", cfg.TimeOutReceipt)
	fmt.Fprintf(&b, "MODBUS_MASTER_TIME_AFTER_TRANSMIT=%d\n", cfg.TimeAfterTransmit)
	fmt.Fprintf(&b, "MODBUS_DEBUG_LEVEL=%d\n", cfg.DebugLevel)
	fmt.Fprintf(&b, "MODBUS_MAP_COIL_READ=%d\n", cfg.MapCoilRead)
	fmt.Fprintf(&b, "MODBUS_MAP_COIL_WRITE=%d\n", cfg.MapCoilWrite)
	fmt.Fprintf(&b, "MODBUS_MAP_INPUT=%d\n", cfg.MapInputs)
	fmt.Fprintf(&b, "MODBUS_MAP_HOLDING=%d\n", cfg.MapHolding)
	fmt.Fprintf(&b, "MODBUS_MAP_REGISTER_READ=%d\n", cfg.MapRegisterRead)
	fmt.Fprintf(&b, "MODBUS_MAP_REGISTER_WRITE=%d\n", cfg.MapRegisterWrite)
	return b.String()
}

func (m *classicladder) emitModbusIOConf() string {
	if len(m.modbus.cfg.Requests) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=1.0")
	for _, req := range m.modbus.cfg.Requests {
		if req.SlaveAddr == "" {
			continue
		}
		inverted := 0
		if req.LogicInverted {
			inverted = 1
		}
		// One line per request; TCP and serial differ only in what the
		// address field holds, not in the layout.
		fmt.Fprintf(&b, "%s,%d,%d,%d,%d,%d\n",
			req.SlaveAddr, req.TypeReq, req.FirstModbusElement,
			req.NbrModbusElements, inverted, req.OffsetVarMapped)
	}
	return b.String()
}

func (m *classicladder) emitSequential() string {
	var b strings.Builder
	fmt.Fprintln(&b, "#VER=1.0")

	// Steps, transitions and comments are in use when they have been placed on
	// a page. That is the flag everything else reads — GetSequential, and the
	// engine's own init-step scan — and the one 2.9 saves by. Testing anything
	// else wrote every unused slot to the file: 128 steps and 50 comments of
	// nothing, for a project with no chart at all.
	for i := 0; i < C.CL_MAX_STEPS; i++ {
		step := &m.rt.steps[i]
		if step.num_page < 0 {
			continue
		}
		fmt.Fprintf(&b, "S%d,%d,%d,%d,%d,%d\n", i,
			int(step.init_step), int(step.step_number),
			int(step.num_page), int(step.posi_x), int(step.posi_y))
	}

	// Transitions
	for i := 0; i < C.CL_MAX_TRANSITIONS; i++ {
		trans := &m.rt.transitions[i]
		if trans.num_page < 0 {
			continue
		}
		fmt.Fprintf(&b, "T%d", i)
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			fmt.Fprintf(&b, ",%d", int(trans.num_step_to_activ[j]))
		}
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			fmt.Fprintf(&b, ",%d", int(trans.num_step_to_desactiv[j]))
		}
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			fmt.Fprintf(&b, ",%d", int(trans.num_trans_linked_for_start[j]))
		}
		for j := 0; j < C.CL_MAX_SWITCHS; j++ {
			fmt.Fprintf(&b, ",%d", int(trans.num_trans_linked_for_end[j]))
		}
		fmt.Fprintf(&b, ",%d,%d,%d\n",
			int(trans.num_page), int(trans.posi_x), int(trans.posi_y))
	}

	// Conditions, in their own pass as 2.9 writes them, and for every
	// transition that exists rather than only those whose condition looks
	// set: "%B0" is variable type 0 offset 0, so a test for a non-zero pair
	// drops exactly the condition a chart is most likely to use.
	for i := 0; i < C.CL_MAX_TRANSITIONS; i++ {
		trans := &m.rt.transitions[i]
		if trans.num_page < 0 {
			continue
		}
		fmt.Fprintf(&b, "C%d,0,%d/%d\n", i,
			int(trans.var_type_condi), int(trans.var_num_condi))
	}

	// Comments
	for i := 0; i < C.CL_MAX_SEQ_COMMENTS; i++ {
		sc := &m.rt.seq_comments[i]
		if sc.num_page < 0 {
			continue
		}
		comment := C.GoStringN(&sc.comment[0], C.CL_SEQ_COMMENT_LGT)
		// Trim null bytes
		if idx := strings.IndexByte(comment, 0); idx >= 0 {
			comment = comment[:idx]
		}
		fmt.Fprintf(&b, "N%d,%d,%d,%d,%s\n", i,
			int(sc.num_page), int(sc.posi_x), int(sc.posi_y), comment)
	}

	return b.String()
}
