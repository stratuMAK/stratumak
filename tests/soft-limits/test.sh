#!/bin/bash
# milltask: a joint pushed past its soft limit with the amps off, and the way back.
# The servo-thread trace (soft-limits.hal) is appended to, so start clean.
rm -f trace.txt

. ../stmak-driver.sh
stmak_start_server soft-limits.ini
stmak_wait_ready
./test-ui.py
