#!/bin/bash
. ../stmak-driver.sh
stmak_start_server homing-absolute-encoder.ini
stmak_wait_ready
./test-ui.py
