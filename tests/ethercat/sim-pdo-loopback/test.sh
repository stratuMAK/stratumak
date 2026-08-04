#!/bin/bash
# stmak EtherCAT integration test — PDO value round-trip on the sim transport.
#
# The loopback slave (bus.sim) echoes output process data (SM2) into input
# process data (SM3), so writing the output PDO HAL pin makes the same value
# reappear on the input PDO HAL pin after a couple of servo cycles. This
# exercises the full cyclic data path in both directions:
#   halcmd setp dout -> domain -> frame -> slave -> (loopback) -> frame -> din
# Failure is signalled by this script's exit value (checkresult is a no-op).
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }

# Wait for OP first (nothing round-trips before the master reaches OP).
deadline=$(( SECONDS + $(stmak_scale 15) ))
while [ $SECONDS -lt $deadline ]; do
    [ "$(getp ethercat.0.all-op)" = TRUE ] && break
    sleep 0.1
done

fail=0
[ "$(getp ethercat.0.all-op)" = TRUE ] || { echo "FAIL: master did not reach OP" >&2; fail=1; }

# Drive a few distinct values through the output pin and confirm each comes back
# on the input pin (proving the value tracks, not a fixed reading).
roundtrip() {
    local val=$1 got
    halcmd setp ethercat.0.io.dout "$val" >/dev/null 2>&1
    local dl=$(( SECONDS + $(stmak_scale 5) ))
    while [ $SECONDS -lt $dl ]; do
        got=$(getp ethercat.0.io.din)
        [ "$got" = "$val" ] && break
        sleep 0.05
    done
    got=$(getp ethercat.0.io.din)
    echo "dout=$val -> din=$got"
    [ "$got" = "$val" ] || { echo "FAIL: PDO round-trip: set dout=$val, din=$got" >&2; return 1; }
    return 0
}

for v in 42 7 255 0; do
    roundtrip "$v" || fail=1
done

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim PDO loopback: OK"
exit $fail
