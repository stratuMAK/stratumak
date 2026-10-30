#!/bin/bash
rm -f sim.var
. "$(dirname "$0")/../stmak-driver.sh"
stmak_start_server test.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
./test-ui.py
