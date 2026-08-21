// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package motsetup pushes a machine's INI configuration into a motmod instance.
//
// The mapping from [TRAJ], [JOINT_n], [AXIS_*] and [SPINDLE_n] onto the motctl
// configuration calls is a property of the *machine*, not of any one task
// implementation: every module that owns a motion stack has to perform exactly
// this push before it can command a single move. milltask did it first
// (internal/task/config.go); pnptask needs the same push for the same reasons,
// so the mapping lives here instead of in two copies that drift apart — the
// kind of drift where an inch machine's soft limits end up 25.4x tight in one
// module and correct in the other.
//
// Units: every length-dimensioned value is converted from the machine's
// configured linear units to the millimetres the motion controller works in
// (Options.LinearUnits is machine-units-per-mm, so mm = value/LinearUnits).
// Angular joints and the A/B/C axes are already in degrees and are left alone.
//
// What is deliberately NOT here: everything that is task policy rather than
// machine configuration — the interpreter's startup code, the canon's modal
// units, the tool-change position, the MDI queue depth. Those stay with the
// module that has an interpreter.
package motsetup

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// MaxJoints and MaxAxes bound the fixed-size Result arrays; they match the
// motion controller's own limits (motstat.MAX_JOINTS / MAX_AXIS).
const (
	MaxJoints = 16
	MaxAxes   = 9
)

// Pose is the motion controller's 9-coordinate pose.
type Pose = motctl.Pose

// MotionConfig is the subset of motctl used at init time to configure joints,
// axes, spindles and trajectory parameters from INI values.
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

// Options describes the machine the push applies to. The caller parses these
// four values itself because it needs them anyway (and validates them to its
// own taste — milltask defaults a missing [KINS]JOINTS, pnptask refuses a
// [TRAJ]COORDINATES without X, Y and Z).
type Options struct {
	// NumJoints is how many [JOINT_n] sections are configured and activated.
	NumJoints int
	// NumSpindles is how many [SPINDLE_n] sections to push. Zero skips the
	// spindle push entirely — a pick-and-place machine has no spindle, and
	// pushing defaults for one would be configuration nobody wrote.
	NumSpindles int
	// AxisMask is the [TRAJ]COORDINATES bitmask (X=1, Y=2, Z=4, A=8, …).
	AxisMask int32
	// LinearUnits is machine units per mm: 1.0 for a metric machine, 1/25.4
	// for an inch one.
	LinearUnits float64
}

// HomingParams are the INI-fixed homing parameters that are NOT exposed as
// runtime HAL pins. Callers cache them so a HAL-driven home/offset/sequence
// change can re-push them unchanged instead of zeroing them (milltask's
// inihal).
type HomingParams struct {
	FinalVel, SearchVel, LatchVel float64
	Flags, VolatileHome           int32
}

// Result carries the derived values the caller needs after the push: the
// trajectory limits it must clamp its own commands to, and the per-joint and
// per-axis maxima used for jog clamping and per-move vel/acc blending. They are
// returned rather than re-read from the INI because they are the *converted*
// (mm) values, and re-deriving them is exactly how the two unit systems drift.
type Result struct {
	MaxVelocity     float64
	MaxAcceleration float64

	JointLinear [MaxJoints]bool         // per-joint linearity ([JOINT_n]TYPE)
	JointMaxVel [MaxJoints]float64      // per-joint max velocity, for jog clamping
	JointHoming [MaxJoints]HomingParams // INI-fixed homing params, for re-push

	AxisMaxVel [MaxAxes]float64
	AxisMaxAcc [MaxAxes]float64

	// The pushed axis position limits (mm for linear axes). Kept so a caller
	// that generates its own motion can refuse an out-of-range target with a
	// descriptive error before dispatch — motion rejects such a command, but
	// only with a bare command status.
	AxisMinPos [MaxAxes]float64
	AxisMaxPos [MaxAxes]float64
}

