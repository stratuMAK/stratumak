#!/usr/bin/env python3
"""A following error opened by jogging must reach the operator, named.

The joint tracks its command but covers only `lag.gain` of the commanded
distance, so the error grows with distance travelled -- the shape of a
mis-scaled axis, and the case where an operator most needs to be told what
stopped the machine.  This differs from tests/ferror-message, which injects a
step into the feedback with no motion: here a real jog is in progress and is
aborted by the trip, which is the path a machine actually takes.

The trip point follows from the limit being velocity-scaled with a floor:

    ferror_limit = FERROR * |vel_cmd| / vel_limit,  floored at MIN_FERROR

With FERROR=1.0, MIN_FERROR=0.5, vel_limit=10 and a 10% shortfall, the limit
is at its 0.5 floor for jogs up to 5 mm/s, so the error (0.1 * distance)
reaches it after 5 mm however fast the jog is.
"""

import subprocess
import time

import gmi
import stmak_test
from gmi.constants import *

stmak_test.install_constants()

GAIN = 0.9
JOINT = 0
VEL = 8.0


def pin_set(pin, value):
    subprocess.run(["halcmd", "setp", pin, str(value)], check=True)


s = gmi.Stat()
c = gmi.Command()
e = gmi.ErrorChannel()

stmak_test.wait_for_startup(s)
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()

for j in (0, 1, 2):
    c.home(j)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:3]), "all joints homed")

while e.poll():
    pass

pin_set("lag.gain", GAIN)

# A continuous jog expires unless refreshed, so re-issue it while waiting --
# an operator holding the button does the same.
t0 = time.time()
stopped = None
while time.time() - t0 < 15.0:
    try:
        c.jog(JOG_CONTINUOUS, True, JOINT, VEL)
    except Exception:
        pass          # rejected once the trip has switched the machine off
    s.poll()
    if not s.enabled or s.task_state != STATE_ON:
        stopped = time.time() - t0
        break
    time.sleep(0.10)
try:
    c.jog_stop(True, JOINT)
except Exception:
    pass

if stopped is None:
    stmak_test.fail("jogging a joint with a %.0f%% shortfall never tripped"
                    % ((1.0 - GAIN) * 100))
print("machine stopped %.2fs into the jog" % stopped)

msgs = []


def collect():
    m = e.poll()
    while m:
        msgs.append(m)
        m = e.poll()
    return any("following error" in text for _, text in msgs)


stmak_test.wait_until(
    collect, "an operator message naming the following error",
    timeout=5.0, detail=lambda: "messages seen: %r" % (msgs,))

print("operator messages: %r" % (msgs,))
print("PASS")
