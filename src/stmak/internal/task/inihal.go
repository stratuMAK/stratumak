// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"errors"
	"fmt"
	"math"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

const (
	maxJoints = 16
	maxAxes   = 9
	epsilon   = 0.00001
)

// iniHal holds the HAL component and pins for runtime INI parameter override.
// Pin values are read each cycle; changes are pushed to the motion controller.
//
// Units: all length-dimensioned pins (limits, velocities, accelerations, jerk,
// home/offset, backlash, ferror) are in stmak-internal millimeters, NOT the INI's
// machine units. That matches the rest of stratuMAK: the motion controller runs in mm
// (positions, feedback, gmi API), config.go converts the INI machine-unit values
// to mm before the initial push, and the traj pins here are initialized from the
// already-mm t.maxVelocity/t.maxAcceleration. Values read from these pins are
// therefore pushed to motion unchanged. A HAL file that drives these pins must
// supply mm (as it must for every other length in stratuMAK); do NOT wire raw
// machine-unit [SECTION]KEY substitutions into them.
type iniHal struct {
	comp *hal.Component

	// Traj pins
	trajDefaultVel *hal.Pin[float64]
	trajMaxVel     *hal.Pin[float64]
	trajDefaultAcc *hal.Pin[float64]
	trajMaxAcc     *hal.Pin[float64]

	// Arc blend pins
	arcBlendEnable           *hal.Pin[bool]
	arcBlendFallback         *hal.Pin[bool]
	arcBlendOptDepth         *hal.Pin[int32]
	arcBlendGapCycles        *hal.Pin[float64]
	arcBlendRampFreq         *hal.Pin[float64]
	arcBlendTangentKinkRatio *hal.Pin[float64]

	// Joint pins (indexed)
	jointBacklash   [maxJoints]*hal.Pin[float64]
	jointFerror     [maxJoints]*hal.Pin[float64]
	jointMinFerror  [maxJoints]*hal.Pin[float64]
	jointMinLimit   [maxJoints]*hal.Pin[float64]
	jointMaxLimit   [maxJoints]*hal.Pin[float64]
	jointMaxVel     [maxJoints]*hal.Pin[float64]
	jointMaxAcc     [maxJoints]*hal.Pin[float64]
	jointMaxJerk    [maxJoints]*hal.Pin[float64]
	jointHome       [maxJoints]*hal.Pin[float64]
	jointHomeOffset [maxJoints]*hal.Pin[float64]
	jointHomeSeq    [maxJoints]*hal.Pin[int32]

	// INI-fixed homing params (not HAL pins), cached from the Task so a
	// home/offset/seq change re-pushes them instead of zeroing them.
	jointHoming [maxJoints]jointHomingParams

	// Axis pins (indexed by axis letter position: x=0..w=8)
	axisMinLimit [maxAxes]*hal.Pin[float64]
	axisMaxLimit [maxAxes]*hal.Pin[float64]
	axisMaxVel   [maxAxes]*hal.Pin[float64]
	axisMaxAcc   [maxAxes]*hal.Pin[float64]

	// Cached previous values for change detection
	old iniHalValues

	numJoints int
}

// iniHalValues stores the last-read values for change detection.
type iniHalValues struct {
	trajDefaultVel float64
	trajMaxVel     float64
	trajDefaultAcc float64
	trajMaxAcc     float64

	arcBlendEnable           bool
	arcBlendFallback         bool
	arcBlendOptDepth         int32
	arcBlendGapCycles        float64
	arcBlendRampFreq         float64
	arcBlendTangentKinkRatio float64

	jointBacklash   [maxJoints]float64
	jointFerror     [maxJoints]float64
	jointMinFerror  [maxJoints]float64
	jointMinLimit   [maxJoints]float64
	jointMaxLimit   [maxJoints]float64
	jointMaxVel     [maxJoints]float64
	jointMaxAcc     [maxJoints]float64
	jointMaxJerk    [maxJoints]float64
	jointHome       [maxJoints]float64
	jointHomeOffset [maxJoints]float64
	jointHomeSeq    [maxJoints]int32

	axisMinLimit [maxAxes]float64
	axisMaxLimit [maxAxes]float64
	axisMaxVel   [maxAxes]float64
	axisMaxAcc   [maxAxes]float64
}

