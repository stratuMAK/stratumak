// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package pnptask is the pick-and-place task module: a sibling of milltask for
// a different kind of machine, not a replacement for it.
//
// There is no G-code here. Motion is generated from station definitions, tray
// grids and a dead-zone route planner (pkg/pnproute), and jobs are commanded
// purely over HAL pins, so the module integrates with a PLC world without any
// UI dependency. Like milltask it owns its own motion stack and drives it
// through the motctl/motstat GMI clients; no io/iocontrol instance is involved
// because there is no toolchanger.
//
// Usage in a HAL file:
//
//	load pnptask <pnp.task> motion_instance=pnp.mot pickers=2 \
//	                        persist_instance=persist
//
// Load arguments:
//   - motion_instance=<name>   motctl/motstat provider; falls back to
//     [EMCMOT]MOTION_INSTANCE in the INI (like milltask), then "motmod"
//   - pickers=<1|2>            2 enables the alternating-picker logic (D5)
//   - persist_instance=<name>  persist API instance, spelled like every other
//     module's persist arg; unset = in-memory state only (D6, no default
//     lookup)
//
// Configuration lives in the [PNPTASK*] INI sections, resolved through the
// instance namespace ([pnp.task:PNPTASK] with fallback to [PNPTASK]) — see
// config.go and docs/dev/PNPTASK_DESIGN.md §5.1. The HAL interface is in
// pins.go.
//
// This is phase 5 of the design document. The module loads and validates its
// configuration, exports its complete HAL interface, pushes the machine
// configuration into motmod and runs the machine (machine.go: estop/enable
// sequencing, homing, and manual mode with jogging, manual picker control and the
// position teach outputs); it tracks the world jobs act on (stations.go: tray slot
// states, process-station occupancy and the per-picker held-material records),
// optionally backed by persist_sqlite (persist.go); and it runs jobs commanded
// over the start-job handshake (job.go, actions.go, motion.go).
//
// One picker is used per job, but no code names a picker: the pick takes whichever
// one is free and the place is performed by the one holding the job's material
// (D20). What phase 6 adds is the alternating-picker *decisions* — skipping the
// pick when a picker already holds the origin's material, and swapping the
// occupant out of a busy process station.
package pnptask

