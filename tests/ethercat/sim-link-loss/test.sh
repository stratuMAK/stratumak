#!/bin/bash
# gomc EtherCAT integration test — link loss / rescan (the cable-pull field
# scenario), hardware-free on the sim transport.
#
# The sim consults a companion "bus.sim.link" control file for its link state
# (content "0" = down, "1" = up). The test brings the bus to OP, drops the link
# and confirms the master reports the slave lost (link-up / slaves-responding /
# slave-online all fall), then restores the link and confirms the master
# rescans and drives the slave back to OP. Failure is signalled by the exit
# value (checkresult is a no-op).
. "$(dirname "$0")/../../hal-stream-driver.sh"

# Start with the link up (no control file); clean up any stale one from a
# previous aborted run.
rm -f bus.sim.link

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }
# wait_pin <pin> <value>: poll until the pin reads the value, on a deadline.
wait_pin() {
    local dl=$(( SECONDS + $(gomc_scale 20) ))
    while [ $SECONDS -lt $dl ]; do
        [ "$(getp "$1")" = "$2" ] && return 0
        sleep 0.1
    done
    return 1
}

fail=0

# 1. Bring the bus to OP.
wait_pin ethercat.0.all-op TRUE || { echo "FAIL: master never reached OP" >&2; fail=1; }
echo "OP:   link-up=$(getp ethercat.0.link-up) responding=$(getp ethercat.0.slaves-responding) oper=$(getp ethercat.0.io.slave-oper)"

# 2. Drop the link — the slave must be reported lost.
echo 0 > bus.sim.link
wait_pin ethercat.0.link-up FALSE       || { echo "FAIL: link-up did not drop on link loss" >&2; fail=1; }
wait_pin ethercat.0.io.slave-online FALSE || { echo "FAIL: slave stayed online after link loss" >&2; fail=1; }
echo "DOWN: link-up=$(getp ethercat.0.link-up) responding=$(getp ethercat.0.slaves-responding) online=$(getp ethercat.0.io.slave-online)"
[ "$(getp ethercat.0.slaves-responding)" = 0 ] || { echo "FAIL: slaves-responding not 0 on link loss" >&2; fail=1; }

# 3. Restore the link — the master must rescan and return the slave to OP.
echo 1 > bus.sim.link
wait_pin ethercat.0.link-up TRUE        || { echo "FAIL: link did not recover" >&2; fail=1; }
wait_pin ethercat.0.io.slave-oper TRUE  || { echo "FAIL: slave did not return to OP after rejoin" >&2; fail=1; }
echo "UP:   link-up=$(getp ethercat.0.link-up) responding=$(getp ethercat.0.slaves-responding) oper=$(getp ethercat.0.io.slave-oper)"

rm -f bus.sim.link
halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim link-loss/rescan: OK"
exit $fail
