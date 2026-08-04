#!/usr/bin/env python3
# Ported to the stmak gmi REST/WS client (removed NML linuxcnc module).
import gmi
from gmi.constants import *
import sys

import stmak_test

c = stmak_test.Command(); s = gmi.Stat(); e = gmi.ErrorChannel()

def settle():
    # gmi.Stat updates over WS and lags c.wait_complete(), so wait for the
    # interpreter to go idle before reading position.
    #
    # This used to sleep a flat 0.7s and then poll a bounded `for _ in
    # range(50)` loop that fell through *silently* on expiry: a slow run then
    # asserted on a stale position and reported "ended at wrong location"
    # instead of "timed out". Wall-clock deadline, raises.
    stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                        "the interpreter to return to idle after an MDI",
                        detail=lambda st: "interp_state=%d" % st.interp_state)

c.state(STATE_ESTOP_RESET); c.state(STATE_ON)
c.home(0); c.home(1); c.home(2)
stmak_test.wait_stat(s, lambda st: all(st.homed[0:3]),
                    "joints 0-2 to finish homing", timeout=30.0,
                    detail=lambda st: "homed=%s" % (list(st.homed[0:3]),))

c.mode(MODE_MDI)

c.mdi("O<obug> call [0]"); c.wait_complete(); settle()
if s.position[0] != 0:
    print("ended at wrong location (did O-call terminate with error?)"); sys.exit(1)

c.mdi("O<obug> call [1]"); c.wait_complete(); settle()
# The unit-less sub runs in the machine's units (G20 on this inch config,
# matching 2.9): G0 G53 X1 = 1 inch; s.position is stmak-mm.
if abs(s.position[0] - 1 * 25.4) > 1e-5:
    print("ended at wrong location (did O-call terminate with error?)", s.position); sys.exit(1)
print("done! it all worked")
sys.exit(0)
