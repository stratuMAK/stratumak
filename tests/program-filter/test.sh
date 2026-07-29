#!/bin/bash
# The controller converts [FILTER] source files into the G-code it runs.
. ../gomc-driver.sh
gomc_start_server test.ini
gomc_wait_ready
./test-ui.py
