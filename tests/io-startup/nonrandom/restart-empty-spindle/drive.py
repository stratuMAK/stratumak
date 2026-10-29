#!/usr/bin/env python3

# Two-phase driver for the non-random restart test (see test.sh).
#   load   boot 1: M61 Q1 mounts tool 1, so slot 0 holds T1's row (Z+1.0).
#          A tooltable write goes to SQLite before it returns, so once the
#          status shows it, a store that persisted slot 0 has done so.
#   check  boot 2: the spindle must report empty AND G43 H0 must apply a
#          zero offset — pre-fix, slot 0 still held T1 and H0 applied its
#          full length to a spindle io reported as empty.
#
# Every wait is for a state that holds until the next command.  The offset
# G43 H0 must leave is also the one the machine boots with, so G43 H1 sets
# one first: only the H0 can then bring it back to zero.

import sys

import gmi
import stmak_test
from gmi.constants import *


def fail(msg):
    print("Error: " + msg)
    sys.exit(1)


def slots(st):
    return [(i, t.id) for i, t in enumerate(st.tool_table) if t.id > 0]


def mdi(cmd):
    c.mdi(cmd)
    stmak_test.drain_mdi(s)


c = stmak_test.Command()
s = gmi.Stat()

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MDI)
stmak_test.wait_stat(s, lambda st: st.task_state == STATE_ON
                     and st.task_mode == MODE_MDI,
                     "the machine to come on in MDI mode",
                     detail=lambda st: "task_state=%d task_mode=%d"
                                       % (st.task_state, st.task_mode))

phase = sys.argv[1]

if phase == "load":
    mdi("M61 Q1")
    stmak_test.wait_stat(s, lambda st: st.tool_in_spindle == 1
                         and st.tool_table[0].id == 1,
                         "tool 1 in the spindle and in slot 0",
                         detail=lambda st: "tool_in_spindle=%d slots=%r"
                                           % (st.tool_in_spindle, slots(st)))
    print("tool 1 mounted, slot 0 mirrors it")

elif phase == "check":
    s.poll()
    # Tool 1 back in its own slot shows the table this boot reads is the
    # one boot 1 wrote, so an empty slot 0 below is not an unread table.
    if 1 not in [tool for _, tool in slots(s)]:
        fail("the tool table did not come back: slots %r" % slots(s))
    if s.tool_in_spindle != 0:
        fail("spindle reports tool %d after restart, expected empty (0)"
             % s.tool_in_spindle)
    if s.tool_table[0].id > 0:
        fail("slot 0 still holds tool %r after restart — the stale "
             "session copy survived" % s.tool_table[0].id)

    # The operator-visible consequence: a bare G43 H0 must not move Z.
    # G43 H1 first, so the offset read after H0 is one G43 has set, not
    # the zero the machine booted with.
    mdi("G43 H1")
    stmak_test.wait_stat(s, lambda st: abs(st.tool_offset[2]) > 1e-9,
                         "G43 H1 to apply tool 1's length",
                         detail=lambda st: "tool_offset=%r" % (st.tool_offset[2],))
    # Only the G43 H0 can bring it back to zero now; a phantom offset from
    # a stale slot 0 never does, and the wait fails.
    mdi("G43 H0")
    stmak_test.wait_stat(s, lambda st: abs(st.tool_offset[2]) < 1e-9,
                         "G43 H0 to apply no offset with an empty spindle",
                         detail=lambda st: "tool_offset=%r" % (st.tool_offset[2],))
    print("spindle empty after restart, G43 H0 applies no offset")

else:
    fail("unknown phase %r" % phase)
