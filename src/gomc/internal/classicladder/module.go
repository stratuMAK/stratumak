// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package classicladder implements the Classic Ladder PLC as a gomod.
//
// The RT refresh function runs in a HAL thread (C code via cgo).
// All non-RT logic — file I/O, API handlers, watch loops — lives in Go.
package classicladder

/*
#cgo CFLAGS: -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include
#cgo LDFLAGS:

#include "classicladder_rt.h"

#include <stdlib.h>
#include <string.h>
#include <dlfcn.h>

// hal_export_funct wrapper — Go can't take address of C function directly.
static int go_hal_export_funct(const char *name, classicladder_rt_t *rt,
                               int comp_id) {
    return hal_export_funct(name, classicladder_refresh, rt, 1, 0, comp_id);
}

// Self dl_handle for RT component registration.
static void *self_dl_handle(void) {
    return dlopen(NULL, RTLD_NOW);
}
*/
import "C"

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/gomc"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

func init() {
	gomc.RegisterModule("classicladder", newClassicLadder)
}

// classicladder implements gomc.Module.
type classicladder struct {
	logger      *slog.Logger
	rt          *C.classicladder_rt_t
	compID      C.int
	mu          sync.RWMutex // protects program data modifications
	name        string
	functName   string
	projectFile string
	modbus      *modbusMaster
	modbusSlave *modbusSlave
	slavePort   int
	// Which HAL pin carries which ladder variable, recorded as the pins were
	// created. Fixed after construction, so it needs no lock.
	halPins []halPinRef
}

// parseModuleArgs turns the `load classicladder ...` argument list into the
// PLC sizes, the positional project-file path, and the modbus slave port.
// The arrays are allocated to exactly these counts, so a bad value must
// refuse the load here — it cannot be clamped into meaning something else.
func parseModuleArgs(args []string) (C.cl_sizes_t, string, int, error) {
	// Default sizes
	sizes := C.cl_sizes_t{
		nbr_rungs:        C.CL_DEF_RUNGS,
		nbr_bits:         C.CL_DEF_BITS,
		nbr_words:        C.CL_DEF_WORDS,
		nbr_timers:       C.CL_DEF_TIMERS,
		nbr_monostables:  C.CL_DEF_MONOSTABLES,
		nbr_counters:     C.CL_DEF_COUNTERS,
		nbr_timers_iec:   C.CL_DEF_TIMERS_IEC,
		nbr_phys_inputs:  C.CL_DEF_PHYS_INPUTS,
		nbr_phys_outputs: C.CL_DEF_PHYS_OUTPUTS,
		nbr_arithm_expr:  C.CL_DEF_ARITHM_EXPR,
		nbr_sections:     C.CL_DEF_SECTIONS,
		nbr_symbols:      C.CL_DEF_SYMBOLS,
		nbr_s32_in:       C.CL_DEF_S32_IN,
		nbr_s32_out:      C.CL_DEF_S32_OUT,
		nbr_float_in:     C.CL_DEF_FLOAT_IN,
		nbr_float_out:    C.CL_DEF_FLOAT_OUT,
		nbr_error_bits:   C.CL_DEF_ERROR_BITS,
	}

	// Parse args for size overrides (e.g. numRungs=200 numBits=500)
	// and project file path (last positional arg or modbus_port=N).
	var projectFile string
	var slavePort int
	for _, arg := range args {
		if !strings.Contains(arg, "=") {
			// Positional arg = project file path
			projectFile = arg
			continue
		}
		parts := strings.SplitN(arg, "=", 2)
		key, val := parts[0], parts[1]
		n, err := strconv.Atoi(val)
		if err != nil {
			return C.cl_sizes_t{}, "", 0, fmt.Errorf("classicladder: %s: not a number: %q", key, val)
		}
		var dst *C.int
		switch key {
		case "numRungs":
			dst = &sizes.nbr_rungs
		case "numBits":
			dst = &sizes.nbr_bits
		case "numWords":
			dst = &sizes.nbr_words
		case "numTimers":
			dst = &sizes.nbr_timers
		case "numMonostables":
			dst = &sizes.nbr_monostables
		case "numCounters":
			dst = &sizes.nbr_counters
		case "numTimersIec":
			dst = &sizes.nbr_timers_iec
		case "numPhysInputs":
			dst = &sizes.nbr_phys_inputs
		case "numPhysOutputs":
			dst = &sizes.nbr_phys_outputs
		case "numArithmExpr":
			dst = &sizes.nbr_arithm_expr
		case "numSections":
			dst = &sizes.nbr_sections
		case "numSymbols":
			dst = &sizes.nbr_symbols
		case "numS32in":
			dst = &sizes.nbr_s32_in
		case "numS32out":
			dst = &sizes.nbr_s32_out
		case "numFloatIn":
			dst = &sizes.nbr_float_in
		case "numFloatOut":
			dst = &sizes.nbr_float_out
		case "modbus_port":
			if n < 1 || n > 65535 {
				return C.cl_sizes_t{}, "", 0, fmt.Errorf("classicladder: modbus_port out of range: %d", n)
			}
			slavePort = n
			continue
		default:
			return C.cl_sizes_t{}, "", 0, fmt.Errorf("classicladder: unknown argument %q", key)
		}
		if n < 0 || n > C.CL_SIZE_LIMIT {
			return C.cl_sizes_t{}, "", 0, fmt.Errorf("classicladder: %s out of range: %d (0..%d)", key, n, int(C.CL_SIZE_LIMIT))
		}
		*dst = C.int(n)
	}
	if sizes.nbr_rungs < 1 || sizes.nbr_sections < 1 {
		return C.cl_sizes_t{}, "", 0, fmt.Errorf("classicladder: numRungs and numSections must be at least 1")
	}
	return sizes, projectFile, slavePort, nil
}

