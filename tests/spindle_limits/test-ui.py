#!/usr/bin/env python3

# Ported to the stratuMAK REST/WS API (gmi client).  The classic NML linuxcnc module
# is command/stat-less now; drive via gmi.Command/Stat and read HAL pins with
# halcmd (there is no userspace hal.component anymore).

import gmi
from gmi.constants import *
import stmak_test

import subprocess
import sys


class _HAL:
    @staticmethod
    def get_value(pin):
        out = subprocess.check_output(["halcmd", "getp", pin])
        return float(out.split()[-1])


hal = _HAL()


def wait_for_linuxcnc_startup(status):
    """Poll Status until the interpreter is idle and settled."""
    stmak_test.wait_stat(status, lambda st: st.exec_state == EXEC_DONE
                         and st.interp_state == INTERP_IDLE
                         and st.task_state == STATE_ESTOP,
                         "the machine to start up")


def expect(*speeds):
    """Wait for spindle N's speed-out pin and status speed to be speeds[N].

    Every step expects other values than the one before it, so the wait
    cannot be met by the previous step's state; a limit that is wrong never
    gets there and the wait fails, naming what it saw.  (A flat 0.5 s
    settle stood here.)"""
    def seen():
        s.poll()
        return [(hal.get_value('spindle.%d.speed-out' % n), s.spindle[n]['speed'])
                for n in range(len(speeds))]

    stmak_test.wait_until(
        lambda: all(pin == want and stat == want
                    for (pin, stat), want in zip(seen(), speeds)),
        "spindle speeds %r (pin and status)" % (speeds,),
        detail=lambda: "(speed-out, status speed) per spindle: %r" % (seen(),))


# stmak_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

# Wait for LinuxCNC to initialize itself so the Status buffer stabilizes.
wait_for_linuxcnc_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.wait_complete()


# Check G-code / MDI spindle commands

c.mode(MODE_MDI)

c.mdi("M3 S10 $0")
c.mdi("M3 S10 $1")
c.mdi("M3 S10 $2")
c.wait_complete()
expect(100, 200, 300)

c.mdi("M3 S10000 $0")
c.mdi("M3 S10000 $1")
c.mdi("M3 S10000 $2")
c.wait_complete()
expect(1000, 2000, 3000)

c.mdi("M4 S10 $0")
c.mdi("M4 S10 $1")
c.mdi("M4 S10 $2")
c.wait_complete()
expect(-500, -200, -300)

c.mdi("M4 S10000 $0")
c.mdi("M4 S10000 $1")
c.mdi("M4 S10000 $2")
c.wait_complete()
expect(-1500, -2500, -3000)

# Check Python interface commands

c.spindle(SPINDLE_FORWARD, 10, 0, 0)
c.spindle(SPINDLE_FORWARD, 10, 1, 0)
c.spindle(SPINDLE_FORWARD, 10, 2, 0)
c.wait_complete()
expect(100, 200, 300)

c.spindle(SPINDLE_FORWARD, 10000, 0, 0)
c.spindle(SPINDLE_FORWARD, 10000, 1, 0)
c.spindle(SPINDLE_FORWARD, 10000, 2, 0)
c.wait_complete()
expect(1000, 2000, 3000)

c.spindle(SPINDLE_REVERSE, 10, 0, 0)
c.spindle(SPINDLE_REVERSE, 10, 1, 0)
c.spindle(SPINDLE_REVERSE, 10, 2, 0)
c.wait_complete()
expect(-500, -200, -300)

c.spindle(SPINDLE_REVERSE, 10000, 0, 0)
c.spindle(SPINDLE_REVERSE, 10000, 1, 0)
c.spindle(SPINDLE_REVERSE, 10000, 2, 0)
c.wait_complete()
expect(-1500, -2500, -3000)

sys.exit(0)
