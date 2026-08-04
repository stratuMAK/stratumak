#!/bin/bash
# Shared driver for the tool-info/* subtests. Classic ran `linuxcnc -r test.ini`,
# which launched the [DISPLAY] program; stmakd does NOT launch [DISPLAY], so
# start the server and drive it with the subtest's own gmi test-ui.py.
#
# (Previously this file was just `. ../shared-test.sh`, which — since bash `source`
# resolves relative to the CWD, the subtest dir — re-sourced THIS file forever and
# segfaulted the shell (exit 139). The xfail masked that.)

. ../../stmak-driver.sh

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

stmak_start_server --inherit test.ini

stmak_wait_ready

./test-ui.py
