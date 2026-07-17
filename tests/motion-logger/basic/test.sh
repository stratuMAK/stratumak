#!/bin/bash -e
# runtests invokes this as `bash -x test.sh`, which bypasses the shebang's -e —
# set it explicitly or a failing mid-script step (e.g. the motion-logger diff
# in test-ui.py) is silently swallowed.
set -e

# gomc full-instance test: milltask -> motion-logger interceptor -> real motmod.
# The interceptor logs the motctl command stream to out.motion-logger; the driver
# slices it per sub-test and diffs against expected.*.

. "$(dirname "$0")/../../gomc-driver.sh"

rm -f out.motion-logger* result.*

gomc-server -r test.ini &
SRV=$!
GOMC_SRV=$SRV
export GOMC_SRV
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# Wait for milltask to be up, serving, and initialised. test-ui.py goes straight
# to c.state(STATE_ESTOP_RESET), so readiness has to be established here: the old
# `grep milltask` loop had no failure branch (on expiry the test ran against a
# dead server) and its trailing `sleep 0.5` guessed at task init instead of
# asking the status buffer.
gomc_wait_ready

./test-ui.py
