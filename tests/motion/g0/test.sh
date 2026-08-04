#!/bin/bash

# Ported to stmak: run a resident stmakd, capture motion samples with
# halsampler, and drive the machine with the rsh->gmi translator instead of
# piping linuxcncrsh commands into `nc localhost 5007`.

. "$(dirname "$0")/../../stmak-driver.sh"

wait_for_pin() {
    pin="$1"
    value="$2"
    maxwait=10 # seconds
    while [ 0 -lt $maxwait ]; do
        cur=$(halcmd getp "$pin" 2>/dev/null | awk '{print $NF}')
        [ "$value" = "$cur" ] && return 0
        # numeric-tolerant compare when the target is a number
        case "$value" in
            ''|*[!0-9.-]*) ;;
            *) awk "BEGIN{exit !(\"$cur\"+0==$value)}" 2>/dev/null && [ -n "$cur" ] && return 0 ;;
        esac
        sleep 1
        maxwait=$(($maxwait - 1))
    done
    echo "error: waiting for pin $pin (want $value) timed out"
    exit 1
}

stmakd -r motion-test.ini >server.log 2>&1 &
stmakpid=$!
samplerpid=""
trap 'kill $samplerpid 2>/dev/null; kill $stmakpid 2>/dev/null; wait 2>/dev/null' EXIT

# Replaces a `grep milltask` loop with no failure branch: ask the status buffer
# and fail loudly if the machine never finished starting up.
STMAK_SRV=$stmakpid
export STMAK_SRV
stmak_wait_ready

wait_for_pin motion.in-position TRUE

echo starting to capture data
halsampler -t >| result.halsamples &
samplerpid=$!
# Bet, not a wait — deliberately kept: nothing observable marks the halsampler
# WebSocket subscription as established, so there is no predicate to poll. See
# the long rationale in tests/hal-stream-driver.sh (hal_sample); closing this
# needs an upstream readiness signal, not a fabricated client-side one.
sleep 0.5   # let the sampler subscribe before motion starts

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO

    echo set mode manual
    echo set estop off
    echo set machine on

    echo set home 0
    echo set home 1
    echo set home 2

    # Wait for homing to complete
    wait_for_pin motion.is-all-homed TRUE

    echo set mode mdi
    # The interpreter starts in the machine's units (G20 here — inch config,
    # matching 2.9), so `g0x1` moves 1 inch. The HAL joint pins are stmak-mm:
    # wait for 25.4.
    dist=1
    dist_mm=25.4
    echo set mdi g0x$dist

    # Wait for movement to complete
    wait_for_pin joint.0.pos-fb $dist_mm
    wait_for_pin joint.0.in-position TRUE
    wait_for_pin joint.1.in-position TRUE

    echo shutdown
) | python3 ../../rsh2gmi.py

kill $samplerpid 2>/dev/null
wait $samplerpid 2>/dev/null
echo finished capturing data

exit 0
