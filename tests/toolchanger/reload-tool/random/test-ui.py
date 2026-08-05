#!/usr/bin/env python3
# --- stratuMAK compatibility shim (prepended) --------------------------------------
# Makes the original NML-based driver body run against the stratuMAK REST/WS API:
#   linuxcnc  -> gmi client (command/stat/error_channel) + gmi.constants
#   hal       -> halcmd-backed shim; h[sig] reads/writes the io signals the old
#                userspace test component was connected to.
import gmi as _gmi
from gmi.constants import *
import subprocess as _subprocess

# Shared sync helpers: deadline-based waiters, and a Command whose
# wait_complete() raises on timeout instead of returning -1.
import stmak_test


class _LinuxcncCompat:
    command = staticmethod(stmak_test.Command)
    stat = staticmethod(_gmi.Stat)
    error_channel = staticmethod(_gmi.ErrorChannel)
    ini = staticmethod(_gmi.IniFile)

    def __getattr__(self, name):
        return globals()[name]


linuxcnc = _LinuxcncCompat()


class _HalCompat:
    HAL_S32 = HAL_U32 = HAL_FLOAT = HAL_BIT = 0
    HAL_IN = HAL_OUT = HAL_IO = 0

    def component(self, *a, **k):
        return self

    def newpin(self, *a, **k):
        return None

    def ready(self, *a, **k):
        pass

    def connect(self, *a, **k):
        pass

    def __getitem__(self, name):
        v = _subprocess.check_output(["halcmd", "gets", name]).decode().strip().split()[-1]
        if v in ("TRUE", "FALSE"):
            return v == "TRUE"
        return float(v)

    def __setitem__(self, name, val):
        _subprocess.run(["halcmd", "sets", name, "1" if val else "0"], check=True)


hal = _HalCompat()
# --- end shim -----------------------------------------------------------------


#!/usr/bin/env python3


import math
import time
import sys
import subprocess
import os
import signal
import glob
import re

xtool   = 0.111 # [EMCIO]TOOL_CHANGE_POSITION x
ytool   = 0.222 # [EMCIO]TOOL_CHANGE_POSITION y
ztool   = 0.333 # [EMCIO]TOOL_CHANGE_POSITION z
EPSILON = 1e-10

def wait_for_hal_pin(name, value, timeout=10):
    start_time = time.time()
    while (time.time() - start_time) < timeout:
        if h[name] == value:
            return
        time.sleep(0.1)
    raise RuntimeError("hal pin %s didn't get to %s after %.3f seconds" % (name, value, timeout))


def verify_stat(tool_in_spindle, tool_from_pocket, pocket_prepped=None):
    """Wait for the status buffer to reflect what the io pins already show.

    Waiting rather than sleeping-then-asserting: the assert is unchanged for a
    value that is already correct, and tolerant of a server that is merely slow.
    pocket_prepped is legitimately -1, so None is the "not checked" sentinel.
    """
    want = {'tool_in_spindle': tool_in_spindle, 'tool_from_pocket': tool_from_pocket}
    if pocket_prepped is not None:
        want['pocket_prepped'] = pocket_prepped
    stmak_test.wait_stat(
        s, lambda st: all(getattr(st, k) == v for k, v in want.items()),
        "status buffer to reach %s" % want,
        detail=lambda st: "got %s" % {k: getattr(st, k) for k in want})


def wait_for_position(x, y, z):
    """Wait for the joints to arrive at a position given in machine units.

    Arrival is a motion, not a status publication, so it gets its own wait: the
    tool-number fields reach their final values while the axes are still moving.
    """
    want = (x * MM, y * MM, z * MM)

    def arrived(st):
        return all(abs(st.joint_actual_position[i] - want[i]) < POS_EPSILON
                   for i in range(3))

    stmak_test.wait_stat(
        s, arrived, "joints to arrive at %r (mm)" % (want,),
        detail=lambda st: "at %r" % (tuple(st.joint_actual_position[:3]),))


c = linuxcnc.command()
s = linuxcnc.stat()
e = linuxcnc.error_channel()

h = hal.component("test-ui")

h.newpin("tool-change", hal.HAL_BIT, hal.HAL_IN)
h.newpin("tool-changed", hal.HAL_BIT, hal.HAL_OUT)
h["tool-changed"] = False

h.newpin("tool-prepare", hal.HAL_BIT, hal.HAL_IN)
h.newpin("tool-prepared", hal.HAL_BIT, hal.HAL_OUT)
h["tool-prepared"] = False

