#!/bin/bash
rm -f sim.var*
# stratuMAK persists interp params and the tool table in db/ (sqlite), not in
# sim.var / the .tbl file — wipe it so each run starts fresh (the driver sets
# a G54 rotation that would otherwise leak into the next run) and the copied
# .tbl below is re-imported.
rm -rf db
cp -f simpockets.tbl.original simpockets.tbl 2>/dev/null
. "$(dirname "$0")/../stmak-driver.sh"
stmak_start_server g43-test.ini
# Readiness from the status buffer: a `grep milltask` loop (no failure
# branch) plus a guessed sleep stood here.
stmak_wait_ready
./test-ui.py
