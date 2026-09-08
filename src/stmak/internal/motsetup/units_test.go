// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// recMotionConfig records the length-dimensioned values pushed to motion so a
// test can assert the machine-units->mm conversion. It embeds noopMotionConfig
// for the methods it does not care about.
type recMotionConfig struct {
	noopMotionConfig
	jointMin, jointMax                  map[int32]float64
	jointBacklash, jointFerror          map[int32]float64
	jointMinFerror, jointVel            map[int32]float64
	jointAcc, jointJerk                 map[int32]float64
	homeOffset, home                    map[int32]float64
	homeFinalVel, homeSearch, homeLatch map[int32]float64
	axisMin, axisMax                    map[int32]float64
	axisVel, axisAcc                    map[int32]float64
	worldHome                           Pose
	trajVelLimit, trajAcc               float64
	jointComp                           []compTriplet
}

func newRec() *recMotionConfig {
	return &recMotionConfig{
		jointMin: map[int32]float64{}, jointMax: map[int32]float64{},
		jointBacklash: map[int32]float64{}, jointFerror: map[int32]float64{},
		jointMinFerror: map[int32]float64{}, jointVel: map[int32]float64{},
		jointAcc: map[int32]float64{}, jointJerk: map[int32]float64{},
		homeOffset: map[int32]float64{}, home: map[int32]float64{},
		homeFinalVel: map[int32]float64{}, homeSearch: map[int32]float64{},
		homeLatch: map[int32]float64{},
		axisMin:   map[int32]float64{}, axisMax: map[int32]float64{},
		axisVel: map[int32]float64{}, axisAcc: map[int32]float64{},
	}
}

func (r *recMotionConfig) SetJointPositionLimits(j int32, min, max float64) error {
	r.jointMin[j], r.jointMax[j] = min, max
	return nil
}
func (r *recMotionConfig) SetJointBacklash(j int32, v float64) error {
	r.jointBacklash[j] = v
	return nil
}
func (r *recMotionConfig) SetJointMaxFerror(j int32, v float64) error {
	r.jointFerror[j] = v
	return nil
}
func (r *recMotionConfig) SetJointMinFerror(j int32, v float64) error {
	r.jointMinFerror[j] = v
	return nil
}
func (r *recMotionConfig) SetJointVelLimit(j int32, v float64) error  { r.jointVel[j] = v; return nil }
func (r *recMotionConfig) SetJointAccLimit(j int32, v float64) error  { r.jointAcc[j] = v; return nil }
func (r *recMotionConfig) SetJointJerkLimit(j int32, v float64) error { r.jointJerk[j] = v; return nil }
func (r *recMotionConfig) SetJointHomingParams(j int32, offset, home, finalVel, searchVel, latchVel float64, flags, seq, vol int32) error {
	r.homeOffset[j], r.home[j] = offset, home
	r.homeFinalVel[j], r.homeSearch[j], r.homeLatch[j] = finalVel, searchVel, latchVel
	return nil
}
func (r *recMotionConfig) SetAxisPositionLimits(a int32, min, max float64) error {
	r.axisMin[a], r.axisMax[a] = min, max
	return nil
}
func (r *recMotionConfig) SetAxisVelLimit(a int32, vel, ext float64) error {
	r.axisVel[a] = vel
	return nil
}
func (r *recMotionConfig) SetAxisAccLimit(a int32, acc, ext float64) error {
	r.axisAcc[a] = acc
	return nil
}
func (r *recMotionConfig) SetJointComp(_ int32, nom, fwd, rev float64) error {
	r.jointComp = append(r.jointComp, compTriplet{nom, fwd, rev})
	return nil
}
func (r *recMotionConfig) SetWorldHome(p Pose) error   { r.worldHome = p; return nil }
func (r *recMotionConfig) SetVelLimit(v float64) error { r.trajVelLimit = v; return nil }
func (r *recMotionConfig) SetAcc(v float64) error      { r.trajAcc = v; return nil }

const inch = 25.4

// inchUnits is the scale of an inch machine: machine units per mm.
const inchUnits = units(1.0 / inch)

func closeTo(got, want float64) bool {
	if math.Abs(want) > 1e50 { // sentinel (±1e99): just check same order of magnitude/sign
		return math.Signbit(got) == math.Signbit(want) && math.Abs(got) > 1e50
	}
	return math.Abs(got-want) < 1e-6*math.Max(1, math.Abs(want))
}

