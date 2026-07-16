#!/bin/bash
# Start from the committed parameter fixture: the classic .var-file parameter
# backend (PARAMETER_FILE_MODE=file) writes sim.var on save, so reset it from
# sim.var.pre each run to keep the test deterministic.
rm -f sim.var
cp sim.var.pre sim.var
gomc-server -r test.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
./test-ui.py
