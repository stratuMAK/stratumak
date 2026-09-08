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

joint 0's motor-pos-fb is left unlinked by the HAL file so the test can open a
following error with halcmd setp, without commanding any motion.
"""

import subprocess
import time

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

msgs = []


def ferrors():
    """Every operator message naming a following error, seen so far."""
    m = e.poll()
    while m:
        msgs.append(m)
        m = e.poll()
    return [text for _, text in msgs if "following error" in text]


def stays_enabled(samples=6, interval=0.05):
    """True only if the machine is enabled across several consecutive polls.

    The abort the previous fault triggered can still be in flight, and a
    machine-on that races it comes straight back off.  That matters here
    rather than being cosmetic: with motion disabled, pos_cmd tracks pos_fb
    every cycle, so a setp aimed at opening the next following error would be
    absorbed instead -- the test would then be waiting for a message about a
    fault that never happened.
    """
    for _ in range(samples):
        s.poll()
        if not s.enabled:
            return False
        time.sleep(interval * stmak_test.scale())
    return True


def machine_on(attempts=10):
    for _ in range(attempts):
        try:
            c.state(STATE_ESTOP_RESET)
            c.state(STATE_ON)
            c.mode(MODE_MANUAL)
            c.wait_complete()
        except Exception:
            pass  # a machine-on that lost the race is retried, not fatal
        if stays_enabled():
            return
    s.poll()
    stmak_test.fail("the machine would not stay on (task_state=%d enabled=%r)"
                    % (s.task_state, s.enabled))


def trip(feedback, label):
    seen = len(ferrors())
    pin_set("joint.0.motor-pos-fb", feedback)
    stmak_test.wait_until(
        lambda: len(ferrors()) > seen,
        "%s to reach the operator" % label,
        detail=lambda: "following-error messages seen: %d, all messages: %r"
                       % (len(ferrors()), msgs))
    print("ok: %s" % label)
    # The trip disables motion, which is what makes the next round a second
    # episode rather than a continuation of the first.
    stmak_test.wait_stat(s, lambda st: not st.enabled,
                         "the fault to drop the machine out",
                         detail=lambda st: "enabled=%r" % st.enabled)


machine_on()
trip(5.0, "the first following error")

# Motion tracked pos_cmd to pos_fb while disabled, so the joint reads clean
# again and the machine comes back on.  The operator drives into the same wall
# a second time and is owed the same explanation.
machine_on()
trip(10.0, "the second following error")

print("PASS")
