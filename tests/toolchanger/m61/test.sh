#!/bin/bash
. ../../stmak-driver.sh
cp -f ../simpockets.tbl.original simpockets.tbl
rm -f sim.var*
stmak_start_server m61-test.ini
stmak_wait_ready
./test-ui.py
