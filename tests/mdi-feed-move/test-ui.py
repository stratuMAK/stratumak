#!/usr/bin/env python3
# An MDI feed move must reach motion.
#
# A feed move leaves 2.9's naive-CAM detector (see_segment) holding an OPEN
# chain: it buffers every STRAIGHT_FEED and only flushes when the next point
# cannot link onto the chain. Interp::_execute closes the chain at the end of an
# MDI execution only on its o-word branch (canon.finish() in rs274ngc_pre.cc),
# which a plain block never reaches — so after `g1 x y` the move is still
# buffered when executeMDI returns. stmak then resyncs the canon endpoint from
# the machine at the start of the NEXT MDI line, and that resync drops the chain
# (2.9 GET_EXTERNAL_POSITION -> drop_segments). Net effect before the fix: an MDI
# feed move was silently discarded — no motion, no error, RCS_DONE.
#
# Rapids are unaffected (STRAIGHT_TRAVERSE flushes first), which is why this hid:
# every MDI test in the suite either sets a mode, calls an M-code, or moves G0.
import sys

import gmi
import stmak_test
from gmi.constants import *

# mm. The config is an inch machine; the gmi client boundary is always mm.
TOL = 0.01

c = stmak_test.Command()
s = gmi.Stat()

stmak_test.wait_for_startup(s)
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
# Per joint: this config sets no HOME_SEQUENCE, so "home all" has no sequence
# to run and would leave the machine unhomed — and MDI is refused unhomed.
for joint in range(3):
    c.home(joint)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")

c.mode(MODE_MDI)
c.mdi("g21 g90 g94 f600")
c.wait_complete()


def at(x, y, why):
    stmak_test.wait_stat(
        s,
        lambda st: abs(st.position[0] - x) < TOL and abs(st.position[1] - y) < TOL,
        "%s: position to reach (%.3f, %.3f) mm" % (why, x, y),
        detail=lambda st: "position=(%.4f, %.4f)" % (st.position[0], st.position[1]))


# Baseline: a rapid works even with the chain unflushed, so a failure here is a
# broken config/harness rather than the defect under test.
c.mdi("g0 x0 y0")
c.wait_complete()
at(0.0, 0.0, "g0 to origin")

# The regression: a plain MDI feed move, issued alone, with nothing after it to
# flush the chain.
c.mdi("g1 x5 y5")
c.wait_complete()
at(5.0, 5.0, "plain MDI feed move")

# A second one, moving back: the flush has to leave the chain and the canon
# endpoint clean for the next line, not just get the first move out.
c.mdi("g1 x3 y1")
c.wait_complete()
at(3.0, 1.0, "second MDI feed move")

# The continuation path: an MDI o-word sub whose tail, after a queue buster,
# is the feed move. finishMDI resumes the sub once the queued motion has
# drained, and the resumed round is drained and re-entered before the
# interpreter unwinds far enough to reach its own canon.finish() — so the tail's
# chain has to be closed here too. Note the sub uses M66, not G4: dwell is not a
# queue buster (only probe/M66/M6/user-M set the flags in interp_execute.cc), so
# a G4 version of this sub runs entirely in the entry execution and silently
# tests the plain path again.
c.mdi("o<qbfeed> call")
c.wait_complete()
at(7.0, 7.0, "feed move in an MDI o-word sub tail")

print("mdi-feed-move: OK")
sys.exit(0)