// newIniHal creates the inihal HAL component and all its pins.
// The name parameter allows per-instance components (e.g. "mill1.inihal").
func newIniHal(name string, numJoints int) (*iniHal, error) {
	comp, err := hal.NewComponent(name)
	if err != nil {
		return nil, fmt.Errorf("inihal: hal_init: %w", err)
	}

	h := &iniHal{comp: comp, numJoints: numJoints}

	// Joint pins
	for i := 0; i < numJoints; i++ {
		if h.jointBacklash[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.backlash", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointFerror[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.ferror", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMinFerror[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.min_ferror", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMinLimit[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.min_limit", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMaxLimit[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.max_limit", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMaxVel[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.max_velocity", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMaxAcc[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.max_acceleration", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointMaxJerk[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.max_jerk", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointHome[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.home", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointHomeOffset[i], err = hal.NewPin[float64](comp, fmt.Sprintf("%d.home_offset", i), hal.In); err != nil {
			return nil, err
		}
		if h.jointHomeSeq[i], err = hal.NewPin[int32](comp, fmt.Sprintf("%d.home_sequence", i), hal.In); err != nil {
			return nil, err
		}
	}

	// Axis pins
	letters := "xyzabcuvw"
	for i := 0; i < maxAxes; i++ {
		letter := string(letters[i])
		if h.axisMinLimit[i], err = hal.NewPin[float64](comp, letter+".min_limit", hal.In); err != nil {
			return nil, err
		}
		if h.axisMaxLimit[i], err = hal.NewPin[float64](comp, letter+".max_limit", hal.In); err != nil {
			return nil, err
		}
		if h.axisMaxVel[i], err = hal.NewPin[float64](comp, letter+".max_velocity", hal.In); err != nil {
			return nil, err
		}
		if h.axisMaxAcc[i], err = hal.NewPin[float64](comp, letter+".max_acceleration", hal.In); err != nil {
			return nil, err
		}
	}

	// Traj pins
	if h.trajDefaultVel, err = hal.NewPin[float64](comp, "traj_default_velocity", hal.In); err != nil {
		return nil, err
	}
	if h.trajMaxVel, err = hal.NewPin[float64](comp, "traj_max_velocity", hal.In); err != nil {
		return nil, err
	}
	if h.trajDefaultAcc, err = hal.NewPin[float64](comp, "traj_default_acceleration", hal.In); err != nil {
		return nil, err
	}
	if h.trajMaxAcc, err = hal.NewPin[float64](comp, "traj_max_acceleration", hal.In); err != nil {
		return nil, err
	}

	// Arc blend pins
	if h.arcBlendEnable, err = hal.NewPin[bool](comp, "traj_arc_blend_enable", hal.In); err != nil {
		return nil, err
	}
	if h.arcBlendFallback, err = hal.NewPin[bool](comp, "traj_arc_blend_fallback_enable", hal.In); err != nil {
		return nil, err
	}
	if h.arcBlendOptDepth, err = hal.NewPin[int32](comp, "traj_arc_blend_optimization_depth", hal.In); err != nil {
		return nil, err
	}
	if h.arcBlendGapCycles, err = hal.NewPin[float64](comp, "traj_arc_blend_gap_cycles", hal.In); err != nil {
		return nil, err
	}
	if h.arcBlendRampFreq, err = hal.NewPin[float64](comp, "traj_arc_blend_ramp_freq", hal.In); err != nil {
		return nil, err
	}
	if h.arcBlendTangentKinkRatio, err = hal.NewPin[float64](comp, "traj_arc_blend_tangent_kink_ratio", hal.In); err != nil {
		return nil, err
	}

	if err := comp.Ready(); err != nil {
		return nil, fmt.Errorf("inihal: hal ready: %w", err)
	}

	return h, nil
}

// initPins sets pin values from the current configuration and initializes old values.
func (h *iniHal) initPins(t *Task) {
	h.trajDefaultVel.Set(t.maxVelocity)
	h.trajMaxVel.Set(t.maxVelocity)
	h.trajDefaultAcc.Set(t.maxAcceleration)
	h.trajMaxAcc.Set(t.maxAcceleration)

	// Read back to initialize old values
	h.old.trajDefaultVel = h.trajDefaultVel.Get()
	h.old.trajMaxVel = h.trajMaxVel.Get()
	h.old.trajDefaultAcc = h.trajDefaultAcc.Get()
	h.old.trajMaxAcc = h.trajMaxAcc.Get()

	h.old.arcBlendEnable = h.arcBlendEnable.Get()
	h.old.arcBlendFallback = h.arcBlendFallback.Get()
	h.old.arcBlendOptDepth = h.arcBlendOptDepth.Get()
	h.old.arcBlendGapCycles = h.arcBlendGapCycles.Get()
	h.old.arcBlendRampFreq = h.arcBlendRampFreq.Get()
	h.old.arcBlendTangentKinkRatio = h.arcBlendTangentKinkRatio.Get()

	for i := 0; i < h.numJoints; i++ {
		h.old.jointBacklash[i] = h.jointBacklash[i].Get()
		h.old.jointFerror[i] = h.jointFerror[i].Get()
		h.old.jointMinFerror[i] = h.jointMinFerror[i].Get()
		h.old.jointMinLimit[i] = h.jointMinLimit[i].Get()
		h.old.jointMaxLimit[i] = h.jointMaxLimit[i].Get()
		h.old.jointMaxVel[i] = h.jointMaxVel[i].Get()
		h.old.jointMaxAcc[i] = h.jointMaxAcc[i].Get()
		h.old.jointMaxJerk[i] = h.jointMaxJerk[i].Get()
		h.old.jointHome[i] = h.jointHome[i].Get()
		h.old.jointHomeOffset[i] = h.jointHomeOffset[i].Get()
		h.old.jointHomeSeq[i] = h.jointHomeSeq[i].Get()
		h.jointHoming[i] = t.jointHoming[i] // INI-fixed params, for re-push
	}
	for i := 0; i < maxAxes; i++ {
		h.old.axisMinLimit[i] = h.axisMinLimit[i].Get()
		h.old.axisMaxLimit[i] = h.axisMaxLimit[i].Get()
		h.old.axisMaxVel[i] = h.axisMaxVel[i].Get()
		h.old.axisMaxAcc[i] = h.axisMaxAcc[i].Get()
	}
}

// check reads all pin values and pushes changes to the motion controller.
// Called periodically from the task cycle. Any setter failures are joined and
// returned so the caller can log them: a silently-dropped push would leave the
// machine running with limits/velocities that disagree with the HAL pins.
func (h *iniHal) check(mc MotionConfig) error {
	var errs []error
	// Traj velocity/acceleration
	if v := h.trajDefaultVel.Get(); !floatClose(v, h.old.trajDefaultVel) {
		h.old.trajDefaultVel = v
		if err := mc.SetVel(v); err != nil {
			errs = append(errs, fmt.Errorf("set traj default vel: %w", err))
		}
	}
	if v := h.trajMaxVel.Get(); !floatClose(v, h.old.trajMaxVel) {
		h.old.trajMaxVel = v
		if err := mc.SetVelLimit(v); err != nil {
			errs = append(errs, fmt.Errorf("set traj max vel: %w", err))
		}
	}
	if v := h.trajDefaultAcc.Get(); !floatClose(v, h.old.trajDefaultAcc) {
		h.old.trajDefaultAcc = v
		if err := mc.SetAcc(v); err != nil {
			errs = append(errs, fmt.Errorf("set traj default acc: %w", err))
		}
	}
	if v := h.trajMaxAcc.Get(); !floatClose(v, h.old.trajMaxAcc) {
		h.old.trajMaxAcc = v
		// SetAcc covers both default and max in motctl
		if err := mc.SetAcc(v); err != nil {
			errs = append(errs, fmt.Errorf("set traj max acc: %w", err))
		}
	}

	// Arc blend — check any change, then push all at once.
	// Note: the initial arc-blend push happens in loadTraj (SetupArcBlends,
	// 2.9 initraj.cc parity); runtime changes via these pins are not pushed yet.
	// but we still track changes for completeness.

	// Joint parameters
	for i := 0; i < h.numJoints; i++ {
		joint := int32(i)
		if v := h.jointBacklash[i].Get(); !floatClose(v, h.old.jointBacklash[i]) {
			h.old.jointBacklash[i] = v
			if err := mc.SetJointBacklash(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d backlash: %w", i, err))
			}
		}
		if v := h.jointMinLimit[i].Get(); !floatClose(v, h.old.jointMinLimit[i]) {
			h.old.jointMinLimit[i] = v
			if err := mc.SetJointPositionLimits(joint, v, h.old.jointMaxLimit[i]); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d position limits: %w", i, err))
			}
		}
		if v := h.jointMaxLimit[i].Get(); !floatClose(v, h.old.jointMaxLimit[i]) {
			h.old.jointMaxLimit[i] = v
			if err := mc.SetJointPositionLimits(joint, h.old.jointMinLimit[i], v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d position limits: %w", i, err))
			}
		}
		if v := h.jointMaxVel[i].Get(); !floatClose(v, h.old.jointMaxVel[i]) {
			h.old.jointMaxVel[i] = v
			if err := mc.SetJointVelLimit(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d vel limit: %w", i, err))
			}
		}
		if v := h.jointMaxAcc[i].Get(); !floatClose(v, h.old.jointMaxAcc[i]) {
			h.old.jointMaxAcc[i] = v
			if err := mc.SetJointAccLimit(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d acc limit: %w", i, err))
			}
		}
		if v := h.jointMaxJerk[i].Get(); !floatClose(v, h.old.jointMaxJerk[i]) {
			h.old.jointMaxJerk[i] = v
			if err := mc.SetJointJerkLimit(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d jerk limit: %w", i, err))
			}
		}
		if v := h.jointFerror[i].Get(); !floatClose(v, h.old.jointFerror[i]) {
			h.old.jointFerror[i] = v
			if err := mc.SetJointMaxFerror(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d max ferror: %w", i, err))
			}
		}
		if v := h.jointMinFerror[i].Get(); !floatClose(v, h.old.jointMinFerror[i]) {
			h.old.jointMinFerror[i] = v
			if err := mc.SetJointMinFerror(joint, v); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d min ferror: %w", i, err))
			}
		}
		// Home/offset/sequence — any change pushes all three
		newHome := h.jointHome[i].Get()
		newOffset := h.jointHomeOffset[i].Get()
		newSeq := h.jointHomeSeq[i].Get()
		if !floatClose(newHome, h.old.jointHome[i]) ||
			!floatClose(newOffset, h.old.jointHomeOffset[i]) ||
			newSeq != h.old.jointHomeSeq[i] {
			h.old.jointHome[i] = newHome
			h.old.jointHomeOffset[i] = newOffset
			h.old.jointHomeSeq[i] = newSeq
			// Re-push with the INI-fixed params intact (final/search/latch vel,
			// flags, volatile-home) instead of zeros — otherwise a runtime HAL
			// home change would wipe them and break homing.
			hp := h.jointHoming[i]
			if err := mc.SetJointHomingParams(joint, newOffset, newHome,
				hp.FinalVel, hp.SearchVel, hp.LatchVel, hp.Flags, newSeq, hp.VolatileHome); err != nil {
				errs = append(errs, fmt.Errorf("set joint %d homing params: %w", i, err))
			}
		}
	}

	// Axis parameters
	for i := 0; i < maxAxes; i++ {
		axis := int32(i)
		if v := h.axisMinLimit[i].Get(); !floatClose(v, h.old.axisMinLimit[i]) {
			h.old.axisMinLimit[i] = v
			if err := mc.SetAxisPositionLimits(axis, v, h.old.axisMaxLimit[i]); err != nil {
				errs = append(errs, fmt.Errorf("set axis %d position limits: %w", i, err))
			}
		}
		if v := h.axisMaxLimit[i].Get(); !floatClose(v, h.old.axisMaxLimit[i]) {
			h.old.axisMaxLimit[i] = v
			if err := mc.SetAxisPositionLimits(axis, h.old.axisMinLimit[i], v); err != nil {
				errs = append(errs, fmt.Errorf("set axis %d position limits: %w", i, err))
			}
		}
		if v := h.axisMaxVel[i].Get(); !floatClose(v, h.old.axisMaxVel[i]) {
			h.old.axisMaxVel[i] = v
			if err := mc.SetAxisVelLimit(axis, v, 0); err != nil {
				errs = append(errs, fmt.Errorf("set axis %d vel limit: %w", i, err))
			}
		}
		if v := h.axisMaxAcc[i].Get(); !floatClose(v, h.old.axisMaxAcc[i]) {
			h.old.axisMaxAcc[i] = v
			if err := mc.SetAxisAccLimit(axis, v, 0); err != nil {
				errs = append(errs, fmt.Errorf("set axis %d acc limit: %w", i, err))
			}
		}
	}
	return errors.Join(errs...)
}

// exit shuts down the HAL component. Exit is best-effort teardown; a failure
// here cannot be recovered from and does not affect correctness of shutdown.
func (h *iniHal) exit() {
	if h.comp != nil {
		_ = h.comp.Exit()
	}
}

func floatClose(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}
