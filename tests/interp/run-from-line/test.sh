#!/bin/bash -e
# stmakd does not launch the [DISPLAY] program; start a resident server, then
# drive the test with the gmi-based test-ui.py.
. "$(dirname "$0")/../../stmak-driver.sh"

stmakd -r test.ini >server.log 2>&1 &
SRV=$!
STMAK_SRV=$SRV
export STMAK_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

stmak_wait_ready

./test-ui.py
