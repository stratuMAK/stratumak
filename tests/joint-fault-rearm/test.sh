#!/bin/bash
. ../stmak-driver.sh
stmak_start_server joint-fault-rearm.ini
stmak_wait_ready
./test-ui.py
