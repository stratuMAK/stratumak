#!/bin/bash
# (file, line) status trace across an o-word call into a separate file.
. ../gomc-driver.sh
gomc_start_server test.ini
gomc_wait_ready
./test-ui.py
