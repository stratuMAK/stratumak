#!/usr/bin/env python3
"""A joint pushed past its soft limit by hand, and the way back (milltask).

The amps being off is exactly when an operator *can* move a joint by hand, so
this is the normal way an axis ends up outside its soft limits, not an exotic
state. Three things have to hold, and each was broken in its own place:

  Nothing is wrong while the machine is off. A disabled machine's command is
  slaved to its feedback so that enabling does not jump it, and measuring that
  mirror against the limits reports a trajectory-planner fault for something
  the planner did not do.

  The machine still has to switch on. The operator needs the axis powered to
  drive it back, so a limit trip that refuses the enable traps them.

  And the way back has to exist: a jog *toward* the valid range must move,
  while a jog toward a limit still stops at it.

The pnptask suite covers the same ground through HAL pins (tests/pnptask/
softlimit). This one goes through milltask's task layer, which has its own
state machine deciding whether the machine may go ON.
"""

import subprocess
import sys
import time

import gmi
import stmak_test
from gmi.constants import *

TIMEOUT = stmak_test.DEFAULT_TIMEOUT * stmak_test.scale()

# Motion works internally in millimetres whatever the INI is written in: on
# this inch config a G20 G0 X1.0 puts 25.4 on joint.0.motor-pos-cmd and the
# 10-inch limit reaches motion as 254. The HAL pins this test writes and reads
# are in that same frame, so its numbers are mm.
X = 0
X_MAX = 254.0         # [JOINT_0]MAX_LIMIT = 10 inch, in motion's millimetres
OUTSIDE = X_MAX + 12.7

_checks = 0


def check(cond, label, detail=None):
    global _checks
    if cond:
        _checks += 1
        print("PASS %s" % label)
        sys.stdout.flush()
        return
    print("FAIL %s%s" % (label, "" if detail is None else " (%s)" % detail))
    sys.stdout.flush()
    sys.exit(1)


def sets(signal, value):
    subprocess.call(["halcmd", "sets", signal, str(value)])


def setp(pin, value):
    subprocess.call(["halcmd", "setp", pin, str(value)])


def getp(pin):
    """The value only: halcmd prints the whole 'bit OUT name = TRUE' line."""
    line = subprocess.check_output(["halcmd", "getp", pin]).strip().decode()
    return line.rsplit("=", 1)[-1].strip()


def joint_pos():
    s.poll()
    return s.joint[X]["output"]


def drain_errors():
    """Take everything queued, so a later poll speaks only about what follows."""
    while e.poll() is not None:
        pass


def collect_errors(seconds):
    """Every message the machine sends in the next `seconds`."""
    msgs = []
    deadline = time.time() + seconds
    while time.time() < deadline:
        err = e.poll()
        if err is not None:
            msgs.append(err[1])
        time.sleep(0.05)
    return msgs


def wait_for(pred, desc, timeout=None):
    deadline = time.time() + (TIMEOUT if timeout is None else timeout)
    while time.time() < deadline:
        if pred():
            return True
        time.sleep(0.05)
    return False


c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

# ── a homed machine, jogging its axes ───────────────────────────────────────

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()

if not wait_for(lambda: (s.poll(), sum(s.homed[0:3]))[1] == 3, "homing"):
    print("failed to home; s.homed = %s" % (s.homed,))
    sys.exit(1)

c.teleop_enable(1)
c.wait_complete()

# ── the hand ────────────────────────────────────────────────────────────────

c.state(STATE_OFF)
c.wait_complete()
if not wait_for(lambda: (s.poll(), s.enabled)[1] == 0, "the machine to go off"):
    print("machine did not go off")
    sys.exit(1)

drain_errors()
sets("hand-pos", OUTSIDE)
sets("hand-sel", 1)

# A second of it: the hand is not a transient, and motion has to have sampled
# the axis where it now stands before the check means anything.
msgs = collect_errors(1.0)
check(not [m for m in msgs if "soft limit" in m],
      "a hand on a disabled axis is not reported as a soft-limit fault",
      "; ".join(m for m in msgs if "soft limit" in m))

