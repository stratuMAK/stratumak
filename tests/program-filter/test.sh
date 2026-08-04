#!/bin/bash
# The controller converts [FILTER] source files into the G-code it runs.
. ../stmak-driver.sh
stmak_start_server test.ini
stmak_wait_ready
./test-ui.py