// Push reads the machine sections of ini and configures mc: trajectory limits
// and the arc-blend optimizer, then every joint, axis and spindle. It is called
// once at startup, before any motion is commanded.
func Push(ini *inifile.IniFile, opts Options, mc MotionConfig) (*Result, error) {
	res := &Result{}
	u := units(opts.LinearUnits)

	if err := pushTraj(ini, opts, u, mc, res); err != nil {
		return nil, fmt.Errorf("traj config: %w", err)
	}
	for j := int32(0); j < int32(opts.NumJoints) && int(j) < MaxJoints; j++ {
		linear, err := pushJoint(ini, j, u, mc, res)
		if err != nil {
			return nil, fmt.Errorf("joint %d config: %w", j, err)
		}
		// Cached for jog clamping (matches C JointConfig[].MaxVel), converted
		// machine->mm for linear joints.
		section := fmt.Sprintf("JOINT_%d", j)
		res.JointMaxVel[j] = u.toMMLinear(getFloatOr(ini, section, "MAX_VELOCITY", 1.0), linear)
	}
	numAxes := AxisCount(opts.AxisMask)
	for a := int32(0); a < int32(numAxes) && int(a) < MaxAxes; a++ {
		if opts.AxisMask&(1<<a) == 0 {
			continue
		}
		if err := pushAxis(ini, a, u, mc, res); err != nil {
			return nil, fmt.Errorf("axis %d config: %w", a, err)
		}
		// Cached for jog clamping and the canon's per-move vel/acc blending
		// (matches C AxisConfig[].MaxVel/MaxAcc). Converted machine->mm for
		// linear axes; angular (A/B/C) left in degrees.
		axSection := AxisSection(a)
		axLinear := AxisIsLinear(a)
		res.AxisMaxVel[a] = u.toMMLinear(getFloatOr(ini, axSection, "MAX_VELOCITY", 1.0), axLinear)
		res.AxisMaxAcc[a] = u.toMMLinear(getFloatOr(ini, axSection, "MAX_ACCELERATION", 1.0), axLinear)
	}
	for s := int32(0); s < int32(opts.NumSpindles); s++ {
		if err := pushSpindle(ini, s, mc); err != nil {
			return nil, fmt.Errorf("spindle %d config: %w", s, err)
		}
	}
	return res, nil
}

// pushTraj configures the trajectory planner: velocity and acceleration
// defaults and limits, the feed-override ceiling, probe error inhibits, the
// arc-blend optimizer and the world home position.
func pushTraj(ini *inifile.IniFile, opts Options, u units, mc MotionConfig, res *Result) error {
	// Velocities. TRAJ velocity limits are linear (the "..._LINEAR_..." keys),
	// so convert machine-units->mm to match the mm-internal motion controller.
	// The minAxisVel fallback also returns machine units, so scale after it.
	defaultVel := getFloatOr(ini, "TRAJ", "DEFAULT_LINEAR_VELOCITY",
		getFloatOr(ini, "TRAJ", "DEFAULT_VELOCITY", 1.0))
	maxVel := getFloatOr(ini, "TRAJ", "MAX_LINEAR_VELOCITY",
		getFloatOr(ini, "TRAJ", "MAX_VELOCITY", 0))
	if maxVel <= 0 {
		maxVel = minAxisVel(ini, opts.AxisMask)
	}
	defaultVel = u.toMM(defaultVel)
	maxVel = u.toMM(maxVel)
	res.MaxVelocity = maxVel
	if err := mc.SetVelLimit(maxVel); err != nil {
		return err
	}
	if err := mc.SetVel(clamp(defaultVel, 0, maxVel)); err != nil {
		return err
	}

	// Acceleration.
	// If no explicit [TRAJ] acceleration limit, derive from the minimum of
	// [AXIS_*]MAX_ACCELERATION for active axes.  Using 1e99 as a sentinel
	// causes catastrophic floating-point cancellation in the TP's trapezoidal
	// velocity planner (sqrt(B²+C) - B ≈ 0 when B is huge).
	defaultAcc := getFloatOr(ini, "TRAJ", "DEFAULT_LINEAR_ACCELERATION",
		getFloatOr(ini, "TRAJ", "DEFAULT_ACCELERATION", 0))
	maxAcc := getFloatOr(ini, "TRAJ", "MAX_LINEAR_ACCELERATION",
		getFloatOr(ini, "TRAJ", "MAX_ACCELERATION", 0))
	if maxAcc <= 0 {
		maxAcc = minAxisAcc(ini, opts.AxisMask)
	}
	if defaultAcc <= 0 {
		defaultAcc = maxAcc
	}
	// TRAJ acceleration limits are linear too: convert machine-units->mm.
	defaultAcc = u.toMM(defaultAcc)
	maxAcc = u.toMM(maxAcc)
	res.MaxAcceleration = maxAcc
	if err := mc.SetAcc(clamp(defaultAcc, 0, maxAcc)); err != nil {
		return err
	}

	// Max feed override.
	maxFeedScale := getFloatOr(ini, "DISPLAY", "MAX_FEED_OVERRIDE", 1.0)
	if err := mc.SetMaxFeedOverride(maxFeedScale); err != nil {
		return err
	}

	// Probe error inhibit.
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
	if homeStr := ini.Get("TRAJ", "HOME"); homeStr != "" {
		if err := mc.SetWorldHome(u.poseToMM(ParsePoseString(homeStr))); err != nil {
			return err
		}
	}
	return nil
}

