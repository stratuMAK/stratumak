#!/usr/bin/env python3
"""halui.auto-inhibit and halui.mdi-inhibit refuse before anything starts.

Without them the only way to forbid automatic motion is to let AUTO start and
then drop the machine's enable, which reaches the operator as an E-stop they
did not cause.  These refuse the mode change and the run outright.

They are separate pins because an interlock that forbids running a program
usually still has to allow the MDI routines an operator homes and touches off
with -- [HALUI]MDI_COMMAND entries go through the same MDI path -- so the test
also pins down that each inhibit leaves the other alone, an MDI move already
in flight when auto-inhibit rises included.
"""

import subprocess
import time

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()


def pin_set(pin, value):
    """Set an inhibit pin and wait until task has actually sampled it.

    The pins are read once per halui monitor tick, so a command issued straight
    after setp can beat the sample and see the old state -- a race that only
    loses under load, which is exactly when it is most confusing.  The status
    field the UI greys its buttons from is the same value the guards read, so
    waiting on it is waiting for the guard to be armed.
    """
    subprocess.run(["halcmd", "setp", pin, "1" if value else "0"], check=True)
    field = {"halui.auto-inhibit": "auto_inhibit",
             "halui.mdi-inhibit": "mdi_inhibit"}.get(pin)
    if field is not None:
        stmak_test.wait_stat(
            s, lambda st: getattr(st, field) == value,
            "%s to reach task status as %s" % (pin, value),
            detail=lambda st: "%s=%s" % (field, getattr(st, field)))


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


def mode_reaches(target, want, label):
    try:
        c.mode(target)
        c.wait_complete()
    except Exception:
        pass  # a refused command may surface as an error; the mode decides
    stmak_test.wait_stat(
        s, lambda st: (st.task_mode == target) == want,
        "%s (task_mode==%d to be %s)" % (label, target, want),
        detail=lambda st: "task_mode=%d" % st.task_mode)
    print("ok: %s" % label)


# Baseline: both modes reachable with neither inhibit set.
pin_set("halui.auto-inhibit", False)
pin_set("halui.mdi-inhibit", False)
mode_reaches(MODE_AUTO, True, "AUTO reachable with no inhibit")
c.mode(MODE_MANUAL); c.wait_complete()
mode_reaches(MODE_MDI, True, "MDI reachable with no inhibit")
c.mode(MODE_MANUAL); c.wait_complete()

# auto-inhibit blocks AUTO and leaves MDI alone.
pin_set("halui.auto-inhibit", True)
mode_reaches(MODE_AUTO, False, "AUTO refused while auto-inhibit is set")
mode_reaches(MODE_MDI, True, "MDI still reachable while auto-inhibit is set")
c.mode(MODE_MANUAL); c.wait_complete()
pin_set("halui.auto-inhibit", False)

# mdi-inhibit blocks MDI and leaves AUTO alone.
pin_set("halui.mdi-inhibit", True)
mode_reaches(MODE_MDI, False, "MDI refused while mdi-inhibit is set")
mode_reaches(MODE_AUTO, True, "AUTO still reachable while mdi-inhibit is set")
c.mode(MODE_MANUAL); c.wait_complete()

# An MDI command itself is refused, not merely the mode.
try:
    c.mdi("G4 P0.01")
    c.wait_complete()
except Exception:
    pass
s.poll()
if s.interp_state != INTERP_IDLE:
    stmak_test.fail("an MDI command ran while mdi-inhibit was set")
print("ok: MDI command refused while mdi-inhibit is set")

pin_set("halui.mdi-inhibit", False)

# --- auto-inhibit refuses the run itself, not merely the mode -------------
# The mode gate is the front door but not the only one: autoCommand switches
# into AUTO by itself, so a Run or a Step issued from any mode would otherwise
# walk straight past a refused mode change.  Step is checked as well as Run
# because stepping is a way to start a program too -- it was the hole the mode
# gate and the Run guard between them left open.
c.mode(MODE_AUTO)
c.wait_complete()
c.program_open("slow.ngc")
pin_set("halui.auto-inhibit", True)

for auto_cmd, name in ((AUTO_RUN, "run"), (AUTO_STEP, "step")):
    try:
        c.auto(auto_cmd)
        c.wait_complete()
    except Exception:
        pass  # a refused command may surface as an error; the state decides
    # A program that did start needs a moment to show up as one: polling the
    # instant after the command would pass whether it was refused or merely
    # slow off the mark.
    time.sleep(0.3 * stmak_test.scale())
    s.poll()
    if s.interp_state != INTERP_IDLE:
        started = s.interp_state
        c.abort()
        c.wait_complete()
        stmak_test.fail("a program %s while auto-inhibit was set "
                        "(interp_state=%d)" % (name, started))
    print("ok: %s refused while auto-inhibit is set" % name)

pin_set("halui.auto-inhibit", False)

# --- auto-inhibit stops a program that is already running -----------------
# Refusing new runs is only half an interlock: if the guard opens mid-program
# the machine would otherwise cut to the end of the file.  The pin aborts the
# run and says why, rather than the program merely stopping.
c.mode(MODE_AUTO)
c.wait_complete()
c.program_open("slow.ngc")
c.auto(AUTO_RUN)
stmak_test.wait_stat(
    s, lambda st: st.interp_state in (INTERP_READING, INTERP_WAITING),
    "the program to be running",
    detail=lambda st: "interp_state=%d" % st.interp_state)
print("ok: program running")

pin_set("halui.auto-inhibit", True)
stmak_test.wait_stat(
    s, lambda st: st.interp_state == INTERP_IDLE,
    "auto-inhibit to abort the running program",
    detail=lambda st: "interp_state=%d" % st.interp_state)
print("ok: auto-inhibit aborts a running program")

pin_set("halui.auto-inhibit", False)

# --- auto-inhibit leaves an MDI command in flight alone -------------------
# The abort above must stop at AUTO.  The interpreter reads as busy for an
# MDI command exactly as it does for a program, so a rising edge that only
# looked at the interpreter state would also cut short the touch-off or homing
# move the operator is in the middle of -- the very MDI routines the pin is
# documented to leave alone.  The move has to run to its endpoint, not merely
# end: an aborted move and a finished one both leave the interpreter idle, so
# the position is what decides.
c.mode(MODE_MDI)
c.wait_complete()
c.mdi("G0 X0")
c.wait_complete()
stmak_test.drain_mdi(s)
c.mdi("G1 F120 X5")  # 5 mm at 120 mm/min: 2.5 s in flight
stmak_test.wait_stat(
    s, lambda st: st.interp_state != INTERP_IDLE,
    "the MDI move to be running",
    detail=lambda st: "interp_state=%d" % st.interp_state)
pin_set("halui.auto-inhibit", True)
stmak_test.wait_stat(
    s, lambda st: st.interp_state == INTERP_IDLE,
    "the MDI move to end",
    detail=lambda st: "interp_state=%d" % st.interp_state)
s.poll()
if abs(s.position[0] - 5.0) > 1e-3:
    stmak_test.fail("the MDI move was cut short by auto-inhibit "
                    "(X=%g, want 5)" % s.position[0])
print("ok: auto-inhibit leaves a running MDI command alone")

pin_set("halui.auto-inhibit", False)
print("PASS")
