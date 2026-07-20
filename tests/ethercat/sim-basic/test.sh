#!/bin/bash
# gomc EtherCAT integration test — the lcec driver on the in-process sim
# transport (transportType="sim"), no hardware.
#
# Proves the whole driver pipeline: the sim transport parses the bus-description
# file (bus.sim), the emulated slave is scanned and configured, the master
# reaches OP on the servo thread, and the configured PDO entries surface as HAL
# pins. Failure is signalled by this script's exit value (checkresult is a no-op).
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }
pin_exists() { halcmd show pin 2>/dev/null | grep -qw "$1"; }

# The AL state machine advances on the servo thread; wait for OP on a deadline
# rather than a fixed sleep.
deadline=$(( SECONDS + $(gomc_scale 15) ))
while [ $SECONDS -lt $deadline ]; do
    [ "$(getp ethercat.0.all-op)" = TRUE ] && break
    sleep 0.1
done

allop=$(getp ethercat.0.all-op)
oper=$(getp ethercat.0.io.slave-oper)
responding=$(getp ethercat.0.slaves-responding)
echo "all-op=$allop slave-oper=$oper slaves-responding=$responding"

fail=0
[ "$allop" = TRUE ] || { echo "FAIL: master did not reach OP" >&2; fail=1; }
[ "$oper" = TRUE ]  || { echo "FAIL: slave did not reach OP" >&2; fail=1; }
[ "$responding" = 1 ] || { echo "FAIL: expected 1 slave responding, got '$responding'" >&2; fail=1; }

# The configured PDO entries must surface as HAL pins (output 0x7000:01 -> dout,
# input 0x6000:01 -> din).
for pin in ethercat.0.io.dout ethercat.0.io.din; do
    pin_exists "$pin" || { echo "FAIL: missing PDO HAL pin $pin" >&2; fail=1; }
done

# The output PDO pin is a writable HAL input; setting it must be accepted (the
# value is placed in the domain and shipped to the slave each cycle). A full
# value round-trip assertion is deferred to the next milestone.
halcmd setp ethercat.0.io.dout 42 >/dev/null 2>&1 \
    || { echo "FAIL: could not set output PDO pin ethercat.0.io.dout" >&2; fail=1; }

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim: OK"
exit $fail
