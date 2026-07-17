#!/usr/bin/env python3
# Ported to the gomc gmi REST/WS client (was the removed NML linuxcnc module).
#
# Reproduces the read-ahead / stop-button overrun bug (#865, cf. #579/#393/#241):
# fill the interp read-ahead queue while running a program, hit stop (abort), and
# verify the machine does NOT keep cutting past the stop point. Axis position now
# comes from s.actual_position (the userspace test-ui HAL comp + postgui.hal are
# gone); the read-ahead depth comes from s.queue (motion queue depth).

import gmi
from gmi.constants import *
import gomc_test

import time
import sys

# Time increment and timeout, seconds
TIME_INCR = 0.1
TIMEOUT = 10.0


def wait_for_startup(s, timeout=10.0):
    start = time.time()
    while time.time() - start < timeout:
        s.poll()
        if (s.angular_units != 0.0 and s.linear_units != 0.0 and s.axis_mask != 0
                and s.exec_state == EXEC_DONE and s.interp_state == INTERP_IDLE
                and s.task_state == STATE_ESTOP):
            return
        time.sleep(0.1)
    raise RuntimeError("Timeout waiting for gomc startup")


#
# connect to LinuxCNC (gomc)
#
# gomc_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()
wait_for_startup(s)


def xy():
    s.poll()
    p = s.actual_position
    return p[0], p[1]


def print_stats(x=None, y=None):
    px, py = xy()
    if x is None:
        sys.stderr.write("queue=%d; queue_full=%s; x=%.3f; y=%.3f\n" %
                         (s.queue, s.queue_full, px, py))
    else:
        sys.stderr.write("queue=%d; queue_full=%s; x=%.3f(%.3f); y=%.3f(%.3f)\n" %
                         (s.queue, s.queue_full, px, x - px, py, y - py))


#
# Come out of E-stop, turn the machine on, home
#
c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()
start_time = time.time()
while True:
    s.poll()
    homed = s.homed
    if homed[0] and homed[1] and homed[2]:
        break
    if (time.time() - start_time) > TIMEOUT:
        sys.stderr.write("Failed to home in time\n")
        sys.exit(1)
    time.sleep(TIME_INCR)

#
# Set up tool and start position
#
c.mode(MODE_MDI)
c.mdi('T1M6')       # Load tool 1
c.mdi('G0 X2 Y2')   # Move near to start: unit-less MDI runs in the machine's
                    # units (G20 on this inch config, matching 2.9) = 2 inches.
c.wait_complete()
# s.actual_position is gomc-mm: 2 in = 50.8 mm.
start_mm = 2 * 25.4
start_time = time.time()
px, py = xy()
while (abs(px - start_mm) > .001) or (abs(py - start_mm) > .001):
    if (time.time() - start_time) > TIMEOUT:
        sys.stderr.write("Failed to reach start position in time\n")
        sys.exit(1)
    time.sleep(TIME_INCR)
    px, py = xy()
sys.stderr.write("Starting at X=%.3f Y=%.3f\n" % (px, py))


#
# Run the generated program, wait for motion to start and the queue to
# fill up
#
c.mode(MODE_AUTO)
c.program_open('3D_Chips.ngc')
c.auto(AUTO_RUN, 0)
start_time = time.time()
s.poll()
while not (s.queue > 1000):
    if (time.time() - start_time) > TIMEOUT:
        sys.stderr.write("Failed to load segments from program\n")
        sys.exit(1)
    print_stats()
    time.sleep(TIME_INCR)
sys.stderr.write("Program partially loaded\n")

#
# Now stop the program and wait for any additional motion
#
# Program X stepover is 2.5mm, or 0.1in; error out if the X stepover
# exceeds 0.15in = 3.81mm (fudge added to eliminate a legitimate stepover
# during lag between cycles). Positions from gmi are mm.
stepover_limit_mm = 0.15 * 25.4

pre_stop_x, pre_stop_y = xy()
c.abort()

# Sample the live position each iteration (like the classic test's HAL-pin
# read in the loop condition); stop when two consecutive samples are equal
# (motion has settled). Seeding cur_x = last_x before the loop instead would
# make the condition false on the first check and skip the overrun check
# entirely.
start_time = time.time()
last_x = pre_stop_x
fail = False
while (time.time() - start_time) < TIMEOUT:
    time.sleep(TIME_INCR)
    cur_x, _ = xy()
    if cur_x == last_x:
        break
    print_stats(pre_stop_x, pre_stop_y)
    err = abs(pre_stop_x - cur_x)
    if err > stepover_limit_mm:
        sys.stderr.write(
            'ERROR:  X axis motion stopped %.3f mm past stop position\n' % err)
        fail = True
    last_x = cur_x

if fail:
    sys.exit(1)

sys.stderr.write('Success\n')
sys.exit(0)
