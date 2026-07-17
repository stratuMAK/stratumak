#!/usr/bin/env python3
# Ported to the gomc gmi REST/WS client (removed NML linuxcnc module).
import gmi
from gmi.constants import *
import sys, os

import gomc_test

c = gomc_test.Command(); s = gmi.Stat(); e = gmi.ErrorChannel()


def _interp(st):
    return "interp_state=%d" % st.interp_state


c.state(STATE_ESTOP_RESET); c.state(STATE_ON)
c.home(0); c.home(1); c.home(2)
# Had no failure branch at all: on expiry it fell through and ran the program
# unhomed, failing somewhere else (or passing having tested nothing).
gomc_test.wait_stat(s, lambda st: all(st.homed[0:3]),
                    "joints 0-2 to finish homing", timeout=30.0,
                    detail=lambda st: "homed=%s" % (list(st.homed[0:3]),))

c.mode(MODE_AUTO)
c.program_open("test.ngc")
c.auto(AUTO_RUN, 1)

# wait for the interpreter to start running test.ngc
gomc_test.wait_stat(s, lambda st: st.interp_state != INTERP_IDLE,
                    "the interpreter to start running test.ngc",
                    timeout=10.0, detail=_interp)

# rename the file mid-run: the interpreter should hold the program, not re-read it
os.rename('test.ngc', 'moved-test.ngc')

# wait for the program to finish
gomc_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                    "the program to finish", timeout=20.0, detail=_interp)

os.rename('moved-test.ngc', 'test.ngc')
print("done! it all worked")
sys.exit(0)
