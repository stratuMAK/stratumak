// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// pushRec records the trajectory-level calls on top of the joint/axis ones
// recMotionConfig already captures.
type pushRec struct {
	*recMotionConfig
	vel, velLimit, acc float64
	maxFeedOverride    float64
	arcBlend           []float64 // enable, fallback, depth, gapCycles, rampFreq, kinkRatio
	spindles           []int32
	activated          []int32
}

func newPushRec() *pushRec { return &pushRec{recMotionConfig: newRec()} }

func (r *pushRec) SetVel(v float64) error      { r.vel = v; return nil }
func (r *pushRec) SetVelLimit(v float64) error { r.velLimit = v; return nil }
func (r *pushRec) SetAcc(v float64) error      { r.acc = v; return nil }
func (r *pushRec) SetMaxFeedOverride(v float64) error {
	r.maxFeedOverride = v
	return nil
}
func (r *pushRec) SetupArcBlends(enable, fallback, depth, gap int32, ramp, kink float64) error {
	r.arcBlend = []float64{float64(enable), float64(fallback), float64(depth), float64(gap), ramp, kink}
	return nil
}
func (r *pushRec) JointActivate(j int32) error { r.activated = append(r.activated, j); return nil }
func (r *pushRec) SetSpindleParams(s int32, _, _, _, _, _ float64, _ int32, _ float64) error {
	r.spindles = append(r.spindles, s)
	return nil
}

const mmMachine = `[TRAJ]
COORDINATES = X Y Z
DEFAULT_LINEAR_VELOCITY = 200
MAX_LINEAR_VELOCITY = 800
DEFAULT_LINEAR_ACCELERATION = 2000
MAX_LINEAR_ACCELERATION = 4000
HOME = 1 2 3
[AXIS_X]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[AXIS_Y]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[AXIS_Z]
MAX_VELOCITY = 300
MAX_ACCELERATION = 3000
[JOINT_0]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[JOINT_1]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[JOINT_2]
MAX_VELOCITY = 300
MAX_ACCELERATION = 3000
`

func mustParse(t *testing.T, text string) *inifile.IniFile {
	t.Helper()
	ini, err := inifile.ParseString(text)
	if err != nil {
		t.Fatalf("parse ini: %v", err)
	}
	return ini
}

// The full push: trajectory limits, every joint activated, and the per-joint /
// per-axis maxima handed back in Result — the values a caller clamps its own
// commands to, so a silent zero here would surface as a machine that refuses to
// move rather than as a config error.
func TestPush_MetricMachine(t *testing.T) {
	rec := newPushRec()
	res, err := Push(mustParse(t, mmMachine), Options{
		NumJoints: 3, NumSpindles: 0, AxisMask: 7, LinearUnits: 1.0,
	}, rec)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !closeTo(rec.velLimit, 800) || !closeTo(rec.vel, 200) {
		t.Errorf("traj vel = %g (limit %g), want 200 (limit 800)", rec.vel, rec.velLimit)
	}
	if !closeTo(rec.acc, 2000) {
		t.Errorf("traj acc = %g, want 2000", rec.acc)
	}
	if !closeTo(res.MaxVelocity, 800) || !closeTo(res.MaxAcceleration, 4000) {
		t.Errorf("result limits = %g/%g, want 800/4000", res.MaxVelocity, res.MaxAcceleration)
	}
	if len(rec.activated) != 3 {
		t.Errorf("activated joints = %v, want all three", rec.activated)
	}
	if !closeTo(res.JointMaxVel[2], 300) || !closeTo(res.AxisMaxVel[2], 300) || !closeTo(res.AxisMaxAcc[2], 3000) {
		t.Errorf("Z maxima = joint %g / axis %g,%g; want 300 / 300,3000",
			res.JointMaxVel[2], res.AxisMaxVel[2], res.AxisMaxAcc[2])
	}
	// [TRAJ]HOME reaches motion as a pose.
	if !closeTo(rec.worldHome.X, 1) || !closeTo(rec.worldHome.Z, 3) {
		t.Errorf("world home = %+v, want X=1 Z=3", rec.worldHome)
	}
	// ARC_BLEND_ENABLE defaults ON: without the push the TP silently falls back
	// to parabolic blending.
	if len(rec.arcBlend) != 6 || rec.arcBlend[0] != 1 || rec.arcBlend[2] != 50 {
		t.Errorf("arc blends = %v, want enable=1 depth=50 defaults", rec.arcBlend)
	}
	// No [DISPLAY]MAX_FEED_OVERRIDE: the ceiling is 1.0, not 0 — a zero would
	// pin every feed override to a standstill.
	if !closeTo(rec.maxFeedOverride, 1.0) {
		t.Errorf("max feed override = %g, want 1.0", rec.maxFeedOverride)
	}
	// NumSpindles = 0 (a pick-and-place machine): no spindle is configured.
	if len(rec.spindles) != 0 {
		t.Errorf("spindles pushed = %v, want none", rec.spindles)
	}
}