func newClassicLadder(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (gomc.Module, error) {
	sizes, projectFile, slavePort, err := parseModuleArgs(args)
	if err != nil {
		return nil, err
	}

	rt := C.classicladder_rt_alloc(&sizes)
	if rt == nil {
		return nil, fmt.Errorf("classicladder: failed to allocate RT instance")
	}
	C.classicladder_rt_init_data(rt)

	// Create HAL RT component
	dlHandle := C.self_dl_handle()
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	compID := C.hal_init_ex(cName, dlHandle, C.COMPONENT_TYPE_REALTIME)
	if compID < 0 {
		C.classicladder_rt_free(rt)
		return nil, fmt.Errorf("classicladder: hal_init_ex failed: %d", int(compID))
	}

	// Export the RT refresh function to HAL. The instance number is part of
	// the name, as it is for the pins and as it was in 2.9, so that existing
	// configs keep working: addf classicladder.0.refresh <thread>
	functName := name + ".0.refresh"
	cFunctName := C.CString(functName)
	defer C.free(unsafe.Pointer(cFunctName))
	rv := C.go_hal_export_funct(cFunctName, rt, compID)
	if rv != 0 {
		C.hal_exit(compID)
		C.classicladder_rt_free(rt)
		return nil, fmt.Errorf("classicladder: hal_export_funct failed: %d", int(rv))
	}

	// Create HAL pins
	halPins, err := createHALPins(rt, compID, name)
	if err != nil {
		C.hal_exit(compID)
		C.classicladder_rt_free(rt)
		return nil, err
	}

	C.hal_ready(compID)

	m := &classicladder{
		logger:      logger,
		rt:          rt,
		compID:      compID,
		name:        name,
		functName:   functName,
		projectFile: projectFile,
		modbus:      newModbusMaster(rt, logger),
		modbusSlave: newModbusSlave(rt, logger),
		slavePort:   slavePort,
		halPins:     halPins,
	}

	// Register REST API
	reg := apiserver.DefaultRegistry()
	if reg != nil {
		m.registerAPI(reg, name)
	}

	// Register WebSocket watch API
	wreg := apiserver.DefaultWatchRegistry()
	if wreg == nil {
		apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())
		wreg = apiserver.DefaultWatchRegistry()
	}
	if wreg != nil {
		m.registerWatch(wreg, name)
	}

	logger.Info("classicladder loaded", "name", name, "comp_id", int(compID))
	return m, nil
}

func (m *classicladder) Start() error {
	// Load project file if specified as module argument
	if m.projectFile != "" {
		// The positional project-file argument is a configuration path like any
		// other — resolved server-side and contained (internal/pathres).  This
		// is the argument cmd/halcmd's resolveArgPath used to rewrite
		// client-side; the CLI now sends it verbatim.
		projectFile, err := pathres.Resolve(m.projectFile, pathres.Read)
		if err != nil {
			m.logger.Error("failed to resolve project", "path", m.projectFile, "err", err)
			return err
		}
		m.projectFile = projectFile
		if err := m.loadCLPFile(m.projectFile); err != nil {
			m.logger.Error("failed to load project", "path", m.projectFile, "err", err)
			return err
		}
		C.cl_prepare_all_datas_before_run(m.rt)
		m.setState(C.CL_STATE_RUN)
	}
	// Start Modbus master if configured
	m.modbus.start()
	// Start Modbus slave if configured
	if m.slavePort > 0 {
		m.modbusSlave.start(m.slavePort)
	}
	return nil
}

