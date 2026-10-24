#!/usr/bin/env python3
# Drive the joint limit scenarios via the gmi REST client; findings go to
# result.txt for checkresult.
import gmi
from gmi.constants import *
import stmak_test
import time

c = stmak_test.Command()
s = gmi.Stat()
e = gmi.ErrorChannel()
out = open('result.txt', 'w')

def errors():
    msgs = []
    while True:
        m = e.poll()
        if not m:
            return msgs
        msgs.append(m[1])

def mdi(cmd):
    c.mode(MODE_MDI)
    c.wait_complete()
    c.mdi(cmd)
    c.wait_complete(30)
    return errors()

def settle():
    last = None
    for _ in range(100):
        s.poll()
        cur = tuple(round(v, 4) for v in s.joint_actual_position[:5])
        if cur == last:
            return cur
        last = cur
        time.sleep(0.2)
    return last

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_MANUAL)
c.wait_complete()
c.home(-1)
c.wait_complete()
stmak_test.wait_stat(s, lambda st: all(st.homed[:5]), "all joints homed")
errors()

# 1: C90 at X30 -- joint X/Y on a circle of r=30 at 90 deg/s would be 47 mm/s
for line in mdi("G0 X30 Y0 Z0 A0 C0") + mdi("G0 C90") + mdi("G0 C0"):
    out.write("move error: %s\n" % line)
out.write("marker C90 done\n")

# 2: C180 at X50: end points (+-50, 0) inside, the half circle crosses Y=+-50
mdi("G0 X50")
refused = mdi("G0 C180")
time.sleep(0.5)
for line in refused + errors():
    out.write("refused: %s\n" % line)
pos = settle()
out.write("after refused C180: C %.4f\n" % pos[4])

# 3: world jog of C into the Y joint limit (+-40 at C~53 deg)
c.mode(MODE_MANUAL)
c.wait_complete()
c.teleop_enable(1)
time.sleep(0.3)
# a continuous jog is held like a UI holds a key: refreshed, or the task's
# jog watchdog stops it
t = time.time()
while time.time() - t < 4.0:
    c.jog(JOG_CONTINUOUS, False, 5, 90.0)
    time.sleep(0.2)
pos = settle()
c.jog_stop(False, 5)
time.sleep(0.3)
out.write("jog stop: joint Y %.4f C %.4f\n" % (pos[1], pos[4]))
for line in errors():
    out.write("jog error: %s\n" % line)
c.jog(JOG_INCREMENT, False, 5, 90.0, -20.0)
time.sleep(1.5)
pos = settle()
out.write("jog back: %.4f\n" % pos[1])
for line in errors():
    out.write("jog error: %s\n" % line)
out.close()
time.sleep(0.3)
