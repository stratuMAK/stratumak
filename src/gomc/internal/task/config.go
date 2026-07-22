// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

// MotionConfig is the subset of motctl used at init time to configure
// joints, axes, spindles, and trajectory parameters from INI values.
type MotionConfig interface {
	// Trajectory
	SetVel(vel float64) error
	SetVelLimit(vel float64) error
	SetAcc(acc float64) error
	SetMaxFeedOverride(max float64) error
	SetWorldHome(pos Pose) error
	SetProbeErrInhibit(jogInhibit, homeInhibit int32) error
	SetupArcBlends(enable, fallbackEnable, optDepth, gapCycles int32, rampFreq, tangentKinkRatio float64) error

	// Joints
	JointActivate(joint int32) error
	SetJointPositionLimits(joint int32, min, max float64) error
	SetJointBacklash(joint int32, backlash float64) error
	SetJointMaxFerror(joint int32, ferror float64) error
	SetJointMinFerror(joint int32, ferror float64) error
	SetJointVelLimit(joint int32, vel float64) error
	SetJointAccLimit(joint int32, acc float64) error
	SetJointJerkLimit(joint int32, jerk float64) error
	SetJointHomingParams(joint int32, offset, home, homeFinalVel, searchVel, latchVel float64, flags, sequence, volatileHome int32) error
	SetJointComp(joint int32, nominal, fwd, rev float64) error

	// Axes
	SetAxisPositionLimits(axis int32, min, max float64) error
	SetAxisVelLimit(axis int32, vel, extOffsetVel float64) error
	SetAxisAccLimit(axis int32, acc, extOffsetAcc float64) error
	SetAxisLockingJoint(axis int32, joint int32) error

	// Spindles
	SetSpindleParams(spindle int32, maxPosSpeed, minPosSpeed, maxNegSpeed, minNegSpeed, homeSearchVel float64, homeSequence int32, increment float64) error
}

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

	if err := loadTraj(ini, t, mc); err != nil {
		return fmt.Errorf("traj config: %w", err)
	}
	for j := int32(0); j < int32(t.numJoints); j++ {
		if err := loadJoint(ini, t, j, mc); err != nil {
			return fmt.Errorf("joint %d config: %w", j, err)
		}
		// Store joint max velocity for jog clamping (matches C JointConfig[].MaxVel).
		// Converted machine->mm for linear joints (jointLinear set in loadJoint above).
		section := fmt.Sprintf("JOINT_%d", j)
		t.jointMaxVel[j] = t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_VELOCITY", 1.0), t.jointLinear[j])
	}
	numAxes := axisCount(t.axisMask)
	for a := int32(0); a < int32(numAxes); a++ {
		if t.axisMask&(1<<a) == 0 {
			continue
		}
		if err := loadAxis(ini, t, a, mc); err != nil {
			return fmt.Errorf("axis %d config: %w", a, err)
		}
		// Store per-axis max velocity/acceleration for jog clamping and for the
		// canon's per-move vel/acc blending (matches C AxisConfig[].MaxVel/MaxAcc).
		// Converted machine->mm for linear axes; angular (A/B/C) left in degrees.
		axSection := axisSection(a)
		axLinear := axisIsLinear(a)
		t.axisMaxVel[a] = t.machineToMMLinear(getFloatOrSection(ini, axSection, "MAX_VELOCITY", 1.0), axLinear)
		t.axisMaxAcc[a] = t.machineToMMLinear(getFloatOrSection(ini, axSection, "MAX_ACCELERATION", 1.0), axLinear)
	}
	for s := int32(0); s < int32(t.numSpindles); s++ {
		if err := loadSpindle(ini, s, mc); err != nil {
			return fmt.Errorf("spindle %d config: %w", s, err)
		}
	}

	// MDI queue depth from [TASK] section (default 10, matching C milltask).
	if n := getIntOr(ini, "TASK", "MDI_QUEUED_COMMANDS", t.maxMDIQueued); n > 0 {
		t.maxMDIQueued = n
	}

	return nil
}

