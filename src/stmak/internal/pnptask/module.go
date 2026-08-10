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
// This is phase 2 of the design document: the module loads, validates its
// configuration and exports its complete HAL interface. Machine control,
// stations, the job engine and the alternating-picker logic follow in the
// later phases; nothing here commands motion yet.
package pnptask

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
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

	// The motion stack this instance drives, resolved in Start (see there for
	// why not in the factory).
	mc *motctl.MotctlClient
	ms *motstat.MotstatClient

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

	// The HAL component is created in the factory, not in Start: the "net"
	// lines that wire this instance run immediately after the load line, long
	// before any module is started, and they can only link pins that already
	// exist.
	comp, err := hal.NewComponent(name)
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

	if err := comp.Ready(); err != nil {
		_ = comp.Exit()
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

// Start resolves the motion stack this instance drives. Pushing the motmod
// configuration and starting the poll loop arrive with the machine-control
// phase; what Start already guarantees is that the motion instance named on
// the load line exists and speaks the expected API version.
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

	m.logger.Info("pnptask started", "motion_instance", m.motInstance)
	return nil
}

// Stop must tolerate never having been started — the launcher stops every
// loaded module even when a peer's Start failed first. Nothing runs yet.
func (m *pnptaskModule) Stop() {}

func (m *pnptaskModule) Destroy() {
	if m.comp != nil {
		if err := m.comp.Exit(); err != nil {
			m.logger.Debug("pnptask HAL component exit error", "error", err)
		}
		m.comp = nil
	}
}