// pushJoint configures one joint and activates it. It reports the joint's
// linearity so the caller can convert its own per-joint values the same way.
func pushJoint(ini *inifile.IniFile, joint int32, u units, mc MotionConfig, res *Result) (bool, error) {
	section := fmt.Sprintf("JOINT_%d", joint)

	// Linearity: LINEAR joints have length-dimensioned config that must be
	// converted machine-units->mm to match the mm-internal motion controller.
	// ANGULAR (rotary) joints are in degrees and are left unscaled.
	linear := JointTypeIsLinear(ini.Get(section, "TYPE"))
	if joint >= 0 && int(joint) < MaxJoints {
		res.JointLinear[joint] = linear
	}

	// Position limits (INI machine units -> internal mm, matching move targets).
	minLimit := u.toMMLinear(getFloatOr(ini, section, "MIN_LIMIT", -1e99), linear)
	maxLimit := u.toMMLinear(getFloatOr(ini, section, "MAX_LIMIT", 1e99), linear)
	if err := mc.SetJointPositionLimits(joint, minLimit, maxLimit); err != nil {
		return linear, err
	}

	// Backlash (length)
	backlash := u.toMMLinear(getFloatOr(ini, section, "BACKLASH", 0), linear)
	if err := mc.SetJointBacklash(joint, backlash); err != nil {
		return linear, err
	}

	// Following error (length)
	ferror := u.toMMLinear(getFloatOr(ini, section, "FERROR", 1), linear)
	if err := mc.SetJointMaxFerror(joint, ferror); err != nil {
		return linear, err
	}
	minFerror := u.toMMLinear(getFloatOr(ini, section, "MIN_FERROR", ferror), linear)
	if err := mc.SetJointMinFerror(joint, minFerror); err != nil {
		return linear, err
	}

	// Homing. HOME/HOME_OFFSET are positions (length); the *_VEL are velocities.
	// finalVel default -1 is a "use joint max vel" sentinel; scaling preserves
	// its sign so the sentinel semantics are unchanged.
	home := u.toMMLinear(getFloatOr(ini, section, "HOME", 0), linear)
	offset := u.toMMLinear(getFloatOr(ini, section, "HOME_OFFSET", 0), linear)
	searchVel := u.toMMLinear(getFloatOr(ini, section, "HOME_SEARCH_VEL", 0), linear)
	latchVel := u.toMMLinear(getFloatOr(ini, section, "HOME_LATCH_VEL", 0), linear)
	finalVel := u.toMMLinear(getFloatOr(ini, section, "HOME_FINAL_VEL", -1), linear)
	useIndex := getIntOr(ini, section, "HOME_USE_INDEX", 0)
	noEncoderReset := getIntOr(ini, section, "HOME_INDEX_NO_ENCODER_RESET", 0)
	ignoreLimits := getBoolOr(ini, section, "HOME_IGNORE_LIMITS", false)
	isShared := getBoolOr(ini, section, "HOME_IS_SHARED", false)
	sequence := getIntOr(ini, section, "HOME_SEQUENCE", 999)
	volatileHome := getIntOr(ini, section, "VOLATILE_HOME", 0)
	lockingIndexer := getIntOr(ini, section, "LOCKING_INDEXER", 0)
	absoluteEncoder := getIntOr(ini, section, "HOME_ABSOLUTE_ENCODER", 0)

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
		return linear, err
	}
	// Cache the INI-fixed params so a runtime HAL home/offset/seq change doesn't
	// zero them on re-push (milltask's inihal).
	if joint >= 0 && int(joint) < MaxJoints {
		res.JointHoming[joint] = HomingParams{
			FinalVel: finalVel, SearchVel: searchVel, LatchVel: latchVel,
			Flags: flags, VolatileHome: int32(volatileHome),
		}
	}

	// Velocity, acceleration and jerk (all length-dimensioned for linear joints)
	maxVel := u.toMMLinear(getFloatOr(ini, section, "MAX_VELOCITY", 1.0), linear)
	if err := mc.SetJointVelLimit(joint, maxVel); err != nil {
		return linear, err
	}
	maxAcc := u.toMMLinear(getFloatOr(ini, section, "MAX_ACCELERATION", 1.0), linear)
	if err := mc.SetJointAccLimit(joint, maxAcc); err != nil {
		return linear, err
	}
	maxJerk := u.toMMLinear(getFloatOr(ini, section, "MAX_JERK", 0.0), linear)
	if maxJerk > 0.0 {
		if err := mc.SetJointJerkLimit(joint, maxJerk); err != nil {
			return linear, err
		}
	}

	// Leadscrew / screw-error compensation ([JOINT_n]COMP_FILE). Loaded before
	// activation, matching C++ which pushes the table to motion at startup.
	// The triplets are lengths in machine units like every other value in this
	// section, so they get the same conversion on the way to the mm-internal
	// controller. (The C++ path and the milltask port this was extracted from
	// pushed them raw — inch-machine compensation was silently 25.4x off.)
	if compFile := ini.Get(section, "COMP_FILE"); compFile != "" {
		compType := getIntOr(ini, section, "COMP_FILE_TYPE", 0)
		setComp := func(j int32, nominal, fwd, rev float64) error {
			return mc.SetJointComp(j,
				u.toMMLinear(nominal, linear),
				u.toMMLinear(fwd, linear),
				u.toMMLinear(rev, linear))
		}
		if err := LoadJointComp(joint, compFile, compType, setComp); err != nil {
			return linear, fmt.Errorf("COMP_FILE %q: %w", compFile, err)
		}
	}

	// Activate
	return linear, mc.JointActivate(joint)
}

