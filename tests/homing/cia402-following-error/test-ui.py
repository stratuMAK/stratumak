#!/usr/bin/env python3

# Regression driver for the CiA 402 drive-internal homing following-error bug.
#
# The simulated drive (sim_cia402_drive) reproduces the real drive's behaviour:
# during homing it seeks a switch (pos-fb 0 -> 5 mm) then, at homing-attained,
# atomically redefines its origin — pos-fb STEPS 5 -> 0 mm. Before the
# get_at_index_search_wait() fix in homemod_cia402.c, control.c computed the
# following error from that stepped feedback (against the not-yet-resynced
# offset) BEFORE the homing tick could correct it, tripping a spurious
# "joint 0 following error" and switching the machine off — even though the
# joint was marked homed.
#
# PASS  (fixed):   joint homes, machine stays ON, no following error, DRO moved.
# FAIL  (pre-fix): machine leaves STATE_ON during homing (motion error).

import gmi
import gomc_test
from gmi.constants import *

import time
import sys

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


def drain_errors(acc):
    while True:
        m = e.poll()
        if m is None:
            return
        acc.append(m[1])


# Bring the machine up.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
gomc_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MANUAL,
    "machine ON in manual mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

s.poll()
start_pos = s.joint_actual_position[0]
print("joint 0 start position = %.4f mm" % start_pos)

errors = []
drain_errors(errors)  # discard anything stale from bring-up

# Command drive-internal homing of joint 0.
c.home(0)

deadline = time.monotonic() + 15.0 * gomc_test.scale()
peak_move = 0.0
homed = False
while time.monotonic() < deadline:
    s.poll()
    peak_move = max(peak_move, abs(s.joint_actual_position[0] - start_pos))
    drain_errors(errors)

    # THE REGRESSION: the machine must never leave STATE_ON while homing.
    if s.task_state != STATE_ON:
        print("FAIL: machine left STATE_ON during homing (task_state=%d, peak move=%.4f mm)"
              % (s.task_state, peak_move))
        if errors:
            print("  operator errors: %r" % errors)
        sys.exit(1)

    if s.homed[0]:
        homed = True
        break
    time.sleep(0.02)

if not homed:
    print("FAIL: joint 0 did not home within the deadline (task_state=%d, peak move=%.4f mm)"
          % (s.task_state, peak_move))
    if errors:
        print("  operator errors: %r" % errors)
    sys.exit(1)

# The step lands at homing-attained; give a moment for any late fault to surface.
time.sleep(0.3)
s.poll()
drain_errors(errors)

ok = True
if s.task_state != STATE_ON:
    print("FAIL: machine switched off just after homing (task_state=%d)" % s.task_state)
    ok = False
if any("following error" in m.lower() for m in errors):
    print("FAIL: a following error was reported during homing: %r" % errors)
    ok = False
if peak_move < 1.0:
    print("FAIL: joint position did not move during homing (peak move=%.4f mm, expected ~5)"
          % peak_move)
    ok = False

if ok:
    print("PASS: joint 0 homed=%s, machine still ON, DRO moved (peak=%.4f mm), no following error"
          % (s.homed[0], peak_move))
    sys.exit(0)
sys.exit(1)
