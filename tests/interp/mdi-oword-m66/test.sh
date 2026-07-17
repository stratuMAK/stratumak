#!/bin/bash
. ../../gomc-driver.sh
gomc_start_server --log /tmp/gomc-m66.log interp.ini
gomc_wait_ready
./test-ui.py
