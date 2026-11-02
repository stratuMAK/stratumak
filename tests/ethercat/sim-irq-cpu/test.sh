#!/bin/bash
# The EtherCAT master pins its NIC IRQs to the CPU of the thread that runs its
# read function, before its bus scan -- found from the HAL files although the
# thread is declared after the module is loaded (ethercat.hal), as in a machine
# config whose threads.hal comes last.
#
# A coating machine failed its first starts after every reboot: the scan's
# replies were handled on the housekeeping CPUs and came back later than the
# datagram timeout.  The launcher reserves every newthread's CPU while parsing
# (env->sched->funct_cpu), the plugin passes it as irq_cpu, and the master pins
# before the idle phase starts the scan.  The sim transport only records the
# request, so no privileges are needed.
. "$(dirname "$0")/../../stmak-scale.sh"

stmakd -r ethercat.ini >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

stmak_wait_until 30 "stmakd to be ready" 'grep -q "ready, waiting for shutdown" server.log'

fail=0
line_of() { grep -n -m1 "$1" server.log | cut -d: -f1; }

derived=$(line_of 'NIC IRQs to CPU 0, where ethercat.read-all runs')
pinned=$(line_of 'Pinned 1 transport IRQ(s) of bus.sim to CPU 0')
scan=$(line_of 'Scanning bus')

[ -n "$derived" ] || { echo "FAIL: the plugin did not derive CPU 0 from servo-thread" >&2; fail=1; }
[ -n "$pinned" ]  || { echo "FAIL: the master did not pin the IRQs to CPU 0" >&2; fail=1; }
[ -n "$scan" ]    || { echo "FAIL: no bus scan" >&2; fail=1; }
if [ -n "$pinned" ] && [ -n "$scan" ] && [ "$pinned" -gt "$scan" ]; then
    echo "FAIL: the IRQs were pinned after the scan began (line $pinned > $scan)" >&2
    fail=1
fi

if [ $fail -ne 0 ]; then
    grep -E 'ethercat|EtherCAT' server.log | head -40 >&2
fi
[ $fail -eq 0 ] && echo "ethercat irq-cpu: OK"
exit $fail
