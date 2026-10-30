#!/bin/bash
# Resident-server harness (see tests/abort/feed-rate): start stmakd in the
# background, wait for the milltask comp, then drive it with the gmi client.
rm -f server.log sim.var sim.var.bak
. "$(dirname "$0")/../../stmak-driver.sh"
stmak_start_server test.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
./test-ui.py
