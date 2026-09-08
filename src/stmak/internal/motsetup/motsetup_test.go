// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// noopMotionConfig is a do-nothing MotionConfig for exercising loadJoint.
type noopMotionConfig struct{}

func (noopMotionConfig) SetVel(float64) error                  { return nil }
func (noopMotionConfig) SetVelLimit(float64) error             { return nil }
func (noopMotionConfig) SetAcc(float64) error                  { return nil }
func (noopMotionConfig) SetMaxFeedOverride(float64) error      { return nil }
func (noopMotionConfig) SetWorldHome(Pose) error               { return nil }
func (noopMotionConfig) SetProbeErrInhibit(int32, int32) error { return nil }
func (noopMotionConfig) SetupArcBlends(int32, int32, int32, int32, float64, float64) error {
	return nil
}
func (noopMotionConfig) JointActivate(int32) error                            { return nil }
func (noopMotionConfig) SetJointPositionLimits(int32, float64, float64) error { return nil }
func (noopMotionConfig) SetJointBacklash(int32, float64) error                { return nil }
func (noopMotionConfig) SetJointMaxFerror(int32, float64) error               { return nil }
func (noopMotionConfig) SetJointMinFerror(int32, float64) error               { return nil }
func (noopMotionConfig) SetJointVelLimit(int32, float64) error                { return nil }
func (noopMotionConfig) SetJointAccLimit(int32, float64) error                { return nil }
func (noopMotionConfig) SetJointJerkLimit(int32, float64) error               { return nil }
func (noopMotionConfig) SetJointHomingParams(int32, float64, float64, float64, float64, float64, int32, int32, int32) error {
	return nil
}
func (noopMotionConfig) SetJointComp(int32, float64, float64, float64) error { return nil }
func (noopMotionConfig) SetAxisPositionLimits(int32, float64, float64) error { return nil }
func (noopMotionConfig) SetAxisVelLimit(int32, float64, float64) error       { return nil }
func (noopMotionConfig) SetAxisAccLimit(int32, float64, float64) error       { return nil }
func (noopMotionConfig) SetAxisLockingJoint(int32, int32) error              { return nil }
func (noopMotionConfig) SetSpindleParams(int32, float64, float64, float64, float64, float64, int32, float64) error {
	return nil
}

// pushJoint must cache the INI-fixed homing params so a later runtime HAL
// home/offset/seq change (milltask's inihal) can re-push them instead of
// zeroing them.
func TestPushJoint_CachesHomingParams(t *testing.T) {
	ini, err := inifile.ParseString(`[JOINT_0]
MIN_LIMIT=-10
MAX_LIMIT=10
MAX_VELOCITY=5
MAX_ACCELERATION=50
HOME=1.0
HOME_OFFSET=0.5
HOME_SEARCH_VEL=2.0
HOME_LATCH_VEL=0.5
HOME_FINAL_VEL=3.0
HOME_SEQUENCE=2
HOME_IGNORE_LIMITS=1
VOLATILE_HOME=1
`)
	if err != nil {
		t.Fatalf("parse ini: %v", err)
	}

	res := &Result{}
	if _, err := pushJoint(ini, 0, units(1.0), noopMotionConfig{}, res); err != nil {
		t.Fatalf("pushJoint: %v", err)
	}

	hp := res.JointHoming[0]
	if hp.FinalVel != 3.0 || hp.SearchVel != 2.0 || hp.LatchVel != 0.5 {
		t.Errorf("cached vels = %+v, want FinalVel=3 SearchVel=2 LatchVel=0.5", hp)
	}
	if hp.Flags&1 == 0 { // HOME_IGNORE_LIMITS = 1
		t.Errorf("cached flags = %d, want HOME_IGNORE_LIMITS bit set", hp.Flags)
	}
	if hp.VolatileHome != 1 {
		t.Errorf("cached volatileHome = %d, want 1", hp.VolatileHome)
	}
}
