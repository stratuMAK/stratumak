#!/bin/bash
# Re-expression of the classic Python remap/variable-injection test on stmak.
# M405/M406/M407 are remapped to C interp_ext prolog/epilog handlers that inject
# and retrieve a per-remap local named parameter.  The remaps are run singly and
# then all three in one block (the scoping/several-remaps-in-a-block case).
set -x
. ../../stmak-driver.sh
rm -f sim.var sim.var.bak
stmak_start_server test.ini
stmak_wait_ready

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO
    echo set estop off
    echo set machine on
    echo set mode mdi
    echo 'set mdi m405'
    echo 'set mdi m406'
    echo 'set mdi m407'
    echo 'set mdi m405 m406 m407'
    echo set wait done
    echo shutdown
) | ../../rsh2gmi.py

exit 0
