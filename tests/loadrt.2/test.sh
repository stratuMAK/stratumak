#!/bin/bash
# loadrt.hal deliberately issues one failing 'loadrt streamer' (missing args);
# -k lets the resident server keep going.  Then query funct/pin via halcmd.
gomc-server -r -k -f loadrt.hal --serve &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
# Fail loudly on expiry: falling through ran the two `halcmd list` calls against
# a dead server, producing empty output that diffs against `expected` as a
# content mismatch rather than "the server never started". stdout is compared
# against `expected`, so the diagnostic goes to stderr.
. "$(dirname "$0")/../gomc-scale.sh"   # readiness deadline honours GOMC_TEST_TIMEOUT_SCALE
ready=""
for i in $(seq "$(gomc_scale 100)"); do
    if halcmd show comp >/dev/null 2>&1; then ready=1; break; fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** server never became ready within 10s" >&2; exit 1; }
halcmd list funct
halcmd list pin
