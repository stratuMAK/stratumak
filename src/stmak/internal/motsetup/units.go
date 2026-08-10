// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"math"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// units converts INI values from the machine's configured linear units to the
// internal millimetres the motion controller works in. A task's canon emits
// move targets in mm, so every length handed to motion must be mm too — soft
// limits were the historical exception: passed straight from the INI in machine
// units, motion enforced them 25.4x too tight on an inch machine and rejected
// legal moves.
type units float64 // machine units per mm

// toMM converts a length-dimensioned value. A non-positive scale (an unset or
// nonsensical [TRAJ]LINEAR_UNITS) passes the value through rather than dividing
// by zero.
func (u units) toMM(v float64) float64 {
	if u <= 0 {
		return v
	}
	return v / float64(u)
}

// toMMLinear converts v iff linear; otherwise returns it unchanged. Angular
// (A/B/C) config is already in the internal degree units — the canon never
// unit-scales angular coordinates — so scaling it would be wrong.
func (u units) toMMLinear(v float64, linear bool) float64 {
	if !linear {
		return v
	}
	return u.toMM(v)
}

// poseToMM converts the linear coordinates (X,Y,Z,U,V,W) of a pose, leaving the
// angular ones (A,B,C) untouched.
func (u units) poseToMM(p Pose) Pose {
	p.X = u.toMM(p.X)
	p.Y = u.toMM(p.Y)
	p.Z = u.toMM(p.Z)
	p.U = u.toMM(p.U)
	p.V = u.toMM(p.V)
	p.W = u.toMM(p.W)
	return p
}

// AxisIsLinear reports whether the axis at the given index is a linear
// (length-dimensioned) axis. X,Y,Z,U,V,W (indices 0,1,2,6,7,8) are linear;
// A,B,C (3,4,5) are angular (degrees). Only linear config values are converted
// machine-units->mm.
func AxisIsLinear(index int32) bool {
	switch index {
	case 0, 1, 2, 6, 7, 8: // X Y Z U V W
		return true
	default: // 3,4,5 = A B C (angular); anything else defensively non-linear
		return false
	}
}

// JointTypeIsLinear parses a [JOINT_n]TYPE value. LINEAR (the default when
// unset) means length-dimensioned; ANGULAR means degrees. Matches the C config
// (emcmotcfg). A joint's linearity ultimately follows the axis it drives, but
// reading TYPE matches the C config and is simplest.
func JointTypeIsLinear(s string) bool {
	return !strings.EqualFold(strings.TrimSpace(s), "ANGULAR")
}

// ParseAxisMask turns a [TRAJ]COORDINATES string into the axis bitmask motion
// uses. An empty string defaults to XYZ, matching the C config.
func ParseAxisMask(coord string) int32 {
	if coord == "" {
		return 7 // XYZ default
	}
	var mask int32
	for _, c := range strings.ToUpper(coord) {
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

// AxisCount returns one past the highest axis index present in the mask — the
// loop bound for iterating configured axes.
func AxisCount(mask int32) int {
	n := 0
	for i := 0; i < MaxAxes; i++ {
		if mask&(1<<i) != 0 {
			n = i + 1
		}
	}
	return n
}

// AxisSection is the INI section name of an axis index.
func AxisSection(axis int32) string {
	names := []string{"AXIS_X", "AXIS_Y", "AXIS_Z", "AXIS_A", "AXIS_B", "AXIS_C", "AXIS_U", "AXIS_V", "AXIS_W"}
	if axis >= 0 && int(axis) < len(names) {
		return names[axis]
	}
	return "AXIS_X"
}

// ParsePoseString reads a whitespace-separated coordinate list ([TRAJ]HOME) in
// canonical XYZABCUVW order. Missing trailing coordinates stay zero.
func ParsePoseString(s string) Pose {
	var p Pose
	fields := strings.Fields(s)
	vals := make([]float64, len(fields))
	for i, f := range fields {
		vals[i] = parseFloat(f, 0)
	}
	set := []*float64{&p.X, &p.Y, &p.Z, &p.A, &p.B, &p.C, &p.U, &p.V, &p.W}
	for i, dst := range set {
		if i < len(vals) {
			*dst = vals[i]
		}
	}
	return p
}

// minAxisVel returns the minimum MAX_VELOCITY across all active axes. Used as
// the fallback when [TRAJ]MAX_LINEAR_VELOCITY is not specified.
func minAxisVel(ini *inifile.IniFile, axisMask int32) float64 {
	return minAxisValue(ini, axisMask, "MAX_VELOCITY")
}

// minAxisAcc returns the minimum MAX_ACCELERATION across all active axes. Used
// as the fallback when [TRAJ]MAX_LINEAR_ACCELERATION is not specified.
func minAxisAcc(ini *inifile.IniFile, axisMask int32) float64 {
	return minAxisValue(ini, axisMask, "MAX_ACCELERATION")
}

// minAxisValue is the shared body of the two fallbacks above. The ultimate 1.0
// fallback should not happen with a valid config, but a zero limit reaching the
// TP would stop the machine dead rather than merely running it slowly.
func minAxisValue(ini *inifile.IniFile, axisMask int32, key string) float64 {
	best := 0.0
	for i := 0; i < MaxAxes; i++ {
		if axisMask&(1<<i) == 0 {
			continue
		}
		v := getFloatOr(ini, AxisSection(int32(i)), key, 0)
		if v <= 0 {
			continue
		}
		if best <= 0 || v < best {
			best = v
		}
	}
	if best <= 0 {
		return 1.0
	}
	return best
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

func abs(v float64) float64 { return math.Abs(v) }
