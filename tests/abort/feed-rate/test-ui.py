#!/usr/bin/env python3
# Ported to the stmak gmi REST/WS client (was the removed NML linuxcnc module).
import gmi
import stmak_test
from gmi.constants import *
import math, time, sys

def wait_for_startup(s, timeout=10.0):
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if (s.angular_units != 0.0 and s.linear_units != 0.0 and s.axis_mask != 0
                and s.exec_state == EXEC_DONE and s.interp_state == INTERP_IDLE
                and s.task_state == STATE_ESTOP):
            return
        time.sleep(0.1)
    raise RuntimeError("Timeout")

c = stmak_test.Command(); s = gmi.Stat(); e = gmi.ErrorChannel()
wait_for_startup(s)

c.state(STATE_ESTOP_RESET); c.state(STATE_ON); c.home(-1); c.wait_complete()
c.mode(MODE_MDI)

feed_rate = 234.5
c.mdi('g94 ; normal "units per minute" feed mode')
c.mdi('f%.1f' % feed_rate); c.wait_complete()
c.abort(); c.wait_complete()
# The abort must leave the feed rate the F word set. Poll for it rather than
# sleeping 0.3 s and hoping: on a slow runner the old sleep expired before the
# setting propagated and the test failed as an inscrutable float mismatch
# instead of naming the wait that lost.
stmak_test.wait_stat(
    s, lambda st: math.fabs(st.settings[1] - feed_rate) < 0.0000001,
    "feed rate %.1f to survive the abort" % feed_rate,
    detail=lambda st: "settings[1]=%r" % (st.settings[1],))
print("feed rate:{}".format(s.settings[1]))
assert(math.fabs(s.settings[1] - feed_rate) < 0.0000001)

feed_rate = 345.6
c.mdi('g95 ; "units per revolution" feed mode')
c.mdi('f%.1f' % feed_rate); c.wait_complete()
c.abort(); c.wait_complete()
# The abort must leave the feed rate the F word set. Poll for it rather than
# sleeping 0.3 s and hoping: on a slow runner the old sleep expired before the
# setting propagated and the test failed as an inscrutable float mismatch
# instead of naming the wait that lost.
stmak_test.wait_stat(
    s, lambda st: math.fabs(st.settings[1] - feed_rate) < 0.0000001,
    "feed rate %.1f to survive the abort" % feed_rate,
    detail=lambda st: "settings[1]=%r" % (st.settings[1],))
print("feed rate:{}".format(s.settings[1]))
assert(math.fabs(s.settings[1] - feed_rate) < 0.0000001)
sys.exit(0)
