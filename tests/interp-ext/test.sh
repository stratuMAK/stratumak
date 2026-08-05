#!/bin/bash
# Dedicated test for the stratuMAK interp_ext API (register_oword) -- the replacement
# for classic Python O-word subroutines.  A cmod (test_interp_ext) registers a
# C O-word `o<test_oword>` that returns the sum of its args; we call it from MDI
# and confirm the handler was dispatched with the right arguments.
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
    echo 'set mdi o<test_oword> call [10] [20]'
    echo set wait done
    echo shutdown
) | ../rsh2gmi.py

exit 0
