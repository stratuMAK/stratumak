#!/bin/bash
# Run and2 on a 1ms thread for a second, then emit its recorded tmax so
# checkresult can confirm per-function timing is nonzero.  stratuMAK has no
# userspace comps / loadusr, so a resident stmakd + halcmd replaces the
# classic halrun `test.hal`.
stmakd -r -f threads.hal --serve >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# Wait for the REST API to accept commands. Fail loudly on expiry: falling
# through ran `halcmd start` against a dead server and emitted an empty tmax,
# which checkresult reports as a bare `test` failure rather than "the server
# never started". Diagnostics go to stderr — stdout must stay the single tmax
# integer checkresult cats.
ready=""
for i in $(seq 100); do
    if halcmd show comp 2>/dev/null | grep -q and2; then
        ready=1
        break
    fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** and2 never loaded within 10s; see $PWD/server.log" >&2; exit 1; }

halcmd start
sleep 1                       # accumulate ~1000 invocations of the 1ms thread
# stratuMAK's `getp` does not resolve RW params (e.g. and2.0.tmax); read the value
# from `show param` instead.  Column layout: Type Dir Name Value.
halcmd show param | awk '$3=="and2.0.tmax"{print $4}'
halcmd stop
