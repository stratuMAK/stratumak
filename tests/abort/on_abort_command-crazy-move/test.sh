#!/bin/bash
# Resident-server harness: stmakd does not launch the [DISPLAY] program, so
# start the server in the background, wait for the milltask comp, then drive it
# with the gmi client (same pattern as tests/abort/feed-rate).
rm -f sim.var
. "$(dirname "$0")/../../stmak-driver.sh"
stmak_start_server test.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
./test-ui.py
