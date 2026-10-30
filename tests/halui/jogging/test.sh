#!/bin/bash
# stmakd does not launch [DISPLAY]; drive the resident server with the UI.
. "$(dirname "$0")/../../stmak-driver.sh"
stmak_start_server halui.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
halcmd -f postgui.hal
./test-ui.py
