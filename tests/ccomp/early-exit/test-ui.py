#!/usr/bin/env python3
# Ported to stratuMAK: gmi client instead of the removed NML linuxcnc module.
import gmi
from gmi.constants import *
import stmak_test
import math, sys

# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

# test.sh waited for readiness (stmak_wait_ready).
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0); c.home(1); c.home(2)
c.wait_complete()
# wait_complete does not wait for homing; the homed flags and rest do.
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]) and st.inpos,
                     "X, Y and Z homed and at rest")
c.mode(MODE_MDI)
stmak_test.wait_stat(s, lambda st: st.task_mode == MODE_MDI, "MDI mode")
c.mdi('g0 x0.1 y0.2 z0.3')
c.mdi('g41.1 d0.1')
c.mdi('g40')
c.mdi('g0 x0.9  ; surprise motion on Y and Z, to 0!!')
# Every queued MDI executed and the machine at rest (in-position also waits
# for the joints' interpolators), so the position read next is final; a
# wait_complete plus 0.4 s stood here.
stmak_test.drain_mdi(s)
stmak_test.wait_stat(s, lambda st: st.inpos and st.queue == 0, "the moves to finish")
s.poll()
print("position: {}".format(s.position))
# The unit-less MDI runs in the machine's units (G20 on this inch config,
# matching 2.9); s.position is stmak-mm, so expect 25.4x the MDI numbers.
inch = 25.4
assert(math.fabs(s.position[0] - 0.9 * inch) < 0.0000001)
assert(math.fabs(s.position[1] - 0.2 * inch) < 0.0000001)
assert(math.fabs(s.position[2] - 0.3 * inch) < 0.0000001)
sys.exit(0)
