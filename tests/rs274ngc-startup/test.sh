#!/bin/bash

# stratuMAK persists interp params and the tool table in db/ (sqlite) — wipe it so
# each run imports the checked-in .tbl fresh and starts with clean params.
rm -rf db
# stmakd does not launch the [DISPLAY] program; drive it ourselves.
. "$(dirname "$0")/../stmak-driver.sh"
stmak_start_server test.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
./test-ui.py
