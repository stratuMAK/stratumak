#!/bin/bash
# Dedicated test for the stratuMAK mcode_handler API (user M100-M199 implementation,
# the replacement for classic USER_M_PATH python/shell M-codes).  A cmod
# (test_mcode_handler) registers handlers via register_handler(); we drive them
# through the remote (rsh2gmi.py) interface and confirm
#   - M101 P Q was dispatched with the right P/Q and ran to completion, and
#   - M103's return value reached the interpreter in #5399, which is how a
#     G-code program reads a handler's answer.
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
    # The result lands in #5399 on the interpreter's next read, so it takes a
    # second MDI line to observe it. (debug,...) expands the parameter and
    # publishes it to the operator display, which server.log captures.
    echo set mdi m103
    echo set wait done
    echo "set mdi (debug, udr=#5399)"
    echo set wait done
    # Same read, but from the block after the call inside an o-word sub — the
    # shape a real program has. Here the interpreter must honour the M-code's
    # INTERP_EXECUTE_FINISH and drain before reading on; the two MDI lines above
    # would pass even if it did not, because each is its own invocation.
    echo "set mdi o<m103check> call"
    echo set wait done
) | ../rsh2gmi.py

# Do NOT send 'shutdown': `set wait done` returns before the async M101 handler
# finishes its ~0.5s of work, and shutting down here would abort the in-flight
# handler (it logs "aborted" instead of "completed").  Waiting for the last MDI
# line's output is enough: the M-codes run in program order, so by the time the
# o<m103check> sub has printed sub-udr=, M101 has long since logged its
# completion (checkresult greps server.log for both); the EXIT trap tears the
# server down.
for i in $(seq 100); do
    grep -q 'sub-udr=' server.log && break
    sleep 0.1
done

exit 0
