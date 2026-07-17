#!/bin/bash
# Re-expression of the classic Python remap/fail/epilog test on gomc.
# A C interp_ext epilog (failingepilog) returns INTERP_EXT_ERROR for M400 after
# its NGC body ran.  We issue M400 via MDI (expected to error), then a normal G0
# move to prove the interpreter unwound back to top level and is still usable.
set -x
. ../../../gomc-driver.sh
rm -f sim.var sim.var.bak
gomc_start_server test.ini
gomc_wait_ready

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
