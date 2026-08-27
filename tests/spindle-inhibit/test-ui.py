#!/usr/bin/env python3
"""spindle.N.start-inhibit refuses a start and stops a spindle that is running.

spindle.N.inhibit was already there but is a clutch, not an ignition: it only
zeroes the speed *scale*.  M3 still succeeds, the spindle state stays on, the
status buffer keeps reporting the commanded speed -- and dropping the pin spins
the spindle straight back up with no operator action, which is the opposite of
what an interlock is for.  start-inhibit refuses the command outright, stops a
spindle that is already turning, and leaves it stopped.

Also pins down the enables bug both inhibits shared: the guards read
"enables & *inhibit_pin", and since a bit pin is 0 or 1 that ANDed against
0x01 == SS_ENABLED, so *both* inhibits silently stopped working whenever
spindle-scale override was switched off.
"""

import subprocess
import time

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()


def pin_set(pin, value):
    subprocess.run(["halcmd", "setp", pin, "1" if value else "0"], check=True)


def pin_get(pin):
    out = subprocess.run(["halcmd", "getp", pin], check=True,
                         capture_output=True, text=True).stdout.strip()
    return out.split("=")[-1].strip()


s = gmi.Stat()
c = gmi.Command()

stmak_test.wait_for_startup(s)
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
for j in (0, 1, 2):
    c.home(j)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")
c.mode(MODE_MDI)
c.wait_complete()

pin_set("spindle.0.inhibit", False)
pin_set("spindle.0.start-inhibit", False)


def spindle_state():
    """(on-pin, speed-out, status-enabled) -- HAL truth and what the UI sees."""
    s.poll()
    return (pin_get("spindle.0.on") == "TRUE",
            float(pin_get("spindle.0.speed-out")),
            bool(s.spindle[0].get("enabled")))


def expect(label, on, speed, enabled):
    got = spindle_state()
    want = (on, speed, enabled)
    if got != want:
        stmak_test.fail("%s: expected on/speed/enabled %r, got %r" % (label, want, got))
    print("ok: %s" % label)


# --- baseline -------------------------------------------------------------
c.mdi("M3 S1000")
c.wait_complete()
expect("M3 runs the spindle with no inhibit", True, 1000.0, True)
c.mdi("M5")
c.wait_complete()
expect("M5 stops it", False, 0.0, False)

# --- start-inhibit refuses the start --------------------------------------
pin_set("spindle.0.start-inhibit", True)
try:
    c.mdi("M3 S1000")
    c.wait_complete()
except Exception:
    pass  # refusal may surface as an error; the state is what decides
expect("M3 refused while start-inhibit is set", False, 0.0, False)

# ...and releasing the pin must not spin it up: nothing was ever started.
pin_set("spindle.0.start-inhibit", False)
time.sleep(0.3)  # give the servo cycle a chance to restart it, if it would
expect("spindle stays stopped after start-inhibit is released", False, 0.0, False)

# --- start-inhibit stops a spindle that is already running -----------------
c.mdi("M3 S1000")
c.wait_complete()
expect("spindle running again", True, 1000.0, True)

pin_set("spindle.0.start-inhibit", True)
stmak_test.wait_until(
    lambda: spindle_state() == (False, 0.0, False),
    "start-inhibit to stop the running spindle",
    detail=lambda: "on/speed/enabled=%r" % (spindle_state(),))
print("ok: start-inhibit stops a running spindle")

# The dangerous case the old inhibit had: dropping the pin must NOT restart it.
pin_set("spindle.0.start-inhibit", False)
time.sleep(0.3)  # give the servo cycle a chance to restart it, if it would
expect("spindle stays stopped after the pin drops", False, 0.0, False)

# --- the old inhibit still masks speed, and now survives SS off ------------
c.mdi("M3 S1000")
c.wait_complete()
pin_set("spindle.0.inhibit", True)
stmak_test.wait_until(
    lambda: spindle_state()[1] == 0.0, "inhibit to mask the speed",
    detail=lambda: "speed-out=%r" % (spindle_state()[1],))
print("ok: spindle.0.inhibit masks the speed")

# Before the fix this ANDed against SS_ENABLED, so turning spindle override
# off silently disabled the inhibit and the spindle came back to full speed.
c.set_spindle_override(False)
c.wait_complete()
time.sleep(0.3)  # give the servo cycle a chance to restart it, if it would
if spindle_state()[1] != 0.0:
    stmak_test.fail("spindle.0.inhibit stopped working with spindle override "
                    "disabled: speed-out=%r" % (spindle_state()[1],))
print("ok: spindle.0.inhibit still holds with spindle override disabled")
c.set_spindle_override(True)
c.wait_complete()

pin_set("spindle.0.inhibit", False)
c.mdi("M5")
c.wait_complete()
print("PASS")
