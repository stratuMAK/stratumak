#!/usr/bin/env python3

# Ported to the gomc REST/WS API (gmi client).  Drive joint.<n>.jog-* directly
# via halcmd (the userspace jogwheel-encoder hal.component is gone) and read the
# joint position from gmi.Stat.joint_actual_position.

import gmi
import gomc_test
from gmi.constants import *

import subprocess
import time
import sys
import math


def _halcmd(*args):
    return subprocess.check_output(["halcmd", *args]).split()[-1].decode()


class HalShim:
    """'joint-<n>-position' -> gmi.Stat.joint_actual_position[n];
       'joint-<n>-jog-*'     -> halcmd getp/setp joint.<n>.jog-*."""

    def __getitem__(self, name):
        parts = name.split('-')
        n = int(parts[1])
        field = '-'.join(parts[2:])
        if field == 'position':
            s.poll()
            return s.joint_actual_position[n]
        return float(_halcmd("getp", "joint.%d.%s" % (n, field)))

    def __setitem__(self, name, val):
        parts = name.split('-')
        n = int(parts[1])
        field = '-'.join(parts[2:])
        if field in ('jog-counts', 'jog-enable'):
            val = int(val)
        subprocess.check_call(["halcmd", "setp", "joint.%d.%s" % (n, field), str(val)],
                              stdout=subprocess.DEVNULL)


h = HalShim()


def wait_for_joint_to_stop(joint_number):
    pos_pin = 'joint-%d-position' % joint_number
    start_time = time.time()
    # A 2 s ceiling was a bet on an idle machine; the loop returns as soon as two
    # consecutive samples match, so a generous deadline only bounds the failure.
    timeout = gomc_test.DEFAULT_TIMEOUT * gomc_test.scale()
    prev_pos = h[pos_pin]
    while (time.time() - start_time) < timeout:
        time.sleep(0.1)
        new_pos = h[pos_pin]
        if new_pos == prev_pos:
            return
        prev_pos = new_pos
    print("Error: joint didn't stop jogging!")
    print("joint %d is at %.6f %.6f seconds after reaching target (prev_pos=%.6f)" % (joint_number, h[pos_pin], timeout, prev_pos))
    sys.exit(1)


# Positions here are gomc-internal millimeters. simple_tp's arrival deadband
# TINY_DP = max_acc * period^2 * 0.001 scales with max_acc, which is now in mm/s^2
# (25.4x larger than the old inch-scaled motion): for these configs (1000 inch/s^2
# = 25400 mm/s^2, 1 ms servo) it is 2.54e-5 mm, so a jog legitimately stops that
# far short of its target. The original 1e-6 epsilon was an inch tolerance that
# happened to match the inch-scale deadband; in mm it is 25.4x too tight. Use an
# mm-scale tolerance comfortably above the deadband (still 0.1 um).
def close_enough(a, b, epsilon=0.0001):
    return math.fabs(a - b) < epsilon


def jog_joint(joint_number, counts=1, scale=0.001):
    timeout = 5.0

    start_pos = {}
    for j in range(3):
        start_pos[j] = h['joint-%d-position' % j]

    target = h['joint-%d-position' % joint_number] + (counts * scale)

    h['joint-%d-jog-scale' % joint_number] = scale
    h['joint-%d-jog-enable' % joint_number] = 1
    h['joint-%d-jog-counts' % joint_number] = int(h['joint-%d-jog-counts' % joint_number]) + counts

    start_time = time.time()
    while not close_enough(h['joint-%d-position' % joint_number], target) and (time.time() - start_time < timeout):
        time.sleep(0.010)

    h['joint-%d-jog-enable' % joint_number] = 0

    # Let the joint come to rest BEFORE sampling its final position. The wait
    # loop above exits on the first status sample within tolerance (or its
    # timeout), but under suite load a status read taken mid-settle lags the true
    # position: the joint climbs into tolerance a few ms after we look. Checking
    # then would spuriously fail ("didn't get to target, got to 0.000981" — a
    # value that IS within epsilon). A stopped joint reports a stable, true
    # position, so wait for the stop first and every check below reads settled
    # values.
    wait_for_joint_to_stop(joint_number)

    print("joint jogged from %.6f to %.6f (%d counts at scale %.6f)" % (start_pos[joint_number], h['joint-%d-position' % joint_number], counts, scale))

    success = True
    for j in range(3):
        pin_name = 'joint-%d-position' % j
        if j == joint_number:
            if not close_enough(h[pin_name], target):
                print("joint %d didn't get to target (start=%.6f, target=%.6f, got to %.6f)" % (joint_number, start_pos[joint_number], target, h['joint-%d-position' % joint_number]))
                success = False
        else:
            if not close_enough(h[pin_name], start_pos[j]):
                print("joint %d moved from %.6f to %.6f but should not have!" % (j, start_pos[j], h[pin_name]))
                success = False

    if not success:
        sys.exit(1)


#
# connect to LinuxCNC
#

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
# Jogging is only accepted in manual mode: wait for the mode switch to actually
# land instead of assuming 0.5 s is always enough. When it wasn't, the jogs below
# were silently refused and the test failed as "joint didn't get to target".
gomc_test.wait_stat(s, lambda st: st.task_mode == MODE_MANUAL,
                    "task_mode to become MODE_MANUAL",
                    detail=lambda st: "task_mode=%d task_state=%d"
                                      % (st.task_mode, st.task_state))


#
# run the test
#

jog_joint(0, counts=1, scale=0.001)
jog_joint(1, counts=10, scale=-0.025)
jog_joint(2, counts=-100, scale=0.100)

sys.exit(0)
