#!/usr/bin/env python3
# Ported to the stratuMAK gmi REST/WS client (was the removed NML linuxcnc module).
#
# Reproduces the read-ahead / stop-button overrun bug (#865, cf. #579/#393/#241):
# fill the interp read-ahead queue while running a program, hit stop (abort), and
# verify the machine does NOT keep cutting past the stop point. Axis position now
# comes from s.actual_position (the userspace test-ui HAL comp + postgui.hal are
# gone); the read-ahead depth comes from s.queue (motion queue depth).

import gmi
from gmi.constants import *
import stmak_test

import sys


#
# connect to LinuxCNC (stratuMAK)
#
# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()
stmak_test.wait_for_startup(s)


def xy():
    s.poll()
    p = s.actual_position
    return p[0], p[1]


def print_stats(x=None, y=None):
    px, py = xy()
    if x is None:
        sys.stderr.write("queue=%d; queue_full=%s; x=%.3f; y=%.3f\n" %
                         (s.queue, s.queue_full, px, py))
    else:
        sys.stderr.write("queue=%d; queue_full=%s; x=%.3f(%.3f); y=%.3f(%.3f)\n" %
                         (s.queue, s.queue_full, px, x - px, py, y - py))


#
# Come out of E-stop, turn the machine on, home
#
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "X, Y and Z homed")

#
# Set up tool and start position
#
c.mode(MODE_MDI)
c.mdi('T1M6')       # Load tool 1
c.mdi('G0 X2 Y2')   # Move near to start: unit-less MDI runs in the machine's
                    # units (G20 on this inch config, matching 2.9) = 2 inches.
c.wait_complete()
# s.actual_position is stmak-mm: 2 in = 50.8 mm.
start_mm = 2 * 25.4
stmak_test.wait_stat(
    s, lambda st: abs(st.actual_position[0] - start_mm) <= .001
                  and abs(st.actual_position[1] - start_mm) <= .001,
    "the start position X2 Y2", detail=lambda st: "at %r" % (st.actual_position[:2],))
px, py = xy()
sys.stderr.write("Starting at X=%.3f Y=%.3f\n" % (px, py))


#
# Run the generated program and wait for the read-ahead queue to fill up.
#
c.mode(MODE_AUTO)
c.program_open('3D_Chips.ngc')
c.auto(AUTO_RUN, 0)
stmak_test.wait_stat(s, lambda st: st.queue > 1000,
                     "the program to fill the motion queue",
                     detail=lambda st: "queue=%d" % st.queue)
sys.stderr.write("Program partially loaded\n")

#
# Pause, then abort, then require that nothing moved after the abort.
#
# The bug (#865): the abort's state-tag restore flushed segments the canon
# still buffered into the queue, and the machine went on cutting after the
# stop. Pausing first leaves the queue full but the machine standing, so the
# correct answer is exact -- no motion at all after the abort -- and does not
# depend on when the abort reaches motion. (Aborting at full speed measured
# the overrun against a position read before the abort over REST: any row
# end within that latency, at 400 in/s, failed the test on correct code.)
# minmax on X/Y in the servo thread (watch.hal) records where the machine
# went; it is armed once the machine stands.
c.auto(AUTO_PAUSE)
stmak_test.wait_stat(s, lambda st: st.paused and st.current_vel == 0.0,
                     "the paused program to stand still",
                     detail=lambda st: "paused=%s current_vel=%r" % (st.paused, st.current_vel))
px, py = xy()
stmak_test.setp("watch-reset", False)
c.abort()
c.wait_complete()
stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE
                     and st.queue == 0 and st.inpos,
                     "the aborted program to settle")

# The joints' interpolators may still have been converging on the paused
# position by a few micrometres when the watch was armed; a crazy move is
# millimetres.
TOL_MM = 0.01
fail = False
for axis, pos in (("X", px), ("Y", py)):
    lo = stmak_test.getp("%smm.min" % axis.lower())
    hi = stmak_test.getp("%smm.max" % axis.lower())
    moved = max(abs(hi - pos), abs(pos - lo))
    sys.stderr.write("%s: paused at %.4f mm, range after the abort %.4f..%.4f\n"
                     % (axis, pos, lo, hi))
    if moved > TOL_MM:
        sys.stderr.write("ERROR:  %s axis moved %.3f mm after the abort\n" % (axis, moved))
        fail = True

if fail:
    sys.exit(1)

sys.stderr.write('Success\n')
sys.exit(0)
