#!/usr/bin/env python3

import gmi
from gmi.constants import *
import stmak_test
import sys

# Initialization
# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MDI)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MDI,
    "machine ON in MDI mode")

c.mdi('(print,pre 1)')
c.mdi('(print,pre 2)')
c.mdi('M400')
c.mdi('(print,post 1)')
c.mdi('(print,post 2)')
c.mdi('(print,post 3)')
c.mdi('(print,post 4)')
c.mdi('(print,post 5)')
c.mdi('(print,post 6)')

# Shutdown: every queued MDI executed, not just the last command acknowledged.
stmak_test.drain_mdi(s)
sys.exit(0)

