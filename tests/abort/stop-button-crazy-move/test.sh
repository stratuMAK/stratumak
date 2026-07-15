#!/bin/bash
# Resident-server harness: gomc-server does not launch the [DISPLAY] program, so
# start the server in the background, wait for the milltask comp, then drive it
# with the gmi client (same pattern as tests/abort/feed-rate).
rm -f sim.var
gomc-server -r test.ini >/tmp/gomc-stop-button.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
./test-ui.py
