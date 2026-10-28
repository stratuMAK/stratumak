#!/usr/bin/env python3

# Ported to the stratuMAK REST/WS API: uses the `gmi` client instead of the removed
# NML `linuxcnc` module.
#
# How far X got before the abort is measured in the servo thread by a minmax
# component (motion-test.hal), held in reset outside each program run. This used
# to be a streamed halsampler record filtered by Z afterwards. Its "max X0.000,
# 0 samples" failures on CI were not stream loss, as first assumed, but the
# dropped MDI described in run_and_abort: the machine never reached the run's
# Z level, so the filter found nothing.

import gmi
from gmi.constants import *
import stmak_test
import sys, os

# stratuMAK: HAL joint positions are millimetres (mm-everywhere convention);
# the program and the expectations are inch.
MM = 25.4


def check_max_x(z_lev, expected_max_x):
    res = 0
    max_x = stmak_test.getp('x-max') / MM
    print("z=%.1f; max_x = %.6f" % (z_lev, max_x))
    if abs(max_x - expected_max_x) > 0.1:
        print("*** ERROR max X%.3f != expected X%.3f" % (max_x, expected_max_x))
        res += 1
    return res


def check_status(msg, expected_mode, expected_p, expected_q):
    res = 0
    s.poll()
    control_mode = None
    for i in s.gcodes:
        if i in (610, 611, 640):
            control_mode = i
    if len(s.settings) == 3:
        print("%s:  Control mode = %d" % (msg, control_mode))
        p, q = (expected_p, expected_q)
    else:
        print("%s:  Control mode = %d; tolerances = P%.3f Q%.3f" % (
            msg, control_mode, s.settings[3], s.settings[4]))
        p, q = (s.settings[3], s.settings[4])
    if control_mode != expected_mode:
        print("*** ERROR control mode %d != expected %d" % (control_mode, expected_mode))
        res += 1
    if expected_p is not None and abs(p - expected_p) > 0.001:
        print("*** ERROR blend tolerance P%.3f != expected P%.3f" % (p, expected_p))
        res += 1
    if expected_q is not None and abs(q - expected_q) > 0.001:
        print("*** ERROR naive CAM tolerance Q%.3f != expected Q%.3f" % (q, expected_q))
        res += 1
    return res


def run_and_abort(msg, z_lev, expected_max_x, expected_mode, expected_p, expected_q, *init_cmds):
    res = 0
    c.mode(MODE_MDI)
    c.mdi('F1200 G0X0Y0Z%.1f' % z_lev)
    c.wait_complete()
    # Every run starts from X0 Y0 at its own Z level. Checked, not assumed:
    # this MDI follows the previous run's abort, and when the planner dropped
    # it ("TP is aborting") the run started where that abort had left the
    # machine — already past Y1.0, so it was aborted at once and reported a
    # wrong max X (CI, "G64 redo").
    stmak_test.wait_stat(
        s, lambda st: all(abs(a - b) < 1e-6 for a, b in
                          zip(st.actual_position[:3], (0.0, 0.0, z_lev))),
        "%s: the machine at X0 Y0 Z%.1f" % (msg, z_lev),
        detail=lambda st: "actual_position=%r" % (st.actual_position[:3],))
    for cmd in init_cmds:
        print("Running command '%s'" % cmd)
        c.mdi(cmd)
        c.wait_complete()
    stmak_test.drain_mdi(s)
    res += check_status('%s post-command' % msg, expected_mode, expected_p, expected_q)

    print("Running program 'test.ngc'")
    # The machine stands at X0: measure from here.
    stmak_test.setp('x-max-reset', False)
    c.mode(MODE_AUTO)
    c.program_open("test.ngc")
    c.auto(AUTO_RUN, 0)

    # No wait_complete() here. stratuMAK's WaitComplete settles on the interpreter
    # going idle, so after AUTO_RUN it does not return until the PROGRAM ends —
    # and this program is one we deliberately abort part-way through, so it
    # never would. (Classic NML's wait_complete only acked the command, which is
    # why the idiom reads as harmless.) It used to "work" solely because the
    # 5s timeout returned -1 unchecked, making it an accidental sleep. The real
    # precondition for aborting is that motion has started, which is what the
    # wait below actually establishes.
    # Wait on ACTUAL position, not commanded. What this test measures is how far
    # the real path got in X before the abort (5.0 exact-stop vs 3.7 blended), so
    # aborting is only meaningful once the machine has physically finished the
    # first zig out to X5. Commanded position completes that zig almost
    # immediately, while the axes are still near X0 — the abort then landed at
    # max_x ~0.4 and the tolerance under test never showed up in the samples.
    # This only worked before because the wait_complete() above burned ~5s of
    # real running time first, by silently timing out.
    #
    # Bounded: an unbounded wait here hung the whole test run when the server
    # came up without its REST/WS listener (stale instance holding the port) —
    # and would equally hang on any real never-moves bug.
    stmak_test.wait_stat(
        s, lambda st: st.actual_position[1] >= 1.0,
        "%s: the machine to actually reach Y1.0 (first zig complete)" % msg,
        timeout=30.0,
        detail=lambda st: "actual_position=%r" % (st.actual_position[:3],))
    res += check_status('%s pre-abort' % msg, expected_mode, expected_p, expected_q)
    c.abort()
    c.wait_complete()
    # The planner decelerates along the path after an abort: wait for it to
    # stop, so max X is final when it is read.
    stmak_test.wait_stat(s, lambda st: st.inpos and st.queue == 0,
                         "%s: the machine to stop after the abort" % msg)

    res += check_status('%s post-abort' % msg, expected_mode, expected_p, expected_q)
    res += check_max_x(z_lev, expected_max_x)
    stmak_test.setp('x-max-reset', True)
    print()
    return res


# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
# machine_units(): this config is LINEAR_UNITS=inch and the test's thresholds
# (Y1.0, max X5.0) are inch, as classic linuxcnc.stat() reported them. Plain
# gmi.Stat is mm-everywhere, so a bare `>= 1.0` against it means 1 MILLIMETRE —
# it tripped ~4% into the first zig and the abort landed at max_x ~0.45 instead
# of 5.0. This view reports what the test was written against.
s = gmi.Stat().machine_units()
e = gmi.ErrorChannel()

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "X, Y and Z homed")

res = 0
res += run_and_abort('G61',          0.0, 5.0, 610, None, None, 'G61')
res += run_and_abort('G61 redo',     0.5, 5.0, 610, None, None)
res += run_and_abort('G61.1',        1.0, 5.0, 611, None, None, 'G61.1')
res += run_and_abort('G61.1 redo',   1.5, 5.0, 611, None, None)
res += run_and_abort('G64P0.5',      2.0, 4.5, 640,  0.5,  0.0, 'G64P0.5Q0')
res += run_and_abort('G64P0.5 redo', 2.5, 4.5, 640,  0.5,  0.0)
res += run_and_abort('G64',          3.0, 3.7, 640,  0.0,  0.0, 'G64')
res += run_and_abort('G64 redo',     3.5, 3.7, 640,  0.0,  0.0)
res += run_and_abort('G64P0Q6',      4.0, 0.0, 640,  0.0,  6.0, 'G64Q6')
res += run_and_abort('G64P0Q6 redo', 4.5, 0.0, 640,  0.0,  6.0)

for f in ('sim.var', 'sim.var.bak'):
    if os.path.exists(f):
        os.unlink(f)
print("Exiting with %d errors" % res)
sys.exit(res)
