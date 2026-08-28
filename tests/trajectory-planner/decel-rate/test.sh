#!/bin/bash
# What deceleration does the trajectory planner actually apply at the end of the
# queue, and does a blended segment really get half of it?
#
# The answer is what a task streaming segments ahead of the planner has to size
# a braking allowance by (pnptask's BLEND_TAIL_MARGIN), so it should be measured
# and not guessed. A deceleration ramp is tens of milliseconds, so it is sampled
# by filestream in the servo thread; nothing slower can see its shape.
set -e
rm -f vel.txt sim.var sim.var.bak

stmakd -r decel.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done

./run-decel.py

# Stop the server so filestream flushes and closes vel.txt before checkresult.
kill $SRV 2>/dev/null; wait 2>/dev/null
trap - EXIT
