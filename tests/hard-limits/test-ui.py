#!/usr/bin/env python3

import gmi
import gomc_test
from gmi.constants import *

import time
import sys
import subprocess
import os
import signal
import glob
import re

# Deadline for the poll loops below. These loops predate gomc_test and keep
# their own bodies (custom diagnostics + sys.exit), but a 5 s ceiling is a bet
# on an idle machine: waiting longer costs nothing on the happy path, since
# every loop exits as soon as its condition holds. Honours
# GOMC_TEST_TIMEOUT_SCALE like the rest of the suite.
TIMEOUT = gomc_test.DEFAULT_TIMEOUT * gomc_test.scale()


def print_status(status):
    status.poll()
    print("status.axis[0]: {}".format(status.axis[0]))
    print("status.axis[1]: {}".format(status.axis[1]))
    print("status.joint[0]: {}".format(status.joint[0]))
    print("status.joint[1]: {}".format(status.joint[1]))
    print("status.current_vel: {}".format(status.current_vel))
    # echo_serial_number intentionally omitted: it is the classic NML command-
    # serial handshake, which gomc (REST/WebSocket, synchronous wait_complete)
    # does not have.
    print("status.enabled: {}".format(status.enabled))
    print("status.estop: {}".format(status.estop))
    print("status.exec_state: {}".format(status.exec_state))
    print("status.inpos: {}".format(status.inpos))
    print("status.interp_state: {}".format(status.interp_state))
    print("status.interpreter_errcode: {}".format(status.interpreter_errcode))
    print("status.limit: {}".format(status.limit))
    print("status.motion_mode: {}".format(status.motion_mode))
    print("status.motion_type: {}".format(status.motion_type))
    print("status.position: {}".format(status.position))
    print("status.state: {}".format(status.state))
    print("status.task_mode: {}".format(status.task_mode))
    print("status.task_state: {}".format(status.task_state))
    print("status.velocity: {}".format(status.velocity))
    sys.stdout.flush()


def assert_wait_complete(command):
    r = command.wait_complete()
    print("wait_complete() returns {}".format(r))
    assert((r == RCS_DONE) or (r == RCS_ERROR))


#
# connect to HAL
#

def _set_lim(v):
    subprocess.call(['halcmd', 'sets', 'x-neg-lim-sw', '1' if v else '0'])


#
# connect to LinuxCNC
#

c = gomc_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()


#
# Come out of E-stop, turn the machine on, switch to Manual mode, and home.
#

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.home(0)
c.home(1)
c.home(2)
c.wait_complete()

# wait for homing to complete
start_time = time.time()
s.poll()
all_homed = s.homed[0]+s.homed[1]+s.homed[2]
while (all_homed != 3) and (time.time() - start_time < TIMEOUT):
    time.sleep(0.100)
    s.poll()
    all_homed = s.homed[0]+s.homed[1]+s.homed[2]

if all_homed != 3:
    print("failed to home")
    print("s.homed: {}".format(s.homed))
    sys.exit(1)

c.teleop_enable(0)


#
# run the test: start a jog on X, then trip a limit switch
#

# jog arguments are: (jog_type, joint_flag, axis, velocity)
c.jog(JOG_CONTINUOUS, 1, 0, -0.1)

# verify that we're starting to move
s.poll()
old_x = s.position[0]
start_time = time.time()
while (old_x == s.position[0]) and (time.time() - start_time < TIMEOUT):
    time.sleep(0.1)
    s.poll()

if old_x == s.position[0]:
    print("no jog movement")
    sys.exit(1)

print("x started moving (%.6f to %.6f)" % (old_x, s.position[0]))
print_status(s)

# verify that Status reflects the situation
assert(s.joint[0]['min_soft_limit'] == False)
assert(s.joint[0]['min_hard_limit'] == False)
assert(s.joint[0]['max_soft_limit'] == False)
assert(s.joint[0]['max_hard_limit'] == False)
assert(s.joint[0]['inpos'] == False)
assert(s.joint[0]['enabled'] == True)

assert(not (1 in s.limit))
assert(s.inpos == False)
assert(s.enabled == True)

# trip the limit switch
_set_lim(True)

# let linuxcnc react to the limit switch
expected_error = 'joint 0 on limit switch error'
start_time = time.time()
while (time.time() - start_time < TIMEOUT):
    error = e.poll()
    if error != None:
        if error[1] == expected_error:
            break
        else:
            print("linuxcnc sent other error %d: %s" % (error[0], error[1]))
    time.sleep(0.1)

if error == None or error[1] != expected_error:
    print("no limit switch error from LinuxCNC")
    sys.exit(1)

print("linuxcnc sent error %d: %s" % (error[0], error[1]))
print_status(s)

# verify that we're stopping
s.poll()
start_time = time.time()
while (s.joint[0]['velocity'] != 0.0) and (time.time() - start_time < TIMEOUT):
    time.sleep(0.1)
    s.poll()

if s.joint[0]['velocity'] != 0.0:
    print("limit switch didn't stop movement")
    sys.exit(1)

