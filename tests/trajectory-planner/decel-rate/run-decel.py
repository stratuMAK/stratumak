#!/usr/bin/env python3
# Drive decel.ngc to completion via the gmi REST client.
import gmi
from gmi.constants import *
import stmak_test
import sys

# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(-1)
c.wait_complete()

c.mode(MODE_AUTO)
c.program_open('decel.ngc')
c.auto(AUTO_RUN, 0)

# stmak_test.Command's wait_complete() after AUTO_RUN returns when the
# program has ended (and raises if it does not in time); then wait for the
# planner to finish the motion it queued. The capture runs until the server
# stops, so nothing more is needed for the last servo cycles to be in the file.
# (Two timed polls stood here: both fell through silently on expiry, and a run
# shorter than one poll was never seen to start.)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE
                     and st.queue == 0 and st.inpos,
                     "the program's motion to finish")
sys.exit(0)