func pushAxis(ini *inifile.IniFile, axis int32, u units, mc MotionConfig, res *Result) error {
	section := AxisSection(axis)

	// Linearity by axis letter/index: X,Y,Z,U,V,W are linear (length, scaled to
	// mm); A,B,C are angular (degrees, unscaled).
	linear := AxisIsLinear(axis)

	// Position limits (INI machine units -> internal mm, matching move targets).
	minLimit := u.toMMLinear(getFloatOr(ini, section, "MIN_LIMIT", -1e99), linear)
	maxLimit := u.toMMLinear(getFloatOr(ini, section, "MAX_LIMIT", 1e99), linear)
	if err := mc.SetAxisPositionLimits(axis, minLimit, maxLimit); err != nil {
		return err
	}
	res.AxisMinPos[axis] = minLimit
	res.AxisMaxPos[axis] = maxLimit

	// Ext offset ratio (reduces available vel/acc for the axis proper)
	avRatio := getFloatOr(ini, section, "OFFSET_AV_RATIO", 0)
	if avRatio < 0 || avRatio > 0.9 {
		avRatio = 0.1
	}

	// Velocity (length/s for linear axes)
	maxVel := u.toMMLinear(getFloatOr(ini, section, "MAX_VELOCITY", 1.0), linear)
	if err := mc.SetAxisVelLimit(axis, (1-avRatio)*maxVel, avRatio*maxVel); err != nil {
		return err
	}

	// Acceleration (length/s^2 for linear axes)
	maxAcc := u.toMMLinear(getFloatOr(ini, section, "MAX_ACCELERATION", 1.0), linear)
	if err := mc.SetAxisAccLimit(axis, (1-avRatio)*maxAcc, avRatio*maxAcc); err != nil {
		return err
	}

	// Locking indexer
	lockingJoint := getIntOr(ini, section, "LOCKING_INDEXER_JOINT", -1)
	return mc.SetAxisLockingJoint(axis, int32(lockingJoint))
}

func pushSpindle(ini *inifile.IniFile, spindle int32, mc MotionConfig) error {
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
		slowestNeg = -abs(v)
	}
	if s := ini.Get(section, "MAX_REVERSE_VELOCITY"); s != "" {
		v := parseFloat(s, 1e99)
		fastestNeg = -abs(v)
	}

	homeSequence := int32(getIntOr(ini, section, "HOME_SEQUENCE", 0))
	searchVel := getFloatOr(ini, section, "HOME_SEARCH_VELOCITY", 0)
	increment := getFloatOr(ini, section, "INCREMENT", 100)

	return mc.SetSpindleParams(spindle, fastestPos, slowestPos, fastestNeg, slowestNeg, searchVel, homeSequence, increment)
}
