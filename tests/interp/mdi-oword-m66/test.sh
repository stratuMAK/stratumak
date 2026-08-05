#!/bin/bash
. ../../stmak-driver.sh
stmak_start_server --log /tmp/stmak-m66.log interp.ini
stmak_wait_ready
./test-ui.py
