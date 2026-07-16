#!/bin/bash
# Shared driver for the tool-info/* subtests. Classic ran `linuxcnc -r test.ini`,
# which launched the [DISPLAY] program; gomc-server does NOT launch [DISPLAY], so
# start the server and drive it with the subtest's own gmi test-ui.py.
#
# (Previously this file was just `. ../shared-test.sh`, which — since bash `source`
# resolves relative to the CWD, the subtest dir — re-sourced THIS file forever and
# segfaulted the shell (exit 139). The xfail masked that.)

rm -f sim.var*
# Provision the pristine tool table (2.9 shared-test.sh parity) — the working
# copy is gitignored scratch the test mutates, so recreate it from the tracked
# source every run: the subtest's simpockets.tbl.original when present
# (random-with-startup-tool), else the shared ../simpockets.tbl template.
rm -f simpockets.tbl
if [ -f simpockets.tbl.original ]; then
    cp simpockets.tbl.original simpockets.tbl
else
    cp ../simpockets.tbl .
fi

gomc-server -r test.ini &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

for i in $(seq 100); do
    halcmd show comp 2>/dev/null | grep -q milltask && break
    sleep 0.1
done
sleep 0.5

./test-ui.py
