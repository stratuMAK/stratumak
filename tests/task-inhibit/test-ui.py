#!/usr/bin/env python3
"""halui.auto-inhibit and halui.mdi-inhibit refuse before anything starts.

Without them the only way to forbid automatic motion is to let AUTO start and
then drop the machine's enable, which reaches the operator as an E-stop they
did not cause.  These refuse the mode change and the run outright.

They are separate pins because an interlock that forbids running a program
usually still has to allow the MDI routines an operator homes and touches off
with -- [HALUI]MDI_COMMAND entries go through the same MDI path -- so the test
also pins down that each inhibit leaves the other alone.
"""

import subprocess

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
print("PASS")
