#!/usr/bin/env python3
"""halui.machine.ui-disable takes estop and machine power away from the UIs.

While the pin is high, emccmd refuses every set_state -- estop, estop reset,
machine on and off -- unless the caller passes force.  halui's machine and
estop pins keep working, a forced caller keeps control, and program flow,
which has its own pin, is left alone.
"""

import subprocess
import time
import urllib.error

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()

s = gmi.Stat()
c = gmi.Command()


def lock(value):
    """Drive the pin through the signal the HAL file links it to, and wait
    until the task has sampled it: the stat field is the value the gate reads.
    """
    subprocess.run(["halcmd", "sets", "machine-lock", "1" if value else "0"],
                   check=True)
    stmak_test.wait_stat(
        s, lambda st: st.machine_ui_disabled == value,
        "machine_ui_disabled to reach %s" % value,
        detail=lambda st: "machine_ui_disabled=%s" % st.machine_ui_disabled)


def pulse(pin):
    """halui machine and estop pins act on the rising edge."""
    subprocess.run(["halcmd", "setp", pin, "1"], check=True)
    time.sleep(0.1 * stmak_test.scale())
    subprocess.run(["halcmd", "setp", pin, "0"], check=True)


def refused(what, fn, *args, **kw):
    """The command must come back as a 409 refusal, not run."""
    try:
        fn(*args, **kw)
    except urllib.error.HTTPError as e:
        if e.code != 409:
            stmak_test.fail("%s: HTTP %d, want 409" % (what, e.code))
        print("ok: %s refused" % what)
        return
    stmak_test.fail("%s went through while machine.ui-disable was set" % what)


def state_is(states, desc):
    stmak_test.wait_stat(
        s, lambda st: st.task_state in states, desc,
        detail=lambda st: "task_state=%d" % st.task_state)


def state_stays(state, desc):
    time.sleep(0.3 * stmak_test.scale())
    s.poll()
    if s.task_state != state:
        stmak_test.fail("%s (task_state=%d, want %d)"
                        % (desc, s.task_state, state))


OFF = (STATE_OFF, STATE_ESTOP_RESET)

stmak_test.wait_for_startup(s)
s.poll()
if s.machine_ui_disabled:
    stmak_test.fail("machine_ui_disabled is set with the pin low")
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
state_is((STATE_ON,), "an unforced machine on")
print("ok: estop reset and machine on accepted with the pin low")

lock(True)

# --- machine on: off and estop are refused ----------------------------------
refused("machine off", c.state, STATE_OFF)
refused("estop", c.state, STATE_ESTOP)
state_stays(STATE_ON, "a refused command took the machine down")
print("ok: machine stays on")

# Program flow has its own pin.
c.set_optional_stop(True)
stmak_test.wait_stat(s, lambda st: st.optional_stop, "unforced optional stop")
c.set_optional_stop(False)
print("ok: program flow is not affected")

pulse("halui.machine.off")
state_is(OFF, "halui machine.off")
print("ok: halui machine.off")

# --- machine off: on is refused ---------------------------------------------
refused("machine on", c.state, STATE_ON)
state_stays(s.task_state, "a refused machine on changed the state")
c.state(STATE_ON, force=True)
state_is((STATE_ON,), "the forced machine on")
print("ok: forced machine on")

pulse("halui.estop.activate")
state_is((STATE_ESTOP,), "halui estop.activate")
print("ok: halui estop.activate")

# --- estop: reset is refused ------------------------------------------------
refused("estop reset", c.state, STATE_ESTOP_RESET)
state_stays(STATE_ESTOP, "a refused estop reset left estop")
c.state(STATE_ESTOP_RESET, force=True)
state_is(OFF, "the forced estop reset")
print("ok: forced estop reset")

pulse("halui.machine.on")
state_is((STATE_ON,), "halui machine.on")
print("ok: halui machine.on")

c.state(STATE_ESTOP, force=True)
state_is((STATE_ESTOP,), "the forced estop")
print("ok: forced estop")

pulse("halui.estop.reset")
state_is(OFF, "halui estop.reset")
print("ok: halui estop.reset")

# --- pin low again ----------------------------------------------------------
lock(False)
c.state(STATE_ON)
state_is((STATE_ON,), "an unforced machine on")
c.state(STATE_OFF)
state_is(OFF, "an unforced machine off")
c.state(STATE_ESTOP)
state_is((STATE_ESTOP,), "an unforced estop")
c.state(STATE_ESTOP_RESET)
state_is(OFF, "an unforced estop reset")
print("ok: everything goes through with the pin low")

print("PASS")
