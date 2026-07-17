#!/bin/bash
. ../../gomc-driver.sh
cp -f ../simpockets.tbl.original simpockets.tbl
rm -f sim.var*
gomc_start_server m61-test.ini
gomc_wait_ready
./test-ui.py