# No fault -- but no denial either. The pin is state, not a fault: it is
# documented as "TRUE if outside a limit", and the machine IS outside one,
# de-energised or not. A lamp or an interlock chain wired to it must not go
# dark just because the amps dropped.
check(getp("motion.on-soft-limit") == "TRUE",
      "the on-soft-limit pin reports the pushed-out machine while off")

# ── the machine still comes back on ─────────────────────────────────────────

c.state(STATE_ON)
c.wait_complete()
check(wait_for(lambda: (s.poll(), s.enabled)[1] == 1, "the machine to come on"),
      "the machine switches on with a joint outside its limits")

# The hand lets go; the axis stays where it was pushed.
sets("hand-sel", 0)
time.sleep(0.2)
check(joint_pos() > X_MAX,
      "the axis really is outside its soft limit",
      "joint 0 at %.3f mm, limit %.1f mm" % (joint_pos(), X_MAX))

# ── the way back ────────────────────────────────────────────────────────────

before = joint_pos()
c.jog(JOG_CONTINUOUS, 0, X, -25.0)
time.sleep(1.0)
c.jog(JOG_STOP, 0, X)
time.sleep(0.5)
moved_back = before - joint_pos()
check(moved_back > 1.0,
      "jogging back toward the valid range moves the axis",
      "moved %.3f mm, wanted more than 1" % moved_back)

# And the limit still holds from the inside. A teleop jog targets the limit
# itself, so "further out" is not something a jog can ask for -- what has to be
# true is that jogging toward a limit stops at it.
c.jog(JOG_CONTINUOUS, 0, X, 25.0)
time.sleep(4.0)
c.jog(JOG_STOP, 0, X)
time.sleep(0.5)
check(joint_pos() <= X_MAX + 0.001,
      "jogging toward the limit stops at it",
      "joint 0 ended at %.4f mm, limit %.1f mm" % (joint_pos(), X_MAX))

# ── a joint limit tightened under the axis ──────────────────────────────────
#
# The ini.N halpins move the JOINT limit without touching the axis limit, so
# the trip becomes invisible in axis frame -- the case where the teleop clamp
# contains nothing and the RT backstop has to stop the jogs itself
# (axis_outside_limits in motion). The task monitor cannot stand in for it
# here: its abort latches after the first trip, so it is exactly the SECOND
# jog attempt that tells the two apart -- with the backstop it freezes at
# servo rate, without it it runs off toward the still-wide axis limit.

# Somewhere clearly inside first.
c.jog(JOG_CONTINUOUS, 0, X, -25.0)
if not wait_for(lambda: joint_pos() < X_MAX - 25.0, "jogging clear of the limit"):
    print("could not jog clear; joint 0 at %.3f mm" % joint_pos())
    sys.exit(1)
c.jog(JOG_STOP, 0, X)
time.sleep(0.5)

drain_errors()
TIGHT = joint_pos() - 12.7  # a joint limit below where the machine stands
setp("milltask.inihal.0.max_limit", TIGHT)
msgs = collect_errors(1.0)
check([m for m in msgs if "soft limit" in m],
      "tightening the joint limit under the machine reports the trip")

# The first abort (task monitor or backstop) has come and gone; this is the
# second attempt, heading away from validity but well inside the axis limits.
before = joint_pos()
c.jog(JOG_CONTINUOUS, 0, X, 25.0)
time.sleep(1.0)
c.jog(JOG_STOP, 0, X)
time.sleep(0.5)
check(joint_pos() - before < 1.0,
      "a jog is stopped by the RT backstop when the trip is joint-frame",
      "moved %.3f mm past a tripped joint limit" % (joint_pos() - before))

# Restoring the limit clears the trip, and the machine jogs again.
setp("milltask.inihal.0.max_limit", X_MAX)
time.sleep(0.5)
before = joint_pos()
c.jog(JOG_CONTINUOUS, 0, X, 25.0)
time.sleep(1.0)
c.jog(JOG_STOP, 0, X)
time.sleep(0.5)
check(joint_pos() - before > 1.0,
      "restoring the joint limit gives the jogs back",
      "moved %.3f mm" % (joint_pos() - before))

if _checks != 9:
    print("expected 9 checks, made %d" % _checks)
    sys.exit(1)
print("ALL OK")
