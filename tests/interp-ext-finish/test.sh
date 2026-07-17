#!/bin/bash
# Dedicated test for a C interp_ext handler returning INTERP_EXECUTE_FINISH.
#
# The M66 re-expression (tests/interp/mdi-oword-m66) covers the NGC-side
# queue-buster; this covers the C-handler return path.  The cmod
# test_interp_ext_finish registers a remap prolog for M510 that returns
# INTERP_EXECUTE_FINISH on its first (phase 0) execution and INTERP_EXT_OK on
# the post-drain re-execution (phase 1).  We queue a real move, then call M510,
# then another move, and confirm from the log that the two-phase finish cycle
# fired -- i.e. the C return value was honoured as a queue-buster -- and that
# the program completed without error.
set -x
. ../gomc-driver.sh
rm -f sim.var sim.var.bak
gomc_start_server test.ini
gomc_wait_ready

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO
    echo set estop off
    echo set machine on
    echo set mode mdi
    echo 'set mdi G0 X1'
    echo 'set mdi M510'
    echo 'set mdi G0 X0'
    echo set wait done
    echo shutdown
) | ../rsh2gmi.py

exit 0
