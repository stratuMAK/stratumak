#!/usr/bin/env python3
# Minimal linuxcncrsh -> gmi translator. Reads the subset of linuxcncrsh 'set'
# commands used by the tool tests on stdin and drives the machine via the gmi
# REST client. Replaces piping the command stream into `nc localhost 5007`.
#
# Synchronisation lives in stmak_test (see lib/python/stmak_test.py): the Command
# here raises rather than silently returning -1 from a timed-out wait_complete,
# and drain_mdi polls a predicate against a deadline instead of sleeping.
import sys

import stmak_test
from gmi.constants import *

c = stmak_test.Command()
s = stmak_test.Stat()


for raw in sys.stdin:
    cmd = raw.strip()
    if not cmd:
        continue
    low = cmd.lower()
    if low.startswith('hello') or low.startswith('set enable') or low.startswith('set wait received'):
        continue
    elif low == 'set estop off':
        c.state(STATE_ESTOP_RESET)
    elif low == 'set estop on':
        c.state(STATE_ESTOP)
    elif low == 'set machine on':
        c.state(STATE_ON)
    elif low == 'set machine off':
        c.state(STATE_OFF)
    elif low == 'set mode mdi':
        c.mode(MODE_MDI)
    elif low == 'set mode manual':
        c.mode(MODE_MANUAL)
    elif low == 'set mode auto':
        c.mode(MODE_AUTO)
    elif low == 'set wait done':
        # RCS_ERROR is NOT fatal: these tool tests deliberately issue commands
        # that error (e.g. G10 L1 P0) and introspect the resulting state
        # afterwards. A timeout IS fatal, and stmak_test.Command raises on it —
        # a hung interpreter must not read as a clean run.
        c.wait_complete()
    elif low.startswith('set mdi '):
        c.mdi(cmd[len('set mdi '):])
        # No settle needed before polling: the /mdi POST registers the command
        # before it returns (Task.executeMDI sets interpState synchronously, or
        # the command lands on mdiQueue), so the drain predicate cannot read
        # "already idle" for an MDI that has not started yet.
        stmak_test.drain_mdi(s)
    elif low.startswith('set home '):
        c.home(int(cmd.split()[-1]))
    elif low.startswith('set teleop_enable '):
        c.teleop_enable(cmd.split()[-1] in ('1', 'true', 'on'))
    elif low == 'shutdown':
        break
    # everything else (get ..., etc.) is ignored
