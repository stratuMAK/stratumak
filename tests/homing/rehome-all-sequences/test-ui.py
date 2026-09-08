#!/usr/bin/env python3

# Regression driver for Home All on an already-homed machine.
#
# The homing sequence FSM is only ticked while motion is in FREE mode (see the
# call site of do_homing_sequence), and it told the caller to leave FREE the
# moment all-homed first read true.  On a RE-home that is far too early: the
# joints in the later sequences still carry their homed flags from the first
# homing, so all-homed reads true again as soon as sequence 0 finishes.  Motion
# switched to teleop there, the FSM stopped being ticked, and the remaining
# sequences never ran -- Home All on a homed machine re-homed sequence 0 alone.
#
# Worse, the FSM kept its state: unhoming any joint dropped all-homed, motion
# returned to FREE, the frozen FSM resumed and homed the rest unprompted.  That
# second symptom is what this test asserts on, because it needs no timing --
# the joint must simply stay unhomed until it is asked to home.
#
# PASS (fixed):   the second Home All homes all three joints; unhoming joint 1
#                 afterwards leaves it unhomed.
# FAIL (pre-fix): joint 1 (sequence 1) homes itself seconds after being
#                 unhomed, with nothing having asked it to.

import time

import gmi
import stmak_test
from gmi.constants import *

c = stmak_test.Command()
s = gmi.Stat()

NJ = 3


def homed():
    s.poll()
    return [bool(s.joint[j]["homed"]) for j in range(NJ)]


def wait_homed(want, desc):
    stmak_test.wait_stat(s, lambda st: homed() == want, desc)


# Machine on.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
stmak_test.wait_stat(s, lambda st: st.task_state == STATE_ON, "machine ON")
c.mode(MODE_MANUAL)

# First Home All: the uncontroversial case, and the fixture for the second.
c.home(-1)
wait_homed([True] * NJ, "all joints homed by the first Home All")

# Second Home All, on an already-homed machine. Every joint must end homed --
# with the bug the FSM freezes after sequence 0, but since nothing unhomes the
# later joints they still *read* homed here, so this alone does not
# discriminate. It does catch the sequence erroring out entirely.
c.home(-1)
wait_homed([True] * NJ, "all joints homed after the second Home All")

# The discriminator: with the FSM frozen mid-sequence, dropping all-homed
# returns motion to FREE and the stale sequence resumes on its own. Joint 1 is
# in sequence 1, i.e. the first sequence the frozen FSM would have gone on to.
c.unhome(1)
stmak_test.wait_stat(s, lambda st: homed() == [True, False, True],
                     "joint 1 unhomed")

# Nothing has asked joint 1 to home. It must stay unhomed. A stale sequence
# resumes within a servo cycle of motion returning to FREE, so a short settle
# is enough; scale it like every other wait in the suite.
time.sleep(3.0 * stmak_test.scale())
now = homed()
if now[1]:
    stmak_test.fail("joint 1 homed itself after being unhomed — a stale "
                    "homing sequence resumed (homed=%s)" % (now,))
if now != [True, False, True]:
    stmak_test.fail("unexpected homed state after unhoming joint 1: %s" % (now,))

print("ok: Home All on a homed machine leaves no pending sequence")
