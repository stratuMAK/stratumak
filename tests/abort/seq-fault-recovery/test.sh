#!/bin/bash
# Resident-server harness (see tests/abort/feed-rate): start stmakd in the
# background, wait for the milltask comp, then drive it with the gmi client.
rm -f server.log sim.var sim.var.bak
stmakd -r test.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
./test-ui.py
