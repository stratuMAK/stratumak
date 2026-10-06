#!/usr/bin/env python3

# Regression driver: the homing reference is the drive's origin, not a sample.
#
# The simulated drive raises homing-attained 0.4 mm BEFORE it has reached the
# origin it has just defined (attained-lead; what a LinMot does mid-retract).
# homemod_cia402 used to read motor-pos-fb on that cycle and declare it to be
# HOME_OFFSET, so the joint frame landed wherever the slider happened to be
# when the flag came — on the real machine a different few tenths of a mm off
# the drive's zero on every homing run, under positions taught in an earlier
# one. Now motor_offset = -HOME_OFFSET: drive 0 is joint HOME_OFFSET, and the
# final move to HOME then really goes to HOME_OFFSET distance from the origin.
#
# PASS (fixed):   after homing, motor-offset == -HOME_OFFSET and the drive
#                 stands at HOME - HOME_OFFSET (= +0.5 mm) in its own frame.
# FAIL (pre-fix): motor-offset is off by the lead (0.1 instead of 0.5) and
#                 the drive stands at 0.1 mm.

import gmi
import stmak_test
from gmi.constants import *

import subprocess
import time
import sys

HOME = 0.0
HOME_OFFSET = -0.5   # keep in step with cia402-early.ini
LEAD = 0.4           # keep in step with cia402-early-sim.hal

c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

errors = []


def drain_errors():
    while True:
        m = e.poll()
        if m is None:
            return
        errors.append(m[1])


def getp(name):
    # halcmd getp prints "<value>" (or "<type> <dir> <name> = <value>"); the
    # last field is the value either way.
    out = subprocess.check_output(["halcmd", "getp", name])
    return float(out.split()[-1])


# Bring the machine up.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MANUAL,
    "machine ON in manual mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

drain_errors()
errors.clear()  # discard anything stale from bring-up

c.home(0)

deadline = time.monotonic() + 15.0 * stmak_test.scale()
homed = False
while time.monotonic() < deadline:
    s.poll()
    drain_errors()
    if s.task_state != STATE_ON:
        print("FAIL: machine left STATE_ON during homing (task_state=%d)" % s.task_state)
        if errors:
            print("  operator errors: %r" % errors)
        sys.exit(1)
    if s.homed[0]:
        homed = True
        break
    time.sleep(0.02)

if not homed:
    print("FAIL: joint 0 did not home within the deadline (task_state=%d)" % s.task_state)
    if errors:
        print("  operator errors: %r" % errors)
    sys.exit(1)

# Let the final move settle and any late fault surface.
time.sleep(0.5)
s.poll()
drain_errors()

motor_offset = getp("joint.0.motor-offset")
drive_pos = getp("simdrv.0.pos-fb")
joint_pos = s.joint_actual_position[0]

ok = True
if s.task_state != STATE_ON:
    print("FAIL: machine switched off after homing (task_state=%d)" % s.task_state)
    ok = False
if errors:
    print("FAIL: operator errors during homing: %r" % errors)
    ok = False
# THE REGRESSION: the reference is the drive's origin, so the offset is
# exactly -HOME_OFFSET; sampling the slider at the flag gives -HOME_OFFSET-LEAD.
if abs(motor_offset - (-HOME_OFFSET)) > 1e-3:
    print("FAIL: joint.0.motor-offset = %.4f, want %.4f (sampled the slider at "
          "homing-attained, %.1f mm short of the drive's origin?)"
          % (motor_offset, -HOME_OFFSET, LEAD))
    ok = False
# And the joint really went to HOME: HOME - HOME_OFFSET from the drive's origin.
if abs(drive_pos - (HOME - HOME_OFFSET)) > 1e-3:
    print("FAIL: drive stands at %.4f in its own frame after homing, want %.4f"
          % (drive_pos, HOME - HOME_OFFSET))
    ok = False
if abs(joint_pos - HOME) > 1e-3:
    print("FAIL: joint position = %.4f after homing, want HOME = %.4f" % (joint_pos, HOME))
    ok = False

if ok:
    print("PASS: early HomingAttained: motor-offset %.4f, drive at %.4f, joint at %.4f"
          % (motor_offset, drive_pos, joint_pos))
    sys.exit(0)
sys.exit(1)
