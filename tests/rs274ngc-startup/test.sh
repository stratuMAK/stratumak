#!/bin/bash

# stratuMAK persists interp params and the tool table in db/ (sqlite) — wipe it so
# each run imports the checked-in .tbl fresh and starts with clean params.
rm -rf db
# stmakd does not launch the [DISPLAY] program; drive it ourselves.
stmakd -r test.ini &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
for i in $(seq 100); do halcmd show comp 2>/dev/null | grep -q milltask && break; sleep 0.1; done
sleep 0.5
./test-ui.py
