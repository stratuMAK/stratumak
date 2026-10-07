#!/usr/bin/env python3
"""halui.program.ui-disable takes program flow away from the UIs only.

While the pin is high, emccmd refuses run/step/pause/resume, the abort of a
running program and the optional-stop and block-delete toggles unless the
caller passes force.  halui keeps program flow, a forced caller keeps it, the
program itself carries on, and stopping an MDI command stays available.
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
    subprocess.run(["halcmd", "sets", "ui-lock", "1" if value else "0"],
                   check=True)
    stmak_test.wait_stat(
        s, lambda st: st.program_ui_disabled == value,
        "program_ui_disabled to reach %s" % value,
        detail=lambda st: "program_ui_disabled=%s" % st.program_ui_disabled)


def pulse(pin):
    """halui program pins act on the rising edge."""
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
    stmak_test.fail("%s went through while program.ui-disable was set" % what)


def interp_is(states, desc):
    stmak_test.wait_stat(
        s, lambda st: st.interp_state in states, desc,
        detail=lambda st: "interp_state=%d" % st.interp_state)


stmak_test.wait_for_startup(s)
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
for j in (0, 1, 2):
    c.home(j)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")

s.poll()
if s.program_ui_disabled:
    stmak_test.fail("program_ui_disabled is set with the pin low")
c.set_optional_stop(False)
c.set_block_delete(False)
print("ok: toggles accepted with the pin low")

lock(True)

# --- the toggles -----------------------------------------------------------
refused("set_optional_stop", c.set_optional_stop, True)
refused("set_block_delete", c.set_block_delete, True)
s.poll()
if s.optional_stop or s.block_delete:
    stmak_test.fail("a refused toggle changed the task")

c.set_optional_stop(True, force=True)
stmak_test.wait_stat(s, lambda st: st.optional_stop, "forced optional stop")
print("ok: forced set_optional_stop")

pulse("halui.program.block-delete.on")
stmak_test.wait_stat(s, lambda st: st.block_delete, "halui block delete")
print("ok: halui block-delete.on")

# --- run, pause, resume, abort ---------------------------------------------
c.mode(MODE_AUTO)
c.wait_complete()
c.program_open("slow.ngc")
refused("auto run", c.auto, AUTO_RUN)
refused("auto step", c.auto, AUTO_STEP)
time.sleep(0.3 * stmak_test.scale())
s.poll()
if s.interp_state != INTERP_IDLE:
    stmak_test.fail("a refused run started the program")

c.auto(AUTO_RUN, force=True)
interp_is((INTERP_READING, INTERP_WAITING), "the forced run to start")
print("ok: forced auto run")

refused("auto pause", c.auto, AUTO_PAUSE)
refused("abort of a running program", c.abort)
time.sleep(0.3 * stmak_test.scale())
s.poll()
if s.interp_state not in (INTERP_READING, INTERP_WAITING):
    stmak_test.fail("the program stopped after refused commands "
                    "(interp_state=%d)" % s.interp_state)
print("ok: program carries on")

pulse("halui.program.pause")
interp_is((INTERP_PAUSED,), "halui pause")
print("ok: halui pause")

refused("auto resume", c.auto, AUTO_RESUME)
refused("abort of a paused program", c.abort)
c.auto(AUTO_RESUME, force=True)
interp_is((INTERP_READING, INTERP_WAITING), "the forced resume")
print("ok: forced resume")

pulse("halui.program.stop")
interp_is((INTERP_IDLE,), "halui stop")
print("ok: halui stop")

c.auto(AUTO_RUN, force=True)
interp_is((INTERP_READING, INTERP_WAITING), "the second forced run")
c.abort(force=True)
interp_is((INTERP_IDLE,), "the forced abort")
print("ok: forced abort")

# --- stopping MDI is not program flow ---------------------------------------
c.mode(MODE_MDI)
c.wait_complete()
c.mdi("G0 X0")
c.wait_complete()
stmak_test.drain_mdi(s)
c.mdi("G1 F60 X5")  # 5 s in flight
interp_is((INTERP_READING, INTERP_WAITING), "the MDI move to run")
c.abort()
interp_is((INTERP_IDLE,), "the MDI abort")
s.poll()
if abs(s.position[0] - 5.0) < 1e-3:
    stmak_test.fail("the MDI move ran to its end instead of being aborted")
print("ok: abort of an MDI command goes through")

# --- pin low again ----------------------------------------------------------
lock(False)
c.set_optional_stop(False)
c.set_block_delete(False)
c.mode(MODE_AUTO)
c.wait_complete()
c.auto(AUTO_RUN)
interp_is((INTERP_READING, INTERP_WAITING), "an unforced run")
c.abort()
interp_is((INTERP_IDLE,), "an unforced abort")
print("ok: everything goes through with the pin low")

print("PASS")
