#!/usr/bin/env python3
# motion.in-position rises only once the joints have stopped (see README):
# the joint command latched at its rising edge must be where the joint rests.
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


def check(what, edges_before):
    """After a move: one more rising edge, and the joint command latched at
    it equal to where the joint rests now."""
    stmak_test.wait_stat(s, lambda st: st.inpos and st.queue == 0,
                         "%s: in position" % what)
    edges = int(getp("latch.0.edges"))
    at_edge = getp("latch.0.at-edge")
    now = getp("joint.0.motor-pos-cmd")
    if edges <= edges_before:
        print("FAIL: %s: motion.in-position never rose" % what)
        return False
    if at_edge != now:
        print("FAIL: %s: in position with the joint command at %.6f, it came "
              "to rest at %.6f" % (what, at_edge, now))
        return False
    print("PASS: %s: in position where the joint rests (%.6f)" % (what, now))
    return True


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MDI)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MDI,
    "machine ON in MDI mode")

ok = True
for target in (10.0, 0.0):
    edges = int(getp("latch.0.edges"))
    c.mdi("G0 X%g" % target)
    stmak_test.drain_mdi(s)
    ok &= check("MDI G0 X%g" % target, edges)

c.mode(MODE_MANUAL)
c.teleop_enable(True)
stmak_test.wait_stat(s, lambda st: st.motion_mode == TRAJ_MODE_TELEOP,
                     "teleop mode")
for vel in (8.0, -8.0):
    edges = int(getp("latch.0.edges"))
    start = getp("joint.0.motor-pos-cmd")
    c.jog(JOG_CONTINUOUS, 0, 0, vel)
    # When the stop comes does not matter: any stop decelerates. The jog is
    # re-sent on every poll: task stops a continuous jog its client does not
    # refresh within 2 s, and the wait is bounded by distance, not time.
    stmak_test.wait_until(lambda: (c.jog(JOG_CONTINUOUS, 0, 0, vel),
                                   abs(getp("joint.0.motor-pos-cmd") - start) > 2.0)[1],
                          "the jog at %g mm/s to cover 2 mm" % vel)
    c.jog(JOG_STOP, 0, 0)
    ok &= check("teleop jog at %g mm/s, stopped" % vel, edges)

sys.exit(0 if ok else 1)
