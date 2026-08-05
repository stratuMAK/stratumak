#!/bin/bash
. ../stmak-driver.sh
stmak_start_server hard-limits.ini
stmak_wait_ready
./test-ui.py
