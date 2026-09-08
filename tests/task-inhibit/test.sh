#!/bin/bash
. ../stmak-driver.sh
stmak_start_server task-inhibit.ini
stmak_wait_ready
./test-ui.py
