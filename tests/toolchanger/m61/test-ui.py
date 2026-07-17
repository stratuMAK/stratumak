#!/usr/bin/env python3
# --- gomc compatibility shim (prepended) --------------------------------------
# Makes the original NML-based driver body run against the gomc REST/WS API:
#   linuxcnc  -> gmi client (command/stat/error_channel) + gmi.constants
#   hal       -> halcmd-backed shim; h[sig] reads/writes the io signals the old
#                userspace test component was connected to.
import gmi as _gmi
import gomc_test as _gomc_test
from gmi.constants import *
import subprocess as _subprocess


class _LinuxcncCompat:
    # gomc_test.Command, not _gmi.Command: gmi's wait_complete() reports a
    # timed-out wait as -1 in a normal 200 body, so the bare c.wait_complete()
    # calls below would silently proceed against an unsettled machine. The
    # strict subclass raises instead, failing at the point it went wrong.
    command = staticmethod(_gomc_test.Command)
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


import time
import sys
import os


# this is how long we wait for linuxcnc to do our bidding.  Sized for a loaded
# CI runner, not an idle workstation: every wait below polls its predicate and
# returns as soon as it holds, so a generous ceiling costs nothing on the happy
# path -- it only bounds how long a genuine failure takes to report.
timeout = 10.0


def introspect():
    os.system("halcmd show pin python-ui")


def wait_for_pin_value(pin_name, value, timeout=timeout):
    print("waiting for %s to go to %f (timeout=%f)" % (pin_name, value, timeout))

    start = time.time()
    while (h[pin_name] != value) and ((time.time() - start) < timeout):
        time.sleep(0.1)

    if h[pin_name] != value:
        print("timeout!  pin %s is %f, didn't get to %f" % (pin_name, h[pin_name], value))
        introspect()
        sys.exit(1)

    print("pin %s went to %f!" % (pin_name, value))


def verify_pin_value(pin_name, value):
    if (h[pin_name] != value):
        print("pin %s is %f, not %f" % (pin_name, h[pin_name], value))
        sys.exit(1);

    print("pin %s is %f" % (pin_name, value))


def get_interp_param(param_number):
    # gomc: the emcerror WS watch destructively flushes queued messages and the
    # push loop suppresses byte-identical consecutive payloads, so a DEBUG
    # display that repeats the previous one within a watch tick is LOST (see
    # PRODUCTION_READINESS.md "operator messages lost").  Pace the MDIs past
    # the 200 ms watch tick and retry on a lost reply.
    for attempt in range(3):
        time.sleep(0.3)
        c.mdi("(debug, #%d)" % param_number)
        c.wait_complete()

        # wait up to 2 seconds for a reply
        start = time.time()
        while (time.time() - start) < 2:
            error = e.poll()
            if error == None:
                time.sleep(0.010)
                continue

            kind, text = error
            if kind == linuxcnc.OPERATOR_DISPLAY:
                return float(text)

            print(text)

    # Every retry lost its reply.  Fail here, naming the param: returning None
    # only defers the failure to the caller's "%f" % None, which raises a
    # TypeError that says nothing about which parameter went missing.
    print("ERROR: no OPERATOR_DISPLAY reply for interp param #%d after 3 attempts"
          % param_number)
    sys.exit(1)


def verify_interp_param(param_number, expected_value):
    param_value = get_interp_param(param_number)
    print("interp param #%d = %f (expecting %f)" % (param_number, param_value, expected_value))
    if param_value != expected_value:
        print("ERROR: interp param #%d = %f, expected %f" % (param_number, param_value, expected_value))
        sys.exit(1)


def verify_stable_pin_values(pins, duration=1):
    start = time.time()
    while (time.time() - start) < duration:
        for pin_name in list(pins.keys()):
            val = h[pin_name]
            if val != pins[pin_name]:
                print("ERROR: pin %s = %f (expected %f)" % (pin_name, val, pins[pin_name]))
                sys.exit(1)
        time.sleep(0.010)


