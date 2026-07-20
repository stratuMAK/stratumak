#!/bin/bash
# iocontrol v2 abort-wedge regression (Tier-1 hotspot #5). See README.
. ../../gomc-driver.sh

# Fresh tooltable DB so the .tbl is (re)imported on this run.
rm -rf db sim.var*

gomc_start_server toolchange-wedge.ini
gomc_wait_ready
./test-ui.py
