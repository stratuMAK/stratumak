#!/bin/bash
. ../stmak-driver.sh
stmak_start_server test.ini
stmak_wait_ready
./test-ui.py
