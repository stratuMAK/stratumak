#!/usr/bin/env python3
"""A burst that overflows the C log ring is counted, and is over.

The ring is a bounded multi-producer queue: a full ring drops the message
it had no room for and nothing else, chatter below WARN stops taking slots
before the ring is full, and a level no sink would print is not enqueued at
all.  The coating machine burst at every thread start (23 EtherCAT slaves
going OP, HAL, motmod, DEBUG lines nobody prints); with the earlier ring one
burst silenced the C-module log for the rest of the run, and every fault
after it switched the machine off without a word.  ferror-message passes in
the sim because the sim never bursts.

So: overflow the ring on purpose with chatter the log prints, require the
overflow to be reported with nothing but chatter dropped, then send a
message that has to reach the operator -- the following error ferror-message
uses -- and require it.  Then the same burst at DEBUG, which must cost no
slot at all.
"""

import os
import subprocess
import time

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

# Well over the ring's 8192 slots, in one servo cycle, and at a level the
# log prints, so every message takes a slot the drain has to free.
BURST = 40000
LEVEL_DEBUG, LEVEL_INFO = 0, 1


def reports():
    return [l for l in server_log().splitlines() if RING_FULL in l]


def bursts_fired():
    return subprocess.run(["halcmd", "getp", "logburst.bursts"], capture_output=True, text=True).stdout.strip()


def attr(line, key):
    for field in line.split():
        if field.startswith(key + "="):
            return field[len(key) + 1:]
    return None


pin_set("logburst.count", BURST)
pin_set("logburst.level", LEVEL_INFO)
pin_set("logburst.fire", 1)
stmak_test.wait_until(
    lambda: len(reports()) > 0,
    "the drain to report the overflow (%r in server.log)" % RING_FULL,
    detail=lambda: "bursts fired: %s" % bursts_fired())
pin_set("logburst.fire", 0)
report = reports()[0]
print("overflow reported: %s" % report.split("msg=", 1)[1])
assert int(attr(report, "info")) > 0, "the INFO burst was not what overflowed: %s" % report
assert attr(report, "error") == "0" and attr(report, "warn") == "0", \
    "the burst cost a message above INFO: %s" % report

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

# The same burst at DEBUG: nobody prints it, so the ring's floor keeps it
# out and it costs no slot.  The burst is confirmed fired; no report follows.
before = len(reports())
pin_set("logburst.level", LEVEL_DEBUG)
pin_set("logburst.fire", 1)
stmak_test.wait_until(lambda: bursts_fired().endswith("= 2"), "the DEBUG burst to fire")
pin_set("logburst.fire", 0)
# Give the drain more than its poll interval to report, were there anything.
time.sleep(1.0)
assert len(reports()) == before, \
    "a DEBUG burst took ring slots: %r" % (reports()[before:],)
assert server_log().count("burst 0 of") == 1, "DEBUG burst lines reached the log"
print("DEBUG burst of %d cost nothing" % BURST)
print("PASS")
