#!/usr/bin/env python3
"""A second fault episode must be reported like the first.

check_for_faults reports once per fault episode rather than once per
JOINT_ERROR_FLAG, so the primary cause is named and its knock-on effects stay
quiet -- an amp fault stops the drive following, and the following error that
arrives right behind it is not the interesting message.

The half of that arrangement nothing covered is the re-arm.  The gate is a
per-joint flag cleared when the joint is next seen active, enabled and clean;
narrow that clearing and the first fault explains itself while every one after
it is silent -- the machine stopping for no stated reason, which is the
failure tests/ferror-message exists to prevent, arriving one machine-on later
instead of at the first trip.

Two rounds of the same following error with a machine-off between them,
because that is what the clearing has to survive.

Each episode is counted up to a log barrier (logmark.comp, see barrier()):
exactly one "joint 0 following error" per episode, so a lost re-arm and a
leaking gate both fail, and neither depends on how long a poll waits.

joint 0's motor-pos-fb is left unlinked by the HAL file so the test can open a
following error with halcmd setp, without commanding any motion.
"""

import subprocess

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()


def pin_set(pin, value):
    # stmak_test.setp/getp address signals (halcmd sets/gets); this is a pin.
    subprocess.run(["halcmd", "setp", pin, str(value)], check=True)


s = gmi.Stat()
c = gmi.Command()
e = gmi.ErrorChannel()

stmak_test.wait_for_startup(s)

msgs = []       # every operator message so far, in arrival order
marks = 0


def texts():
    m = e.poll()
    while m:
        msgs.append(m[1])
        m = e.poll()
    return msgs


def barrier():
    """The operator messages since the previous barrier, complete.

    logmark logs "mark N" into the same ring motmod logs into, in the servo
    thread, and the ring reaches the operator in order: once the mark is
    here, every message motmod logged before it is here too.  Whether one
    episode said too little or too much is then a count, not a question of
    how long to wait for a straggler.
    """
    global marks
    marks += 1
    start = len(texts())
    pin_set("logmark.fire", 1)
    tag = "mark %d" % marks
    stmak_test.wait_until(lambda: tag in texts()[start:],
                          "the log barrier %r to reach the operator" % tag,
                          detail=lambda: "messages: %r" % msgs[start:])
    pin_set("logmark.fire", 0)
    got = texts()[start:]
    return got[:got.index(tag)]


def machine_on():
    # The fault drops the task to ESTOP_RESET (monitor.checkMotionEnabled)
    # and nothing turns it on again but this; once the task is ON, motion
    # is enabled (SetState settles on the published enable) and stays so
    # until the next fault.  With motion disabled, pos_cmd tracks pos_fb
    # every cycle and a setp could not open a following error.
    c.state(STATE_ESTOP_RESET)
    c.state(STATE_ON)
    stmak_test.wait_stat(s, lambda st: st.task_state == STATE_ON and st.enabled,
                         "the machine to come on",
                         detail=lambda st: "task_state=%d enabled=%r"
                                           % (st.task_state, st.enabled))


def trip(feedback, label):
    pin_set("joint.0.motor-pos-fb", feedback)
    # The trip disables motion and the task answers with ESTOP_RESET, which
    # is what makes the next round a second episode rather than a
    # continuation of the first.  It holds until the next machine-on, so
    # the wait cannot miss it.
    stmak_test.wait_stat(s, lambda st: st.task_state == STATE_ESTOP_RESET
                         and not st.enabled,
                         "%s to drop the machine out" % label,
                         detail=lambda st: "task_state=%d enabled=%r"
                                           % (st.task_state, st.enabled))
    said = [t for t in barrier() if "following error" in t]
    if said != ["joint 0 following error"]:
        stmak_test.fail("%s: want exactly one 'joint 0 following error', "
                        "the operator got %r" % (label, said))
    print("ok: %s" % label)


barrier()       # whatever the start-up said is not part of either episode
machine_on()
trip(5.0, "the first following error")

# Motion tracked pos_cmd to pos_fb while disabled, so the joint reads clean
# again and the machine comes back on.  The operator drives into the same wall
# a second time and is owed the same explanation.
machine_on()
trip(10.0, "the second following error")

print("PASS")
