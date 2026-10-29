#!/bin/bash
# What deceleration does the trajectory planner actually apply at the end of the
# queue, and does a blended segment really get half of it?
#
# The answer sizes the tail a task streaming segments ahead of the planner has
# to leave at the end of a leg (pnptask's withTriangleTail did, until D46
# removed the streamed approach), so it is measured here rather than assumed. A deceleration ramp is tens of milliseconds, so it is sampled
# by filestream in the servo thread; nothing slower can see its shape.
set -e
rm -f vel.txt sim.var sim.var.bak

. ../../stmak-driver.sh
stmak_start_server decel.ini
# The machine must be up before the run starts: a guess at readiness here let
# the script's first commands reach a task that was not ready yet.
stmak_wait_ready

./run-decel.py
stmak_check_filestream
# The EXIT trap stops the server, so filestream flushes and closes vel.txt
# before checkresult reads it.
