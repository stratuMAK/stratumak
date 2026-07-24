#!/usr/bin/env python3

# Regression driver for the disable->enable jerk-filter re-anchor.
#
# While the machine is DISABLED the jerk filter does not run and pos_cmd
# tracks pos_fb ("set position commands to match feedbacks, this avoids
# disturbances when enabling"). The filter ring, however, still holds the
# pre-disable positions: without re-anchoring it on the enabling transition,
# the first enabled cycle commands a single-cycle step from the current
# position back to the pre-disable window average. The discriminating case is
# an axis that MOVED while the machine was off (brake released, gravity sag,
# pushed by hand, absolute encoder waking up elsewhere): the step is the full
# displacement — the same drive-fault lurch the homing frame-shift fix
# closed, at the machine-on transition instead.
#
# Sequence: machine ON at 0 (perfect-servo loop) -> machine OFF -> feedback
# switched to a "pushed" position 5 mm away (pos_cmd tracks it while
# disabled) -> arm the step watchdog -> machine ON again.
#
# PASS (fixed):   machine comes ON and stays ON, no step (watchdog well under
#                 0.1 mm), and a subsequent jog moves normally.
# FAIL (pre-fix): the first enabled cycle steps ~5 mm (watchdog trips) and/or
#                 the following error faults the machine off.

import gmi
import gomc_test
from gmi.constants import *

import subprocess
import time
import sys

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

errors = []


def drain_errors():
    while True:
        m = e.poll()
        if m is None:
            return
        errors.append(m[1])


def halcmd(*args):
    subprocess.run(["halcmd", *args], check=True)


def getp(name):
    out = subprocess.check_output(["halcmd", "getp", name])
    return float(out.split()[-1])


PUSH = 5.0

# Phase 1: bring the machine up with the perfect-servo loop.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
gomc_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MANUAL,
    "machine ON in manual mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

# Phase 2: machine off, then "push" the axis: feedback switches to the
# driver-set position and the disabled tracking follows it.
c.state(STATE_OFF)
# "Machine off, not estopped" reports as STATE_ESTOP_RESET (classic 2.9
# determineState semantics) — wait for leaving ON, not for a literal
# STATE_OFF.
gomc_test.wait_stat(s, lambda st: st.task_state != STATE_ON, "machine OFF",
                    detail=lambda st: "task_state=%d" % st.task_state)

halcmd("sets", "j0-push", str(PUSH))
halcmd("sets", "j0-pushsel", "1")

deadline = time.monotonic() + 5.0 * gomc_test.scale()
while time.monotonic() < deadline:
    if abs(getp("joint.0.motor-pos-cmd") - PUSH) < 1e-6:
        break
    time.sleep(0.02)
else:
    print("FAIL: disabled tracking never followed the pushed feedback "
          "(motor-pos-cmd=%.4f, want %.1f)" % (getp("joint.0.motor-pos-cmd"), PUSH))
    sys.exit(1)

drain_errors()
errors.clear()

# Phase 3: arm the watchdog, re-enable, and give the filter a full window
# (30 cycles) plus margin to show any drain.
halcmd("sets", "j0-watch-arm", "1")
c.state(STATE_ON)
time.sleep(0.3)
s.poll()
drain_errors()

max_step = getp("watch.0.max-step")

ok = True
if s.task_state != STATE_ON:
    print("FAIL: machine did not stay ON after re-enable (task_state=%d) — "
          "stale filter history commanded an excursion the following-error "
          "check caught" % s.task_state)
    ok = False
if errors:
    print("FAIL: operator errors on re-enable: %r" % errors)
    ok = False
# Normal per-cycle motion at MAX_VELOCITY=10 mm/s is 0.01 mm; the un-anchored
# history steps ~5 mm (the push displacement) in one cycle.
if max_step > 0.1:
    print("FAIL: motor-pos-cmd stepped %.4f mm in one servo cycle on "
          "re-enable — jerk-filter history not re-anchored" % max_step)
    ok = False

if not ok:
    sys.exit(1)

# Phase 4: liveness — the re-anchored filter must still pass motion. Close
# the servo loop again first (feedback follows the command; the values are
# equal at this instant, so the switchover is seamless) or the pinned
# feedback trips the following-error check the moment the jog moves. Then
# jog and verify the axis moves smoothly (watchdog stays armed: the jog must
# not trip it either).
halcmd("sets", "j0-pushsel", "0")
c.jog(JOG_INCREMENT, True, 0, 5.0, 2.0)
deadline = time.monotonic() + 5.0 * gomc_test.scale()
moved = False
while time.monotonic() < deadline:
    if abs(getp("joint.0.motor-pos-cmd") - (PUSH + 2.0)) < 0.01:
        moved = True
        break
    time.sleep(0.02)

s.poll()
drain_errors()
max_step = getp("watch.0.max-step")
if s.task_state != STATE_ON or errors:
    print("FAIL: jog after re-enable faulted (task_state=%d, errors=%r)"
          % (s.task_state, errors))
    sys.exit(1)
if not moved:
    print("FAIL: jog after re-enable did not reach target "
          "(motor-pos-cmd=%.4f, want %.1f) — filter wedged?"
          % (getp("joint.0.motor-pos-cmd"), PUSH + 2.0))
    sys.exit(1)
if max_step > 0.1:
    print("FAIL: jog after re-enable stepped %.4f mm in one cycle" % max_step)
    sys.exit(1)

print("PASS: re-enable after off-state movement is step-free "
      "(max per-cycle step %.4f mm) and motion still runs" % max_step)
sys.exit(0)
