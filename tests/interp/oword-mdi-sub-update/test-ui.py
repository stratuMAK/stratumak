#!/usr/bin/env python3
# Ported to the stmak gmi REST/WS client (removed NML linuxcnc module).
# The sub's `(print, test RAN)` goes to the interpreter's stdout (the
# stmakd log), which test.sh cats into `result`; we only need to drive the
# three MDI calls with an edit in between.
import gmi
from gmi.constants import *
import os
from shutil import copyfile

import stmak_test

# Where test.sh's `stmak_start_server interp.ini` (no --log/--inherit) puts the
# server's output, and what its closing `cat server.log` feeds into `result`
# for checkresult to grep. Each call's `test RAN` must be in here before we
# disturb the sub on disk, so wait for the line itself rather than for a
# nominal 0.3s in which it has "probably" been written.
SERVER_LOG = 'server.log'

c = stmak_test.Command(); s = gmi.Stat()

def wait_for_startup():
    stmak_test.wait_stat(
        s,
        lambda st: (st.angular_units != 0.0 and st.linear_units != 0.0
                    and st.axis_mask != 0 and st.exec_state == EXEC_DONE
                    and st.interp_state == INTERP_IDLE
                    and st.task_state == STATE_ESTOP),
        "the machine to finish starting up",
        detail=lambda st: ("interp_state=%d exec_state=%d task_state=%d"
                           % (st.interp_state, st.exec_state, st.task_state)))

wait_for_startup()
c.state(STATE_ESTOP_RESET); c.state(STATE_ON); c.home(-1); c.wait_complete()
c.mode(MODE_MDI)

copyfile('subs/test1-orig.ngc', 'subs/test1.ngc')
c.mdi('o<test1> call [20]'); c.wait_complete()
stmak_test.wait_file_contains(SERVER_LOG, 'test RAN', count=1)

# ./edit.sh is a synchronous `sed -i` under os.system(), so the rewritten sub is
# already on disk when it returns -- the ordering the next interp read needs is
# guaranteed by the call itself, not by any sleep. What the wait below covers is
# the same thing as above: this call's output reaching the log.
if os.system('./edit.sh') != 0:
    stmak_test.fail("edit.sh failed to rewrite subs/test1.ngc")
c.mdi('o<test1> call [20]'); c.wait_complete()
stmak_test.wait_file_contains(SERVER_LOG, 'test RAN', count=2)

c.mdi('o<test1> call [20]'); c.wait_complete()
stmak_test.wait_file_contains(SERVER_LOG, 'test RAN', count=3)
