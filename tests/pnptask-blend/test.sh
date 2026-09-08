#!/bin/bash
# Does the second leg of a streamed approach blend into the first, or does the
# head stop at the wait position?
#
# Both the trigger and the measurement are in the servo thread, which is the
# only way this question can be asked. The release is a comparator on the
# commanded X position (blend.hal) so the second leg is dispatched at an exact
# machine coordinate every run; the velocity is captured by filestream every
# servo cycle. Driving either from a script over REST puts a non-realtime
# latency in series with a realtime effect, and the answer then depends on the
# latency rather than on the planner -- which is how this was got wrong
# repeatedly before the rig was built this way.
set -e
# Tray contents and station occupancy survive a restart by design (D6), so
# without wiping them each run starts from what the last one left and the same
# release point runs a different job.
rm -rf db vel.txt

. ../stmak-driver.sh
stmak_start_server blend.ini
./test.py
