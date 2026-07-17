#!/bin/bash
. ../gomc-driver.sh
gomc_start_server test.ini
gomc_wait_ready
./test-ui.py
