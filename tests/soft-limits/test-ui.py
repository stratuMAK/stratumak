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

Then the realtime backstop, which stops a jog the axis-frame clamp cannot
hold. Its rule is per joint -- a joint past a limit and still heading further
out -- and three things follow that a whole-machine "is any axis outside"
rule got wrong: the jog back in is not hard-stopped at the limit edge on
re-entry (observed in the servo thread, because the abort is a single-cycle
velocity step); a joint-frame trip on Y is stopped while X sits outside its
axis limits; and the way back in survives an external offset in the recovery
direction.

The pnptask suite covers the first part through HAL pins (tests/pnptask/
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
Y = 1
X_MAX = 254.0         # [JOINT_0]MAX_LIMIT = 10 inch, in motion's millimetres
Y_MAX = 254.0         # [JOINT_1]MAX_LIMIT, the same
OUTSIDE = X_MAX + 12.7

# X jogs at [AXIS_X]MAX_VELOCITY * (1 - OFFSET_AV_RATIO) = 20.32 mm/s and
# accelerates at 30 in/s^2 * 0.8: one servo cycle (1 ms) of ramp changes the
# velocity by 0.61 mm/s. An abort with immediate=1 zeroes it in one cycle.
X_JOG_VEL = 25.4 * 0.8
X_RAMP_STEP = 762.0 * 0.8 * 0.001

TRACE = "trace.txt"

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


def joint_pos(j=X):
    s.poll()
    return s.joint[j]["output"]


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


def trace_window(mark):
    """The servo-thread samples taken while trace-mark was `mark`.

    Rows of (vel, cmd, jog_on, on_soft, mot_err); see soft-limits.hal for the
    columns. filestream's I/O thread flushes the file as it drains, so the
    window is complete once the jog it brackets has settled.
    """
    rows = []
    for line in open(TRACE):
        p = line.split()
        if len(p) >= 6 and int(p[0]) == mark:
            rows.append((float(p[1]), float(p[2]), int(p[3]), int(p[4]), int(p[5])))
    return rows


def fail(msg):
    print(msg)
    sys.stdout.flush()
    sys.exit(1)


def at_rest():
    s.poll()
    return s.inpos


def wait_rest(desc):
    if not wait_for(at_rest, desc):
        fail("timed out waiting for %s" % desc)


def jog_until(axis, vel, pred, desc):
    """Jog continuously until pred() holds, then stop and wait for rest.

    The jog is re-sent on every poll: task stops a continuous jog its client
    does not refresh within 2 s, and these waits are bounded by where the
    axis gets to, not by a time that a slow runner might not give the jog
    (a fixed sleep stood here).
    """
    c.jog(JOG_CONTINUOUS, 0, axis, vel)
    ok = wait_for(lambda: (c.jog(JOG_CONTINUOUS, 0, axis, vel), pred())[1], desc)
    c.jog(JOG_STOP, 0, axis)
    if not ok:
        fail("timed out waiting for %s; joint %d at %.3f mm" % (desc, axis, joint_pos(axis)))
    wait_rest("the jog to stop after %s" % desc)


def jog_once(axis, dist, desc):
    """One incremental jog of `dist` mm, then wait for the machine to stand.

    For the checks that a jog is STOPPED: jog_until re-sends its continuous
    jog on every poll, and each re-sent jog past a tripped limit runs a few
    cycles before the backstop sees it heading out -- so how far the axis
    got depended on how many polls fit in before the test saw it at rest.
    An incremental jog is sent once and needs no refreshing (the task's
    watchdog is for continuous jogs only); where it ends is decided in the
    servo thread alone.
    """
    start = joint_pos(axis)
    c.jog(JOG_INCREMENT, 0, axis, 25.0, dist)
    # Moved and at rest: the jog has started and ended.  A jog that is
    # refused outright never moves; that fails the wait, naming the axis.
    # 1e-4: even the jog the backstop stops at once creeps ~2 um first.
    if not wait_for(lambda: at_rest() and abs(joint_pos(axis) - start) > 1e-4, desc):
        fail("timed out waiting for %s to move and stop; joint %d at %.3f mm"
             % (desc, axis, joint_pos(axis)))
    sys.stderr.write("%s: joint %d went from %.4f to %.4f mm\n"
                     % (desc, axis, start, joint_pos(axis)))


def limit_is(j, value):
    """Wait until motion applies a joint's max limit (milltask.inihal.N)."""
    def _applied():
        s.poll()
        return abs(s.joint[j]["max_position_limit"] - value) < 1e-6
    if not wait_for(_applied, "joint %d's max limit to become %.3f" % (j, value)):
        fail("motion did not take joint %d's max limit %.3f (it has %.3f)"
             % (j, value, s.joint[j]["max_position_limit"]))


def wait_trace(mark):
    """Wait until the trace holds the whole window marked `mark`: filestream's
    I/O thread writes it after the fact, so it is complete once a later row
    with another mark is in the file."""
    def _closed():
        seen = False
        for line in open(TRACE):
            p = line.split()
            if len(p) < 6:
                continue
            if int(p[0]) == mark:
                seen = True
            elif seen:
                return True
        return False
    if not wait_for(_closed, "the trace of window %d" % mark):
        fail("the trace never held all of window %d" % mark)


def outside_and_off():
    """With the machine off, shove X past its limit and wait for motion to see it."""
    sets("hand-pos", OUTSIDE)
    sets("hand-sel", 1)
    if not wait_for(lambda: joint_pos() > X_MAX, "the hand to push X outside"):
        fail("the hand did not move X outside; joint 0 at %.3f mm" % joint_pos())


def hand_push_outside():
    """Switch off, shove X past its limit, switch on, let go."""
    c.state(STATE_OFF)
    c.wait_complete()
    if not wait_for(lambda: (s.poll(), s.enabled)[1] == 0, "the machine to go off"):
        fail("machine did not go off")
    outside_and_off()
    c.state(STATE_ON)
    c.wait_complete()
    if not wait_for(lambda: (s.poll(), s.enabled)[1] == 1, "the machine to come on"):
        fail("machine did not come on")
    sets("hand-sel", 0)
    if not joint_pos() > X_MAX:
        fail("the hand did not leave X outside; joint 0 at %.3f mm" % joint_pos())


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
    fail("failed to home; s.homed = %s" % (s.homed,))

c.teleop_enable(1)
c.wait_complete()

# ── the hand ────────────────────────────────────────────────────────────────

c.state(STATE_OFF)
c.wait_complete()
if not wait_for(lambda: (s.poll(), s.enabled)[1] == 0, "the machine to go off"):
    fail("machine did not go off")

drain_errors()
outside_and_off()

# The pin is state, not a fault: it is documented as "TRUE if outside a
# limit", and the machine IS outside one, de-energised or not. A lamp or an
# interlock chain wired to it must not go dark just because the amps dropped.
on_soft_off = wait_for(lambda: getp("motion.on-soft-limit") == "TRUE",
                       "the on-soft-limit pin")

# No fault. Motion has seen the axis outside (above); a second of messages is
# what a fault would have produced in the meantime.
msgs = collect_errors(1.0)
check(not [m for m in msgs if "soft limit" in m],
      "a hand on a disabled axis is not reported as a soft-limit fault",
      "; ".join(m for m in msgs if "soft limit" in m))
check(on_soft_off,
      "the on-soft-limit pin reports the pushed-out machine while off")

# ── the machine still comes back on ─────────────────────────────────────────

c.state(STATE_ON)
c.wait_complete()
check(wait_for(lambda: (s.poll(), s.enabled)[1] == 1, "the machine to come on"),
      "the machine switches on with a joint outside its limits")

# The hand lets go; the axis stays where it was pushed.
sets("hand-sel", 0)
check(joint_pos() > X_MAX,
      "the axis really is outside its soft limit",
      "joint 0 at %.3f mm, limit %.1f mm" % (joint_pos(), X_MAX))

# ── the way back ────────────────────────────────────────────────────────────
#
# The enable with the machine outside reports the trip once and the task
# monitor aborts; from then on the machine is outside, on, and quiet. Wait for
# that report and for rest, so the jog below is not caught by that abort. The
# jog back in is bracketed by trace-mark 1 for the servo-thread look at it.

if not wait_for(lambda: any("soft limit" in m for m in collect_errors(0.1)),
                "the enable's soft-limit report"):
    fail("the enable outside the limit was not reported")
wait_rest("the machine to settle after the enable")
sets("trace-mark", 1)
before = joint_pos()
# Until the axis is 2 mm back inside: the window then holds the re-entry and
# the ramp-down JOG_STOP asks for (an ABORT with immediate=0, which
# decelerates at the axis acceleration).
jog_until(X, -25.0, lambda: joint_pos() < X_MAX - 2.0, "X to re-enter its limit")
sets("trace-mark", 0)
moved_back = before - joint_pos()
check(moved_back > 1.0,
      "jogging back toward the valid range moves the axis",
      "moved %.3f mm, wanted more than 1" % moved_back)

# What the servo thread saw of that jog. A backstop that misreads the
# re-entry -- the joint command is still outside for the two cycles of cubic
# lag after the axis sum is in -- aborts with immediate=1: the velocity drops
# from jog speed to zero in one cycle, and the motion error flag goes up.
# Neither may happen here.
wait_trace(1)
rows = trace_window(1)
if not rows:
    fail("the trace holds no samples of the recovery jog")
vel = [r[0] for r in rows]
biggest_step = max(abs(b - a) for a, b in zip(vel, vel[1:]))
check(biggest_step < 4 * X_RAMP_STEP,
      "the recovery jog re-enters the limit without a velocity step",
      "largest per-cycle velocity change %.3f mm/s, ramp step %.3f, jog %.2f"
      % (biggest_step, X_RAMP_STEP, X_JOG_VEL))
check(not any(r[4] for r in rows),
      "no motion error is raised on the re-entry",
      "motion error set in %d of %d samples" % (sum(r[4] for r in rows), len(rows)))
on_soft = [r[3] for r in rows]
check(on_soft[0] == 1 and on_soft[-1] == 0 and on_soft == sorted(on_soft, reverse=True),
      "the on-soft-limit pin clears once, on the re-entry, and stays clear",
      "on-soft-limit went %s" % "".join("1" if v else "0" for v in on_soft[::50]))

# And the limit still holds from the inside. A teleop jog targets the limit
# itself, so "further out" is not something a jog can ask for -- what has to be
# true is that jogging toward a limit stops at it: the jog runs until the axis
# stands still near the limit, and it must stand at or inside it.
jog_until(X, 25.0, lambda: at_rest() and joint_pos() > X_MAX - 1.0,
          "X to stop at its limit")
check(joint_pos() <= X_MAX + 0.001,
      "jogging toward the limit stops at it",
      "joint 0 ended at %.4f mm, limit %.1f mm" % (joint_pos(), X_MAX))

# ── a joint limit tightened under the axis ──────────────────────────────────
#
# The ini.N halpins move the JOINT limit without touching the axis limit, so
# the trip becomes invisible in axis frame -- the case where the teleop clamp
# contains nothing and the RT backstop has to stop the jogs itself. The task
# monitor cannot stand in for it here: its abort latches after the first trip,
# so it is exactly the SECOND jog attempt that tells the two apart -- with the
# backstop it freezes at servo rate, without it it runs off toward the
# still-wide axis limit.

# Somewhere clearly inside first.
jog_until(X, -25.0, lambda: joint_pos() < X_MAX - 25.0, "X to get clear of the limit")

drain_errors()
TIGHT = joint_pos() - 12.7  # a joint limit below where the machine stands
setp("milltask.inihal.0.max_limit", TIGHT)
limit_is(X, TIGHT)
check(wait_for(lambda: any("soft limit" in m for m in collect_errors(0.1)),
               "the trip to be reported"),
      "tightening the joint limit under the machine reports the trip")

# The first abort (task monitor or backstop) has come and gone; this is the
# second attempt, heading away from validity but well inside the axis limits.
wait_rest("the machine to settle after the trip")
before = joint_pos()
jog_once(X, 25.0, "the jog past the tripped limit")
check(joint_pos() - before < 1.0,
      "a jog is stopped by the RT backstop when the trip is joint-frame",
      "moved %.3f mm past a tripped joint limit" % (joint_pos() - before))

# Restoring the limit clears the trip, and the machine jogs again.
setp("milltask.inihal.0.max_limit", X_MAX)
limit_is(X, X_MAX)
if not wait_for(lambda: getp("motion.on-soft-limit") == "FALSE", "the trip to clear"):
    fail("restoring the joint limit did not clear on-soft-limit")
before = joint_pos()
jog_until(X, 25.0, lambda: joint_pos() - before > 1.0, "X to jog again")
check(joint_pos() - before > 1.0,
      "restoring the joint limit gives the jogs back",
      "moved %.3f mm" % (joint_pos() - before))

# ── one axis outside, a joint-frame trip on another ─────────────────────────
#
# X is pushed out again and left there: an axis outside its own limits is a
# state the machine has to work in, not a lock on every other joint. Y stands
# at 0 with its joint limit tightened to 12.7 mm, still far inside AXIS_Y's;
# a Y+ jog crosses that joint limit at full speed and has to be stopped
# there, by the backstop, per joint. A backstop that asked "is any axis
# outside" saw X, took the trip for an axis-frame one it need not stop, and
# let Y run on to the axis limit -- and since it re-asserted the motion error
# every cycle X was outside, the task monitor stayed latched and never
# stopped it either.

hand_push_outside()
drain_errors()
y_before = joint_pos(Y)
Y_TIGHT = y_before + 12.7
setp("milltask.inihal.1.max_limit", Y_TIGHT)
limit_is(Y, Y_TIGHT)
# One 25 mm jog: stopped by the backstop near its joint limit, or -- without
# it -- run out to its end, twice the limit away.
jog_once(Y, 25.0, "the Y jog toward its tightened limit")
y_after = joint_pos(Y)
check(y_before + 5.0 < y_after < Y_TIGHT + 1.0,
      "a joint-frame trip on Y is stopped while X sits outside its axis limits",
      "Y went from %.3f to %.3f mm, joint limit %.3f mm" % (y_before, y_after, Y_TIGHT))
setp("milltask.inihal.1.max_limit", Y_MAX)
limit_is(Y, Y_MAX)

# ── the way back, under an external offset ──────────────────────────────────
#
# An external offset in the recovery direction puts the axis sum 5 mm closer
# to the limit than the teleop planner's own position. The jog back in has to
# work exactly as without it. It did not, when the backstop judged the jog's
# target -- which axis_jog_cont sets against the bare limits -- with the
# offset added: the way back in then read as outward, and was aborted every
# servo cycle.

setp("axis.x.eoffset-scale", 0.1)
setp("axis.x.eoffset-enable", 1)
setp("axis.x.eoffset-counts", -50)
if not wait_for(lambda: abs(float(getp("axis.x.eoffset")) + 5.0) < 0.01,
                "the external offset to settle"):
    fail("external offset did not settle; axis.x.eoffset = %s" % getp("axis.x.eoffset"))
if not joint_pos() > X_MAX:
    fail("X is inside its limit with the offset applied; joint 0 at %.3f mm" % joint_pos())
before = joint_pos()
jog_until(X, -25.0, lambda: before - joint_pos() > 1.0, "X to jog back in")
moved_back = before - joint_pos()
check(moved_back > 1.0,
      "the jog back in is not aborted over an external offset in the recovery direction",
      "moved %.3f mm, wanted more than 1" % moved_back)

if _checks != 14:
    fail("expected 14 checks, made %d" % _checks)
print("ALL OK")
