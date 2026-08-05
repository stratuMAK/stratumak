#!/bin/bash -e
rm -f motion-samples.log

# stmakd does not launch the [DISPLAY] program; start a resident server,
# capture the motion sampler with halsampler (connect before motion starts),
# then drive the test with the gmi-based test-ui.py.
. "$(dirname "$0")/../../stmak-driver.sh"

stmakd -r test.ini >server.log 2>&1 &
SRV=$!
STMAK_SRV=$SRV
export STMAK_SRV
trap 'kill $SAMPLER 2>/dev/null; kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# Replaces a `grep milltask` loop with no failure branch plus a trailing
# `sleep 0.5` that guessed at task init: ask the status buffer instead, and fail
# loudly if the machine never came up.
stmak_wait_ready

halsampler -t > motion-samples.log &
SAMPLER=$!
# Bet, not a wait — deliberately kept: nothing observable marks the halsampler
# WebSocket subscription as established, so there is no predicate to poll. See
# the long rationale in tests/hal-stream-driver.sh (hal_sample); closing this
# needs an upstream readiness signal, not a fabricated client-side one.
sleep 0.5
./test-ui.py
