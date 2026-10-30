#!/usr/bin/env python3
"""Single-stepping test.ngc stops after every move, exactly at its end point.

A step in stratuMAK runs the program on to the end of the next motion command
and pauses there (sequencer.go: isMotionCmd); the lines in between -- the
assignments, the O-word repeat, the DEBUGs and the M67 -- run without a stop
of their own.  So every step is checked against the move it must have made:
the task reports PAUSED with the axes at that move's end point.  A step that
ran on past its move is never paused there and fails the wait, however the
polls fall; a step that did not move at all does too.  The M67 counter is
checked at each stop, and the step after the last move ends the program.

Each wait is for a state that holds until the next step is sent, so a poll
cannot miss it -- unlike the exec_state "waiting for motion" window this test
used to watch, which a zero-length or quick step could open and close between
two polls.
"""

import subprocess
import sys

import gmi
from gmi.constants import *

import stmak_test

c = stmak_test.Command()
s = gmi.Stat()

EPS = 1e-6      # inch


def counter():
    # halcmd getp prints "<type> <dir> <name> = <value>"; take the last field.
    out = subprocess.check_output(["halcmd", "getp", "motion.analog-out-00"])
    return float(out.split()[-1])


def xy(st):
    # actual_position is stmak-mm; the program runs G20 on this inch config.
    return st.actual_position[0] / 25.4, st.actual_position[1] / 25.4


def stops():
    """(x, y, counter) at the end of every move of test.ngc, in order.

    From the second pass on G1 X[5 + #1] is where the last pass ended: a
    move of length zero queues no motion, so no step stops after it.
    """
    at = (0.0, 0.0)
    for a in range(0, 30, 5):          # #1 = 0, 5, ... 25; M67 Q[#1] first
        for to in ((5 + a, at[1]),     # G1 X[5 + #1]
                   (5 + a, 5 + a),     # G1 Y[5 + #1]
                   (10 + a, 5 + a)):   # G1 X[10 + #1]
            if to != at:
                at = to
                yield to[0], to[1], a


def state(st):
    x, y = xy(st)
    return "interp_state=%d X%.4f Y%.4f line=%d" % (st.interp_state, x, y,
                                                    st.current_line)


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0)
c.home(1)
c.home(2)
stmak_test.wait_stat(s, lambda st: all(st.homed[0:3]),
                     "joints 0-2 to finish homing",
                     detail=lambda st: "homed=%s" % (list(st.homed[0:3]),))

c.mode(MODE_AUTO)
c.program_open('test.ngc')

for n, (gx, gy, gc) in enumerate(stops(), 1):
    c.auto(AUTO_STEP)
    stmak_test.wait_stat(
        s,
        lambda st: st.interp_state == INTERP_PAUSED
        and abs(xy(st)[0] - gx) < EPS and abs(xy(st)[1] - gy) < EPS,
        "step %d to pause at the end of its move, X%g Y%g" % (n, gx, gy),
        detail=state)
    got = counter()
    if got != gc:
        stmak_test.fail("step %d: M67 counter %g, want %g" % (n, got, gc))
    sys.stderr.write("step %d: paused at X%g Y%g, counter %g\n" % (n, gx, gy, got))

c.auto(AUTO_STEP)
stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                     "the step after the last move to end the program",
                     detail=state)
s.poll()
if abs(xy(s)[0] - 35) > EPS or abs(xy(s)[1] - 30) > EPS:
    stmak_test.fail("the program ended away from its last move: %s" % state(s))
sys.stderr.write("Finished: program ended after %d steps\n" % (n + 1))
