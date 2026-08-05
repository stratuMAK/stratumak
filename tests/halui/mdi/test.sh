#!/bin/bash
# stmakd does not launch [DISPLAY]; drive the resident server with the UI.
stmakd -r halui.ini >/tmp/stmak-mdi.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
halcmd -f postgui.hal
./test-ui.py