import (
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

func init() {
	stmak.RegisterModule("pnptask", factory)
}

// defaultMotionInstance is the motmod instance a load line without
// motion_instance= drives — the same default milltask uses.
const defaultMotionInstance = "motmod"

// The GMI versions this module is built against; a provider registered at any
// other version is refused rather than called through a mismatched ABI.
const (
	motctlVersion = 1
	// mot is the motion controller's RT-side interface, consumed by the cycle
	// function: the Cartesian feedback for the dead-zone check, and the motion
	// snapshot the control loop reads instead of motstat (version 3).
	motVersion = 3
)

// pnptaskModule is one loaded pnptask instance.
type pnptaskModule struct {
	name   string
	logger *slog.Logger
	ini    *inifile.IniFile // namespaced view: [<name>:SECTION] over [SECTION]
	cfg    *Config

	// planners is one route planner per DEADZONE_FILE, built once at load
	// time and immutable afterwards (see plannerSet).
	planners *plannerSet

	comp *hal.Component
	pins *pinSet

	// deadzone is the flat scene the dead-zone check walks. Built in the
	// factory from planners, fed the motmod callback table in Start, released
	// in Destroy — after the launcher's RT barrier, never before.
	deadzone *deadzoneRT

	// rt is the cycle function's Go side: every pin of the module is read and
	// written by that function in the servo thread, through the frames here
	// (pnp_rt.h, docs/dev/PNPTASK_RT_BOUNDARY.md). Same lifecycle as deadzone.
	rt *rtIO

	// world is the runtime model behind the station pins: tray contents, process
	// station occupancy and the per-picker held-material records (§7.1). Built
	// in the factory alongside the pins it publishes on, seeded from them (and
	// from persistence) when the control loop starts.
	world *world

	// The motion stack this instance drives, resolved in Start (see there for
	// why not in the factory). They are interfaces rather than the generated
	// clients so the control loop can be tested against a scripted motion
	// stack; Start assigns the real ones.
	mc motionControl
	ms motionStatus

	// limits are the machine limits pushed to motmod at Start, kept for the
	// clamping this module has to do itself (jog velocity, and the per-move
	// vel/acc of the later phases).
	limits *motsetup.Result

	// ctl is the control loop; nil until Start.
	ctl *control

	// tickHook is the unit tests' simulated hardware: driveRT runs it before
	// every servo cycle of the cycle function the loop runs itself, so the
	// simulated gripper and fixtures answer in the loop's own time instead of
	// on a goroutine the scheduler can hold back past a settle budget. Nil in
	// production. Set before driveRT and startControl only.
	tickHook func()

	// handshakes counts the jobs and parks the control loop has ended, after
	// the commit that ends each. Nothing in production reads it: the unit
	// tests wait on it, since a job refused at once raises busy for too short
	// a time to be polled.
	handshakes atomic.Uint32

	// wakes counts the control loop's wakes, for the tests that check it
	// sleeps while nothing changes.
	wakes atomic.Uint32

	// motInstance and persistInstance are resolved in the later phases; they
	// are parsed here so a typo on the load line is at least recorded next to
	// the config it belongs to.
	motInstance     string
	persistInstance string
	pickers         int
}

func factory(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	logger = logger.With("module", "pnptask", "instance", name)

	// Every station, tray and limit comes out of the INI, so an INI-less
	// launcher (halrun mode) cannot run this module. Say so instead of
	// nil-dereferencing on the next line.
	if ini == nil {
		return nil, fmt.Errorf("pnptask %q: requires an INI file (loaded without one)", name)
	}

	m := &pnptaskModule{
		name:    name,
		logger:  logger,
		ini:     ini.WithNamespace(name),
		pickers: 1,
	}
	if err := m.parseArgs(args); err != nil {
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	// Like milltask, the motion instance may come from the INI so a shared HAL
	// file does not have to hardcode it per instance; the load arg still wins.
	if m.motInstance == "" {
		if v := strings.TrimSpace(m.ini.Get("EMCMOT", "MOTION_INSTANCE")); v != "" {
			m.motInstance = v
		} else {
			m.motInstance = defaultMotionInstance
		}
	}

	cfg, err := LoadConfig(m.ini)
	if err != nil {
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	m.cfg = cfg

	// Build the route planners and finish the configuration validation against
	// them, before anything HAL-side exists: a station taught inside a dead
	// zone is a config error, and finding it here costs one failed load rather
	// than a job that dies mid-cycle with a part in the picker.
	planners, err := newPlanners(cfg)
	if err != nil {
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	m.planners = planners
	for _, w := range planners.warnings {
		logger.Warn("pnptask: " + w)
	}
	for _, w := range planners.homeWarnings(cfg) {
		logger.Warn("pnptask: " + w)
	}
	for _, w := range cfg.Warnings {
		logger.Warn("pnptask: " + w)
	}

	// The HAL component is created in the factory, not in Start: the "net"
	// lines that wire this instance run immediately after the load line, long
	// before any module is started, and they can only link pins that already
	// exist.
	//
	// It is a REALTIME component because this module exports a cyclic function
	// (the dead-zone check below); HAL refuses one from a userspace component.
	comp, err := hal.NewRTComponent(name)
	if err != nil {
		return nil, fmt.Errorf("pnptask %q: creating HAL component: %w", name, err)
	}
	m.comp = comp

	m.rt = &rtIO{}

	// A probing swap takes the occupant out with the free picker while the
	// other carries the job's material: with one picker there is none.
	if m.pickers < 2 {
		for _, s := range cfg.Procs {
			if s.ProbeSwap {
				_ = comp.Exit()
				return nil, fmt.Errorf("pnptask %q: [%s]PROBE_SWAP needs pickers=2", name, s.Section)
			}
		}
	}

	pins, err := newPins(comp, cfg, m.pickers, &m.rt.out)
	if err != nil {
		// The launcher only tears down modules whose factory returned one, so
		// a component created and then abandoned here would hold its name (and
		// its share of HAL memory) for the life of the process.
		_ = comp.Exit()
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	m.pins = pins
	m.world = newWorld(cfg, pins, m.pickers, logger)

	// The cycle function and the dead-zone check it runs work in the servo
	// thread, so what they walk is assembled here — once, the geometry out of
	// the planners that already offset the zones, the frames and pin maps out
	// of the pins — and the function is exported before Ready, as HAL requires
	// and as the addf lines that follow this load line expect.
	dz, err := newDeadzoneRT(planners, pins.deadzoneFree)
	if err != nil {
		_ = comp.Exit()
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	m.deadzone = dz
	fail := func(err error) (stmak.Module, error) {
		_ = comp.Exit()
		m.rt.free()
		dz.free()
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	if err := m.rt.build(pins, dz); err != nil {
		return fail(err)
	}
	if err := m.rt.export(comp); err != nil {
		return fail(fmt.Errorf("exporting %s: %w", cycleFunct, err))
	}
	if err := comp.Ready(); err != nil {
		return fail(fmt.Errorf("hal ready: %w", err))
	}

	logger.Info("pnptask configured",
		"motion_instance", m.motInstance,
		"pickers", m.pickers,
		"persist_instance", m.persistInstance,
		"trays", len(cfg.Trays),
		"procs", len(cfg.Procs),
		"traydefs", len(cfg.TrayDefs),
		"deadzone_files", len(cfg.DeadzoneFiles))
	for i, pl := range planners.planners {
		logger.Debug("route planner built",
			"index", i, "file", planners.files[i],
			"nodes", pl.NodeCount(), "edges", pl.EdgeCount())
	}

	return m, nil
}

// parseArgs reads the key=value load arguments. An unknown key is an error
// rather than a shrug: the whole module is commanded through configuration, and
// a mistyped "picker=2" that silently leaves the second picker unexported would
// only surface as a missing HAL pin much later.
func (m *pnptaskModule) parseArgs(args []string) error {
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			return fmt.Errorf("load argument %q: expected key=value", arg)
		}
		switch k {
		case "motion_instance":
			if v == "" {
				return fmt.Errorf("motion_instance=: empty instance name")
			}
			m.motInstance = v
		case "pickers":
			n, err := strconv.Atoi(v)
			if err != nil || (n != 1 && n != 2) {
				return fmt.Errorf("pickers=%q: must be 1 or 2", v)
			}
			m.pickers = n
		case "persist_instance":
			// The same spelling task, tooltable, halscope and ngcpreview use
			// for the same persist API — parseArgs hard-rejects unknown keys,
			// so a divergent spelling here would fail every HAL line written
			// by analogy with those modules.
			if v == "" {
				return fmt.Errorf("persist_instance=: empty instance name")
			}
			m.persistInstance = v
		default:
			return fmt.Errorf("unknown load argument %q", k)
		}
	}
	return nil
}

// Start resolves the motion stack this instance drives, pushes the machine
// configuration into it and starts the control loop.
//
// This lookup belongs in Start and *not* in the factory. The launcher runs
// every module's constructor — where a provider registers its API — before it
// starts any of them, so a motmod loaded on a LATER line of the HAL file is
// registered by the time Start runs. Resolving it in the factory would instead
// impose a HAL-file ordering rule (motmod strictly before pnptask) and fail
// with a confusing "no such API" on any config that does not happen to obey
// it. The HAL pins go the other way — they are created in the factory, because
// the "net" lines that link them execute right after the load line — so the
// two halves of this module deliberately live in different lifecycle stages.
func (m *pnptaskModule) Start() error {
	reg := apiserver.DefaultRegistry()
	if reg == nil {
		return fmt.Errorf("pnptask %q: no API registry available", m.name)
	}

	motctlCbs, err := reg.GetAPIFor(m.name, "motctl", m.motInstance, motctlVersion)
	if err != nil {
		return fmt.Errorf("pnptask %q: motctl API lookup (%s): %w", m.name, m.motInstance, err)
	}
	m.mc = motctl.NewMotctlClient(unsafe.Pointer(motctlCbs))
	// The motion status comes out of the cycle function's frame, read in the
	// servo thread with the pins (rtStatus), not from motstat.
	m.ms = &rtStatus{rt: m.rt, numJoints: m.cfg.NumJoints}

	// The cyclic dead-zone check needs a Cartesian position, and it takes it
	// from mot — motmod's RT-side interface, whose accessors are @rt_safe.
	// motstat, which the Go loop above uses, is the non-RT snapshot interface:
	// its accessors take the reader mutex and copy the whole status struct, so
	// the servo thread must not touch them.
	//
	// Refused rather than degraded: without it every deadzone.N.free pin would
	// sit at "not clear" forever, which is safe but silently useless, and the
	// module already treats a missing motctl/motstat on the same instance as
	// fatal.
	motCbs, err := reg.GetAPIFor(m.name, "mot", m.motInstance, motVersion)
	if err != nil {
		return fmt.Errorf("pnptask %q: mot API lookup (%s): %w", m.name, m.motInstance, err)
	}
	m.deadzone.setMot(unsafe.Pointer(motCbs))

	// Optional state persistence (D6): no default lookup, an absent load arg
	// means in-memory state only. Resolved here for the same reason the motion
	// stack is — the provider may be loaded on a later HAL line.
	if m.persistInstance != "" {
		store, err := openPersist(m.name, m.persistInstance, persistNamespace(m.name), m.logger)
		if err != nil {
			return fmt.Errorf("pnptask %q: %w", m.name, err)
		}
		m.world.persist = store
	}

	return m.startControl()
}

// startControl pushes the machine configuration into motmod and starts the
// control loop.
//
// It is split from Start at exactly the C ABI boundary: above it is the
// callback-table lookup, which cannot run without a real provider, and below it
// everything goes through the motionControl/motionStatus interfaces — which is
// what lets the whole machine state machine be exercised against a scripted
// motion stack instead of only in a live sim.
func (m *pnptaskModule) startControl() error {
	// Push the machine configuration ([TRAJ], [JOINT_n], [AXIS_*]) into motmod
	// before anything can command a move — motion starts with zeroed limits,
	// and a move commanded against those goes nowhere or, worse, everywhere.
	// The INI view is the namespaced one, so a two-machine config can give each
	// instance its own [<instance>:JOINT_0] without renaming sections.
	//
	// No spindles: a pick-and-place machine has none, and pushing defaults for
	// one would be configuration nobody wrote.
	limits, err := motsetup.Push(m.ini, motsetup.Options{
		NumJoints:   m.cfg.NumJoints,
		NumSpindles: 0,
		AxisMask:    m.cfg.AxisMask(),
		LinearUnits: m.cfg.LinearUnits,
	}, m.mc)
	if err != nil {
		return fmt.Errorf("pnptask %q: pushing motion configuration: %w", m.name, err)
	}
	// The pushed per-axis limits are what every move's velocity blend divides
	// by, and motsetup's reader is deliberately lenient — an explicit
	// [AXIS_*]MAX_VELOCITY = 0 (or nan) would otherwise surface only at the
	// first job, as a fault that does not name the key. The three linear axes
	// this module moves are checked here. (!(v > 0) also catches NaN.)
	for i, letter := range []byte{'X', 'Y', 'Z'} {
		vel, acc := limits.AxisMaxVel[i], limits.AxisMaxAcc[i]
		if !(vel > 0) || math.IsInf(vel, 0) || !(acc > 0) || math.IsInf(acc, 0) {
			return fmt.Errorf("pnptask %q: [AXIS_%c]MAX_VELOCITY/MAX_ACCELERATION must be positive and finite (got %v / %v)",
				m.name, letter, vel, acc)
		}
	}
	m.limits = limits

	// From here on every motion command first commits the outputs (see
	// committedMotion). The push above wrote no outputs.
	m.mc = committedMotion{motionControl: m.mc, rt: m.rt}

	// Every pin is read by the cycle function, so nothing can be seeded before
	// it has run once. In the servo thread that is within a millisecond; not
	// at all means it is on no thread, and the module could see no input and
	// drive no output.
	if !m.rt.waitFirstFrame(firstFrameTimeout) {
		return fmt.Errorf("pnptask %q: the cycle function has not run: it must be on the servo "+
			"thread AFTER motion-controller (addf %s.%s servo-thread)", m.name, m.name, cycleFunct)
	}

	// Seed the station model from its pins and from persistence before the loop
	// exists: from here on the model belongs to the control goroutine.
	m.world.start(m.rt.trayIDs(len(m.cfg.Trays)))

	m.ctl = newControl(m)
	m.ctl.start()

	m.logger.Info("pnptask started",
		"motion_instance", m.motInstance,
		"joints", m.cfg.NumJoints,
		"max_velocity", limits.MaxVelocity,
		"max_acceleration", limits.MaxAcceleration)
	return nil
}

// Stop must tolerate never having been started — the launcher stops every
// loaded module even when a peer's Start failed first.
func (m *pnptaskModule) Stop() {
	if m.ctl != nil {
		m.ctl.shutdown()
		m.ctl = nil
	}
	if m.world != nil && m.world.persist != nil {
		// The loop is gone, so nothing else will write: flush whatever its last
		// cycles changed before the handle closes.
		m.world.flush()
		m.world.persist.close()
		m.world.persist = nil
	}
}

// Destroy releases the HAL component and, only then, the memory the cyclic
// function was walking.
//
// The order matters and so does the phase. By the time Destroy runs the
// launcher has removed this component's functions from every thread and waited
// for a full cycle of every realtime thread (runtime unload), or stopped the
// threads synchronously (shutdown) — so nothing is still executing the
// dead-zone check. Freeing the block any earlier, in Stop for instance, is a
// use-after-free inside the servo thread.
func (m *pnptaskModule) Destroy() {
	if m.comp != nil {
		if err := m.comp.Exit(); err != nil {
			m.logger.Debug("pnptask HAL component exit error", "error", err)
		}
		m.comp = nil
	}
	if m.rt != nil {
		m.rt.free()
		m.rt = nil
	}
	if m.deadzone != nil {
		m.deadzone.free()
		m.deadzone = nil
	}
}
