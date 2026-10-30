#!/bin/bash
. ../../stmak-driver.sh
stmak_start_server interp.ini
stmak_wait_ready
./test-ui.py
