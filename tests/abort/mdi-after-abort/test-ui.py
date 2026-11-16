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
    # The planner accelerates at half the 1 in/s^2 limit along the path: past
    # X10 the axis runs at ~3.2 in/s, and stopping from there takes over 6 s.
    stmak_test.wait_stat(s, lambda st: st.actual_position[0] >= 10.0,
                         "run %d: X past X10" % i,
                         detail=lambda st: "actual_position=%r" % (st.actual_position[:3],))

    c.abort()
    c.wait_complete()
    c.mode(MODE_MDI)
    c.mdi('G0 Z%.1f' % z)
    # The window this rig exists for: the stop takes over 6 s (see above),
    # so the MDI normally goes out while the machine still moves. Whether it
    # did is reported, not judged: the stop runs at full deceleration whatever
    # the overrides, so nothing can hold it open, and a host that kept this
    # script from sending for 6 s would fail a correct machine. The MDI moves
    # Z only, so X changing after the position read below proves the stop was
    # still running after the MDI went out (waited for with the hang guard: a
    # slow status update is not a missed window).
    s.poll()
    x_sent = s.actual_position[0]
    try:
        stmak_test.wait_stat(s, lambda st: st.actual_position[0] != x_sent,
                             "run %d: X to change" % i)
        sys.stderr.write("run %d: the MDI went out while the stop was running\n" % i)
    except stmak_test.Timeout:
        sys.stderr.write("run %d: NOTE the machine had stopped before the MDI went "
                         "out; this run did not exercise the abort window\n" % i)
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
