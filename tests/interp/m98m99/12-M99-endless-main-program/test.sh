#!/bin/bash -e
# runtests invokes this as `bash -x test.sh`, which bypasses the shebang's -e —
# set it explicitly or a failing mid-script step (e.g. the motion-logger diff
# in test-ui.py) is silently swallowed.
set -e

# gomc full-instance test: milltask -> motion-logger interceptor -> real motmod.
# The task run loops the program 3x (M99 endless, counter-terminated); test-ui.py
# diffs out.motion-logger vs expected.motion-logger (on stderr). Then the same
# program is run in the standalone rs274 interpreter, whose stdout is compared
# against `expected` by the runtests harness.
. ../../../gomc-driver.sh
rm -f out.motion-logger*

gomc_start_server --inherit motion-logger.ini

gomc_wait_ready

./test-ui.py

kill $GOMC_SRV 2>/dev/null; wait 2>/dev/null; trap - EXIT

# Standalone interpreter: should run the program once (M99 in main exits).
rs274 -g test.ngc | awk '{$1=""; print}'
exit ${PIPESTATUS[0]}
