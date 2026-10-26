#!/usr/bin/env python3

# Regression driver: coordinated-mode in-position waits for the jerk filter.
#
# The jerk filter (boxcar on the joint commands, window acc/(jerk*period))
# runs after the trajectory planner.  When the planner reports done the
# joints still have up to a window of travel left -- here 500 cycles, about
# 1 mm.  motion.in-position (INPOS) is what task waits on before M-codes,
# I/O, the end of an MDI command, a probe's "finished without contact" and
# mode switches; it must not go TRUE before the joints have arrived.
#
# Sequence: machine ON, MDI G0 X10, then G0 X0; inpos_latch records the
# joint command at each rising edge of motion.in-position (deterministic --
# no race against how quickly the test gets to look).
#
# PASS (fixed):   at both edges the joint command is at the target.
# FAIL (pre-fix): at the edge the command is still ~1 mm short.

import gmi
import stmak_test
from gmi.constants import *

import subprocess
import sys

c = stmak_test.Command()
s = gmi.Stat()


def getp(name):
    out = subprocess.check_output(["halcmd", "getp", name])
    return float(out.split()[-1])


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MDI)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MDI,
    "machine ON in MDI mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

ok = True
for n, target in enumerate((10.0, 0.0), start=1):
    c.mdi("G0 X%g" % target)
    stmak_test.drain_mdi(s)
    edges = int(getp("latch.0.edges"))
    at_edge = getp("latch.0.at-edge")
    now = getp("joint.0.motor-pos-cmd")
    if edges < n:
        print("FAIL: G0 X%g: motion.in-position never rose (edges=%d)" % (target, edges))
        ok = False
    elif abs(at_edge - target) > 1e-6:
        print("FAIL: G0 X%g: in position with the joint command at %.4f -- "
              "%.4f mm still in the jerk filter" % (target, at_edge, abs(target - at_edge)))
        ok = False
    if abs(now - target) > 1e-6:
        print("FAIL: G0 X%g: MDI finished with the joint command at %.4f" % (target, now))
        ok = False

if not ok:
    sys.exit(1)
print("PASS: in position only once the jerk filter has delivered the joint to the target")
sys.exit(0)
