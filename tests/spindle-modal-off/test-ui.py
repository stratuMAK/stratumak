#!/usr/bin/env python3
"""S survives a machine off/on cycle; M3 does not.

S and M3 are different kinds of thing.  S is a modal setting -- how fast the
spindle should turn when it turns -- and an operator expects to set it once.
M3 is a command to turn: it describes the machine's present state, and
switching the machine off ends that state, as the spindle coming to a stop
makes plain.

The canon kept both.  Its spindleDir is what SetSpindleSpeed consults to decide
whether a bare S word is a retune of a turning spindle or only a stored
setting, and nothing cleared it when the machine went off, so after

    machine on, home, MDI "S1000", MDI "M3"   -> spindle turns at 1000
    machine off, machine on, MDI "S1000"      -> spindle turns at 1000

the second S started the spindle on its own.  Nothing was typed to start it and
nothing on screen said it would.

The same staleness reached the interpreter by a second route:
GET_EXTERNAL_SPINDLE answered from the sign of the stored speed, which
SetSpindleSpeed keeps as a magnitude, so it reported CLOCKWISE for any spindle
that had ever been given an S word.  Interp::synch reads that into
spindle_turning on every reset, so the machine-off handed the freshly reset
interpreter back the belief M3 is supposed to establish.  The test's last step
-- M3 after the off/on cycle -- is what pins down that the speed really did
survive, so the fix is "forget the direction", not "forget the spindle".
"""

import subprocess

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()


def pin_get(pin):
    out = subprocess.run(["halcmd", "getp", pin], check=True,
                         capture_output=True, text=True).stdout.strip()
    return out.split("=")[-1].strip()


s = gmi.Stat()
c = gmi.Command()

stmak_test.wait_for_startup(s)


def spindle_state():
    """(on-pin, speed-out) -- what the drive is actually being told."""
    return (pin_get("spindle.0.on") == "TRUE",
            float(pin_get("spindle.0.speed-out")))


def expect(label, on, speed):
    got = spindle_state()
    want = (on, speed)
    if got != want:
        stmak_test.fail("%s: expected on/speed %r, got %r" % (label, want, got))
    print("ok: %s" % label)


def machine_on():
    c.state(STATE_ESTOP_RESET)
    c.state(STATE_ON)
    c.mode(MODE_MANUAL)
    c.wait_complete()
    stmak_test.wait_stat(s, lambda st: st.enabled, "the machine to come on",
                         detail=lambda st: "task_state=%d" % st.task_state)
    s.poll()
    if not all(s.homed[:3]):
        for j in (0, 1, 2):
            c.home(j)
        c.wait_complete()
        stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")
    c.mode(MODE_MDI)
    c.wait_complete()


machine_on()

# Two separate MDI lines, the way an operator types them.
c.mdi("S1000")
c.wait_complete()
expect("S alone does not start the spindle", False, 0.0)
c.mdi("M3")
c.wait_complete()
expect("M3 starts it at the speed S set", True, 1000.0)

c.state(STATE_OFF)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: not st.enabled, "the machine to go off",
                     detail=lambda st: "enabled=%r" % st.enabled)
expect("the machine going off stops the spindle", False, 0.0)

machine_on()

# The regression: with the direction still stored, this S restarted the spindle.
c.mdi("S1000")
c.wait_complete()
expect("S alone still does not start the spindle after off/on", False, 0.0)

# ...and the speed did survive, so this is a fix and not an amputation.
c.mdi("M3")
c.wait_complete()
expect("M3 after the cycle runs at the surviving speed", True, 1000.0)

c.mdi("M5")
c.wait_complete()
print("PASS")
