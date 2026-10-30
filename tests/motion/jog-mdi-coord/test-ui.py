#!/usr/bin/env python3

# Regression driver: an MDI after a jog issued in MDI mode.
#
# A jog switches motion to teleop (axis jog) or free (joint jog) but leaves
# the task mode at MDI.  The next MDI saw "already in MDI", did not ask motion
# for coordinated mode again, and every line failed in motion with "need to be
# enabled, in coord mode for linear move" -- until something switched the task
# mode.  Motion also only makes the switch in position, so the request must
# wait for a jog that is still running out (and the jerk filter behind it).
#
# Per jog kind (axis, joint), from MDI mode at X0:
#   1. jog +2 and, without waiting for it, MDI G91 G0 X1 then G90:
#      the MDI must run after the jog, from where the jog ended (interpreter
#      re-synched), ending at X3 in coordinated mode
#   2. MDI G0 X0 returns to the start
# motion.in-position must not stay TRUE while the joint moves: in teleop it
# was never recomputed and kept its value from before the jog. inpos_motion
# counts such cycles in the servo thread for the whole run (reading the pin
# once, 0.1 s into the 0.4 s jog, failed whenever the script was late).
#
# PASS (fixed):   both MDIs run, X ends at 3 then 0, motion in coord mode.
# FAIL (pre-fix): the MDIs fail, X stays at 2, motion stays in teleop/free.

import gmi
import stmak_test
from gmi.constants import *

import subprocess
import sys

c = stmak_test.Command()
s = gmi.Stat()


def getp(name):
    out = subprocess.check_output(["halcmd", "getp", name]).decode().split()[-1]
    return out.upper() == "TRUE" if out.upper() in ("TRUE", "FALSE") else float(out)


def x():
    s.poll()
    return s.actual_position[0]


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.home(-1)
stmak_test.wait_stat(s, lambda st: st.homed[0], "joint 0 homed")
c.mode(MODE_MDI)
c.mdi("G90 G0 X0")
stmak_test.drain_mdi(s)

ok = True
for jjog, kind in ((False, "axis"), (True, "joint")):
    moved_before = getp("moved.0.count")
    c.jog(JOG_INCREMENT, jjog, 0, 5.0, 2.0)
    try:
        c.mdi("G91 G0 X1")
        c.mdi("G90")
    except Exception as e:
        print("FAIL: %s jog: MDI after the jog refused: %s" % (kind, e))
        ok = False
    stmak_test.drain_mdi(s)
    s.poll()
    if abs(x() - 3.0) > 1e-6 or s.motion_mode != TRAJ_MODE_COORD:
        print("FAIL: %s jog: after G91 G0 X1 at X%.4f, motion mode %d "
              "(want X3, coordinated)" % (kind, x(), s.motion_mode))
        ok = False
    c.mdi("G0 X0")
    stmak_test.drain_mdi(s)
    if abs(x()) > 1e-6:
        print("FAIL: %s jog: G0 X0 ended at X%.4f" % (kind, x()))
        ok = False
    moved = getp("moved.0.count") - moved_before
    if moved:
        print("FAIL: %s jog: X moved in %d servo cycles with motion.in-position "
              "TRUE" % (kind, moved))
        ok = False

if not ok:
    sys.exit(1)
print("PASS: MDI after a jog re-enters coordinated mode and runs from the jogged position")
sys.exit(0)
