#!/usr/bin/env python3
"""Homing an absolute-encoder joint: mode selection and idempotency.

See README for the two defects this covers. The machine is a closed loop, so
"the joint moved" is decided by joint.N.motor-pos-cmd: homing an absolute
joint shifts motor_offset and the reported position by the same amount, which
leaves the motor command untouched. A joint that actually traverses moves it.
"""

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()

HOME_OFFSET = 5.0
HOME = 0.0
TOL = 1e-4

s = gmi.Stat()
c = gmi.Command()

stmak_test.wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()


def motor_cmd(joint):
    # halcmd gets reads a signal; the HAL file nets motor-pos-cmd as jNpos.
    return stmak_test.getp("j%dpos" % joint)


def position(joint):
    s.poll()
    return s.joint_actual_position[joint]


def wait_homed(joint, want=True):
    stmak_test.wait_stat(
        s, lambda st: bool(st.homed[joint]) == want,
        "joint %d homed==%s" % (joint, want),
        detail=lambda st: "homed=%s" % (tuple(st.homed[:3]),))


def check(label, got, want, tol=TOL):
    if abs(got - want) > tol:
        stmak_test.fail("%s: expected %.6f, got %.6f" % (label, want, got))
    print("ok: %s = %.6f" % (label, got))


# 1. HOME_ABSOLUTE_ENCODER=2 -- position adopts HOME_OFFSET, joint stays put.
#    Before the flag mapping was fixed this performed the final move too, so
#    the motor command travelled from 0 to -HOME_OFFSET.
motor_before = motor_cmd(0)
c.home(0)
c.wait_complete()
wait_homed(0)
check("joint 0 position after home", position(0), HOME_OFFSET)
check("joint 0 motor command (=2 must not move)", motor_cmd(0), motor_before)

# 2. HOME_ABSOLUTE_ENCODER=1 -- same position adoption, then a traverse to HOME.
motor_before = motor_cmd(1)
c.home(1)
c.wait_complete()
wait_homed(1)
stmak_test.wait_stat(
    s, lambda st: abs(st.joint_actual_position[1] - HOME) < TOL,
    "joint 1 to reach HOME after its final move",
    detail=lambda st: "pos=%.6f" % st.joint_actual_position[1])
if abs(motor_cmd(1) - motor_before) < TOL:
    stmak_test.fail(
        "joint 1: HOME_ABSOLUTE_ENCODER=1 must perform the final move, but the "
        "motor command never left %.6f" % motor_before)
print("ok: joint 1 moved to HOME, motor command %.6f -> %.6f"
      % (motor_before, motor_cmd(1)))

# 3. HOME_NO_REHOME -- homing an already-homed absolute joint is a no-op.
pos_before = position(0)
c.home(0)
c.wait_complete()
wait_homed(0)
check("joint 0 position after re-home while homed", position(0), pos_before)

# 4. Idempotency -- unhome clears `homed` and so defeats the HOME_NO_REHOME
#    guard. The second run must still land on HOME_OFFSET rather than adding
#    it again.
c.unhome(0)
c.wait_complete()
wait_homed(0, want=False)
motor_before = motor_cmd(0)
c.home(0)
c.wait_complete()
wait_homed(0)
check("joint 0 position after unhome+home (HOME_OFFSET must not accumulate)",
      position(0), HOME_OFFSET)
check("joint 0 motor command after unhome+home (must not move)",
      motor_cmd(0), motor_before)

# 5. Control: a conventional joint is unaffected by all of the above.
c.home(2)
c.wait_complete()
wait_homed(2)
stmak_test.wait_stat(
    s, lambda st: abs(st.joint_actual_position[2] - HOME) < TOL,
    "joint 2 to reach HOME",
    detail=lambda st: "pos=%.6f" % st.joint_actual_position[2])
print("ok: joint 2 homed conventionally")

print("PASS")
