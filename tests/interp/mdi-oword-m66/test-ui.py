#!/usr/bin/env python3
# Ported to the stratuMAK gmi REST/WS client (removed NML linuxcnc module).
import gmi
from gmi.constants import *
import sys

import stmak_test

c = stmak_test.Command(); s = gmi.Stat(); e = gmi.ErrorChannel()

def run(arg):
    # c.mdi() registers the command before it returns, and the task goes
    # idle only after the MDI's motion has drained (finishMDI), so a drained
    # queue means the sub has run to its end, moves included.
    c.mdi("O<obug> call [%d]" % arg)
    stmak_test.drain_mdi(s)


c.state(STATE_ESTOP_RESET); c.state(STATE_ON)
c.home(0); c.home(1); c.home(2)
stmak_test.wait_stat(s, lambda st: all(st.homed[0:3]),
                    "joints 0-2 to finish homing", timeout=30.0,
                    detail=lambda st: "homed=%s" % (list(st.homed[0:3]),))

c.mode(MODE_MDI)

# With the M66 queue buster first: the sub must carry on past it to the G0.
run(1)
# The unit-less sub runs in the machine's units (G20 on this inch config,
# matching 2.9): G0 G53 X1 = 1 inch; s.position is stmak-mm.
if abs(s.position[0] - 1 * 25.4) > 1e-5:
    print("ended at wrong location (did O-call terminate with error?)", s.position); sys.exit(1)

# Without it, and back to X0 -- run second, so the move it must make is
# not one homing has already made for it.
run(0)
if s.position[0] != 0:
    print("ended at wrong location (did O-call terminate with error?)", s.position); sys.exit(1)
print("done! it all worked")
sys.exit(0)
