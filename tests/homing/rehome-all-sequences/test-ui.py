#!/usr/bin/env python3

# Regression driver for Home All on an already-homed machine.
#
# The homing sequence FSM is only ticked while motion is in FREE mode (see the
# call site of do_homing_sequence), and it told the caller to leave FREE the
# moment all-homed first read true.  On a RE-home that is far too early: the
# joints in the later sequences still carry their homed flags from the first
# homing, so all-homed reads true again as soon as the first sequence finishes.
# Motion switched to teleop there, the FSM stopped being ticked, and the
# remaining sequences never ran -- Home All on a homed machine re-homed the
# lowest sequence alone.
#
# The joints are homed immediately at their current position (no search or
# latch velocity), so a re-home is observable as a position: move the axes off
# zero, Home All, and every joint that actually re-homes reads 0 again.  That
# is a positive assertion about each joint and needs no timing assumptions
# about the sequence -- unlike watching the homed flags, which never drop
# observably here and are all still set from the first homing.
#
# The FSM also kept its state across the freeze, which is how the bug
# announced itself on the machine: unhome any joint afterwards, all-homed
# drops, motion returns to FREE, and the stale sequence resumes and homes the
# rest with nothing having asked it to.  Checked as a second phase.
#
# PASS (fixed):   all three joints read 0 after the second Home All, and
#                 unhoming one afterwards leaves it unhomed.
# FAIL (pre-fix): the joints in sequences 1 and 2 are still sitting at the
#                 position they were moved to.

import time

import gmi
import stmak_test
from gmi.constants import *

c = stmak_test.Command()
s = gmi.Stat()

NJ = 3
OFF = 5.0
TOL = 1e-3


def homed():
    s.poll()
    return [bool(s.joint[j]["homed"]) for j in range(NJ)]


def positions():
    s.poll()
    return [s.joint_actual_position[j] for j in range(NJ)]


# Machine on and homed: the fixture for the re-home.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
stmak_test.wait_stat(s, lambda st: st.task_state == STATE_ON, "machine ON")
c.mode(MODE_MANUAL)
c.home(-1)
stmak_test.wait_stat(s, lambda st: homed() == [True] * NJ,
                     "all joints homed by the first Home All")

# Move every axis off zero so a re-home has something to show.
c.mode(MODE_MDI)
c.mdi("G0 X%g Y%g Z%g" % (OFF, OFF, OFF))
stmak_test.drain_mdi(s)
stmak_test.wait_stat(s, lambda st: all(abs(p - OFF) < TOL for p in positions()),
                     "all three joints moved to %g" % OFF,
                     detail=lambda st: "positions=%s" % (positions(),))
c.mode(MODE_MANUAL)

# The case under test.
c.home(-1)
try:
    stmak_test.wait_stat(s, lambda st: all(abs(p) < TOL for p in positions()),
                         "every joint re-homed by the second Home All",
                         detail=lambda st: "positions=%s" % (positions(),))
except Exception:
    pos = positions()
    stale = [j for j in range(NJ) if abs(pos[j]) >= TOL]
    stmak_test.fail("second Home All left joints %s un-re-homed at %s — the "
                    "sequence stopped after the first step" % (stale, pos))

# Second phase: no sequence may be left pending. With the FSM frozen
# mid-sequence, dropping all-homed returns motion to FREE and the stale
# sequence resumes on its own. Joint 1 is in sequence 1, the first sequence a
# frozen FSM would have gone on to.
c.unhome(1)
stmak_test.wait_stat(s, lambda st: homed() == [True, False, True],
                     "joint 1 unhomed",
                     detail=lambda st: "homed=%s" % (homed(),))
time.sleep(3.0 * stmak_test.scale())
now = homed()
if now != [True, False, True]:
    stmak_test.fail("joint 1 homed itself after being unhomed — a stale "
                    "homing sequence resumed (homed=%s)" % (now,))

print("ok: Home All on a homed machine re-homes every sequence")
