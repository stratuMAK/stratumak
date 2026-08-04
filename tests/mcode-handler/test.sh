#!/bin/bash
# Dedicated test for the stratuMAK mcode_handler API (user M100-M199 implementation,
# the replacement for classic USER_M_PATH python/shell M-codes).  A cmod
# (test_mcode_handler) registers an M101 handler via register_handler(); we
# drive M101 P Q through the remote (rsh2gmi.py) interface and confirm the
# handler was dispatched with the right P/Q and ran to completion.
set -x
. ../stmak-driver.sh
rm -f sim.var sim.var.bak
stmak_start_server sim.ini
stmak_wait_ready

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO
    echo set estop off
    echo set machine on
    echo set mode mdi
    echo set mdi m101 p2 q3
    echo set wait done
) | ../rsh2gmi.py

# Do NOT send 'shutdown': `set wait done` returns before the async M101 handler
# finishes its ~0.5s of work, and shutting down here would abort the in-flight
# handler (it logs "aborted" instead of "completed").  Wait for the handler to
# finish (checkresult greps server.log for it); the EXIT trap tears the server down.
for i in $(seq 100); do
    grep -qE 'M101 (completed|aborted)' server.log && break
    sleep 0.1
done

exit 0
