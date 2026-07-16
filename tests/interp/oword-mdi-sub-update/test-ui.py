#!/usr/bin/env python3
# Ported to the gomc gmi REST/WS client (removed NML linuxcnc module).
# The sub's `(print, test RAN)` goes to the interpreter's stdout (the
# gomc-server log), which test.sh cats into `result`; we only need to drive the
# three MDI calls with an edit in between.
import gmi
from gmi.constants import *
import time, os
from shutil import copyfile

c = gmi.Command(); s = gmi.Stat()

def wait_for_startup(timeout=10.0):
    t = time.time()
    while time.time() - t < timeout:
        s.poll()
        if (s.angular_units != 0.0 and s.linear_units != 0.0 and s.axis_mask != 0
                and s.exec_state == EXEC_DONE and s.interp_state == INTERP_IDLE
                and s.task_state == STATE_ESTOP):
            return
        time.sleep(0.1)
    raise RuntimeError("Timeout")

wait_for_startup()
c.state(STATE_ESTOP_RESET); c.state(STATE_ON); c.home(-1); c.wait_complete()
c.mode(MODE_MDI)

copyfile('subs/test1-orig.ngc', 'subs/test1.ngc')
c.mdi('o<test1> call [20]'); c.wait_complete(); time.sleep(0.3)

os.system('./edit.sh')
c.mdi('o<test1> call [20]'); c.wait_complete(); time.sleep(0.3)

c.mdi('o<test1> call [20]'); c.wait_complete(); time.sleep(0.3)
