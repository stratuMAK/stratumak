#!/bin/bash -e
# runtests invokes this as `bash -x test.sh`, which bypasses the shebang's -e —
# set it explicitly or a failing mid-script step (e.g. the motion-logger diff
# in test-ui.py) is silently swallowed.
set -e

# gomc full-instance test: milltask -> motion-logger interceptor -> real motmod.
. "$(dirname "$0")/../../gomc-driver.sh"

rm -f out.motion-logger*

gomc-server -r mountaindew.ini &
SRV=$!
GOMC_SRV=$SRV
export GOMC_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# test-ui.py goes straight to c.state(STATE_ESTOP_RESET), so readiness must be
# established here. Replaces a `grep milltask` loop with no failure branch plus a
# trailing `sleep 0.5` that guessed at task init.
gomc_wait_ready

./test-ui.py
