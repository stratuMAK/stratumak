#!/usr/bin/env python3

# Regression driver for the homing frame shift vs. jerk filter bug on the
# CiA 402 drive-internal homing path.
#
# At homing-attained the drive atomically redefines its origin (sim drive:
# pos-fb steps 5 -> 0 mm) and homemod_cia402 resyncs the joint frame:
# motor_offset is recomputed and pos_cmd is set to home_offset. Unlike
# switch homing, here the motor command MUST step (the drive's own origin
# stepped), so a command-step watchdog is the wrong instrument. The failure
# signature is physical instead: with MAX_JERK enabled and an unshifted
# boxcar-filter history, the old-frame command drains over a full window
# (31 cycles here) — longer than the drive's 5-cycle opmode handshake back
# to CSP — so the drive is commanded away from its freshly-set origin and
# takes a ~4 mm excursion right after homing. Fixed by shifting the filter
# history in the joint_set_pos_cmd mot callback (same fix as the
# switch-homing lurch on mds-swm-fm45a).
#
# PASS (fixed):   joint homes, machine stays ON, no errors, drive feedback
#                 stays at the redefined origin (max |pos-fb| ~ 0 mm).
# FAIL (pre-fix): watchdog records a ~4 mm feedback excursion after
#                 homing-attained.

import gmi
import stmak_test
from gmi.constants import *

import subprocess
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

s.poll()
start_pos = s.joint_actual_position[0]

drain_errors()
errors.clear()  # discard anything stale from bring-up

# Command drive-internal homing of joint 0.
c.home(0)

# Homed, or the machine switched off trying (THE REGRESSION: it must never
# leave STATE_ON while homing; f-errored is checked from the servo-thread
# record below).
stmak_test.wait_stat(s, lambda st: st.homed[0] or st.task_state != STATE_ON,
                     "joint 0 to home", timeout=15.0)
drain_errors()
if s.task_state != STATE_ON:
    print("FAIL: machine left STATE_ON during homing (task_state=%d)" % s.task_state)
    if errors:
        print("  operator errors: %r" % errors)
    sys.exit(1)

# Then at rest: in free mode in-position waits for the joint's planner and the
# jerk filter behind it, so whatever the homing sequence moves has played out
# (a fixed 0.3 s stood here).
stmak_test.wait_stat(s, lambda st: st.inpos, "joint 0 to come to rest after homing")

s.poll()
drain_errors()

# halcmd getp prints "<type> <dir> <name> = <value>"-style output; take the
# last field.
out = subprocess.check_output(["halcmd", "getp", "watch.0.max-abs"])
excursion = float(out.split()[-1])



# How far the drive travelled, and whether a following error was ever latched,
# from the servo-thread records (cia402-sim.hal).
peak_move = max(stmak_test.getp("seek.0.max") - start_pos, start_pos - stmak_test.getp("seek.0.min"))
ferr_latched = stmak_test.getp("ferrmax.0.max") > 0.5

ok = True
if ferr_latched:
    print("FAIL: joint 0 latched a following error during homing")
    ok = False
if s.task_state != STATE_ON:
    print("FAIL: machine switched off after homing (task_state=%d)" % s.task_state)
    ok = False
if errors:
    print("FAIL: operator errors during homing: %r" % errors)
    ok = False
# After the origin redefinition the drive sits at 0 and (HOME == HOME_OFFSET)
# nothing may command it away. The un-shifted-filter bug drives it to ~4 mm.
if excursion > 0.5:
    print("FAIL: drive moved %.4f mm away from its redefined origin after "
          "homing-attained — stale jerk-filter history drained into the drive"
          % excursion)
    ok = False
if peak_move < 1.0:
    print("FAIL: joint position did not move during homing (peak move=%.4f mm, "
          "expected ~5)" % peak_move)
    ok = False

if ok:
    print("PASS: cia402 drive homing with jerk filter, machine ON, no errors, "
          "DRO moved (peak=%.4f mm), post-resync excursion %.4f mm"
          % (peak_move, excursion))
    sys.exit(0)
sys.exit(1)
