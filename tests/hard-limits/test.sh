#!/bin/bash
. ../gomc-driver.sh
gomc_start_server hard-limits.ini
gomc_wait_ready
./test-ui.py
