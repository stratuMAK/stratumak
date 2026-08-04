#!/bin/bash
# stmakd does not launch the [DISPLAY] program; start it and drive it with
# the (gmi-ported) test-ui.py.
. ../../../stmak-driver.sh
rm -f sim.var
rm -f simpockets.tbl
cp ../simpockets.tbl.save simpockets.tbl
stmak_start_server --inherit test.ini
stmak_wait_ready
./test-ui.py
