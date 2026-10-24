#!/bin/bash
# Joint limit planning ([TRAJ]PLAN_JOINT_LIMITS) under xyzac-trt-kins:
#  - a C move that holds the work point keeps the X/Y joints within their
#    velocity limit (they would need ~2.4x of it at the C axis speed),
#  - a C move whose joint path passes a joint position limit between two
#    allowed end points is refused when queued,
#  - a world jog of C stops on its own where the Y joint reaches its limit,
#  - and nothing trips the joint velocity/acceleration backstop.
# The joint velocities are sampled by filestream in the servo thread.
set -e
rm -f vel.txt sim.var sim.var.bak result.txt

stmakd -r jl.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done

./run-jl.py

# Stop the server so filestream flushes and closes vel.txt before checkresult.
kill $SRV 2>/dev/null; wait 2>/dev/null
trap - EXIT
