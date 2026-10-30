#!/usr/bin/env python3
# Run from line N: lines before N are skipped, N runs, and modal state set by
# the skipped lines reaches the trajectory planner (see README).

import gmi
from gmi.constants import *
import stmak_test
import sys

c = stmak_test.Command()
# machine_units(): the config and the numbers below are inch.
s = gmi.Stat().machine_units()
e = gmi.ErrorChannel()
MM = 25.4

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "X, Y and Z homed")


def run(prog, line):
    c.mode(MODE_AUTO)
    c.program_open(prog)
    c.auto(AUTO_RUN, line)
    stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                         "%s from line %d to finish" % (prog, line))


def messages_until_end(what):
    got = []

    def _end():
        while True:
            m = e.poll()
            if m is None:
                return False
            text = m[1].strip()
            if text == "END":
                return True
            got.append(text)
    stmak_test.wait_until(_end, "%s: the END message" % what,
                          detail=lambda: "got %r" % got)
    return got


res = 0
for n in range(0, 5):
    run("lines.ngc", n)
    got = messages_until_end("lines.ngc from line %d" % n)
    want = ["L%d" % k for k in range(max(n, 1), 5)]
    print("AUTO_RUN %d: %s" % (n, " ".join(got)))
    if got != want:
        print("*** ERROR AUTO_RUN %d ran %r, expected %r" % (n, got, want))
        res += 1

c.mode(MODE_MDI)
c.mdi("G61 G0 X0 Y0 Z0")   # the planner blends only if the skipped G64 reaches it
c.wait_complete()
stmak_test.drain_mdi(s)
stmak_test.setp("x-max-reset", False)
run("blend.ngc", 2)
max_x = stmak_test.getp("x-max") / MM
stmak_test.setp("x-max-reset", True)
print("blend.ngc from line 2: max X %.3f" % max_x)
if not max_x < 4.9:
    print("*** ERROR the corner was not blended (max X %.3f): the G64 on the "
          "skipped line 1 never reached the planner" % max_x)
    res += 1

print("Exiting with %d errors" % res)
sys.exit(res)
