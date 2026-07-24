#!/bin/bash -e
# runtests runs this as `bash -x test.sh`, which bypasses the shebang -e — set it
# explicitly so a failing step (modcompile, or the driver's sys.exit(1)) fails
# the test instead of being swallowed.
set -e

. "$(dirname "$0")/../../gomc-driver.sh"

# Build+install the armable step watchdog cmod (installs to EMC2_CMOD_DIR,
# where gomc-server's `load` resolves it).
${SUDO} modcompile --install arm_step_watch.comp

rm -f server.log

gomc-server -r enable-jerk.ini >server.log 2>&1 &
SRV=$!
GOMC_SRV=$SRV
export GOMC_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

gomc_wait_ready

./test-ui.py
