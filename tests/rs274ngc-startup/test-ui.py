#!/usr/bin/env python3

# Ported to the stratuMAK gmi REST/WS client (from the removed NML linuxcnc module).

import gmi
from gmi.constants import *
import stmak_test

import math
import time
import sys

# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

# Wait for startup + RS274NGC_STARTUP_CODE (G43 H1) to be applied.
t = time.time()
while time.time() - t < 10:
    s.poll()
    if s.interp_state == INTERP_IDLE and s.task_state != 0:
        break
    time.sleep(0.1)
time.sleep(0.5)

s.poll()
retval = 0

if s.g5x_index != 1:
    print("Expected g5x_index=1 (startup in G54), got %d instead" % s.g5x_index)
    retval = 1

# The tool table Z offset is 0.1234 inch; the gmi stat API reports tool
# offsets in stratuMAK's internal millimetres (mm-everywhere convention, see
# src/stmak/UNITS_MM_CONSISTENCY_FIX.md).
expected = 0.1234 * 25.4
if math.fabs(s.tool_offset[2] - expected) > 0.000001:
    print("Expected tool offset of %f (0.1234 in) via startup gcode not detected" % expected)
    print("Got %f instead." % s.tool_offset[2])
    retval = 1

sys.exit(retval)
