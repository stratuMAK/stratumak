#!/bin/bash
# Re-expression of the classic Python remap/fail/prolog test on stmak.
# A C interp_ext prolog (failingprolog) returns INTERP_EXT_ERROR for M400.  We
# issue M400 via MDI (expected to error), then a normal G0 move to prove the
# interpreter unwound back to top level and is still usable.
set -x
. ../../../stmak-driver.sh
rm -f sim.var sim.var.bak
stmak_start_server test.ini
stmak_wait_ready

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO
    echo set estop off
    echo set machine on
    echo set mode mdi
    echo 'set mdi M400'
    echo set wait done
    echo 'set mdi G0 X1'
    echo set wait done
    echo shutdown
) | ../../../rsh2gmi.py

exit 0
