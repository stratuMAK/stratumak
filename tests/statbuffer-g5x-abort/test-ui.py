#!/usr/bin/env python3

import linuxcnc
import gmi
# Reuse constants via the gmi client (classic linuxcnc module is command/stat-less).
import gmi.constants as _gk
for _n in dir(_gk):
    if not _n.startswith('_'):
        setattr(linuxcnc, _n, getattr(_gk, _n))

import math
import time
import sys
import subprocess
import os
import signal
import glob
import re


retval = 0


c = gmi.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()

c.state(linuxcnc.STATE_ESTOP_RESET)
c.state(linuxcnc.STATE_ON)

c.mode(linuxcnc.MODE_AUTO)
c.program_open('test.ngc')

c.mode(linuxcnc.MODE_MDI)
c.mdi('G10 L2 P1 X0 Y0 Z-4 (Set G54)')
c.mdi('G10 L2 P2 X0 Y0 Z-5 (Set G55)')
c.mdi('G10 L2 P3 X0 Y0 Z-6 (Set G56)')
c.mdi('G54 (Use G54)')
c.mdi('G0 X0 Y0 Z0 (Move to 0,0,0)')
c.wait_complete()

# FIXME: this is lame
time.sleep(1)

c.mode(linuxcnc.MODE_AUTO)
c.auto(linuxcnc.AUTO_RUN, 0)

# FIXME: this is lame
time.sleep(1)

c.abort()

c.wait_complete()

# The program was aborted in g55; check.
# gomc: the WS stat watch delivers with up to a few hundred ms lag — poll
# until the post-abort state arrives instead of judging the first snapshot.
deadline = time.time() + 2.0
while time.time() < deadline:
    s.poll()
    if 550 in s.gcodes:
        break
    time.sleep(0.05)
if not 550 in s.gcodes:
    print("Current coordinate system is G%d, not G55" % \
        ([i/10 for i in s.gcodes if i >= 540 and i < 600] + [None])[0])
    retval = 1
else:
    print("Current coordinate system is G55")

# gomc reports positions and offsets in millimetres (mm-everywhere
# convention); the program/expectations here are inch.
MM = 25.4

# MDI G0 takes us to the programmed location in G54
c.mode(linuxcnc.MODE_MDI)
c.mdi('G0 X0 Y0 Z0 (Move to 0,0,0)')
c.wait_complete()
s.poll()

if math.fabs(s.position[0]) > 0.000001:
    print("'G0 X0 Y0 Z0' took us to X=%.6f, should be 0" % s.position[0])
    retval = 1

if math.fabs(s.position[1]) > 0.000001:
    print("'G0 X0 Y0 Z0' took us to Y=%.6f, should be 0" % s.position[1])
    retval = 1

if math.fabs(s.position[2] + 5*MM) > 0.000001:
    print("'G0 X0 Y0 Z0' took us to Z=%.6f, should be %.1f (-5 in)" % (s.position[2], -5*MM))
    retval = 1

c.mdi('(debug,G53+#5220 ?= G55; Z#5422 ?= Z0.0)')
c.wait_complete()
err = e.poll()
print(err)

s.poll()
if s.g5x_index != 2:
    print("status has wrong g5x index: %d (expected 2)" % s.g5x_index)
    retval = 1
else:
    print("status has correct g5x index:  2")

if math.fabs(s.g5x_offset[0]) > 0.000001:
    print("g5x_offset.x is %.6f, should be 0" % s.g5x_offset[0])
    retval = 1

if math.fabs(s.g5x_offset[1]) > 0.000001:
    print("g5x_offset.y is %.6f, should be 0" % s.g5x_offset[1])
    retval = 1

if math.fabs(s.g5x_offset[2] + 5*MM) > 0.000001:
    print("g5x_offset.z is %.6f, should be %.1f (-5 in)" % (s.g5x_offset[2], -5*MM))
    retval = 1

if retval == 0:
    print("Everything ok!")

sys.exit(retval)