print("x stopped moving (pos=%.6f, vel=%.6f)" % (s.position[0], s.joint[0]['velocity']))
print_status(s)

# verify that Status reflects the situation
assert(s.joint[0]['min_soft_limit'] == False)
assert(s.joint[0]['min_hard_limit'] == True)
assert(s.joint[0]['max_soft_limit'] == False)
assert(s.joint[0]['max_hard_limit'] == False)
assert(s.joint[0]['inpos'] == True)
assert(s.joint[0]['enabled'] == False)

assert(s.limit[0] == 1)
assert(s.inpos == True)
assert(s.enabled == False)

# turn the machine back on with Override Limits enabled
c.override_limits()
# Wait for the real precondition of the STATE_ON below: motion has accepted the
# override (the override mask is set), not "a second has elapsed". Until this
# holds, turning the machine on would be refused by the still-tripped limit and
# the enabled/min_hard_limit asserts below would report a confusing mismatch
# rather than "the override never took".
gomc_test.wait_stat(
    s, lambda st: st.joint[0]['override_limits'],
    "joint 0 hard limits to be overridden",
    detail=lambda st: "override_limits=%s min_hard_limit=%s enabled=%s"
                      % (st.joint[0]['override_limits'],
                         st.joint[0]['min_hard_limit'],
                         st.joint[0]['enabled']))
print_status(s)
# command.serial intentionally omitted: the classic NML command-serial
# handshake has no gomc equivalent (synchronous wait_complete instead).
# this fails in 2.6.12 due to the stat RCS message having a status of
# RCS_EXEC...  as if though the override_limits command didn't set status
# back to RCS_DONE when it finished.
# assert_wait_complete(c)

c.state(STATE_ON)
assert_wait_complete(c)

# verify that Status reflects the new situation
s.poll()
assert(s.joint[0]['min_soft_limit'] == False)
assert(s.joint[0]['min_hard_limit'] == True)
assert(s.joint[0]['max_soft_limit'] == False)
assert(s.joint[0]['max_hard_limit'] == False)
assert(s.joint[0]['inpos'] == True)
assert(s.joint[0]['enabled'] == True)

assert(s.limit[0] == 1)
assert(s.inpos == True)
assert(s.enabled == True)

# jog X in the positive direction, off the negative limit switch
c.jog(JOG_CONTINUOUS, 1, 0, 1)

# verify that we're starting to move
s.poll()
old_x = s.position[0]
start_time = time.time()
while (old_x == s.position[0]) and (time.time() - start_time < TIMEOUT):
    time.sleep(0.1)
    s.poll()

if old_x == s.position[0]:
    print("no jog movement")
    sys.exit(1)

print("x started moving (%.6f to %.6f)" % (old_x, s.position[0]))
print_status(s)

# un-trip the limit switch
_set_lim(False)

# let linuxcnc react to the limit switch untripping
start_time = time.time()
while (time.time() - start_time < TIMEOUT):
    s.poll()
    if (s.joint[0]['min_hard_limit'] == False) and (s.limit[0] == 0):
        break
    time.sleep(0.1)

# verify that Status reflects the new situation
assert(s.joint[0]['min_soft_limit'] == False)
assert(s.joint[0]['min_hard_limit'] == False)
assert(s.joint[0]['max_soft_limit'] == False)
assert(s.joint[0]['max_hard_limit'] == False)
assert(s.joint[0]['inpos'] == False)
assert(s.joint[0]['enabled'] == True)

assert(s.limit[0] == 0)
assert(s.inpos == False)
assert(s.enabled == True)

# stop the jog
c.jog(JOG_STOP, 1, 0)

# Wait for the joint to actually come to rest, and for status to reflect the
# cleared limit, BEFORE checking status. The old loop compared old_x against a
# value read in the SAME poll, so its condition was false immediately and it
# never waited (it even printed "stopped moving" while joint velocity was still
# 1.0). Under load the assertions below then ran against a still-moving joint /
# unsettled status and flaked. Wait until two consecutive position samples match
# (stopped) and the min-hard-limit status has cleared.
start_time = time.time()
prev_x = None
stopped = False
while (time.time() - start_time < TIMEOUT):
    s.poll()
    x = s.position[0]
    if prev_x is not None and x == prev_x:
        stopped = True
        if (s.joint[0]['min_hard_limit'] == False) and (s.limit[0] == 0):
            break
    prev_x = x
    time.sleep(0.1)

if not stopped:
    print("JOG_STOP didn't stop movement")
    sys.exit(1)

print("x stopped moving (%.6f)" % s.position[0])
print_status(s)

# verify that Status reflects the new situation
assert(s.joint[0]['min_soft_limit'] == False)
assert(s.joint[0]['min_hard_limit'] == False)
assert(s.joint[0]['max_soft_limit'] == False)
assert(s.joint[0]['max_hard_limit'] == False)
# FIXME: another bug
#assert(s.joint[0]['inpos'] == True)
assert(s.joint[0]['enabled'] == True)

assert(s.limit[0] == 0)
# FIXME: another bug
#assert(s.inpos == True)
assert(s.enabled == True)

# success!
sys.exit(0)
