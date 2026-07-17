#!/bin/bash
# Re-expression of the classic Python remap/introspect test on gomc.
# The C interp_ext O-word o<introspect> reads its args plus live interpreter state
# (feed, spindle speed, named/INI/global params) via the interp_ctx accessors.
set -x
. ../../gomc-driver.sh
rm -f sim.var sim.var.bak
gomc_start_server test.ini
gomc_wait_ready

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO
    echo set estop off
    echo set machine on
    echo set mode mdi
    echo 'set mdi F200'
    echo 'set mdi S3000'
    echo 'set mdi #<_a_global_set_in_test_dot_ngc> = 47.11'
    echo 'set mdi o<introspect> call [1] [2] [3] [#<_ini[example]variable>]'
    echo set wait done
    echo shutdown
) | ../../rsh2gmi.py

exit 0
