#!/usr/bin/env python3
# Regression test for the milltask sequencer hard-fault path (2.9
# EMC_TASK_EXEC_ERROR parity): a failing M1xx handler mid-program must
#   1. latch EXEC_ERROR while the machine stays ON,
#   2. stop the hardware (spindle off, motion aborted),
#   3. flush the readahead already queued past the fault (the G1 X1 after
#      M102 must never execute), and
#   4. leave the task recoverable: after the estop/on cycle that clears the
#      latched error, an MDI must run cleanly — which proves interp on_abort
#      ran (no stale flags) and the sequencer was restarted (recoverSeqFault).
import gmi
import gomc_test
from gmi.constants import *
import sys
import time


def wait_for_startup(s, timeout=10.0):
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if (s.angular_units != 0.0 and s.linear_units != 0.0 and s.axis_mask != 0
                and s.exec_state == EXEC_DONE and s.interp_state == INTERP_IDLE
                and s.task_state == STATE_ESTOP):
            return
        time.sleep(0.1)
    raise RuntimeError("Timeout waiting for gomc startup")


def wait_for(s, cond, what, timeout=15.0):
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if cond(s):
            return
        time.sleep(0.05)
    s.poll()
    raise RuntimeError("Timeout waiting for %s (task_state=%d interp_state=%d "
                       "exec_state=%d x=%.3f)" % (what, s.task_state,
                                                  s.interp_state, s.exec_state,
                                                  s.actual_position[0]))


c = gomc_test.Command()
s = gmi.Stat()
wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.wait_complete()

c.mode(MODE_AUTO)
c.program_open("fail.ngc")
c.auto(AUTO_RUN)

# The failing M102 must drive the task into the latched error stop.
wait_for(s, lambda s: s.exec_state == EXEC_ERROR, "EXEC_ERROR after failing M102")

# The fault is an error stop, not an estop: the machine stays ON.
assert s.task_state == STATE_ON, \
    "task_state=%d after fault, want STATE_ON" % s.task_state

# Hardware stop: the spindle started by M3 must be commanded off.
wait_for(s, lambda s: s.spindle[0]['enabled'] == 0, "spindle off after fault",
         timeout=5.0)

# Readahead flush: the G1 X1 queued past the failing M102 must never run.
# Wait for motion to actually come to rest — two consecutive samples holding the
# same position — rather than sleeping 1 s and hoping it has. The old sleep both
# under-waited on a loaded runner (x sampled mid-decel, then compared against a
# threshold it had not reached yet) and hid the interesting failure: if the
# flushed G1 X1 *did* run, we want the assert below to see the settled endpoint.
# Positions are reported in mm (gomc motion runs in millimetres); the G1 X0.5
# before the fault completes (M102's queue-buster drains motion first), so a
# correct stop leaves x at 12.7 mm, well short of the flushed 25.4 mm target.
_last_x = {"v": None}


def _x_at_rest():
    s.poll()
    now = s.actual_position[0]
    prev, _last_x["v"] = _last_x["v"], now
    # core_sim loops motor-pos-cmd straight back to motor-pos-fb, so a stopped
    # axis reports a bit-identical position; only real motion moves it.
    return prev is not None and abs(now - prev) < 1e-6


gomc_test.wait_until(_x_at_rest, "motion to come to rest after the fault",
                     interval=0.1,
                     detail=lambda: "x=%.3f mm still changing" % s.actual_position[0])
x = s.actual_position[0]
assert x < 0.9 * 25.4, "x=%.3f mm: readahead G1 X1 past the fault was executed" % x
print("fault latched: EXEC_ERROR, machine ON, spindle off, x=%.3f mm (< 22.86)" % x)

# Designed recovery flow: EXEC_ERROR stays latched until an off/on (or
# estop/on) cycle. Use OFF→ON: a full estop cycle here races the iocontrol
# estop-input publication (100 ms CYCLE_TIME) — a stale asserted sample can
# reach the monitor after the machine is back ON and tear it down again;
# the estop leg has its own test (estop-while-running).
c.state(STATE_OFF)
c.state(STATE_ON)
c.wait_complete()
wait_for(s, lambda s: s.exec_state == EXEC_DONE and s.task_state == STATE_ON,
         "error cleared after off/on cycle", timeout=5.0)

# The next MDI must run cleanly: this proves the sequencer was restarted and
# interp on_abort cleared the aborted run's state (no wedge, no stale flags).
c.mode(MODE_MDI)
c.mdi("g0 x0")
rc = c.wait_complete(timeout=10.0)
assert rc != -1, "MDI after fault recovery did not complete"
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE
         and abs(s.actual_position[0]) < 0.01, "MDI g0 x0 completed cleanly",
         timeout=10.0)

print("recovery ok: MDI ran cleanly after the sequencer hard fault")
sys.exit(0)
