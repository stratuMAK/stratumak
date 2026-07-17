#!/usr/bin/env python3

import gmi
from gmi.constants import *
# Reuse linuxcnc_util with the gmi REST/WS client: the classic NML linuxcnc
# module is command/stat-less now, so copy gmi's constants onto it and drive
# via gmi.Command/Stat/ErrorChannel.
import linuxcnc_util
import gomc_test

import time
import sys
import os
import math

# this is how long we wait for linuxcnc to do our bidding
timeout = 5.0


# unbuffer stdout
sys.stdout = os.fdopen(sys.stdout.fileno(), 'w')


#
# connect to LinuxCNC
#

# gomc_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = gomc_test.Command()
e = gmi.ErrorChannel()
s = gmi.Stat()


class MachineUnitsStat:
    """linuxcnc_util compares status.position against machine-unit targets;
    gmi.Stat reports gomc-mm. Convert positions back to this inch config's
    machine units, pass everything else through."""

    def __init__(self, stat):
        self._stat = stat

    def poll(self):
        self._stat.poll()

    def __getattr__(self, name):
        value = getattr(self._stat, name)
        if name == 'position':
            return tuple(v / 25.4 for v in value)
        return value


l = linuxcnc_util.LinuxCNC(command=c, status=MachineUnitsStat(s), error=e)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)   
c.home(-1)   
l.wait_for_home(joints=[1,0,1,0,0,0,0,0,0])


#
# do some jogs in manual mode
#

l.jog_axis('x', -0.5)
l.jog_axis('x', 0.0)

l.jog_axis('z', -0.5)
l.jog_axis('z', 0.0)


#
# do some MDI commands
#

c.mode(MODE_MDI)

gcode = 'g0 x2 z2'
print("running gcode: {}".format(gcode))
c.mdi(gcode)
c.wait_complete()
l.wait_for_axis_to_stop_at('x', 2);
l.wait_for_axis_to_stop_at('z', 2);

gcode = 'g0 x-2'
print("running gcode: {}".format(gcode))
c.mdi(gcode)
c.wait_complete()
l.wait_for_axis_to_stop_at('x', -2);
l.wait_for_axis_to_stop_at('z', 2);

gcode = 'g0 z-2'
print("running gcode: {}".format(gcode))
c.mdi(gcode)
c.wait_complete()
l.wait_for_axis_to_stop_at('x', -2);
l.wait_for_axis_to_stop_at('z', -2);

sys.exit(0)

