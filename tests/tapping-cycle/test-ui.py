#!/usr/bin/env python3
"""A floating tap (G84) runs to the end at its programmed feed and speed.

In a floating tap Z is fed at a rate matched to the spindle speed, not locked
to it; the holder takes up the small difference.  Anything that changes or
stops one side alone runs the holder into a stop and strips or breaks the tap:

  - motion.feed-inhibit stopping Z while the spindle keeps turning,
  - spindle.0.inhibit stopping the spindle while Z keeps feeding,
  - a feed or spindle override other than 100%,
  - motion.feed-hold and a program pause, which stop Z and leave the spindle
    turning.

So for the length of the cycle motion suspends both overrides and defers the
inhibits, the feed hold and the pause (TAP_ACTIVE); they take hold after the
retract.  The task's sequencer holds a pause until then too: a sequencer
stopped mid-cycle would hold the spindle reversal at the bottom.  The pins
are driven from the servo thread when Z passes -2 on the way in (see
tapping-cycle.hal) and everything is judged from the servo-thread trace.

The overrides are the other half of the same bug: the canon never forwarded
the speed-override disable the cycle issues, so a G84 ran under a live
spindle override with the feed override off.
"""

import subprocess
import sys

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()

TRACE = "trace.txt"
S = 300.0           # rpm; with F300 a 1 mm pitch
FEED_MM_S = 5.0     # F300 mm/min
SERVO = 0.001
IN_TAP = (-4.5, -2.5)   # past the trigger, short of the bottom


def check(cond, label, detail=None):
    if cond:
        print("PASS %s" % label)
        sys.stdout.flush()
        return
    stmak_test.fail("%s%s" % (label, "" if detail is None else " (%s)" % detail))


def sets(signal, value):
    subprocess.run(["halcmd", "sets", signal, str(value)], check=True)


def rearm(phase, feed=False, spindle=False, hold=False):
    """Clear the latch, pick the input the trigger drives, mark the trace."""
    sets("latch-reset", 1)
    sets("latch-reset", 0)
    sets("arm-feed", 1 if feed else 0)
    sets("arm-spindle", 1 if spindle else 0)
    sets("arm-hold", 1 if hold else 0)
    sets("phase", phase)


def trace(phase):
    """Rows (z, speed, feed_inh, spin_inh, hold) sampled while phase was `phase`.

    filestream keeps appending every servo cycle, so the file is never
    "stable"; a caller that needs the trace up to some event waits for that
    event to show up in it (caught_up).
    """
    rows = []
    for line in open(TRACE):
        p = line.split()
        if len(p) >= 6 and int(p[0]) == phase:
            rows.append((float(p[1]), float(p[2]), int(p[3]), int(p[4]),
                         int(p[5])))
    return rows


def caught_up(phase):
    """The trace of `phase` up to the retract's arrival back at R."""
    stmak_test.wait_until(
        lambda: any(r[0] < -4.9 for r in trace(phase))
        and trace(phase)[-1][0] >= 2.0 - 1e-6,
        "the trace to reach the end of the retract (phase %d)" % phase,
        detail=lambda: "Z %.4f, min %.4f" % (trace(phase)[-1][0],
                                             min(r[0] for r in trace(phase))))
    return trace(phase)


def in_tap(rows):
    return [r for r in rows if IN_TAP[0] < r[0] < IN_TAP[1]]


def held(phase, column, what):
    """Half a second of the held move: Z must stand at R while it lasts."""
    def rows():
        return [r for r in trace(phase) if column is None or r[column]]
    stmak_test.wait_until(lambda: len(rows()) >= 500,
                          "half a second of the held move in the trace")
    got = rows()
    check(all(abs(r[0] - 2.0) < 1e-6 for r in got),
          "the move after the cycle is held by %s" % what,
          "Z range %.4f..%.4f" % (min(r[0] for r in got), max(r[0] for r in got)))


def tap(timeout=30):
    """One G84 at X0 Y0 from Z5, retract to R2 (G99)."""
    c.mdi("G99 G84 X0 Y0 Z-5 R2 F300")
    try:
        c.wait_complete(timeout=timeout)
    except Exception as ex:
        s.poll()
        stmak_test.fail("the tap cycle did not finish: Z=%.4f (%s)"
                        % (s.joint[2]["output"], ex))


s = gmi.Stat()
c = stmak_test.Command()

stmak_test.wait_for_startup(s)
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
for j in (0, 1, 2):
    c.home(j)
    c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")
c.mode(MODE_MDI)
c.wait_complete()
c.mdi("G21 G90 G17 G94")
c.wait_complete()
c.mdi("M3 S%d" % S)
c.wait_complete()
c.mdi("G0 X0 Y0 Z5")
c.wait_complete()

# --- feed-inhibit mid-tap: the tap finishes, what follows is held -----------
rearm(1, feed=True)
tap()
rows = caught_up(1)
check(any(r[2] and r[0] < -2.0 for r in rows),
      "feed-inhibit asserted inside the tap")