func (m *classicladder) Stop() {
	m.modbus.stop()
	m.modbusSlave.stop()
	C.hal_exit(m.compID)
	C.classicladder_rt_free(m.rt)
}

func (m *classicladder) Destroy() {}

// --- HAL pin creation ---

// halPinRef records which ladder variable a HAL pin carries. Built while the
// pins are created rather than reconstructed later: the HAL-signal lookup needs
// the same names, and a second place that spells them is a second place that can
// drift (see the variable prefixes in the ladder view, which did).
type halPinRef struct {
	varType int
	offset  int
	pin     string
	isInput bool
}

func createHALPins(rt *C.classicladder_rt_t, compID C.int, name string) ([]halPinRef, error) {
	var refs []halPinRef
	record := func(pin string, varType, offset int, isInput bool) {
		refs = append(refs, halPinRef{varType: varType, offset: offset, pin: pin, isInput: isInput})
	}

	// Bit inputs
	for i := C.int(0); i < rt.sizes.nbr_phys_inputs; i++ {
		pin := fmt.Sprintf("%s.0.in-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_bit_new(pinName, C.HAL_IN, &rtHalInputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin in-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_INPUT, int(i), true)
	}

	// Bit outputs
	for i := C.int(0); i < rt.sizes.nbr_phys_outputs; i++ {
		pin := fmt.Sprintf("%s.0.out-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_bit_new(pinName, C.HAL_OUT, &rtHalOutputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin out-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_OUTPUT, int(i), false)
	}

	// S32 inputs
	for i := C.int(0); i < rt.sizes.nbr_s32_in; i++ {
		pin := fmt.Sprintf("%s.0.s32in-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_s32_new(pinName, C.HAL_IN, &rtHalS32Inputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin s32in-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_WORD_INPUT, int(i), true)
	}

	// S32 outputs
	for i := C.int(0); i < rt.sizes.nbr_s32_out; i++ {
		pin := fmt.Sprintf("%s.0.s32out-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_s32_new(pinName, C.HAL_OUT, &rtHalS32Outputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin s32out-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_WORD_OUTPUT, int(i), false)
	}

	// Float inputs
	for i := C.int(0); i < rt.sizes.nbr_float_in; i++ {
		pin := fmt.Sprintf("%s.0.floatin-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_float_new(pinName, C.HAL_IN, &rtHalFloatInputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin floatin-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_FLOAT_INPUT, int(i), true)
	}

	// Float outputs
	for i := C.int(0); i < rt.sizes.nbr_float_out; i++ {
		pin := fmt.Sprintf("%s.0.floatout-%02d", name, int(i))
		pinName := C.CString(pin)
		rv := C.hal_pin_float_new(pinName, C.HAL_OUT, &rtHalFloatOutputs(rt)[i], compID)
		C.free(unsafe.Pointer(pinName))
		if rv != 0 {
			return nil, fmt.Errorf("failed to create pin floatout-%02d: %d", int(i), int(rv))
		}
		record(pin, C.CL_VAR_PHYS_FLOAT_OUTPUT, int(i), false)
	}

	return refs, nil
}

// --- State accessors (atomic, safe from any goroutine) ---

func (m *classicladder) getState() int {
	return int(atomic.LoadInt32((*int32)(unsafe.Pointer(&m.rt.state))))
}

func (m *classicladder) setState(state int) {
	atomic.StoreInt32((*int32)(unsafe.Pointer(&m.rt.state)), int32(state))
}

func (m *classicladder) getScanTimeNs() int32 {
	return atomic.LoadInt32((*int32)(unsafe.Pointer(&m.rt.duration_of_last_scan_ns)))
}

func (m *classicladder) getGeneration() uint32 {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&m.rt.generation)))
}

func (m *classicladder) bumpGeneration() {
	atomic.AddUint32((*uint32)(unsafe.Pointer(&m.rt.generation)), 1)
}
