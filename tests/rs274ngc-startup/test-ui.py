#!/usr/bin/env python3

# Ported to the stratuMAK gmi REST/WS client (from the removed NML linuxcnc module).

import gmi
from gmi.constants import *
import stmak_test

import math
import sys

# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

# The tool table Z offset is 0.1234 inch; the gmi stat API reports tool
# offsets in stratuMAK's internal millimetres (mm-everywhere convention, see
# docs/dev/UNITS_MM_CONSISTENCY_FIX.md).
expected = 0.1234 * 25.4

# RS274NGC_STARTUP_CODE (G43 H1) has run once its offset is in effect: wait
# for exactly that, rather than for an idle interpreter plus half a second.
retval = 0
try:
    stmak_test.wait_stat(
        s, lambda st: math.fabs(st.tool_offset[2] - expected) <= 0.000001,
        "the startup G43 H1 to apply a Z tool offset of %f (0.1234 in)" % expected,
        detail=lambda st: "tool_offset[2]=%f" % st.tool_offset[2])
except stmak_test.Timeout as err:
    print(err)
    retval = 1

if s.g5x_index != 1:
    print("Expected g5x_index=1 (startup in G54), got %d instead" % s.g5x_index)
    retval = 1

sys.exit(retval)
