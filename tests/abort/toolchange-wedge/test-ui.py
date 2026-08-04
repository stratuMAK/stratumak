#!/usr/bin/env python3
# Regression test for the iocontrol v2 abort wedge (Tier-1 hotspot #5, T1/T2).
#
# The stmak port of 2.9's free-running iocontrol loop turned the tool-change
# handshake into a BLOCKING cgo busy-wait on the sequencer goroutine, and the v2
# (iov2) port lost every abort escape. So an abort/estop issued while a tool
# change was in progress — with the changer not (yet) asserting tool-changed —
# froze the sequencer inside cgo; the estop/abort teardown then joined
# <-seqDone forever, wedging the whole controller (only a process kill
# recovered).
#
# core_sim.hal stages the stuck change by construction: tool-prepare is looped
# to tool-prepared (prepare completes) but tool-changed is left undriven, so
# gmi_tool_load blocks until an abort clears tool-change. This test drives a
# tool change into that block and asserts the machine recovers from BOTH an
# abort and an estop. On the buggy build the sequencer wedges and the abort /
# estop / wait_complete below hangs, failing the test by timeout.
import sys
import time

import gmi
import stmak_test
from gmi.constants import *


def wait_for(s, cond, what, timeout=15.0):
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if cond(s):
            return
        time.sleep(0.05)
    s.poll()
    raise RuntimeError(
        "Timeout waiting for %s (task_state=%d interp_state=%d exec_state=%d)"
        % (what, s.task_state, s.interp_state, s.exec_state))


c = stmak_test.Command()
s = stmak_test.Stat()
stmak_test.wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.wait_complete()
c.mode(MODE_MDI)

# ---- Phase 1: task ABORT during a stuck tool change -------------------------
# T1 preps (completes via the tool-prepare loopback), then M6 raises tool-change
# and blocks waiting for tool-changed, which the stuck changer never asserts.
c.mdi("T1 M6")
stmak_test.wait_pin("tool-change-req", True, timeout=10)
print("phase1: tool change in progress (sequencer blocked in gmi_tool_load)")

# Abort. With the fix, gmi_io_abort clears tool-change, gmi_tool_load returns
# -1, the sequencer unwinds and restartSequencer's <-seqDone completes. Without
# it, this abort (or the wait_complete below) hangs until the client deadline.
c.abort()
stmak_test.wait_pin("tool-change-req", False, timeout=10)
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE,
         "interp idle after aborting the stuck change", timeout=10)

# Prove the sequencer is alive, not wedged: a fresh MDI must run to completion.
c.mdi("g0 x1")
c.wait_complete(timeout=10)   # raises if the sequencer never settled
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE,
         "MDI ran to completion after abort recovery", timeout=10)
print("phase1 ok: stuck tool change aborted, sequencer recovered, MDI ran")

# ---- Phase 2: ESTOP during a stuck tool change ------------------------------
# Exercises the machineShutdown -> finishShutdown -> restartSequencer teardown,
# whose <-seqDone join is what hung on the buggy build.
c.mdi("T2 M6")
stmak_test.wait_pin("tool-change-req", True, timeout=10)
print("phase2: tool change in progress")

c.state(STATE_ESTOP)
wait_for(s, lambda s: s.task_state == STATE_ESTOP,
         "STATE_ESTOP after estopping the stuck change (teardown must not wedge)",
         timeout=10)
stmak_test.wait_pin("tool-change-req", False, timeout=10)
print("phase2: estop teardown completed (did not wedge)")

# Recover and prove liveness again.
c.state(STATE_ESTOP_RESET)
stmak_test.wait_pin("estop-loop", True, timeout=5)
c.state(STATE_ON)
wait_for(s, lambda s: s.task_state == STATE_ON and s.exec_state == EXEC_DONE,
         "machine on after estop-reset", timeout=10)
c.mode(MODE_MDI)
c.mdi("g0 x0")
c.wait_complete(timeout=10)   # raises if the sequencer never settled
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE,
         "MDI ran to completion after estop recovery", timeout=10)
print("phase2 ok: stuck tool change estopped, machine recovered, MDI ran")

sys.exit(0)
