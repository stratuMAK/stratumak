#!/usr/bin/env python3

# Smoke-test driver for the installed package, ported to the gomc REST/WS API:
# it uses the `gmi` client package instead of the removed NML `linuxcnc` python
# module. Positions come from gmi.Stat; the custom `motion.analog-out-00`
# counter is read with `halcmd getp` (there is no userspace HAL component
# anymore, so the old test-ui comp and its linuxcnc-test.hal net are gone).
#
# The .ngc and the machine are the same as tests/single-step/, and so is this
# driver — keep the two in step when either changes.

import gmi
from gmi.constants import *

import gomc_test

import subprocess
import time
import sys

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

_WAITING = (
    EXEC_WAITING_FOR_MOTION,
    EXEC_WAITING_FOR_MOTION_AND_IO,
    EXEC_WAITING_FOR_MOTION_QUEUE,
)

# Per-phase deadline for wait_complete_step(), below.
_STEP_TIMEOUT = 15.0


def counter():
    # halcmd getp prints "<type> <dir> <name> = <value>"; take the last field.
    out = subprocess.check_output(["halcmd", "getp", "motion.analog-out-00"])
    return float(out.split()[-1])


def positions():
    # s.actual_position is gomc-mm; the program runs G20 on this inch config,
    # so convert back to inches for the mod-5 goal checks.
    p = s.actual_position
    return p[0] / 25.4, p[1] / 25.4, p[2] / 25.4


def print_state():
    x, y, z = positions()
    sys.stderr.write(
        "line=%d; Xpos=%.2f; Ypos=%.2f; Zpos=%.2f; counter=%d\n" %
        (s.current_line, x, y, z, counter()))


#
# The launcher only waited for the REST endpoint to answer, which proves the
# server is serving, not that the machine has finished booting. Ask the status
# buffer before commanding anything.
#

gomc_test.wait_for_startup(s)

#
# Come out of E-stop, turn the machine on, home, and switch to Auto mode.
#

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.home(0)
c.home(1)
c.home(2)

# A generous ceiling costs nothing on the happy path -- the wait ends as soon
# as homing does; it only bounds the failure on a loaded runner.
gomc_test.wait_stat(s, lambda st: all(st.homed[0:3]),
                    "joints 0-2 to finish homing", timeout=30.0,
                    detail=lambda st: "homed=%s" % (list(st.homed[0:3]),))

c.mode(MODE_AUTO)
c.program_open('linuxcnc-test.ngc')

epsilon = 0.000001


def mod_5_is_0(x):
    return abs((x + epsilon) % 5) < 2 * epsilon


def wait_complete_step():
    '''Single-stepping keeps task in RCS_EXEC (not RCS_DONE), so we cannot use
    wait_complete().  Instead, wait for exec_state to enter a wait-for-motion
    state (the step was accepted) and then leave it (the step finished).

    Each phase gets its own independent deadline, so a slow accept does not eat
    the budget of the wait that follows (which would then name the wrong phase
    in the failure).'''

    def _exec(st):
        return "exec_state=%d interp_state=%d" % (st.exec_state, st.interp_state)

    # Wait for the step to be accepted (exec_state enters a waiting state),
    # or for the interpreter to go idle (program finished).
    gomc_test.wait_stat(
        s,
        lambda st: st.exec_state in _WAITING or st.interp_state == INTERP_IDLE,
        "the step to be accepted (task to start waiting for motion)",
        timeout=_STEP_TIMEOUT, detail=_exec)

    # Wait for Task to be done waiting for Motion.
    gomc_test.wait_stat(
        s, lambda st: st.exec_state not in _WAITING,
        "the step to finish (task to stop waiting for motion)",
        timeout=_STEP_TIMEOUT, detail=_exec)


# Take first three steps (these cause no motion).
c.auto(AUTO_STEP)
c.auto(AUTO_STEP)
c.auto(AUTO_STEP)
wait_complete_step()

count = 0
while True:
    s.poll()
    if s.interp_state == INTERP_IDLE:
        sys.stderr.write("Finished:  Detected program finish\n")
        break

    (x, y) = positions()[0:2]
    if mod_5_is_0(x) and mod_5_is_0(y):
        # Both axes on goal; command the next step and wait for it to finish.
        sys.stderr.write("Taking step from X%.2f Y%.2f\n" % (x, y))
        c.auto(AUTO_STEP)
        wait_complete_step()

    print_state()

    count += 1
    if count >= 1000:  # Shouldn't happen, but prevent runaways
        sys.stderr.write("Finished:  Exceeded max cycles\n")
        sys.exit(1)

    time.sleep(0.1)

end_counter = counter()
if end_counter != 25:
    sys.stderr.write("End counter incorrect:  %d != 25\n" % end_counter)
    sys.exit(1)

sys.exit(0)
