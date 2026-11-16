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
. "$(dirname "$0")/../stmak-scale.sh"
stmak_wait_until --pid $SRV 10 "and2 to load" \
    'halcmd show comp 2>/dev/null | grep -q and2'

halcmd start
# stratuMAK's `getp` does not resolve RW params (e.g. and2.0.tmax); read the value
# from `show param` instead.  Column layout: Type Dir Name Value.
tmax() { halcmd show param | awk '$3=="and2.0.tmax"{print $4}'; }
# tmax is recorded by the thread's first run of and2 and never goes back to 0:
# wait for it, instead of a sleep that assumes the thread has run by then.
stmak_wait_until --pid $SRV 30 "and2 to have run on the thread" \
    '[ -n "$(tmax)" ] && [ "$(tmax)" != 0 ]'
tmax
halcmd stop
