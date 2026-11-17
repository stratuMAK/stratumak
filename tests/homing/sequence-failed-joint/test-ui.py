#!/usr/bin/env python3

# Regression driver: a home-all sequence stops at a joint that failed to home.
#
# Joint 1 homes first (HOME_SEQUENCE 0), joint 0 after it (1): the order a
# machine sets because the second axis may only travel once the first is out
# of the way.  Joint 1's drive reports a homing error.  homemod_cia402 ends
# that joint idle and unhomed -- and the sequence took "idle" for "finished"
# and went on to home joint 0.
#
# PASS (fixed):   joint 0 is never started: unhomed, its drive has not moved.
#                 The sequence is over, so a second home-all with the error
#                 gone homes both.
# FAIL (pre-fix): joint 0 homes behind the failed joint 1.

import gmi
import stmak_test
from gmi.constants import *

import subprocess
import time
import sys

c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


def getp(name):
    out = subprocess.check_output(["halcmd", "getp", name])
    return float(out.split()[-1])


def sets(name, value):
    subprocess.check_call(["halcmd", "sets", name, str(value)])


def fail(msg):
    print("FAIL: " + msg)
    sys.exit(1)


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
stmak_test.wait_stat(
    s, lambda st: st.task_state == STATE_ON and st.task_mode == MODE_MANUAL,
    "machine ON in manual mode",
    detail=lambda st: "task_state=%d task_mode=%d" % (st.task_state, st.task_mode))

# ── joint 1 fails ────────────────────────────────────────────────────────────
sets("j1-home-err", 1)
start0 = getp("simdrv.0.pos-fb")
c.home(-1)

# Joint 1 starts homing and gives up on the drive's error: its message is
# the sign that it did (the homing itself lasts a few cycles).
deadline = time.monotonic() + 10.0 * stmak_test.scale()
while True:
    m = e.poll()
    if m is not None and "homing error" in m[1]:
        break
    if time.monotonic() > deadline:
        fail("joint 1 never reported its drive's homing error")
    time.sleep(0.01)

# Joint 0's turn would come now.  Its drive seeks at 20 mm/s: one second is
# the whole homing run, several times over.
deadline = time.monotonic() + 1.0 * stmak_test.scale()
while time.monotonic() < deadline:
    s.poll()
    if s.joint[0]["homing"] or s.homed[0]:
        fail("joint 0 was homed behind joint 1, which failed (homing=%d homed=%d)"
             % (s.joint[0]["homing"], s.homed[0]))
    time.sleep(0.02)
if s.homed[1]:
    fail("joint 1 reads homed after its drive reported a homing error")
moved = getp("simdrv.0.pos-fb") - start0
if abs(moved) > 1e-6:
    fail("joint 0's drive moved %.4f during the failed sequence" % moved)

# ── the sequence is over: the next home-all works ────────────────────────────
sets("j1-home-err", 0)
c.home(-1)
stmak_test.wait_stat(s, lambda st: st.homed[0] and st.homed[1], "both joints homed",
                     detail=lambda st: "homed=%r" % (st.homed[:2],))

print("PASS: the sequence stopped at the failed joint, and ran through once it homed")
sys.exit(0)
