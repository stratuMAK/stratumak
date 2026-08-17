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
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
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
// other version is refused rather than called through a mismatched ABI. Same
// versions milltask requires of the same two APIs.
const (
	motctlVersion  = 1
	motstatVersion = 1
	// mot is the motion controller's RT-side interface, consumed only by the
	// cyclic dead-zone check (for the Cartesian feedback position).
	motVersion = 2
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

	// deadzone is the flat scene the cyclic dead-zone check walks, and the
	// cyclic function's registration. Built in the factory from planners, fed
	// the motmod callback table in Start, released in Destroy — after the
	// launcher's RT barrier, never before.
	deadzone *deadzoneRT

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
	for _, w := range planners.homeWarnings(cfg) {
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

	pins, err := newPins(comp, cfg, m.pickers)
	if err != nil {
		// The launcher only tears down modules whose factory returned one, so
		// a component created and then abandoned here would hold its name (and
		// its share of HAL memory) for the life of the process.
		_ = comp.Exit()
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	m.pins = pins
	m.world = newWorld(cfg, pins, m.pickers, logger)

	// The dead-zone check runs in the servo cycle, so the geometry it walks is
	// assembled here — once, out of the planners that already offset the zones
	// — and the function is exported before Ready, as HAL requires and as the
	// addf lines that follow this load line expect.
	dz, err := newDeadzoneRT(planners, pins.deadzoneFree)
	if err != nil {
		_ = comp.Exit()
		return nil, fmt.Errorf("pnptask %q: %w", name, err)
	}
	if err := dz.export(comp); err != nil {
		_ = comp.Exit()
		dz.free()
		return nil, fmt.Errorf("pnptask %q: exporting %s: %w", name, deadzoneFunct, err)
	}
	m.deadzone = dz

	if err := comp.Ready(); err != nil {
		_ = comp.Exit()
		dz.free()
		return nil, fmt.Errorf("pnptask %q: hal ready: %w", name, err)
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
	motstatCbs, err := reg.GetAPIFor(m.name, "motstat", m.motInstance, motstatVersion)
	if err != nil {
		return fmt.Errorf("pnptask %q: motstat API lookup (%s): %w", m.name, m.motInstance, err)
	}
	m.mc = motctl.NewMotctlClient(unsafe.Pointer(motctlCbs))
	m.ms = motstat.NewMotstatClient(unsafe.Pointer(motstatCbs))

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
	m.warnIfDeadzoneUnscheduled()

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

	// Seed the station model from its pins and from persistence before the loop
	// exists: from here on the model belongs to the control goroutine.
	m.world.start()

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

// warnIfDeadzoneUnscheduled says so, loudly and with the line to add, when the
// dead-zone check is not on any thread.
//
// The pins are published by the cyclic function and by nothing else, so a
// configuration that never addf's it leaves every deadzone.N.free reading "not
// clear" — the safe direction, and exactly the direction that is impossible to
// tell from a machine legitimately parked inside a zone. Whatever is waiting to
// close would simply never move, with nothing in the log to say why. Hence a
// warning that names the missing line rather than a silent correct-but-useless
// state.
//
// Start is the right moment: every load line has run and the HAL file's addf
// lines with them, so a function with no users here has none by omission.
func (m *pnptaskModule) warnIfDeadzoneUnscheduled() {
	full := m.name + "." + deadzoneFunct
	res, err := halcmd.Show("funct", full)
	if err != nil {
		// Nothing to conclude — say nothing rather than warn about a config
		// that may be perfectly fine.
		m.logger.Debug("pnptask: could not check the dead-zone function's thread", "error", err)
		return
	}
	for _, f := range res.Functs {
		if f.Name == full && f.Users > 0 {
			return
		}
	}
	m.logger.Warn("pnptask: the dead-zone check is not on any thread, so every "+
		"deadzone.N.free pin will stay false (\"not clear\"); add it to the servo "+
		"thread AFTER motion-controller, which is what computes the position it reads",
		"add", fmt.Sprintf("addf %s servo-thread", full))
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
	if m.deadzone != nil {
		m.deadzone.free()
		m.deadzone = nil
	}
}
