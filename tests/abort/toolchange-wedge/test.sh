#!/bin/bash
# iocontrol v2 abort-wedge regression (Tier-1 hotspot #5). See README.
. ../../stmak-driver.sh

# Fresh tooltable DB so the .tbl is (re)imported on this run.
rm -rf db sim.var*

stmak_start_server toolchange-wedge.ini
stmak_wait_ready
./test-ui.py
