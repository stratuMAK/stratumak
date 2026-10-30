#!/bin/bash
# Load the module(s) from setup.hal in a resident stmakd and count the pins
# it created via halcmd. (The classic `halrun setup.hal` relied on the one-shot
# `show pin` rendering to stdout, which the stratuMAK -f executor does not do.) RESULT
# distinguishes a load that succeeds (server comes up) from one that is rejected
# (server exits).
stmakd -r -f setup.hal --serve >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# test.sh in each test directory is a symlink to this file.
. "$(dirname "$(realpath "${BASH_SOURCE[0]}")")/../stmak-scale.sh"

# Wait for one of the two outcomes: the server serves (the load succeeded and
# setup.hal has run) or it exits (the load was rejected). An 8 s loop that
# fell through counted a server still starting as "up" with no pins, and a
# rejected load still exiting as a success.
RESULT=""
for i in $(seq "$(stmak_scale 300)"); do
    if ! kill -0 $SRV 2>/dev/null; then RESULT=1; break; fi
    if halcmd show comp >/dev/null 2>&1; then RESULT=0; break; fi
    sleep 0.1
done
if [ -z "$RESULT" ]; then
    stmak_bind_failure
    echo "*** the server neither came up nor exited within 30 s; see $PWD/server.log" >&2
    exit 1
fi

if [ "$RESULT" = 0 ]; then
    NUM_PINS=$(halcmd list pin 2>/dev/null | egrep "$(cat PIN_NAME_REGEX)" | wc -l)
else
    NUM_PINS=0
fi

[ "$RESULT" -ne "$(cat RESULT)" ] && exit 1
[ "$NUM_PINS" -ne "$(cat NUM_PINS)" ] && exit 1
exit 0