func loadTraj(ini *inifile.IniFile, t *Task, mc MotionConfig) error {
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
	t.axisMask = parseAxisMask(coord)

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

	// Velocities. TRAJ velocity limits are linear (the "..._LINEAR_..." keys),
	// so convert machine-units->mm to match the mm-internal motion controller.
	// The minAxisVel fallback also returns machine units, so scale after it.
	defaultVel := getFloatOr(ini, "TRAJ", "DEFAULT_LINEAR_VELOCITY",
		getFloatOr(ini, "TRAJ", "DEFAULT_VELOCITY", 1.0))
	t.maxVelocity = getFloatOr(ini, "TRAJ", "MAX_LINEAR_VELOCITY",
		getFloatOr(ini, "TRAJ", "MAX_VELOCITY", 0))
	if t.maxVelocity <= 0 {
		t.maxVelocity = minAxisVel(ini, t.axisMask)
	}
	defaultVel = t.machineToMM(defaultVel)
	t.maxVelocity = t.machineToMM(t.maxVelocity)
	if err := mc.SetVelLimit(t.maxVelocity); err != nil {
		return err
	}
	if err := mc.SetVel(clamp(defaultVel, 0, t.maxVelocity)); err != nil {
		return err
	}

	// Acceleration
	// If no explicit [TRAJ] acceleration limit, derive from the minimum of
	// [AXIS_*]MAX_ACCELERATION for active axes.  Using 1e99 as a sentinel
	// causes catastrophic floating-point cancellation in the TP's trapezoidal
	// velocity planner (sqrt(B²+C) - B ≈ 0 when B is huge).
	defaultAcc := getFloatOr(ini, "TRAJ", "DEFAULT_LINEAR_ACCELERATION",
		getFloatOr(ini, "TRAJ", "DEFAULT_ACCELERATION", 0))
	t.maxAcceleration = getFloatOr(ini, "TRAJ", "MAX_LINEAR_ACCELERATION",
		getFloatOr(ini, "TRAJ", "MAX_ACCELERATION", 0))
	if t.maxAcceleration <= 0 {
		t.maxAcceleration = minAxisAcc(ini, t.axisMask)
	}
	if defaultAcc <= 0 {
		defaultAcc = t.maxAcceleration
	}
	// TRAJ acceleration limits are linear too: convert machine-units->mm.
	defaultAcc = t.machineToMM(defaultAcc)
	t.maxAcceleration = t.machineToMM(t.maxAcceleration)
	if err := mc.SetAcc(clamp(defaultAcc, 0, t.maxAcceleration)); err != nil {
		return err
	}

	// Max feed override
	maxFeedScale := getFloatOr(ini, "DISPLAY", "MAX_FEED_OVERRIDE", 1.0)
	if err := mc.SetMaxFeedOverride(maxFeedScale); err != nil {
		return err
	}

	// Probe error inhibit
	jogInhibit := int32(getIntOr(ini, "TRAJ", "NO_PROBE_JOG_ERROR", 0))
	homeInhibit := int32(getIntOr(ini, "TRAJ", "NO_PROBE_HOME_ERROR", 0))
	if err := mc.SetProbeErrInhibit(jogInhibit, homeInhibit); err != nil {
		return err
	}

	// TP arc-blend optimizer (2.9 initraj.cc emcSetupArcBlends, same keys and
	// defaults — notably ARC_BLEND_ENABLE defaults ON). Without this push the
	// motion config stays zeroed and the TP silently falls back to parabolic
	// blending: G64/G64 P<tol> corners stay near-exact-stop instead of being
	// cut to tolerance.
	if err := mc.SetupArcBlends(
		int32(getIntOr(ini, "TRAJ", "ARC_BLEND_ENABLE", 1)),
		int32(getIntOr(ini, "TRAJ", "ARC_BLEND_FALLBACK_ENABLE", 0)),
		int32(getIntOr(ini, "TRAJ", "ARC_BLEND_OPTIMIZATION_DEPTH", 50)),
		int32(getIntOr(ini, "TRAJ", "ARC_BLEND_GAP_CYCLES", 4)),
		getFloatOr(ini, "TRAJ", "ARC_BLEND_RAMP_FREQ", 100.0),
		getFloatOr(ini, "TRAJ", "ARC_BLEND_KINK_RATIO", 0.1),
	); err != nil {
		return err
	}

	// World home (a position handed to motion): linear components machine->mm,
	// angular (A/B/C) left in degrees — same conversion the position limits get.
	homeStr := ini.Get("TRAJ", "HOME")
	if homeStr != "" {
		home := t.poseLinearToMM(parsePoseString(homeStr))
		if err := mc.SetWorldHome(home); err != nil {
			return err
		}
	}

	// RS274NGC startup code
	t.startupCode = ini.Get("RS274NGC", "RS274NGC_STARTUP_CODE")
	if t.startupCode == "" {
		t.startupCode = ini.Get("EMC", "RS274NGC_STARTUP_CODE")
	}

	// Random toolchanger changes the pocket semantics of the tool canon
	// getters (spindle = pocket 0 vs the non-random idx0 toolno=-1
	// empty-spindle convention).
	if v := ini.Get("EMCIO", "RANDOM_TOOLCHANGER"); v != "" && v != "0" {
		t.randomToolchanger = true
	}

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

// jointHomingParams holds the INI-fixed homing parameters that are NOT exposed
// as runtime HAL pins. They are cached so inihal can re-push them unchanged when
// a HAL home/offset/sequence change forces a SetJointHomingParams update.
type jointHomingParams struct {
	finalVel, searchVel, latchVel float64
	flags, volatileHome           int32
}

// machineToMM converts a length expressed in the machine's configured linear
// units to the internal millimeters the motion controller works in. gomc's
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

// axisIsLinear reports whether the axis at the given index is a linear
// (length-dimensioned) axis. X,Y,Z,U,V,W (indices 0,1,2,6,7,8) are linear;
// A,B,C (3,4,5) are angular (degrees). Only linear config values are converted
// machine-units->mm: the canon never unit-scales angular coordinates
// (canon.go: fromProg runs on X/Y/Z/U/V/W only), so angular limits/vels/accels
// are already in the internal degree units and must be left untouched.
func axisIsLinear(index int32) bool {
	switch index {
	case 0, 1, 2, 6, 7, 8: // X Y Z U V W
		return true
	default: // 3,4,5 = A B C (angular); anything else defensively non-linear
		return false
	}
}

// jointTypeIsLinear parses a [JOINT_n]TYPE value. LINEAR (the default when
// unset) means length-dimensioned; ANGULAR means degrees. Matches the C config
// (emcmotcfg). A joint's linearity ultimately follows the axis it drives, but
// reading TYPE matches the C config and is simplest.
func jointTypeIsLinear(s string) bool {
	return !strings.EqualFold(strings.TrimSpace(s), "ANGULAR")
}

// machineToMMLinear converts v machine-units->mm iff linear; otherwise returns
// v unchanged. Used to scale only length-dimensioned (linear) config values.
func (t *Task) machineToMMLinear(v float64, linear bool) float64 {
	if !linear {
		return v
	}
	return t.machineToMM(v)
}

// poseLinearToMM converts the linear coordinates (X,Y,Z,U,V,W) of a pose from
// machine units to mm, leaving the angular coordinates (A,B,C) untouched.
func (t *Task) poseLinearToMM(p Pose) Pose {
	p.X = t.machineToMM(p.X)
	p.Y = t.machineToMM(p.Y)
	p.Z = t.machineToMM(p.Z)
	p.U = t.machineToMM(p.U)
	p.V = t.machineToMM(p.V)
	p.W = t.machineToMM(p.W)
	return p
}

func loadJoint(ini *inifile.IniFile, t *Task, joint int32, mc MotionConfig) error {
	section := fmt.Sprintf("JOINT_%d", joint)

	// Linearity: LINEAR joints have length-dimensioned config that must be
	// converted machine-units->mm to match the mm-internal motion controller.
	// ANGULAR (rotary) joints are in degrees and are left unscaled.
	linear := jointTypeIsLinear(ini.Get(section, "TYPE"))
	if joint >= 0 && int(joint) < len(t.jointLinear) {
		t.jointLinear[joint] = linear
	}

	// Position limits (INI machine units -> internal mm, matching move targets).
	minLimit := t.machineToMMLinear(getFloatOrSection(ini, section, "MIN_LIMIT", -1e99), linear)
	maxLimit := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_LIMIT", 1e99), linear)
	if err := mc.SetJointPositionLimits(joint, minLimit, maxLimit); err != nil {
		return err
	}

	// Backlash (length)
	backlash := t.machineToMMLinear(getFloatOrSection(ini, section, "BACKLASH", 0), linear)
	if err := mc.SetJointBacklash(joint, backlash); err != nil {
		return err
	}

	// Following error (length)
	ferror := t.machineToMMLinear(getFloatOrSection(ini, section, "FERROR", 1), linear)
	if err := mc.SetJointMaxFerror(joint, ferror); err != nil {
		return err
	}
	minFerror := t.machineToMMLinear(getFloatOrSection(ini, section, "MIN_FERROR", ferror), linear)
	if err := mc.SetJointMinFerror(joint, minFerror); err != nil {
		return err
	}

	// Homing. HOME/HOME_OFFSET are positions (length); the *_VEL are velocities.
	// finalVel default -1 is a "use joint max vel" sentinel; scaling preserves
	// its sign so the sentinel semantics are unchanged.
	home := t.machineToMMLinear(getFloatOrSection(ini, section, "HOME", 0), linear)
	offset := t.machineToMMLinear(getFloatOrSection(ini, section, "HOME_OFFSET", 0), linear)
	searchVel := t.machineToMMLinear(getFloatOrSection(ini, section, "HOME_SEARCH_VEL", 0), linear)
	latchVel := t.machineToMMLinear(getFloatOrSection(ini, section, "HOME_LATCH_VEL", 0), linear)
	finalVel := t.machineToMMLinear(getFloatOrSection(ini, section, "HOME_FINAL_VEL", -1), linear)
	useIndex := getIntOrSection(ini, section, "HOME_USE_INDEX", 0)
	noEncoderReset := getIntOrSection(ini, section, "HOME_INDEX_NO_ENCODER_RESET", 0)
	ignoreLimits := getBoolOrSection(ini, section, "HOME_IGNORE_LIMITS", false)
	isShared := getBoolOrSection(ini, section, "HOME_IS_SHARED", false)
	sequence := getIntOrSection(ini, section, "HOME_SEQUENCE", 999)
	volatileHome := getIntOrSection(ini, section, "VOLATILE_HOME", 0)
	lockingIndexer := getIntOrSection(ini, section, "LOCKING_INDEXER", 0)
	absoluteEncoder := getIntOrSection(ini, section, "HOME_ABSOLUTE_ENCODER", 0)

	// Pack boolean flags into a single int32 (must match homing.h defines)
	// HOME_IGNORE_LIMITS=1, HOME_USE_INDEX=2, HOME_IS_SHARED=4,
	// HOME_UNLOCK_FIRST=8, HOME_ABSOLUTE_ENCODER=16, HOME_NO_REHOME=32,
	// HOME_NO_FINAL_MOVE=64, HOME_INDEX_NO_ENCODER_RESET=128
	flags := int32(0)
	if ignoreLimits {
		flags |= 1 // HOME_IGNORE_LIMITS
	}
	if useIndex != 0 {
		flags |= 2 // HOME_USE_INDEX
	}
	if isShared {
		flags |= 4 // HOME_IS_SHARED
	}
	if lockingIndexer != 0 {
		flags |= 8 // HOME_UNLOCK_FIRST
	}
	if absoluteEncoder != 0 {
		flags |= 16 // HOME_ABSOLUTE_ENCODER
	}
	if noEncoderReset != 0 {
		flags |= 128 // HOME_INDEX_NO_ENCODER_RESET
	}

	if err := mc.SetJointHomingParams(joint, offset, home, finalVel, searchVel, latchVel, flags, int32(sequence), int32(volatileHome)); err != nil {
		return err
	}
	// Cache the INI-fixed params so a runtime HAL home/offset/seq change doesn't
	// zero them on re-push (inihal).
	if joint >= 0 && int(joint) < len(t.jointHoming) {
		t.jointHoming[joint] = jointHomingParams{
			finalVel: finalVel, searchVel: searchVel, latchVel: latchVel,
			flags: flags, volatileHome: int32(volatileHome),
		}
	}

	// Velocity, acceleration and jerk (all length-dimensioned for linear joints)
	maxVel := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_VELOCITY", 1.0), linear)
	if err := mc.SetJointVelLimit(joint, maxVel); err != nil {
		return err
	}
	maxAcc := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_ACCELERATION", 1.0), linear)
	if err := mc.SetJointAccLimit(joint, maxAcc); err != nil {
		return err
	}
	maxJerk := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_JERK", 0.0), linear)
	if maxJerk > 0.0 {
		if err := mc.SetJointJerkLimit(joint, maxJerk); err != nil {
			return err
		}
	}

	// Leadscrew / screw-error compensation ([JOINT_n]COMP_FILE). Loaded before
	// activation, matching C++ which pushes the table to motion at startup.
	if compFile := ini.Get(section, "COMP_FILE"); compFile != "" {
		compType := getIntOrSection(ini, section, "COMP_FILE_TYPE", 0)
		if err := loadJointComp(joint, compFile, compType, mc.SetJointComp); err != nil {
			return fmt.Errorf("COMP_FILE %q: %w", compFile, err)
		}
	}

	// Activate
	return mc.JointActivate(joint)
}

// loadJointComp parses a leadscrew-compensation file and pushes each triplet to
// motion, matching C++ usrmotLoadComp. Each data line is "nominal forward
// reverse". compType 0: the file holds nominal/forward/reverse POSITIONS and the
// motion trims are the diffs (nominal - value); any other type: the values are
// already trims and are passed through. Blank / non-triplet lines are skipped
// (C++ stops at the first such line; skipping is a strict superset that loads
// the same pure-triplet files identically and tolerates comments/headers).
func loadJointComp(joint int32, file string, compType int, setComp func(joint int32, nominal, fwd, rev float64) error) error {
	// [JOINT_n]COMP_FILE is a configuration path: resolved server-side and
	// contained by the shared rule (internal/pathres).
	path, err := pathres.Resolve(file, pathres.Read)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		nom, e1 := strconv.ParseFloat(fields[0], 64)
		fwd, e2 := strconv.ParseFloat(fields[1], 64)
		rev, e3 := strconv.ParseFloat(fields[2], 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if compType == 0 {
			fwd = nom - fwd // positions -> trims (diffs), as C++ does
			rev = nom - rev
		}
		if err := setComp(joint, nom, fwd, rev); err != nil {
			return err
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no compensation triplets found")
	}
	return nil
}

func loadAxis(ini *inifile.IniFile, t *Task, axis int32, mc MotionConfig) error {
	section := axisSection(axis)

	// Linearity by axis letter/index: X,Y,Z,U,V,W are linear (length, scaled to
	// mm); A,B,C are angular (degrees, unscaled).
	linear := axisIsLinear(axis)

	// Position limits (INI machine units -> internal mm, matching move targets).
	minLimit := t.machineToMMLinear(getFloatOrSection(ini, section, "MIN_LIMIT", -1e99), linear)
	maxLimit := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_LIMIT", 1e99), linear)
	if err := mc.SetAxisPositionLimits(axis, minLimit, maxLimit); err != nil {
		return err
	}

	// Ext offset ratio (reduces available vel/acc for the axis proper)
	avRatio := getFloatOrSection(ini, section, "OFFSET_AV_RATIO", 0)
	if avRatio < 0 || avRatio > 0.9 {
		avRatio = 0.1
	}

	// Velocity (length/s for linear axes)
	maxVel := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_VELOCITY", 1.0), linear)
	if err := mc.SetAxisVelLimit(axis, (1-avRatio)*maxVel, avRatio*maxVel); err != nil {
		return err
	}

	// Acceleration (length/s^2 for linear axes)
	maxAcc := t.machineToMMLinear(getFloatOrSection(ini, section, "MAX_ACCELERATION", 1.0), linear)
	if err := mc.SetAxisAccLimit(axis, (1-avRatio)*maxAcc, avRatio*maxAcc); err != nil {
		return err
	}

	// Locking indexer
	lockingJoint := getIntOrSection(ini, section, "LOCKING_INDEXER_JOINT", -1)
	if err := mc.SetAxisLockingJoint(axis, int32(lockingJoint)); err != nil {
		return err
	}

	return nil
}

func loadSpindle(ini *inifile.IniFile, spindle int32, mc MotionConfig) error {
	section := fmt.Sprintf("SPINDLE_%d", spindle)

	fastestPos := 1e99
	slowestPos := 0.0
	fastestNeg := -1e99
	slowestNeg := 0.0

	if s := ini.Get(section, "MAX_FORWARD_VELOCITY"); s != "" {
		v := parseFloat(s, 1e99)
		fastestPos = v
		fastestNeg = -v
	}
	if s := ini.Get(section, "MIN_FORWARD_VELOCITY"); s != "" {
		v := parseFloat(s, 0)
		slowestPos = v
		slowestNeg = -v
	}
	if s := ini.Get(section, "MIN_REVERSE_VELOCITY"); s != "" {
		v := parseFloat(s, 0)
		slowestNeg = -math.Abs(v)
	}
	if s := ini.Get(section, "MAX_REVERSE_VELOCITY"); s != "" {
		v := parseFloat(s, 1e99)
		fastestNeg = -math.Abs(v)
	}

	homeSequence := int32(getIntOrSection(ini, section, "HOME_SEQUENCE", 0))
	searchVel := getFloatOrSection(ini, section, "HOME_SEARCH_VELOCITY", 0)
	increment := getFloatOrSection(ini, section, "INCREMENT", 100)

	return mc.SetSpindleParams(spindle, fastestPos, slowestPos, fastestNeg, slowestNeg, searchVel, homeSequence, increment)
}

// --- Helpers ---

func parseAxisMask(coord string) int32 {
	if coord == "" {
		return 7 // XYZ default
	}
	var mask int32
	upper := strings.ToUpper(coord)
	for _, c := range upper {
		switch c {
		case 'X':
			mask |= 1
		case 'Y':
			mask |= 2
		case 'Z':
			mask |= 4
		case 'A':
			mask |= 8
		case 'B':
			mask |= 16
		case 'C':
			mask |= 32
		case 'U':
			mask |= 64
		case 'V':
			mask |= 128
		case 'W':
			mask |= 256
		}
	}
	return mask
}

func axisCount(mask int32) int {
	n := 0
	for i := 0; i < 9; i++ {
		if mask&(1<<i) != 0 {
			n = i + 1
		}
	}
	return n
}

// minAxisAcc returns the minimum MAX_ACCELERATION across all active axes.
// Used as fallback when [TRAJ]MAX_LINEAR_ACCELERATION is not specified.
func minAxisAcc(ini *inifile.IniFile, axisMask int32) float64 {
	minAcc := 0.0
	for i := 0; i < 9; i++ {
		if axisMask&(1<<i) == 0 {
			continue
		}
		sec := axisSection(int32(i))
		acc := getFloatOrSection(ini, sec, "MAX_ACCELERATION", 0)
		if acc <= 0 {
			continue
		}
		if minAcc <= 0 || acc < minAcc {
			minAcc = acc
		}
	}
	if minAcc <= 0 {
		// Ultimate fallback — should not happen with a valid config.
		minAcc = 1.0
	}
	return minAcc
}

// minAxisVel returns the minimum MAX_VELOCITY across all active axes.
// Used as fallback when [TRAJ]MAX_LINEAR_VELOCITY is not specified.
func minAxisVel(ini *inifile.IniFile, axisMask int32) float64 {
	minVel := 0.0
	for i := 0; i < 9; i++ {
		if axisMask&(1<<i) == 0 {
			continue
		}
		sec := axisSection(int32(i))
		vel := getFloatOrSection(ini, sec, "MAX_VELOCITY", 0)
		if vel <= 0 {
			continue
		}
		if minVel <= 0 || vel < minVel {
			minVel = vel
		}
	}
	if minVel <= 0 {
		minVel = 1.0
	}
	return minVel
}

func axisSection(axis int32) string {
	names := []string{"AXIS_X", "AXIS_Y", "AXIS_Z", "AXIS_A", "AXIS_B", "AXIS_C", "AXIS_U", "AXIS_V", "AXIS_W"}
	if axis >= 0 && int(axis) < len(names) {
		return names[axis]
	}
	return "AXIS_X"
}

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

func parsePoseString(s string) Pose {
	var p Pose
	fields := strings.Fields(s)
	vals := make([]float64, len(fields))
	for i, f := range fields {
		vals[i] = parseFloat(f, 0)
	}
	if len(vals) > 0 {
		p.X = vals[0]
	}
	if len(vals) > 1 {
		p.Y = vals[1]
	}
	if len(vals) > 2 {
		p.Z = vals[2]
	}
	if len(vals) > 3 {
		p.A = vals[3]
	}
	if len(vals) > 4 {
		p.B = vals[4]
	}
	if len(vals) > 5 {
		p.C = vals[5]
	}
	if len(vals) > 6 {
		p.U = vals[6]
	}
	if len(vals) > 7 {
		p.V = vals[7]
	}
	if len(vals) > 8 {
		p.W = vals[8]
	}
	return p
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func getFloatOr(ini *inifile.IniFile, section, key string, def float64) float64 {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	return parseFloat(s, def)
}

func getFloatOrSection(ini *inifile.IniFile, section, key string, def float64) float64 {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	return parseFloat(s, def)
}

func getIntOrSection(ini *inifile.IniFile, section, key string, def int) int {
	return getIntOr(ini, section, key, def)
}

func getBoolOrSection(ini *inifile.IniFile, section, key string, def bool) bool {
	s := strings.TrimSpace(strings.ToLower(ini.Get(section, key)))
	if s == "" {
		return def
	}
	switch s {
	case "1", "yes", "true":
		return true
	case "0", "no", "false":
		return false
	}
	return def
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
