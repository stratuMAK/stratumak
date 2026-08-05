#!/usr/bin/env python3

# Two-phase driver for the non-random restart test (see test.sh).
#   load   boot 1: M61 Q1 mounts tool 1, so slot 0 holds T1's row (Z+1.0)
#          in the durable store when the server goes down.
#   check  boot 2: the spindle must report empty AND G43 H0 must apply a
#          zero offset — pre-fix, slot 0 still held T1 and H0 applied its
#          full length to a spindle io reported as empty.

import sys
import time

import gmi
import stmak_test
from gmi.constants import *


def wait_until(what, pred, timeout=10.0):
    start = time.time()
    while time.time() - start < timeout:
        if pred():
            return
        time.sleep(0.1)
    print("Error: timed out waiting for %s" % what)
    sys.exit(1)


c = stmak_test.Command()
s = gmi.Stat()

c.state(STATE_ESTOP_RESET)
c.wait_complete()
c.state(STATE_ON)
c.wait_complete()
c.mode(MODE_MDI)
c.wait_complete()

phase = sys.argv[1]

if phase == "load":
    c.mdi("M61 Q1")
    c.wait_complete()
    wait_until("tool 1 in spindle",
               lambda: (s.poll(), s.tool_in_spindle == 1)[-1])
    s.poll()
    if s.tool_table[0].id != 1:
        print("Error: after M61 Q1 slot 0 holds tool %r, expected 1"
              % s.tool_table[0].id)
        sys.exit(1)
    print("tool 1 mounted, slot 0 mirrors it")

elif phase == "check":
    s.poll()
    if s.tool_in_spindle != 0:
        print("Error: spindle reports tool %d after restart, expected empty (0)"
              % s.tool_in_spindle)
        sys.exit(1)
    if s.tool_table[0].id > 0:
        print("Error: slot 0 still holds tool %r after restart — the stale "
              "session copy survived" % s.tool_table[0].id)
        sys.exit(1)

    # The operator-visible consequence: a bare G43 H0 must not move Z.
    c.mdi("G43 H0")
    c.wait_complete()
    wait_until("zero tool offset after G43 H0",
               lambda: (s.poll(), abs(s.tool_offset[2]) < 1e-9)[-1])
    print("spindle empty after restart, G43 H0 applies no offset")

else:
    print("Error: unknown phase %r" % phase)
    sys.exit(1)
