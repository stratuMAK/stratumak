#!/usr/bin/env python3
"""A burst that overflows the C log ring must not silence it for good.

The ring's producers claim a position and then a slot; when the ring is full
the message is dropped but the position stays claimed, and a consumer that
waits for that position waits forever -- with the ring full behind it, every
message from then on dropped too.  The coating machine did this at thread
start (23 EtherCAT slaves going OP, HAL, motmod, DEBUG lines nobody prints),
and from then on every fault switched the machine off without a word:
ferror-message passes in the sim because the sim never bursts.

So: overflow the ring on purpose, then send a message that has to reach the
operator -- the following error ferror-message uses -- and require both that
the overflow was reported (the test really overflowed) and that the message
still came through.
"""

import os
import subprocess

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()

SERVER_LOG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "server.log")
RING_FULL = "log ring full"


def pin_set(pin, value):
    subprocess.run(["halcmd", "setp", pin, str(value)], check=True)


def server_log():
    with open(SERVER_LOG, encoding="utf-8", errors="replace") as f:
        return f.read()


s = gmi.Stat()
c = gmi.Command()
e = gmi.ErrorChannel()

stmak_test.wait_for_startup(s)

# The burst: more messages in one servo cycle than the ring holds.
pin_set("logburst.fire", 1)
stmak_test.wait_until(
    lambda: RING_FULL in server_log(),
    "the drain to report the overflow (%r in server.log)" % RING_FULL,
    detail=lambda: "bursts fired: %s" % subprocess.run(["halcmd", "getp", "logburst.bursts"], capture_output=True, text=True).stdout.strip())
pin_set("logburst.fire", 0)
report = [l for l in server_log().splitlines() if RING_FULL in l][0]
print("overflow reported: %s" % report.split("msg=", 1)[1])

# Now a message that has to arrive: the following error, opened the way
# ferror-message does it, with joint 0's feedback unlinked.
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: st.enabled, "the machine to come on")

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
    "the following error to reach the operator after the overflow",
    detail=lambda: "messages seen: %r" % (msgs,))

# And in the log, after the overflow report: the C-module line itself, which
# is the ring being drained again, not just the operator list.
log = server_log()
assert log.index(RING_FULL) < log.index("following error"), \
    "the following error is in the log before the overflow report"

print("operator messages: %r" % (msgs,))
print("PASS")
