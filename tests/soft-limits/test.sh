#!/bin/bash
# milltask: a joint pushed past its soft limit with the amps off, and the way back.
. ../stmak-driver.sh
stmak_start_server soft-limits.ini
stmak_wait_ready
./test-ui.py
