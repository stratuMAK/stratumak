#!/bin/bash
. ../stmak-driver.sh
stmak_start_server task-ui-auto-disable.ini
stmak_wait_ready
./test-ui.py
