#!/bin/bash
. ../stmak-driver.sh
stmak_start_server ferror-jog-lag.ini
stmak_wait_ready
./test-ui.py
