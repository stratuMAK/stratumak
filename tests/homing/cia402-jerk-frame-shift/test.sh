#!/bin/bash -e
# runtests runs this as `bash -x test.sh`, which bypasses the shebang -e — set it
# explicitly so a failing step (modcompile, or the driver's sys.exit(1)) fails
# the test instead of being swallowed.
set -e

. "$(dirname "$0")/../../gomc-driver.sh"

# Build+install the simulated CiA 402 drive (symlinked from the sibling test;
# modcompile follows symlinks) and the position-excursion watchdog.
${SUDO} modcompile --install sim_cia402_drive.comp
${SUDO} modcompile --install pos_excursion_watch.comp

rm -f server.log

gomc-server -r cia402-jerk.ini >server.log 2>&1 &
SRV=$!
GOMC_SRV=$SRV
export GOMC_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

gomc_wait_ready

./test-ui.py
