// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/motsetup"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// MotionConfig is the subset of motctl used at init time to configure joints,
// axes, spindles, and trajectory parameters from INI values. The INI -> motion
// push itself lives in internal/motsetup, shared with the other task modules
// that own a motion stack (pnptask); this alias keeps the milltask-local name.
type MotionConfig = motsetup.MotionConfig

// jointHomingParams holds the INI-fixed homing parameters that are NOT exposed
// as runtime HAL pins. They are cached so inihal can re-push them unchanged when
// a HAL home/offset/sequence change forces a SetJointHomingParams update.
type jointHomingParams = motsetup.HomingParams

// loadConfig reads INI sections and configures the motion controller.
// Called once at startup from the factory.
func loadConfig(ini *inifile.IniFile, t *Task, mc MotionConfig) error {
	// Resolver for G-code paths opened over REST (program_open).  G-code is
	// user data, not configuration, so it gets the program directories as
	// roots rather than the config directories.
	iniDir := "."
	if ini != nil && ini.SourceFile() != "" {
		iniDir = filepath.Dir(ini.SourceFile())
	}
	var iniGet func(string, string) string
	if ini != nil {
		iniGet = ini.Get
	}
	t.programRes = pathres.ProgramResolver(iniGet, iniDir)
	// Kept for [FILTER] lookups at program-open time: which converter turns a
	// given extension into G-code, and how long it may take.
	t.iniGet = iniGet

	// Read the milltask-specific half of [TRAJ] (joint/spindle counts, units,
	// the canon's modal units, the interpreter's startup code) first: the shared
	// push below needs the counts and the unit scale as inputs.
	if err := loadTraj(ini, t); err != nil {
		return fmt.Errorf("traj config: %w", err)
	}
	res, err := motsetup.Push(ini, motsetup.Options{
		NumJoints:   t.numJoints,
		NumSpindles: t.numSpindles,
		AxisMask:    t.axisMask,
		LinearUnits: t.linearUnits,
	}, mc)
	if err != nil {
		return err
	}
	t.maxVelocity = res.MaxVelocity
	t.maxAcceleration = res.MaxAcceleration
	t.jointLinear = res.JointLinear
	t.jointMaxVel = res.JointMaxVel
	t.jointHoming = res.JointHoming
	t.axisMaxVel = res.AxisMaxVel
	t.axisMaxAcc = res.AxisMaxAcc

	// MDI queue depth from [TASK] section (default 10, matching C milltask).
	if n := getIntOr(ini, "TASK", "MDI_QUEUED_COMMANDS", t.maxMDIQueued); n > 0 {
		t.maxMDIQueued = n
	}

	return nil
}

