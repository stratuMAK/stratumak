#!/usr/bin/env python3
"""A following error must name itself on the operator message channel.

motmod detects the fault and logs "joint N following error"; milltask's own
"motion error detected -- aborting" names no cause, so that specific message
is the only thing telling an operator what happened.  It travels the C log
ring -> stmak.NotifyLogError -> the task's log hook -> the operator message
list, a chain nothing else in the suite asserts on and which is easy to break
while reworking log routing.

The trip is self-erasing, which is what makes the message load-bearing: once
motion disables, EMCMOT_MOTION_DISABLED sets pos_cmd = pos_fb every cycle, so
joint.N.f-error falls back to 0 and f-errored/error go FALSE.  After the fact
there is no pin evidence left to look at.
"""

import subprocess
import time

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()


def pin_set(pin, value):
    # stmak_test.setp/getp address signals (halcmd sets/gets); these are pins.
    subprocess.run(["halcmd", "setp", pin, str(value)], check=True)


s = gmi.Stat()
c = gmi.Command()
e = gmi.ErrorChannel()

stmak_test.wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: st.enabled, "the machine to come on")

# joint 0's motor-pos-fb is left unlinked by the HAL file, so writing it opens
# a following error with no motion commanded.
pin_set("joint.0.motor-pos-fb", 5.0)

msgs = []


def saw_following_error():
    m = e.poll()
    while m:
        msgs.append(m)
        m = e.poll()
    return any("following error" in text for _, text in msgs)


stmak_test.wait_until(
    saw_following_error,
    "an operator message naming the following error",
    detail=lambda: "messages seen: %r" % (msgs,))

print("operator messages: %r" % (msgs,))
print("PASS")
