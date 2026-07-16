#!/bin/bash
# Classic LinuxCNC ran `exec linuxcnc -r interp.ini` and captured the
# interpreter's stdout into `result`; the sub's `(print, test RAN)` is a plain
# fprintf(stdout), not an operator message. Under gomc the interp runs inside
# gomc-server, so that stdout lands in the server log -- cat it into our own
# stdout at the end so checkresult can grep `result` for 'test RAN' (and for
# the absence of the 'duplicate O-word label' bug).
gomc-server -r interp.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
./test-ui.py
cat server.log
