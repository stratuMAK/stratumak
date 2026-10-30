#!/usr/bin/env python3

# Ported to the stratuMAK REST/WS API (`gmi` client). The motion-logger is now an
# interceptor cmod between milltask and the real motmod. The program is run from
# a specific start line; a large feed override lets real motion complete quickly
# without changing the logged (programmed) SET_LINE velocities. We wait for
# INTERP_IDLE (true completion) so the full command stream is captured, then diff
# out.motion-logger against expected.motion-logger.

import gmi
from gmi.constants import *
import stmak_test

import sys
import subprocess

c = gmi.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


def wait_idle():
    '''Wait for an AUTO_RUN to finish.  Task.AutoRun puts the interpreter in
    READING before the POST returns, so IDLE seen after auto() is the end of
    this run, never the moment before it started.'''
    stmak_test.wait_stat(s, lambda st: st.interp_state == INTERP_IDLE,
                         "the program run to finish",
                         detail=lambda st: "interp_state=%d line=%d"
                                           % (st.interp_state, st.current_line))


c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.wait_complete()
c.mode(MODE_AUTO)
c.feedrate(10000.0)
c.wait_complete()

c.program_open('mountaindew.ngc')
c.auto(AUTO_RUN, 4)
wait_idle()

# The interceptor cmod appends to out.motion-logger from another process; wait
# for it to stop growing instead of sleeping and hoping the tail was flushed. A
# short read here truncates the diff and fails the test for the wrong reason.
stmak_test.wait_file_stable("out.motion-logger")

status = subprocess.call(
    ["diff", "-u", "expected.motion-logger", "out.motion-logger"])
sys.exit(0 if status == 0 else 1)
