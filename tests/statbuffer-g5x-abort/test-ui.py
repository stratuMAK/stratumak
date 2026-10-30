#!/usr/bin/env python3
"""After an abort, interp's state is synched from the status channel.

test.ngc runs in G55, starts a slow move, and the interpreter reads ahead
into G56 and a queue buster.  The abort lands during that move; afterwards
status, MDI motion and interp's own parameters must all say G55 again.

Every step waits for a state that holds until the next command instead of
sleeping: the abort is sent once the status shows the read-ahead G56 with
the G1 under way (the move takes minutes at F1, so there is no hurry), and
the checks run once the task is idle after it.  An abort sent before the
G56 was read would leave nothing to resynch, so that is the trigger, not a
guess at how long reading ahead takes.
"""

import math
import sys

import gmi
from gmi.constants import *
import stmak_test

retval = 0

# stratuMAK reports positions and offsets in millimetres (mm-everywhere
# convention); the program/expectations here are inch.
MM = 25.4
EPS = 1e-6

# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


def g5x(st):
    return [g / 10 for g in st.gcodes if 540 <= g < 600]


def state(st):
    return ("interp_state=%d exec_state=%d motion_line=%d g5x=%s g5x_index=%d "
            "pos=%.4f,%.4f,%.4f" % (st.interp_state, st.exec_state,
                                    st.motion_line, g5x(st), st.g5x_index,
                                    st.position[0], st.position[1],
                                    st.position[2]))


def mdi(cmd):
    c.mdi(cmd)
    stmak_test.drain_mdi(s)


def wait_motion_done(what):
    stmak_test.wait_stat(s, lambda st: st.queue == 0 and st.inpos,
                         "%s to finish moving" % what, detail=state)


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)

c.mode(MODE_AUTO)
c.program_open('test.ngc')

c.mode(MODE_MDI)
mdi('G10 L2 P1 X0 Y0 Z-4 (Set G54)')
mdi('G10 L2 P2 X0 Y0 Z-5 (Set G55)')
mdi('G10 L2 P3 X0 Y0 Z-6 (Set G56)')
mdi('G54 (Use G54)')
mdi('G0 X0 Y0 Z0 (Move to 0,0,0)')
wait_motion_done("the move to G54 0,0,0")

c.mode(MODE_AUTO)
c.auto(AUTO_RUN, 0)

# Interp has read ahead into G56 (g5x_index 3) while motion runs the G1,
# the only move that leaves X=0.
stmak_test.wait_stat(s, lambda st: st.g5x_index == 3 and st.position[0] > 0,
                     "the G1 to run with G56 read ahead", detail=state)
c.abort()
c.wait_complete()
stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE
                     and st.exec_state == EXEC_DONE and st.queue == 0,
                     "the task to settle after the abort", detail=state)

# The program was aborted in g55; check.
if 550 not in s.gcodes:
    print("Current coordinate system is G%s, not G55" % (g5x(s) + [None])[0])
    retval = 1
else:
    print("Current coordinate system is G55")

# MDI G0 takes us to the programmed location in G55
c.mode(MODE_MDI)
mdi('G0 X0 Y0 Z0 (Move to 0,0,0)')
wait_motion_done("'G0 X0 Y0 Z0'")

if math.fabs(s.position[0]) > EPS:
    print("'G0 X0 Y0 Z0' took us to X=%.6f, should be 0" % s.position[0])
    retval = 1

if math.fabs(s.position[1]) > EPS:
    print("'G0 X0 Y0 Z0' took us to Y=%.6f, should be 0" % s.position[1])
    retval = 1

if math.fabs(s.position[2] + 5*MM) > EPS:
    print("'G0 X0 Y0 Z0' took us to Z=%.6f, should be %.1f (-5 in)" % (s.position[2], -5*MM))
    retval = 1

# Interp's own view: #5220 is the coordinate system index, #5422 the Z
# position in it.  The reply travels the error channel; nothing else is sent
# there meanwhile, so the next debug message is this one.
mdi('(debug,G53+#5220 ?= G55; Z#5422 ?= Z0.0)')
got = []


def debug_reply():
    m = e.poll()
    while m:
        got.append(m[1])
        m = e.poll()
    return [t for t in got if "?= G55" in t]


reply = stmak_test.wait_until(debug_reply, "interp's debug reply",
                              detail=lambda: "messages: %r" % got)[0]
print(reply)
if reply != "G53+2.000000 ?= G55; Z0.000000 ?= Z0.0":
    print("interp disagrees with status: %r" % reply)
    retval = 1

s.poll()
if s.g5x_index != 2:
    print("status has wrong g5x index: %d (expected 2)" % s.g5x_index)
    retval = 1
else:
    print("status has correct g5x index:  2")

if math.fabs(s.g5x_offset[0]) > EPS:
    print("g5x_offset.x is %.6f, should be 0" % s.g5x_offset[0])
    retval = 1

if math.fabs(s.g5x_offset[1]) > EPS:
    print("g5x_offset.y is %.6f, should be 0" % s.g5x_offset[1])
    retval = 1

if math.fabs(s.g5x_offset[2] + 5*MM) > EPS:
    print("g5x_offset.z is %.6f, should be %.1f (-5 in)" % (s.g5x_offset[2], -5*MM))
    retval = 1

if retval == 0:
    print("Everything ok!")

sys.exit(retval)