check(min(r[0] for r in rows) <= -4.999,
      "Z still reached the bottom of the tap", "min Z %.4f" % min(r[0] for r in rows))
check(abs(rows[-1][0] - 2.0) < 1e-6,
      "the retract still reached R", "Z %.4f" % rows[-1][0])

# The move after the cycle is held as before -- it is only the cycle that is
# exempt.  A window is the point here: "does not move", over half a second.
sets("phase", 2)
c.mdi("G0 Z5")
held(2, 2, "feed-inhibit")
sets("arm-feed", 0)
c.wait_complete()
s.poll()
check(abs(s.joint[2]["output"] - 5.0) < 1e-6, "released, the held move completes")

# --- spindle inhibit mid-tap: the spindle keeps the tap turning -------------
rearm(3, spindle=True)
tap()
rows = caught_up(3)
inside = [r for r in in_tap(rows) if r[3]]
check(len(inside) > 100, "spindle inhibit asserted inside the tap",
      "%d samples" % len(inside))
check(all(abs(abs(r[1]) - S) < 1e-6 for r in inside),
      "the spindle keeps turning at S under the inhibit, in and out",
      "speeds %r" % sorted(set(r[1] for r in inside)))
# The cycle restarts the spindle CW after the retract; that start is outside
# the cycle, so the inhibit takes it.
stmak_test.wait_pin("speed-out", 0.0)
print("PASS the inhibit takes hold of the spindle after the cycle")
sets("arm-spindle", 0)
stmak_test.wait_pin("speed-out", S)
c.mdi("G0 Z5")
c.wait_complete()

# --- overrides: the tap runs at its programmed feed and speed ----------------
rearm(5)
c.feedrate(0.5)
c.spindleoverride(0.5)
# Positive control: the overrides really are live outside the cycle.
stmak_test.wait_pin("speed-out", S * 0.5)
print("PASS the spindle override applies outside the cycle")
tap()
rows = in_tap(caught_up(5))
check(len(rows) > 100, "the trace covers the tap", "%d samples" % len(rows))
check(all(abs(abs(r[1]) - S) < 1e-6 for r in rows),
      "the spindle runs at S inside the cycle, override ignored",
      "speeds %r" % sorted(set(r[1] for r in rows)))
steps = sorted(abs(b[0] - a[0]) for a, b in zip(rows, rows[1:]) if a[0] != b[0])
step = steps[len(steps) // 2]
check(abs(step - FEED_MM_S * SERVO) < 1e-4,
      "Z feeds at F inside the cycle, override ignored",
      "%.5f mm per cycle, want %.5f" % (step, FEED_MM_S * SERVO))
stmak_test.wait_pin("speed-out", S * 0.5)
print("PASS the spindle override applies again after the cycle")
c.feedrate(1.0)
c.spindleoverride(1.0)
c.mdi("G0 Z5")
c.wait_complete()

# --- feed-hold mid-tap: like feed-inhibit ------------------------------------
rearm(6, hold=True)
tap()
rows = caught_up(6)
check(any(r[4] and r[0] < -2.0 for r in rows), "feed-hold asserted inside the tap")
check(min(r[0] for r in rows) <= -4.999, "Z still reached the bottom of the tap",
      "min Z %.4f" % min(r[0] for r in rows))
sets("phase", 7)
c.mdi("G0 Z5")
held(7, 4, "feed-hold")
sets("arm-hold", 0)
c.wait_complete()

# --- a program pause mid-tap -------------------------------------------------
# Pause is a task command, so it is sent from here when Z passes the mark; the
# check below refuses a run in which it missed the tap.  Both halves are under
# test: motion must not stop the feed, and the task's sequencer must not hold
# the spindle reversal at the bottom -- it is waiting on that reversal when
# the pause arrives.
rearm(8)
c.mode(MODE_AUTO)
c.wait_complete()
c.program_open("tap.ngc")
c.auto(AUTO_RUN, 0)
stmak_test.wait_stat(s, lambda st: st.joint[2]["output"] < -2.0,
                     "Z to pass the mark", interval=0.002)
c.auto(AUTO_PAUSE)
s.poll()
z_paused = s.joint[2]["output"]
check(-5.0 < z_paused < -2.0, "the pause landed inside the tap", "Z %.4f" % z_paused)
rows = caught_up(8)
check(min(r[0] for r in rows) <= -4.999, "Z still reached the bottom under the pause",
      "min Z %.4f" % min(r[0] for r in rows))
check(any(r[1] < 0 for r in rows), "the spindle reversed at the bottom under the pause")
sets("phase", 9)
held(9, None, "the pause")
c.auto(AUTO_RESUME)
stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE, "the program to end")
s.poll()
check(abs(s.joint[2]["output"] - 5.0) < 1e-6, "resumed, the program completes")
print("PASS")
