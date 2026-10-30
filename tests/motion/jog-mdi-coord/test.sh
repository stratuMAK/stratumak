#!/bin/bash -e
# runtests runs this as `bash -x test.sh`, which bypasses the shebang -e — set it
# explicitly so a failing step fails the test instead of being swallowed.
set -e

. "$(dirname "$0")/../../stmak-driver.sh"

# Build+install the in-position watch cmod (installs to EMC2_CMOD_DIR, where
# stmakd's `load` resolves it).
${SUDO} modcompile --install inpos_motion.comp

rm -f server.log

stmakd -r jog-mdi.ini >server.log 2>&1 &
SRV=$!
STMAK_SRV=$SRV
export STMAK_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

stmak_wait_ready

./test-ui.py