// On an inch machine every length-dimensioned trajectory value must reach
// motion in mm — the controller works in mm, and a 25.4x error here is a
// machine that runs 25.4x slow (or, on limits, refuses legal moves).
func TestPush_InchTrajScaledToMM(t *testing.T) {
	rec := newPushRec()
	res, err := Push(mustParse(t, `[TRAJ]
COORDINATES = X Y Z
DEFAULT_LINEAR_VELOCITY = 10
MAX_LINEAR_VELOCITY = 40
MAX_LINEAR_ACCELERATION = 100
HOME = 1 0 0
`), Options{NumJoints: 0, AxisMask: 7, LinearUnits: 1.0 / inch}, rec)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !closeTo(rec.velLimit, 40*inch) || !closeTo(rec.vel, 10*inch) {
		t.Errorf("vel = %g (limit %g), want %g (limit %g)", rec.vel, rec.velLimit, 10*inch, 40*inch)
	}
	if !closeTo(res.MaxAcceleration, 100*inch) {
		t.Errorf("max acc = %g, want %g", res.MaxAcceleration, 100*inch)
	}
	if !closeTo(rec.worldHome.X, 1*inch) {
		t.Errorf("world home X = %g, want %g", rec.worldHome.X, 1*inch)
	}
}

// Without an explicit [TRAJ] limit the fallback is the slowest active axis —
// and it must be scaled after the fallback, not before, since [AXIS_*] values
// are machine units too.
func TestPush_LimitsFallBackToSlowestAxis(t *testing.T) {
	rec := newPushRec()
	res, err := Push(mustParse(t, `[TRAJ]
COORDINATES = X Y Z
[AXIS_X]
MAX_VELOCITY = 800
MAX_ACCELERATION = 4000
[AXIS_Y]
MAX_VELOCITY = 500
MAX_ACCELERATION = 2500
[AXIS_Z]
MAX_VELOCITY = 300
MAX_ACCELERATION = 3000
`), Options{NumJoints: 0, AxisMask: 7, LinearUnits: 1.0}, rec)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !closeTo(res.MaxVelocity, 300) {
		t.Errorf("max vel = %g, want 300 (slowest axis)", res.MaxVelocity)
	}
	if !closeTo(res.MaxAcceleration, 2500) {
		t.Errorf("max acc = %g, want 2500 (lowest axis accel)", res.MaxAcceleration)
	}
	// DEFAULT_* absent: the default velocity is 1 (the C default) and the
	// default acceleration follows the limit.
	if !closeTo(rec.acc, 2500) {
		t.Errorf("default acc = %g, want the limit 2500", rec.acc)
	}
}

// A masked-out axis is never configured: pushing AXIS_A limits on a machine
// whose COORDINATES has no A would apply a section nobody asked for.
func TestPush_SkipsAxesOutsideMask(t *testing.T) {
	rec := newPushRec()
	if _, err := Push(mustParse(t, `[TRAJ]
COORDINATES = X Z
[AXIS_X]
MAX_VELOCITY = 100
[AXIS_Y]
MAX_VELOCITY = 200
[AXIS_Z]
MAX_VELOCITY = 300
`), Options{NumJoints: 0, AxisMask: ParseAxisMask("X Z"), LinearUnits: 1.0}, rec); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, ok := rec.axisVel[1]; ok { // Y
		t.Errorf("axis Y configured despite being outside COORDINATES")
	}
	if _, ok := rec.axisVel[0]; !ok {
		t.Errorf("axis X not configured")
	}
	if _, ok := rec.axisVel[2]; !ok {
		t.Errorf("axis Z not configured")
	}
}
