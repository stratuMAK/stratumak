#!/bin/bash

# stmakd does not launch the [DISPLAY] program; start it and drive it with
# the test UI (ported to the gmi REST/WS client).
. "$(dirname "$0")/../stmak-driver.sh"
stmak_start_server spindle.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready

./test-ui.py
