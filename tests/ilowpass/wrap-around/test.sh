#!/bin/bash
. "$(dirname "$0")/../../hal-stream-driver.sh"
hal_start_server ilowpass.hal || exit 1
halcmd start
getout() { halcmd getp ilowpass.out 2>/dev/null | awk '{print $NF}'; }

# Poll for the filter to have processed a tick rather than sleeping ~100 servo
# ticks with no predicate. gain=1 means ilowpass reaches its target in a single
# cycle, so "out has changed away from its previous value" IS the real signal.
# The waits deliberately test for *change*, not for the expected numbers, so the
# assertion stays in `expected` and cannot be smuggled into the wait. Diagnostics
# go to stderr: stdout must remain exactly the two values `expected` lists.
# Sets OUT to the new value and echoes it.
OUT=""
wait_changed() {   # wait_changed <previous-value> <what>
    local prev="$1" what="$2" cur deadline
    deadline=$(( SECONDS + 30 ))
    while [ "$SECONDS" -lt "$deadline" ]; do
        cur=$(getout)
        if [ -n "$cur" ] && [ "$cur" != "$prev" ]; then
            OUT="$cur"
            echo "$cur"
            return 0
        fi
        sleep 0.02
    done
    echo "*** ilowpass.out never moved off $prev ($what): the thread never ran" >&2
    exit 1
}

# out starts at 0; the first tick drives it to in*scale.
wait_changed 0 "initial settle"
halcmd setp ilowpass.in 21475 >/dev/null
wait_changed "$OUT" "wrap-around after in=21475"
halcmd stop
