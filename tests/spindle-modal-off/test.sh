#!/bin/bash
. ../stmak-driver.sh
stmak_start_server spindle-modal-off.ini
stmak_wait_ready
./test-ui.py
