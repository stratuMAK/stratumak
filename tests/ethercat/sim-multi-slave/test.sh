#!/bin/bash
# gomc EtherCAT integration test — multi-slave bus on the sim transport.
#
# Three CoE slaves (output-only, input-only, bidirectional) at positions 0/1/2.
# Verifies multi-slave scan and per-position configuration: all three reach OP
# together, the expected per-slave PDO pins exist, and a PDO round-trip works on
# the third slave (proving its process-data offset within the shared domain is
# correct — a genuine multi-slave concern). The bidirectional slave loops its
# output back to its input. Failure is signalled by the exit value.
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }
pin_exists() { halcmd show pin 2>/dev/null | grep -qw "$1"; }

deadline=$(( SECONDS + $(gomc_scale 20) ))
while [ $SECONDS -lt $deadline ]; do
    [ "$(getp ethercat.0.all-op)" = TRUE ] && break
    sleep 0.1
done

fail=0
[ "$(getp ethercat.0.all-op)" = TRUE ] || { echo "FAIL: master did not reach OP" >&2; fail=1; }
resp=$(getp ethercat.0.slaves-responding)
echo "all-op=$(getp ethercat.0.all-op) slaves-responding=$resp"
[ "$resp" = 3 ] || { echo "FAIL: expected 3 slaves responding, got '$resp'" >&2; fail=1; }

# Every slave reached OP.
for s in "do" "di" "io"; do
    o=$(getp ethercat.0.$s.slave-oper)
    echo "  $s.slave-oper=$o"
    [ "$o" = TRUE ] || { echo "FAIL: slave '$s' did not reach OP" >&2; fail=1; }
done

# Per-slave PDO pins exist with the expected directions.
for pin in ethercat.0.do.out ethercat.0.di.in ethercat.0.io.out ethercat.0.io.in; do
    pin_exists "$pin" || { echo "FAIL: missing PDO pin $pin" >&2; fail=1; }
done

# PDO round-trip on the third slave (io, loopback): confirms slave 2's offset in
# the shared domain is correct.
halcmd setp ethercat.0.io.out 123 >/dev/null 2>&1
dl=$(( SECONDS + $(gomc_scale 5) ))
while [ $SECONDS -lt $dl ]; do [ "$(getp ethercat.0.io.in)" = 123 ] && break; sleep 0.05; done
got=$(getp ethercat.0.io.in)
echo "io round-trip: out=123 -> in=$got"
[ "$got" = 123 ] || { echo "FAIL: round-trip on slave 2 (io): set out=123, in=$got" >&2; fail=1; }

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim multi-slave: OK"
exit $fail
