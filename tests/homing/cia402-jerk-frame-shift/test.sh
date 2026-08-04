#!/bin/bash -e
# runtests runs this as `bash -x test.sh`, which bypasses the shebang -e — set it
# explicitly so a failing step (modcompile, or the driver's sys.exit(1)) fails
# the test instead of being swallowed.
set -e

. "$(dirname "$0")/../../stmak-driver.sh"

# Build+install the simulated CiA 402 drive (symlinked from the sibling test;
# modcompile follows symlinks) and the position-excursion watchdog.
${SUDO} modcompile --install sim_cia402_drive.comp
${SUDO} modcompile --install pos_excursion_watch.comp

rm -f server.log

stmakd -r cia402-jerk.ini >server.log 2>&1 &
SRV=$!
STMAK_SRV=$SRV
export STMAK_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

stmak_wait_ready

./test-ui.py