def verify_tool_number(tool_number):
    verify_interp_param(5400, tool_number)        # 5400 == tool in spindle
    verify_pin_value('tool-number', tool_number)  # pin from iocontrol

    # verify stat buffer
    s.poll()
    if s.tool_in_spindle != tool_number:
        print("ERROR: stat buffer .tool_in_spindle is %f, should be %f" % (s.tool_in_spindle, tool_number))
        sys.exit(1)
    print("stat buffer .tool_in_spindle is %f" % s.tool_in_spindle)


def do_tool_change_handshake(tool_number, pocket_number):
    # prepare for tool change
    wait_for_pin_value('tool-prepare', 1)
    verify_pin_value('tool-prep-number', tool_number)
    verify_pin_value('tool-prep-pocket', pocket_number)

    h['tool-prepared'] = 1
    wait_for_pin_value('tool-prepare', 0)
    h['tool-prepared'] = 0

    # io drops tool-prepare before task has necessarily published the new
    # pocket_prepped, so poll for it rather than sleeping and hoping.
    deadline = time.time() + timeout
    while True:
        s.poll()
        if s.pocket_prepped == pocket_number or time.time() >= deadline:
            break
        time.sleep(0.01)

    print("tool prepare done, s.pocket_prepped = ", s.pocket_prepped)
    if s.pocket_prepped != pocket_number:
        print("ERROR: wrong pocket prepped in stat buffer (got %d, expected %d)" % (s.pocket_prepped, pocket_number))
        sys.exit(1)

    # change tool
    wait_for_pin_value('tool-change', 1)
    h['tool-changed'] = 1
    wait_for_pin_value('tool-change', 0)
    h['tool-changed'] = 0




#
# set up pins
# shell out to halcmd to net our pins to where they need to go
#

h = hal.component("python-ui")

h.newpin("tool-number", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-prep-number", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-prep-pocket", hal.HAL_S32, hal.HAL_IN)
h.newpin("tool-from-pocket", hal.HAL_S32, hal.HAL_IN)

h.newpin("tool-prepare", hal.HAL_BIT, hal.HAL_IN)
h.newpin("tool-prepared", hal.HAL_BIT, hal.HAL_OUT)

h.newpin("tool-change", hal.HAL_BIT, hal.HAL_IN)
h.newpin("tool-changed", hal.HAL_BIT, hal.HAL_OUT)

h.ready() # mark the component as 'ready'

os.system("halcmd source ./postgui.hal")


#
# connect to LinuxCNC
#

c = linuxcnc.command()
s = linuxcnc.stat()
e = linuxcnc.error_channel()

c.state(linuxcnc.STATE_ESTOP_RESET)
c.state(linuxcnc.STATE_ON)
c.mode(linuxcnc.MODE_MDI)
c.wait_complete()


# at startup, we should have the special tool 0 in the spindle, meaning
# "no tool" or "unknown tool"

verify_tool_number(0)


#
# test m6 to get a baseline
#

print("*** starting 'T1 M6' tool change")

c.mdi('t1 m6')
# No wait_complete() before the handshake: M6 blocks in the interpreter until
# the io handshake completes, and we ARE the io — do_tool_change_handshake
# below is what unblocks it, so waiting here waits for something only the next
# line can cause. This only ever "worked" because gmi's wait_complete gave up
# after 5s and returned -1 unchecked. The handshake opens with its own
# wait_for_pin_value('tool-prepare', 1), which is the real precondition.
do_tool_change_handshake(tool_number=1, pocket_number=1)

print("*** tool change complete")
verify_tool_number(1)

verify_interp_param(5401, 0)      # tlo x
verify_interp_param(5402, 0)      # tlo y
verify_interp_param(5403, 1)      # tlo z
verify_interp_param(5404, 0)      # tlo a
verify_interp_param(5405, 0)      # tlo b
verify_interp_param(5406, 0)      # tlo c
verify_interp_param(5407, 0)      # tlo u
verify_interp_param(5408, 0)      # tlo v
verify_interp_param(5409, 0)      # tlo w
verify_interp_param(5410, 0.125)  # tool diameter
verify_interp_param(5411, 0)      # front angle
verify_interp_param(5412, 0)      # back angle
verify_interp_param(5413, 0)      # tool orientation

