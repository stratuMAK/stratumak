#!/usr/bin/env python3
# Ported to the gomc REST/WS API (`gmi` client). motion-logger is an interceptor
# between milltask and the real motmod.
#
# Github issue #49: the config runs startup gcode
# ([RS274NGC]RS274NGC_STARTUP_CODE = o<init> call). gomc executes it at task init
# (mirroring classic emcTaskPlanInit), before the machine is enabled — so, exactly
# like 2.9, init.ngc produces no real motion here (the first rapid is emitted to
# motion but never runs at estop). This test just checks that aborting after the
# startup code has been processed produces a clean, deterministic teardown and
# does not hang or crash. The captured command stream is diffed against
# expected.motion-logger.

import gmi
from gmi.constants import *
import gomc_test

import sys
import time
import subprocess


def wait_for_startup(s, timeout=15.0):
    # milltask up and idle => the startup code has already run (it executes
    # synchronously during module Start(), before the server accepts clients).
    # exec_state is left at EXEC_ERROR, not EXEC_DONE: init.ngc's first rapid is
    # dispatched to motion at estop, which motmod rejects ("need to be enabled,
    # in coord mode") — the deterministic side effect of gomc running startup
    # motion at init while the machine is off. We only need interp idle + estop.
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if (s.linear_units != 0.0 and s.axis_mask != 0
                and s.interp_state == INTERP_IDLE
                and s.task_state == STATE_ESTOP):
            return
        time.sleep(0.05)
    raise SystemExit("timeout waiting for milltask startup")


c = gmi.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()
wait_for_startup(s)

print("UI abort")
sys.stdout.flush()
c.abort()
c.wait_complete()
# The interceptor cmod writes out.motion-logger from another process: wait for
# the abort's log lines to land and the file to stop growing, rather than
# sleeping and hoping. Reading early truncates the diff below.
gomc_test.wait_file_stable("out.motion-logger")
print("UI done with abort")
sys.stdout.flush()

# Diff here, before the server is torn down: a clean SIGTERM shutdown appends a
# trailing ABORT to out.motion-logger that the checked-in gold must not contain.
status = subprocess.call(
    ["diff", "-u", "expected.motion-logger", "out.motion-logger"])
sys.exit(0 if status == 0 else 1)
