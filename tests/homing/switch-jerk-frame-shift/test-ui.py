#!/usr/bin/env python3

# Regression driver for the homing coordinate-frame shift vs. jerk filter bug.
#
# Switch-type homing redefines the joint coordinate frame at switch trip
# (HOME_SET_COARSE_POSITION): pos_cmd/pos_fb/free_tp.curr_pos shift by
# home_offset - trip_position while motor_offset shifts the other way, so the
# motor command is nominally continuous. The FREE-mode jerk-limiting boxcar
# filter, however, keeps a history of old-frame positions: unless that history
# is shifted too, the filtered pos_cmd drains to the new frame over a full
# window while motor_offset has already jumped — commanding a physical step of
# the whole offset. On a real machine (mds-swm-fm45a) that was a max-velocity
# lurch into the just-tripped switch and a drive ("amplifier") fault the moment
# the home/limit switch closed.
#
# Setup: switch at +8 mm, HOME_OFFSET 2 mm -> 6 mm frame shift at trip;
# MAX_JERK gives a 30-cycle filter window. The home switch is also wired as the
# positive limit switch with HOME_IGNORE_LIMITS, like the real machine.
#
# PASS (fixed):   homing completes, machine stays ON, no operator errors, and
#                 the watchdog's max per-cycle motor-cmd step stays at normal
#                 motion levels (~vel * period = 0.01 mm).
# FAIL (pre-fix): watchdog records a ~6 mm single-cycle step (and on real iron
#                 the machine faults).

import gmi
import stmak_test
from gmi.constants import *

import subprocess
import time
import sys

c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

errors = []


def drain_errors():
    while True:
        m = e.poll()
        if m is None:
            return
        errors.append(m[1])


# Bring the machine up.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MANUAL,
    "machine ON in manual mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

drain_errors()
errors.clear()  # discard anything stale from bring-up

c.home(0)

deadline = time.monotonic() + 20.0 * stmak_test.scale()
homed = False
while time.monotonic() < deadline:
    s.poll()
    drain_errors()

    # The machine must never leave STATE_ON while homing: the tripped limit
    # switch is the home switch and HOME_IGNORE_LIMITS is set.
    if s.task_state != STATE_ON:
        print("FAIL: machine left STATE_ON during homing (task_state=%d)" % s.task_state)
        if errors:
            print("  operator errors: %r" % errors)
        sys.exit(1)

    if s.homed[0]:
        homed = True
        break
    time.sleep(0.02)

if not homed:
    print("FAIL: joint 0 did not home within the deadline (task_state=%d)" % s.task_state)
    if errors:
        print("  operator errors: %r" % errors)
    sys.exit(1)

# Give a moment for any late fault (limit error after homing flag drops,
# following error, amp fault) to surface.
time.sleep(0.3)
s.poll()
drain_errors()

# halcmd getp prints "<type> <dir> <name> = <value>"-style output; take the
# last field.
out = subprocess.check_output(["halcmd", "getp", "watch.0.max-step"])
max_step = float(out.split()[-1])

ok = True
if s.task_state != STATE_ON:
    print("FAIL: machine switched off after homing (task_state=%d)" % s.task_state)
    ok = False
if errors:
    print("FAIL: operator errors during homing: %r" % errors)
    ok = False
# Normal per-cycle motion at MAX_VELOCITY=10 mm/s and 1 ms servo period is
# 0.01 mm; the un-shifted-filter bug steps ~6 mm in one cycle. 0.1 mm sits
# 10x above normal and 60x below the bug.
if max_step > 0.1:
    print("FAIL: motor-pos-cmd stepped %.4f mm in one servo cycle — homing "
          "frame shift leaked into the motor command" % max_step)
    ok = False
if max_step <= 0.0:
    print("FAIL: watchdog saw no motion at all (max-step=%.6f) — miswired test?"
          % max_step)
    ok = False

if ok:
    print("PASS: homed on shared home/limit switch, machine ON, no errors, "
          "max per-cycle motor-cmd step %.4f mm" % max_step)
    sys.exit(0)
sys.exit(1)
