#!/usr/bin/env python3
# Regression test for the commanded-ESTOP teardown while a program runs
# (2.9 emcTaskSetState(ESTOP) parity) and the ESTOP_RESET recovery:
#   1. ESTOP mid-program with the spindle on must stop the hardware — spindle
#      commanded off, motion halted — and commit a consistent state (ESTOP,
#      interp idle, exec done: a commanded estop is not an error stop).
#   2. After estop-reset (which runs 2.9's abort sequence: IoAbort(5) +
#      interp on_abort + synch) and machine-on, an MDI must run cleanly.
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
c.program_open("long.ngc")
c.auto(AUTO_RUN)

# Wait until the program is really running: spindle on and the slow G1 X5
# under way (positions are in mm; X5 = 127 mm at F10 takes ~30 s).
wait_for(s, lambda s: s.spindle[0]['enabled'] == 1 and s.actual_position[0] > 1.0,
         "program running with spindle on")

c.state(STATE_ESTOP)

# The teardown must stop the hardware and commit a consistent estop state.
wait_for(s, lambda s: s.task_state == STATE_ESTOP, "STATE_ESTOP after command",
         timeout=5.0)
wait_for(s, lambda s: s.spindle[0]['enabled'] == 0, "spindle off after estop",
         timeout=5.0)
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE,
         "interp idle / exec done after estop (commanded estop is not an error stop)",
         timeout=5.0)

# Motion must actually be stopped: two samples well apart must match.
s.poll()
x1 = s.actual_position[0]
time.sleep(0.5)
s.poll()
x2 = s.actual_position[0]
assert abs(x2 - x1) < 0.01, "still moving after estop: x %.3f -> %.3f" % (x1, x2)
assert x2 < 127.0 - 1.0, "x=%.3f mm: G1 X5 ran to completion despite estop" % x2
print("estop stopped the machine: spindle off, motion halted at x=%.3f mm" % x2)

# Recovery: estop-reset (runs the 2.9 abort sequence incl. interp on_abort),
# then machine on. The monitor's safety loop (checkEstop) independently samples
# the estop input and tears the machine back down if it reads it asserted, so ON
# must not be commanded until that input has genuinely cleared. The estop input
# is iocontrol.emc-enable-in, carried by core_sim.hal's `estop-loop` signal
# (net estop-loop iocontrol.user-enable-out iocontrol.emc-enable-in), and
# iocontrol reports estop by reading that pin live. So wait on the signal itself
# instead of sleeping 0.5 s: it is the very input the monitor samples, and a
# timeout here names a stuck estop rather than surfacing later as an
# unexplained teardown.
c.state(STATE_ESTOP_RESET)
wait_for(s, lambda s: s.task_state == STATE_ESTOP_RESET, "estop-reset confirmed",
         timeout=5.0)
gomc_test.wait_pin("estop-loop", True)
c.state(STATE_ON)
wait_for(s, lambda s: s.task_state == STATE_ON and s.exec_state == EXEC_DONE,
         "machine on after estop-reset", timeout=5.0)

# The next MDI must run cleanly against the reset interpreter.
c.mode(MODE_MDI)
c.mdi("g0 x0")
rc = c.wait_complete(timeout=10.0)
assert rc != -1, "MDI after estop recovery did not complete"
wait_for(s, lambda s: s.interp_state == INTERP_IDLE and s.exec_state == EXEC_DONE
         and abs(s.actual_position[0]) < 0.01, "MDI g0 x0 completed cleanly",
         timeout=10.0)

print("recovery ok: MDI ran cleanly after estop + estop-reset")
sys.exit(0)
