#!/bin/bash
# (file, line) status trace across an o-word call into a separate file.
. ../stmak-driver.sh
stmak_start_server test.ini
stmak_wait_ready
./test-ui.py
