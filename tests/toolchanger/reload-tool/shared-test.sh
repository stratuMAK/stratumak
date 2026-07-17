#!/bin/bash
# gomc-server does not launch the [DISPLAY] program; start it and drive it with
# the (gmi-ported) test-ui.py.
. ../../../gomc-driver.sh
rm -f sim.var
rm -f simpockets.tbl
cp ../simpockets.tbl.save simpockets.tbl
gomc_start_server --inherit test.ini
gomc_wait_ready
./test-ui.py