// loadTraj reads the [TRAJ] values milltask itself needs — the joint and
// spindle counts, the axis mask, the unit scales, the canon's modal units, the
// interpreter's startup code and the tool-change position. The [TRAJ] values
// that are *pushed to motion* (velocity/acceleration limits, arc blends, world
// home) belong to the machine, not to milltask, and live in motsetup.Push.
func loadTraj(ini *inifile.IniFile, t *Task) error {
	// Validate required settings.
	if ini.Get("KINS", "JOINTS") == "" {
		return fmt.Errorf("[KINS]JOINTS is required")
	}
	if ini.Get("TRAJ", "COORDINATES") == "" {
		return fmt.Errorf("[TRAJ]COORDINATES is required")
	}

	// Joints
	t.numJoints = getIntOr(ini, "KINS", "JOINTS", 3)

	// Spindles
	t.numSpindles = getIntOr(ini, "TRAJ", "SPINDLES", 1)

	// Homing enforcement
	t.noForceHoming = getIntOr(ini, "TRAJ", "NO_FORCE_HOMING", 0) != 0

	// Axis mask from COORDINATES string
	coord := ini.Get("TRAJ", "COORDINATES")
	t.axisMask = motsetup.ParseAxisMask(coord)

	// Units
	t.linearUnits = parseLinearUnits(ini.Get("TRAJ", "LINEAR_UNITS"))
	t.angularUnits = parseAngularUnits(ini.Get("TRAJ", "ANGULAR_UNITS"))

	// The canon (created in NewTask, before the INI is read) starts in the
	// machine's native modal units: G20 on an inch machine, G21 on mm. The
	// interpreter picks this up via GetExternalLengthUnitType at init. Without
	// it an inch machine would run unit-less G-code as millimetres (25.4x
	// small). Mirrors the C canon's INIT_CANON units derivation.
	if t.canon != nil {
		t.canon.state.lengthUnits = machineCanonUnits(t.linearUnits)
		t.canonSnap = *t.canon.state
	}

	// RS274NGC startup code
	t.startupCode = ini.Get("RS274NGC", "RS274NGC_STARTUP_CODE")
	if t.startupCode == "" {
		t.startupCode = ini.Get("EMC", "RS274NGC_STARTUP_CODE")
	}

	// Note: t.randomToolchanger is NOT read here. It changes the pocket
	// semantics of the tool canon getters (spindle = pocket 0 vs the
	// non-random idx0 toolno=-1 empty-spindle convention), which is the tool
	// store's property — Start asks the store, see module.go.

	// Optional move-before-tool-change position (2.9 taskclass readToolChange
	// + emccanon CHANGE_TOOL). INI values are machine units, absolute machine
	// coordinates; 3, 6 or 9 coords accepted. Linear components scale to mm,
	// angular (a,b,c) stay degrees.
	if v := ini.Get("EMCIO", "TOOL_CHANGE_POSITION"); v != "" {
		fields := strings.Fields(v)
		n := 0
		switch {
		case len(fields) >= 9:
			n = 9
		case len(fields) >= 6:
			n = 6
		case len(fields) >= 3:
			n = 3
		}
		var vals [9]float64
		for i := 0; i < n; i++ {
			f, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				n = 0
				break
			}
			vals[i] = f
		}
		if n == 0 {
			return fmt.Errorf("bad format for [EMCIO]TOOL_CHANGE_POSITION: %q", v)
		}
		for _, i := range []int{0, 1, 2, 6, 7, 8} { // x,y,z,u,v,w are linear
			vals[i] = t.machineToMM(vals[i])
		}
		t.toolChangePos = vals
		t.toolChangePosLen = n
	}

	return nil
}

// machineToMM converts a length expressed in the machine's configured linear
// units to the internal millimeters the motion controller works in. stratuMAK's
// canon emits move targets in mm (fromProg/toAbsolute), so every length handed
// to motion must be mm too. Soft-limit positions were the exception: they were
// passed straight from the INI in machine units, so on an inch machine motion
// enforced them 25.4x too tight and rejected legal moves. linearUnits is
// machine-units-per-mm (1.0 for mm, 1/25.4 for inch), so mm = value/linearUnits.
func (t *Task) machineToMM(v float64) float64 {
	if t.linearUnits <= 0 {
		return v
	}
	return v / t.linearUnits
}

// --- Helpers ---

func parseLinearUnits(s string) float64 {
	if s == "" {
		return 1.0 // mm default
	}
	switch strings.ToLower(s) {
	case "mm", "metric":
		return 1.0
	case "in", "inch", "imperial":
		return 1.0 / 25.4
	}
	v := parseFloat(s, 1.0)
	return v
}

func parseAngularUnits(s string) float64 {
	if s == "" {
		return 1.0 // degree default
	}
	switch strings.ToLower(s) {
	case "deg", "degree":
		return 1.0
	case "rad", "radian":
		return math.Pi / 180.0
	case "grad", "gon":
		return 0.9
	}
	return parseFloat(s, 1.0)
}

func getFloatOr(ini *inifile.IniFile, section, key string, def float64) float64 {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	return parseFloat(s, def)
}

func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	if err != nil {
		return def
	}
	return v
}
