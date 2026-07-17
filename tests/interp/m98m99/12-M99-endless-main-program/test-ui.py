#!/usr/bin/env python3

# Ported to the gomc REST/WS API (`gmi` client). motion-logger is now an
# interceptor between milltask and the real motmod. Runs test.ngc from line 4;
# the M99 in the main program loops it (terminated by a counter at 3 loops). A
# large feed override lets real motion complete quickly without changing the
# logged (programmed) SET_LINE velocities.
#
# The motion-logger diff is written to STDERR (and reflected in the exit code) so
# it does NOT pollute stdout, which test.sh reserves for the standalone rs274
# output compared against `expected`.

import gmi
from gmi.constants import *

import sys
import subprocess

import gomc_test

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


def _interp(st):
    return "interp_state=%d" % st.interp_state


def wait_idle(timeout=60.0):
    # Two phases, each with its own deadline, each failing loudly.
    #
    # The leave-idle phase used to fall straight through to the idle-wait when
    # it expired, and the idle-wait then returned instantly against a program
    # that had never started -- yielding an empty out.motion-logger and a diff
    # that blamed the motion output for a run that never happened.
    gomc_test.wait_stat(s, lambda st: st.interp_state != INTERP_IDLE,
                        "the program to start (interpreter to leave idle)",
                        timeout=10.0, detail=_interp)
    gomc_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                        "the program to finish (interpreter to return to idle)",
                        timeout=timeout, detail=_interp)


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.wait_complete()
c.mode(MODE_AUTO)
c.feedrate(10000.0)
c.wait_complete()

c.program_open('test.ngc')
c.auto(AUTO_RUN, 4)
wait_idle()

# motion-logger is a different process's writes: the interpreter going idle does
# not mean its trailing lines have been flushed. Sleeping ~0.2s and hoping
# truncates the tail on a slow flush and fails the diff below. Wait for the log
# to stop growing instead.
gomc_test.wait_file_stable("out.motion-logger")

status = subprocess.call(
    ["diff", "-u", "expected.motion-logger", "out.motion-logger"],
    stdout=sys.stderr)
sys.exit(0 if status == 0 else 1)