verify_interp_param(5420, 0)      # current x
verify_interp_param(5421, 0)      # current y
verify_interp_param(5422, 0)      # current z
verify_interp_param(5423, 0)      # current a
verify_interp_param(5424, 0)      # current b
verify_interp_param(5425, 0)      # current c
verify_interp_param(5426, 0)      # current u
verify_interp_param(5427, 0)      # current v
verify_interp_param(5428, 0)      # current w

c.mdi('g43')
c.wait_complete()

verify_interp_param(5420, 0)      # current x
verify_interp_param(5421, 0)      # current y
verify_interp_param(5422, -1)     # current z
verify_interp_param(5423, 0)      # current a
verify_interp_param(5424, 0)      # current b
verify_interp_param(5425, 0)      # current c
verify_interp_param(5426, 0)      # current u
verify_interp_param(5427, 0)      # current v
verify_interp_param(5428, 0)      # current w

introspect()




#
# now finally test m61
#

print("*** starting 'M61 Q10' tool change")

c.mdi('m61 q10')
c.wait_complete()

verify_stable_pin_values(
    {
        'tool-change': 0,
        'tool-prep-number': 0,
        'tool-prep-pocket': 0,
        'tool-prepare': 0,
        'tool-from-pocket': 1
    },
    duration=1
)

verify_tool_number(10)

verify_interp_param(5401, 0)      # tlo x
verify_interp_param(5402, 0)      # tlo y
verify_interp_param(5403, 3)      # tlo z
verify_interp_param(5404, 0)      # tlo a
verify_interp_param(5405, 0)      # tlo b
verify_interp_param(5406, 0)      # tlo c
verify_interp_param(5407, 0)      # tlo u
verify_interp_param(5408, 0)      # tlo v
verify_interp_param(5409, 0)      # tlo w
verify_interp_param(5410, 0.500)  # tool diameter
verify_interp_param(5411, 0)      # front angle
verify_interp_param(5412, 0)      # back angle
verify_interp_param(5413, 0)      # tool orientation

verify_interp_param(5420, 0)      # current x
verify_interp_param(5421, 0)      # current y
verify_interp_param(5422, -1)     # current z
verify_interp_param(5423, 0)      # current a
verify_interp_param(5424, 0)      # current b
verify_interp_param(5425, 0)      # current c
verify_interp_param(5426, 0)      # current u
verify_interp_param(5427, 0)      # current v
verify_interp_param(5428, 0)      # current w

c.mdi('g43')
c.wait_complete()

verify_interp_param(5420, 0)      # current x
verify_interp_param(5421, 0)      # current y
verify_interp_param(5422, -3)     # current z
verify_interp_param(5423, 0)      # current a
verify_interp_param(5424, 0)      # current b
verify_interp_param(5425, 0)      # current c
verify_interp_param(5426, 0)      # current u
verify_interp_param(5427, 0)      # current v
verify_interp_param(5428, 0)      # current w


#
# use M6 to unload the spindle (T0)
#

print("*** using 'T0 M6' to unload the spindle")
c.mdi("t0 m6")
# See the T1 M6 handshake above: we are the io that completes this M6, so a
# wait_complete() here can only ever time out.
do_tool_change_handshake(tool_number=0, pocket_number=0)
verify_tool_number(0)


#
# use M61 to load T1 in the spindle again
#

print("*** using 'M61 Q1' to load a tool again")
c.mdi("m61 q1")
c.wait_complete()
verify_tool_number(1)


#
# use M61 to unload the spindle (T0)
#

print("*** using 'M61 Q0' to unload the spindle again")
c.mdi("m61 q0")
c.wait_complete()
verify_tool_number(0)


# if we get here it all worked!
sys.exit(0)