// A LINEAR joint on an inch machine must have every length/vel/accel/jerk value
// converted machine-units->mm (x25.4) before being pushed to motion.
func TestPushJoint_InchLinearScaledToMM(t *testing.T) {
	ini, err := inifile.ParseString(`[JOINT_0]
TYPE=LINEAR
MIN_LIMIT=-4
MAX_LIMIT=4
BACKLASH=0.001
FERROR=0.05
MIN_FERROR=0.01
MAX_VELOCITY=2
MAX_ACCELERATION=20
MAX_JERK=100
HOME=1
HOME_OFFSET=0.5
HOME_SEARCH_VEL=0.2
HOME_LATCH_VEL=0.1
HOME_FINAL_VEL=0.3
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res := &Result{}
	rec := newRec()
	if _, err := pushJoint(ini, 0, inchUnits, rec, res); err != nil {
		t.Fatalf("pushJoint: %v", err)
	}
	if !res.JointLinear[0] {
		t.Fatalf("JointLinear[0] = false, want true")
	}
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"min_limit", rec.jointMin[0], -4 * inch},
		{"max_limit", rec.jointMax[0], 4 * inch},
		{"backlash", rec.jointBacklash[0], 0.001 * inch},
		{"ferror", rec.jointFerror[0], 0.05 * inch},
		{"min_ferror", rec.jointMinFerror[0], 0.01 * inch},
		{"max_velocity", rec.jointVel[0], 2 * inch},
		{"max_acceleration", rec.jointAcc[0], 20 * inch},
		{"max_jerk", rec.jointJerk[0], 100 * inch},
		{"home", rec.home[0], 1 * inch},
		{"home_offset", rec.homeOffset[0], 0.5 * inch},
		{"home_search_vel", rec.homeSearch[0], 0.2 * inch},
		{"home_latch_vel", rec.homeLatch[0], 0.1 * inch},
		{"home_final_vel", rec.homeFinalVel[0], 0.3 * inch},
	}
	for _, c := range checks {
		if !closeTo(c.got, c.want) {
			t.Errorf("%s = %g, want %g (x25.4)", c.name, c.got, c.want)
		}
	}
}

// COMP_FILE triplets are lengths in machine units like everything else in the
// joint section, and get the same conversion — the milltask code this package
// was extracted from pushed them raw, leaving inch-machine leadscrew
// compensation silently 25.4x off.
func TestPushJoint_InchCompFileScaledToMM(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	// Type 1: the values are trims and pass through LoadJointComp unchanged,
	// so what the recorder sees is exactly the file times the conversion.
	if err := os.WriteFile(filepath.Join(dir, "comp.txt"), []byte("1.0 0.002 -0.003\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ini, err := inifile.ParseString(`[JOINT_0]
TYPE=LINEAR
COMP_FILE=comp.txt
COMP_FILE_TYPE=1
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res := &Result{}
	rec := newRec()
	if _, err := pushJoint(ini, 0, inchUnits, rec, res); err != nil {
		t.Fatalf("pushJoint: %v", err)
	}
	want := []compTriplet{{1.0 * inch, 0.002 * inch, -0.003 * inch}}
	if !approxComp(rec.jointComp, want) {
		t.Errorf("comp triplets = %v, want %v (x25.4)", rec.jointComp, want)
	}
}

// An ANGULAR joint must NOT be scaled — its values are already in degrees.
func TestPushJoint_InchAngularNotScaled(t *testing.T) {
	ini, err := inifile.ParseString(`[JOINT_3]
TYPE=ANGULAR
MIN_LIMIT=-360
MAX_LIMIT=360
MAX_VELOCITY=30
MAX_ACCELERATION=100
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res := &Result{}
	rec := newRec()
	if _, err := pushJoint(ini, 3, inchUnits, rec, res); err != nil {
		t.Fatalf("pushJoint: %v", err)
	}
	if res.JointLinear[3] {
		t.Fatalf("JointLinear[3] = true, want false (ANGULAR)")
	}
	if !closeTo(rec.jointVel[3], 30) || !closeTo(rec.jointAcc[3], 100) {
		t.Errorf("angular vel/acc = %g/%g, want 30/100 unscaled", rec.jointVel[3], rec.jointAcc[3])
	}
	if !closeTo(rec.jointMax[3], 360) {
		t.Errorf("angular max_limit = %g, want 360 unscaled", rec.jointMax[3])
	}
}

// Axis linearity follows the axis letter/index: Z (index 2) is linear, A (3) is not.
func TestPushAxis_LinearVsAngular(t *testing.T) {
	ini, err := inifile.ParseString(`[AXIS_Z]
MIN_LIMIT=-4
MAX_LIMIT=4
MAX_VELOCITY=2
MAX_ACCELERATION=20
[AXIS_A]
MIN_LIMIT=-360
MAX_LIMIT=360
MAX_VELOCITY=30
MAX_ACCELERATION=100
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rec := newRec()
	if err := pushAxis(ini, 2, inchUnits, rec, &Result{}); err != nil { // Z
		t.Fatalf("pushAxis Z: %v", err)
	}
	if err := pushAxis(ini, 3, inchUnits, rec, &Result{}); err != nil { // A
		t.Fatalf("pushAxis A: %v", err)
	}
	// Z (linear) scaled
	if !closeTo(rec.axisMax[2], 4*inch) || !closeTo(rec.axisVel[2], 2*inch) || !closeTo(rec.axisAcc[2], 20*inch) {
		t.Errorf("Z axis not scaled: max=%g vel=%g acc=%g", rec.axisMax[2], rec.axisVel[2], rec.axisAcc[2])
	}
	// A (angular) unscaled
	if !closeTo(rec.axisMax[3], 360) || !closeTo(rec.axisVel[3], 30) || !closeTo(rec.axisAcc[3], 100) {
		t.Errorf("A axis wrongly scaled: max=%g vel=%g acc=%g", rec.axisMax[3], rec.axisVel[3], rec.axisAcc[3])
	}
}

func TestAxisIsLinear(t *testing.T) {
	linear := map[int32]bool{0: true, 1: true, 2: true, 3: false, 4: false, 5: false, 6: true, 7: true, 8: true}
	for idx, want := range linear {
		if got := AxisIsLinear(idx); got != want {
			t.Errorf("AxisIsLinear(%d) = %v, want %v", idx, got, want)
		}
	}
}

func TestJointTypeIsLinear(t *testing.T) {
	cases := map[string]bool{"": true, "LINEAR": true, "linear": true, "ANGULAR": false, "angular": false, " ANGULAR ": false}
	for in, want := range cases {
		if got := JointTypeIsLinear(in); got != want {
			t.Errorf("JointTypeIsLinear(%q) = %v, want %v", in, got, want)
		}
	}
}
