#!/usr/bin/env python3
# A motion MDI sent while the planner is still decelerating from an abort must
# wait for the stop and then run (see README).

import gmi
from gmi.constants import *
import stmak_test
import sys

c = stmak_test.Command()
# machine_units(): the config and every number below are inch.
s = gmi.Stat().machine_units()
e = gmi.ErrorChannel()

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "X, Y and Z homed")

res = 0
for i, z in enumerate((1.0, 2.0, 3.0)):
    c.mode(MODE_MDI)
    c.mdi('G0 X0 Y0 Z0')
    c.wait_complete()
    stmak_test.drain_mdi(s)

    c.mode(MODE_AUTO)
    c.program_open('test.ngc')
    c.auto(AUTO_RUN, 0)
    # At 1 in/s^2 the axis reaches 4 in/s at X8: past X10 it runs at full speed.
    stmak_test.wait_stat(s, lambda st: st.actual_position[0] >= 10.0,
                         "run %d: X at full speed (past X10)" % i,
                         detail=lambda st: "actual_position=%r" % (st.actual_position[:3],))

    c.abort()
    c.wait_complete()
    c.mode(MODE_MDI)
    c.mdi('G0 Z%.1f' % z)
    # The rig must have hit the window it exists for: at 1 in/s^2 the stop
    # from 4 in/s takes 4 s, so the MDI is queued while the machine still
    # moves. If it ever was not, this run proved nothing — say so.
    s.poll()
    x_sent = s.actual_position[0]
    try:
        stmak_test.wait_stat(s, lambda st: st.actual_position[0] != x_sent,
                             "run %d: X to change" % i, timeout=1.0)
        moving = True
    except stmak_test.Timeout:
        moving = False
    if not moving:
        print("*** ERROR run %d: machine already stopped when the MDI was sent; "
              "the abort window was not exercised" % i)
        res += 1
    c.wait_complete()
    stmak_test.drain_mdi(s)
    stmak_test.wait_stat(
        s, lambda st: abs(st.actual_position[2] - z) < 1e-6,
        "run %d: the MDI after the abort to reach Z%.1f" % (i, z),
        detail=lambda st: "actual_position=%r" % (st.actual_position[:3],))
    print("run %d: MDI G0 Z%.1f after abort arrived" % (i, z))

while True:
    m = e.poll()
    if m is None:
        break
    if 'aborting' in m[1] or "can't add" in m[1]:
        print("*** ERROR operator message: %s" % m[1].strip())
        res += 1

print("Exiting with %d errors" % res)
sys.exit(res)