h.newpin("tool-number", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-prep-number", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-prep-pocket", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-from-pocket", hal.HAL_S32, hal.HAL_IN)

h.ready()

# stratuMAK: no postgui.hal — the shim reads HAL signals directly (no python-ui pins)


# Wait for LinuxCNC to initialize itself so the Status buffer stabilizes.
stmak_test.wait_for_startup(s)

c.state(linuxcnc.STATE_ESTOP_RESET)
c.state(linuxcnc.STATE_ON)
c.home(-1)
c.wait_complete()

c.mode(linuxcnc.MODE_MDI)


#
# At startup tool 3 is in the spindle and no tool prep or change is
# being requested.
#

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 3)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 0)
assert(h['tool-from-pocket'] == 0)

s.poll()
assert(s.tool_in_spindle == 3)
assert(s.tool_from_pocket == 0)
assert(s.pocket_prepped == -1)


#
# Prepare T2
#

c.mdi('t2')
wait_for_hal_pin('tool-prepare', True)

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == True)
assert(h['tool-number'] == 3)
assert(h['tool-prep-number'] == 2)
assert(h['tool-prep-pocket'] == 46)
assert(h['tool-from-pocket'] == 0)

s.poll()
assert(s.tool_in_spindle == 3)
assert(s.tool_from_pocket == 0)
assert(s.pocket_prepped == -1)

h['tool-prepared'] = True
wait_for_hal_pin('tool-prepare', False)
h['tool-prepared'] = False

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 3)
assert(h['tool-prep-number'] == 2)
assert(h['tool-prep-pocket'] == 46)
assert(h['tool-from-pocket'] == 0)

verify_stat(3, 0, 46)  # random tc gives you pocket, which is the same as tool-table-array index


#
# Change to T2
#

c.mdi('m6')
wait_for_hal_pin('tool-change', True)

assert(h['tool-change'] == True)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 3)
assert(h['tool-prep-number'] == 2)
assert(h['tool-prep-pocket'] == 46)
assert(h['tool-from-pocket'] == 0)

verify_stat(3, 0, 46)

# stratuMAK: gmi joint positions are reported in millimetres (mm-everywhere
# convention) and the field is joint_actual_position; TOOL_CHANGE_POSITION is
# in machine units (inch here). Position settle tolerance widened to the mm
# scale (classic 1e-10 inch is below the mm-domain arrival deadband).
#
# Arriving at TOOL_CHANGE_POSITION is a *motion*, so it needs its own wait: the
# tool-number fields above go correct while the axes are still flying, and this
# used to pass only because it borrowed the settle from a sleep that preceded a
# shared poll. Wait for the real thing — arrival — instead of a sample.
MM = 25.4
POS_EPSILON = 1e-4
wait_for_position(xtool, ytool, ztool)

h['tool-changed'] = True
wait_for_hal_pin('tool-change', False)
h['tool-changed'] = False

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 0)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, -1)


#
# Prepare T12
#

c.mdi('t12')
wait_for_hal_pin('tool-prepare', True)

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == True)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 12)
assert(h['tool-prep-pocket'] == 9)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, -1)

h['tool-prepared'] = True
wait_for_hal_pin('tool-prepare', False)
h['tool-prepared'] = False

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 12)
assert(h['tool-prep-pocket'] == 9)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 9)


#
# Prepare T2
# This tool is already in the spindle.
#

c.mdi('t2')
try:
    wait_for_hal_pin('tool-prepare', True, timeout=5)
except RuntimeError:
    # It *should* time out, no tool prep should be performed since the
    # requested tool is already in the spindle.
    pass

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 2)
assert(h['tool-prep-pocket'] == 0)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 0)


#
# Change to prepared tool (T2)
#

c.mdi('m6')
try:
    wait_for_hal_pin('tool-change', True, timeout=5)
except RuntimeError:
    # It *should* time out, no tool change should be performed since
    # the prepared tool is already in the spindle.
    pass

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 2)
assert(h['tool-prep-pocket'] == 0)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 0)

#
# Prepare T0
#

c.mdi('t0')
wait_for_hal_pin('tool-prepare', True)

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == True)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 2)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 0)

h['tool-prepared'] = True
wait_for_hal_pin('tool-prepare', False)
h['tool-prepared'] = False

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 2)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 2)  # random tc gives you pocket, which is the same as tool-table-array index


#
# Change to T0
#

c.mdi('m6')
wait_for_hal_pin('tool-change', True)

assert(h['tool-change'] == True)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 2)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 2)
assert(h['tool-from-pocket'] == 46)

verify_stat(2, 46, 2)

h['tool-changed'] = True
wait_for_hal_pin('tool-change', False)
h['tool-changed'] = False

assert(h['tool-change'] == False)
assert(h['tool-prepare'] == False)
assert(h['tool-number'] == 0)
assert(h['tool-prep-number'] == 0)
assert(h['tool-prep-pocket'] == 0)
assert(h['tool-from-pocket'] == 0)

verify_stat(0, 0, -1)

sys.exit(0)

