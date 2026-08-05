#!/bin/bash
# Re-expression of the classic Python remap/oword-pycall test on stmak.
# C interp_ext O-words o<square>/o<multiply> are called via MDI.  The second
# multiply feeds the previous call's #<_value> back in as an argument, so its
# logged args prove the return value round-tripped into the interpreter.
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
    echo 'set mdi o<square> call [5]'
    echo 'set mdi o<multiply> call [#<_value>] [2]'
    echo 'set mdi o<multiply> call [5] [6] [7]'
    echo set wait done
    echo shutdown
) | ../../rsh2gmi.py

exit 0
