#!/bin/bash
. ../stmak-driver.sh
stmak_start_server ferror-message.ini
stmak_wait_ready
./test-ui.py
