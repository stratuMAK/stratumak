#!/bin/bash
# stmak EtherCAT integration test — all-op counts the CONFIGURED slaves only.
#
# The bus carries two slaves; ethercat-conf.xml claims only the first. The
# unclaimed one answers the scan and stays in PRE-OP forever, so the master's
# AL state word (the bitwise OR over every responding slave) never reads
# "OP only". all-op is derived from the configured slaves instead, so it must
# still go TRUE — that is the state a machine builder cares about.
#
# The PRE-OP assertion below is what keeps this test honest: it proves the
# unconfigured slave really is holding the master's AL state word down, so a
# passing all-op cannot come from an all-OP bus.
#
# Failure is signalled by this script's exit value (checkresult is a no-op).
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }
# wait_pin <pin> <value>: poll until the pin reads the value, on a deadline.
wait_pin() {
    local dl=$(( SECONDS + $(stmak_scale 20) ))
    while [ $SECONDS -lt $dl ]; do
        [ "$(getp "$1")" = "$2" ] && return 0
        sleep 0.1
    done
    return 1
}

fail=0

# 1. The configured slave reaches OP, and all-op follows it.
wait_pin ethercat.0.io.slave-oper TRUE \
    || { echo "FAIL: configured slave did not reach OP" >&2; fail=1; }
wait_pin ethercat.0.all-op TRUE \
    || { echo "FAIL: all-op did not go TRUE with the configured slave in OP" >&2; fail=1; }

# 2. Both slaves are on the bus...
responding=$(getp ethercat.0.slaves-responding)
[ "$responding" = 2 ] \
    || { echo "FAIL: expected 2 slaves responding, got '$responding'" >&2; fail=1; }

# 3. ...and the unconfigured one is still in PRE-OP, dragging the master's
#    AL state word with it. Without this the test would be vacuous.
preop=$(getp ethercat.0.state-preop)
[ "$preop" = TRUE ] \
    || { echo "FAIL: expected the unconfigured slave to hold state-preop TRUE," \
              "got '$preop' — the bus is fully in OP and this test proves nothing" >&2
         fail=1; }

echo "all-op=$(getp ethercat.0.all-op) oper=$(getp ethercat.0.io.slave-oper)" \
     "responding=$responding state-preop=$preop state-op=$(getp ethercat.0.state-op)"

# 4. The global (all-masters) pin must agree with the per-master one.
globalall=$(getp ethercat.all-op)
[ "$globalall" = TRUE ] \
    || { echo "FAIL: global all-op is '$globalall', expected TRUE" >&2; fail=1; }

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim unconfigured-slave: OK"
exit $fail
